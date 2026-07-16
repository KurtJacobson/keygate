package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stripe/stripe-go/v82"

	"github.com/tabloy/keygate/internal/branding"
	"github.com/tabloy/keygate/internal/config"
	"github.com/tabloy/keygate/internal/crypto"
	"github.com/tabloy/keygate/internal/handler"
	"github.com/tabloy/keygate/internal/license"
	"github.com/tabloy/keygate/internal/middleware"
	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/payment"
	"github.com/tabloy/keygate/internal/service"
	"github.com/tabloy/keygate/internal/storage"
	"github.com/tabloy/keygate/internal/store"
	"github.com/tabloy/keygate/internal/version"
	"github.com/tabloy/keygate/pkg/response"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// Security checks on startup
	warnings, fatal := cfg.ValidateSecurityDefaults()
	for _, w := range warnings {
		log.Printf("WARNING: %s", w)
	}
	for _, f := range fatal {
		log.Printf("FATAL: %s", f)
	}
	if len(fatal) > 0 {
		log.Fatalf("security validation failed — fix the above errors before starting")
	}

	db, err := store.New(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	if err := db.RunMigrations("db/migrations"); err != nil {
		log.Fatalf("migrations: %v", err)
	}
	defer db.Close()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// Optional Redis-backed rate limiting
	if cfg.RedisURL != "" {
		logger.Info("Redis rate limiting enabled", "url", cfg.RedisURL)
		// To enable: import github.com/redis/go-redis/v9 and uncomment:
		// rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisURL})
		// middleware.SetRateLimitBackend(middleware.NewRedisBackend(rdb))
	}

	if cfg.StripeSecretKey != "" {
		stripe.Key = cfg.StripeSecretKey
	}

	webhookHTTPTimeout, err := time.ParseDuration(cfg.WebhookHTTPTimeout)
	if err != nil {
		webhookHTTPTimeout = 10 * time.Second
	}
	webhookRetryInterval, err := time.ParseDuration(cfg.WebhookRetryInterval)
	if err != nil {
		webhookRetryInterval = 30 * time.Second
	}

	bf := middleware.NewBruteForceProtection(
		cfg.BFMaxFails,
		time.Duration(cfg.BFLockoutSeconds)*time.Second,
		30*time.Minute,
		5*time.Minute,
	)
	webhookSvc := service.NewWebhookService(db, logger, webhookHTTPTimeout, cfg.WebhookMaxAttempts)
	emailSvc := service.NewEmailService(cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUsername, cfg.SMTPPassword, cfg.SMTPFrom, logger, db)
	// LICENSE_SIGNING_KEY is a 32-byte ed25519 seed in hex. Parsed
	// once here so an invalid value fails fast at startup rather
	// than the first /license/activate call.
	licenseSigningPriv, err := license.PrivateKeyFromHex(cfg.LicenseSigningKey)
	if err != nil {
		log.Fatalf("LICENSE_SIGNING_KEY: %v", err)
	}
	licenseSvc := service.NewLicenseService(db, licenseSigningPriv, logger, bf, webhookSvc)
	usageSvc := service.NewUsageService(db, webhookSvc, emailSvc, logger, cfg.QuotaWarningThreshold)
	seatSvc := service.NewSeatService(db, webhookSvc, emailSvc, logger, cfg.BaseURL)
	entitlementSvc := service.NewEntitlementService(db, logger)
	floatingSvc := service.NewFloatingService(db, logger)

	// ─── Release subsystem (storage + service) ───
	// When STORAGE_* env vars aren't configured we fall back to a Disabled
	// stub so the server still boots. Release endpoints will return 503 when
	// they reach storage; license/billing functions are unaffected.
	var releaseStorage storage.Storage = storage.Disabled{}
	if cfg.IsStorageEnabled() {
		s3, err := storage.NewS3(context.Background(), storage.S3Config{
			Endpoint:       cfg.StorageEndpoint,
			Region:         cfg.StorageRegion,
			Bucket:         cfg.StorageBucket,
			AccessKey:      cfg.StorageAccessKey,
			SecretKey:      cfg.StorageSecretKey,
			ForcePathStyle: cfg.StorageForcePathStyle,
		})
		if err != nil {
			logger.Error("storage: init failed; release endpoints will be disabled", "error", err)
		} else {
			releaseStorage = s3
			logger.Info("storage: initialized", "endpoint", cfg.StorageEndpoint, "bucket", cfg.StorageBucket)
		}
	} else {
		logger.Info("storage: disabled (STORAGE_BUCKET/ACCESS_KEY/SECRET_KEY not all set)")
	}
	uploadTTL, err := time.ParseDuration(cfg.StorageUploadTTL)
	if err != nil {
		logger.Warn("storage: invalid STORAGE_UPLOAD_TTL, using service default",
			"value", cfg.StorageUploadTTL, "error", err)
		uploadTTL = 0 // service applies its own default
	}
	downloadTTL, err := time.ParseDuration(cfg.StorageDownloadTTL)
	if err != nil {
		logger.Warn("storage: invalid STORAGE_DOWNLOAD_TTL, using service default",
			"value", cfg.StorageDownloadTTL, "error", err)
		downloadTTL = 0
	}

	// Master encryption key drives two independent features via HKDF:
	//   - "license-key":                 AEAD over license_key plaintext at rest.
	//                                    Works even when storage is disabled.
	//   - "release-signing-private-key": AEAD over per-product Ed25519
	//                                    private keys. Requires storage.
	//
	// Subkeys are purpose-isolated — ciphertext from one cannot decrypt
	// under another.
	var releaseSigner *service.ReleaseSigningService
	if cfg.IsMasterEncryptionKeyConfigured() {
		masterRaw, err := hex.DecodeString(cfg.ReleaseKeyEncryptionKey)
		if err != nil {
			logger.Error("master key hex decode failed; license/release encryption disabled", "error", err)
		} else {
			// Wire license-key encryption — orthogonal to storage.
			db.LicenseKeyAEAD = crypto.MustDeriveAEAD(masterRaw, "license-key")
			logger.Info("license key encryption: enabled")

			// Best-effort backfill of unencrypted historical rows. Non-blocking,
			// resumable across restarts. No timeout — runs until done or shutdown.
			go func() {
				n, err := db.BackfillLicenseKeyEncrypted(context.Background(), logger)
				if err != nil {
					logger.Warn("license key backfill failed (will retry next start)",
						"encrypted_so_far", n, "error", err)
					return
				}
				if n > 0 {
					logger.Info("license key backfill complete", "encrypted", n)
				}
			}()

			// Release signing requires storage in addition to the master key.
			if cfg.IsStorageEnabled() {
				releaseAEAD := crypto.MustDeriveAEAD(masterRaw, "release-signing-private-key")
				releaseSigner = service.NewReleaseSigningService(service.ReleaseSigningServiceConfig{
					Store:       db,
					Storage:     releaseStorage,
					AEAD:        releaseAEAD,
					Logger:      logger,
					MaxSignSize: cfg.MaxReleaseSignSize,
				})
				logger.Info("release signing: enabled", "max_sign_mb", cfg.MaxReleaseSignSize/(1024*1024))
			}
		}
	} else {
		logger.Warn("license key encryption: DISABLED (RELEASE_KEY_ENCRYPTION_KEY not set)")
	}

	releaseSvc := service.NewReleaseService(service.ReleaseServiceConfig{
		Store:       db,
		Storage:     releaseStorage,
		Signer:      releaseSigner,
		Logger:      logger,
		Webhook:     webhookSvc,
		UploadTTL:   uploadTTL,
		DownloadTTL: downloadTTL,
	})

	licenseH := handler.NewLicenseHandler(licenseSvc)
	authH := &handler.AuthHandler{Store: db, Config: cfg, Email: emailSvc}
	stripeH := &payment.StripeHandler{
		Store:         db,
		WebhookSecret: cfg.StripeWebhookSecret,
		BaseURL:       cfg.BaseURL,
		Email:         emailSvc,
		WebhookSvc:    webhookSvc,
		Livemode:      cfg.StripeLivemode,
	}
	// Initialize thread-safe webhook secret with config value
	stripeH.SetWebhookSecret(cfg.StripeWebhookSecret)
	expiryChecker := service.NewExpiryChecker(db, emailSvc, webhookSvc, logger)
	meteredSyncer := service.NewMeteredBillingSyncer(db, logger)
	adminH := handler.NewAdminHandler(db, webhookSvc, emailSvc, expiryChecker, meteredSyncer)
	usageH := handler.NewUsageHandler(usageSvc)
	seatH := handler.NewSeatHandler(seatSvc)
	entitlementH := handler.NewEntitlementHandler(entitlementSvc)
	floatingH := handler.NewFloatingHandler(floatingSvc)
	webhookAdminH := handler.NewWebhookAdminHandler(db, webhookSvc)
	systemH := handler.NewSystemHandler(db)
	releaseAdminH := handler.NewReleaseAdminHandler(releaseSvc, db)
	releasePublicH := handler.NewReleasePublicHandler(handler.ReleasePublicConfig{
		Service:     releaseSvc,
		Store:       db,
		Storage:     releaseStorage,
		Logger:      logger,
		BaseURL:     cfg.BaseURL,
		DownloadTTL: downloadTTL,
	})
	releaseSigningH := handler.NewReleaseSigningAdminHandler(releaseSigner, db)

	// Sync ADMIN_EMAILS to database roles (backward compatibility / initial setup)
	if len(cfg.AdminEmails) > 0 {
		if err := db.SyncAdminEmails(context.Background(), cfg.AdminEmails); err != nil {
			logger.Error("sync admin emails failed", "error", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Auto-setup Stripe webhook endpoint if:
	// - Stripe secret key is configured
	// - No manual webhook secret override via env var
	// - BASE_URL is not localhost (Stripe can't deliver to localhost)
	if cfg.StripeSecretKey != "" && cfg.StripeWebhookSecret == "" && !payment.IsLocalhostURL(cfg.BaseURL) {
		stripeH.SetupWebhookEndpoint(ctx)
	}

	go webhookSvc.StartRetryLoop(ctx, webhookRetryInterval)

	go floatingSvc.StartCleanupLoop(ctx, time.Minute)

	go expiryChecker.StartExpiryLoop(ctx)

	// Metered billing sync. 5-minute cadence — Stripe aggregates
	// server-side so sub-minute polling burns rate budget for no
	// real benefit. Only starts when a Stripe key is configured;
	// otherwise the dispatcher would error every cycle.
	if cfg.StripeSecretKey != "" {
		go meteredSyncer.StartSyncLoop(ctx, 5*time.Minute)
	}

	go emailSvc.StartEmailQueueProcessor(ctx, db)

	go systemH.StartAutoCheck(ctx.Done())

	// Cleanup expired transient rows every hour. Grouped together
	// because each table grows unboundedly otherwise:
	//   - OTP codes: short TTL, but new rows on every login attempt.
	//   - Refresh tokens: rotation leaves revoked-but-not-yet-expired
	//     rows behind for reuse-detection; safe to drop only past the
	//     original 30-day expires_at.
	//   - Idempotency keys: 24h TTL per row, lots of rows for high-
	//     traffic SDK endpoints (activate / usage / floating-checkout).
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				db.CleanExpiredOTPs(context.Background())
				db.CleanExpiredRefreshTokens(context.Background())
				_, _ = db.IdempotencyPruneExpired(context.Background())
			}
		}
	}()

	// Periodic Stripe checkout sync — catches missed webhooks
	if cfg.StripeSecretKey != "" {
		go func() {
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					stripeH.SyncRecentCheckouts(ctx)
				}
			}
		}()
	}

	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := db.TakeAllSnapshots(context.Background(), time.Now().Add(-24*time.Hour)); err != nil {
					logger.Error("analytics snapshot failed", "error", err)
				}
			}
		}
	}()

	if cfg.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.Default()

	// Trust reverse proxy headers (Traefik, Nginx, etc.) to get real client IP.
	// Trusts X-Forwarded-For / X-Real-Ip from private network ranges.
	r.SetTrustedProxies([]string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"})
	r.RemoteIPHeaders = []string{"X-Forwarded-For", "X-Real-Ip"}

	r.Use(middleware.RequestID())
	r.Use(middleware.PrometheusMetrics())

	// Security headers & attribution (AGPL v3 Section 7b — see NOTICE)
	r.Use(func(c *gin.Context) {
		c.Header(branding.HeaderKey, branding.Project)
		c.Header("X-Frame-Options", "DENY")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-XSS-Protection", "1; mode=block")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		if strings.HasPrefix(cfg.BaseURL, "https://") {
			c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains; preload")
		}
		c.Next()
	})

	r.Use(func(c *gin.Context) {
		if origin := c.GetHeader("Origin"); origin != "" {
			if cfg.IsProduction() && origin != cfg.BaseURL {
				if c.Request.Method == "OPTIONS" {
					c.AbortWithStatus(http.StatusForbidden)
					return
				}
				c.Next()
				return
			}
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Authorization,Content-Type")
			c.Header("Access-Control-Allow-Credentials", "true")
			if c.Request.Method == "OPTIONS" {
				c.AbortWithStatus(http.StatusNoContent)
				return
			}
		}
		c.Next()
	})

	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	r.GET("/health", func(c *gin.Context) {
		status := "ok"
		checks := gin.H{}

		// DB check
		if err := db.DB.PingContext(c.Request.Context()); err != nil {
			status = "degraded"
			checks["database"] = "error: " + err.Error()
		} else {
			checks["database"] = "ok"
		}

		code := http.StatusOK
		if status != "ok" {
			code = http.StatusServiceUnavailable
		}
		c.JSON(code, gin.H{"status": status, "checks": checks, "version": version.Version})
	})

	// API documentation (public, read-only)
	r.GET("/docs", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(`<!DOCTYPE html>
<html><head>
<title>Keygate API</title>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width,initial-scale=1"/>
</head><body>
<script id="api-reference" data-url="/docs/openapi.yaml" data-configuration='{"hideTryIt":true}'></script>
<script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
</body></html>`))
	})
	r.StaticFile("/docs/openapi.yaml", "docs/openapi.yaml")

	v1 := r.Group("/api/v1")

	v1.GET("/version", systemH.GetVersion)

	setupH := handler.NewSetupHandler(db)
	v1.GET("/setup/status", setupH.Status)
	v1.POST("/setup/initialize", setupH.Initialize)

	// Public site config (no auth — used by login page, branding)
	v1.GET("/config", func(c *gin.Context) {
		settings, _ := db.GetPublicSettings(c)
		if settings == nil {
			settings = make(map[string]string)
		}
		// Attribution: AGPL v3 Section 7(b) — see NOTICE
		settings["attribution_text"] = branding.Tagline
		settings["attribution_url"] = branding.URL
		response.OK(c, settings)
	})

	// License-verification public key. SDKs fetch this once (or
	// hardcode it after first run) to verify the offline ed25519
	// token they get back from /license/activate and /license/verify.
	// Long-lived so it can be cached aggressively; rotation is an
	// operator action that forces clients to re-fetch.
	licensePubHex := hex.EncodeToString(licenseSvc.SigningPublicKey())
	v1.GET("/license/pubkey", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=3600")
		response.OK(c, gin.H{
			"algorithm":  "ed25519",
			"public_key": licensePubHex,
			"format":     "hex",
		})
	})

	// Public license endpoints.
	//
	// Auth model: license_key in body IS the credential. We dropped the
	// API-key gate because api_keys embedded in shipped client binaries
	// are not real secrets — anyone with the binary can extract them, so
	// requiring them was security theater while adding integration
	// friction. Tenant scoping still holds: each license row carries a
	// product_id FK, so a license_key can only operate on its own
	// product's data.
	//
	// Defenses kept:
	//   - LicenseBruteForceGuard — per-IP throttle on failed verifies
	//   - RateLimitByIP — aggregate request cap (raised because legitimate
	//     SDK clients poll on a timer)
	//
	// api_keys table + admin CRUD remain for server-to-server use cases
	// (a customer's backend querying license state).
	licRateLimit := max(cfg.RateLimitAPI*2, 120)
	lic := v1.Group("/license",
		middleware.LicenseBruteForceGuard(bf),
		middleware.RateLimitByIP(licRateLimit, time.Minute))
	{
		// Idempotent-by-design endpoints (apply Idempotency-Key middleware
		// to write paths only — read-style verifies are already idempotent).
		//
		// Scope rule for this group: license_key is the SDK credential
		// — it identifies a device for verify / activate / usage /
		// download. It is NOT a seat-management credential. Seat
		// add/remove/list moved to /portal/seats/* (session auth on
		// the logged-in customer); seat-invite acceptance moved to
		// /invites/accept (public, opaque-token only). The reason is
		// trust-model alignment: a license_key sitting on the user's
		// laptop shouldn't be able to silently change the org's
		// member roster.
		idem := middleware.Idempotency(db)
		lic.POST("/activate", idem, licenseH.Activate)
		lic.POST("/usage", idem, usageH.RecordUsage)
		lic.POST("/floating/checkout", idem, floatingH.CheckOut)

		// Read / pure-status endpoints — no idempotency layer needed.
		lic.POST("/verify", licenseH.Verify)
		lic.POST("/deactivate", licenseH.Deactivate)
		lic.POST("/entitlements", entitlementH.Check)
		lic.POST("/usage/status", usageH.GetQuotaStatus)
		lic.POST("/floating/checkin", floatingH.CheckIn)
		lic.POST("/floating/heartbeat", floatingH.Heartbeat)
		lic.POST("/download", releasePublicH.Download)
	}

	// Public invite acceptance — the token is proof of email
	// ownership (we mailed it to the invitee), so no session auth
	// is required, but we DO want rate limiting against token
	// guessing. Live outside /license/* on purpose: this endpoint
	// has nothing to do with the SDK identity.
	v1.POST("/invites/accept",
		middleware.RateLimitByIP(60, time.Minute),
		authH.AcceptInvite(seatSvc))

	// Release feed endpoints.
	//
	// Auth model: all channels (stable / beta / alpha / dev) are public.
	// Trust comes from the artifact's ed25519 signature, verified by
	// Sparkle/Velopack/Tauri on the client — not from URL secrecy.
	// License enforcement happens at app start (POST /license/verify),
	// not at update-feed time. To keep a pre-release channel private,
	// don't publish to it; once published, treat the feed URL as public.
	//
	// Feed clients poll on a timer — sometimes once a minute. We give
	// the per-IP bucket 4× the regular API limit so a single host
	// running multiple installed products doesn't trip the 60/min default.
	feedRateLimit := max(cfg.RateLimitAPI*4, 240)
	feedMW := []gin.HandlerFunc{
		middleware.RateLimitByIP(feedRateLimit, time.Minute),
	}
	v1.GET("/releases/:product_slug/feed.xml", append(feedMW, releasePublicH.FeedSparkle)...)
	v1.GET("/releases/:product_slug/feed.json", append(feedMW, releasePublicH.FeedVelopack)...)
	v1.GET("/releases/:product_slug/upgrade.json", append(feedMW, releasePublicH.FeedTauri)...)

	// Old /releases/feed.* (no product slug, license-key auth) → 410 Gone
	// with a migration hint. Pre-launch hard cutover; no live clients.
	feedGone := func(c *gin.Context) {
		response.Err(c, http.StatusGone, "FEED_PATH_REMOVED",
			"feed URL changed: use /api/v1/releases/{product_slug}/feed.xml|feed.json|upgrade.json. "+
				"Feeds are public; trust is established via the artifact's ed25519 signature.")
	}
	v1.GET("/releases/feed.xml", feedGone)
	v1.GET("/releases/feed.json", feedGone)
	v1.GET("/releases/upgrade.json", feedGone)
	v1.GET("/releases/feed", feedGone)

	auth := v1.Group("/auth", middleware.RateLimitByIP(cfg.RateLimitAuth, time.Minute))
	{
		auth.GET("/providers", authH.Providers)
		auth.POST("/otp/send", authH.OTPSend)
		auth.POST("/otp/verify", authH.OTPVerify)
		auth.POST("/dev-login", authH.DevLogin)
		auth.POST("/logout", middleware.SessionAuth(cfg.JWTSecret, db.FindUserIsAdmin), authH.Logout)
		auth.POST("/refresh", authH.Refresh)
	}

	v1.POST("/webhook/stripe", middleware.RateLimitByIP(60, time.Minute), stripeH.Webhook)
	// Stripe verify is hit by every successful checkout return, so the
	// limit is generous, but the endpoint must NOT be naked: each call
	// proxies to Stripe's API and an attacker could otherwise force us
	// to burn rate-budget against Stripe.
	v1.GET("/checkout/verify",
		middleware.RateLimitByIP(60, time.Minute),
		stripeH.VerifyCheckoutSession)

	// Unified checkout: GET /pay/:checkout_id → Stripe
	r.GET("/pay/:checkout_id", stripeH.CheckoutByPlan)

	portal := v1.Group("/portal", middleware.SessionAuth(cfg.JWTSecret, db.FindUserIsAdmin))
	{
		portal.GET("/me", authH.Me)
		portal.GET("/licenses", func(c *gin.Context) {
			emailVal, _ := c.Get("email")
			emailStr, ok := emailVal.(string)
			if !ok || emailStr == "" {
				response.Unauthorized(c, "unauthorized")
				return
			}
			licenses, err := db.ListLicensesByEmail(c, emailStr)
			if err != nil {
				response.Internal(c)
				return
			}
			response.OK(c, gin.H{"licenses": licenses})
		})
		portal.PUT("/profile", func(c *gin.Context) {
			userID, _ := c.Get("user_id")
			uid, ok := userID.(string)
			if !ok || uid == "" {
				response.Unauthorized(c, "unauthorized")
				return
			}

			var req struct {
				Name string `json:"name"`
			}
			if err := c.ShouldBindJSON(&req); err != nil {
				response.BadRequest(c, "invalid request")
				return
			}

			// Sanitize: trim whitespace, strip HTML tags, limit length
			name := strings.TrimSpace(req.Name)
			// Strip HTML tags to prevent XSS
			name = stripHTMLTags(name)
			if len(name) > 100 {
				response.BadRequest(c, "name too long (max 100 characters)")
				return
			}

			if err := db.UpdateUserProfile(c, uid, name); err != nil {
				response.Internal(c)
				return
			}

			user, err := db.FindUserByID(c, uid)
			if err != nil {
				response.Internal(c)
				return
			}

			response.OK(c, gin.H{
				"id": user.ID, "email": user.Email, "name": user.Name,
				"avatar_url": user.AvatarURL, "role": user.Role,
			})
		})
		portal.GET("/plans", func(c *gin.Context) {
			productID := c.Query("product_id")
			if productID == "" {
				response.BadRequest(c, "product_id is required")
				return
			}
			plans, err := db.ListPlans(c, productID, "")
			if err != nil {
				response.Internal(c)
				return
			}
			// Only return active plans with public info
			var active []gin.H
			for _, p := range plans {
				if p.Active {
					active = append(active, gin.H{
						"id": p.ID, "name": p.Name, "slug": p.Slug,
						"license_type": p.LicenseType, "billing_interval": p.BillingInterval,
						"stripe_price_id": p.StripePriceID, "checkout_id": p.CheckoutID,
					})
				}
			}
			response.OK(c, gin.H{"plans": active})
		})
		// Both portal guards parse the request body to extract
		// license_key. Without an explicit cap a logged-in user could
		// stream gigabytes through the body buffer; session auth is a
		// weak defense against compromised credentials sending
		// memory-exhaustion payloads. 64 KiB is comfortably above any
		// real portal request (license_key + a handful of fields).
		const portalGuardMaxBody = 64 * 1024

		// readGuardBody is shared by both guards: cap the body, read
		// it, restore an in-memory reader for the downstream handler
		// (so c.ShouldBindJSON still sees the same bytes).
		readGuardBody := func(c *gin.Context) ([]byte, bool) {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, portalGuardMaxBody)
			body, err := io.ReadAll(c.Request.Body)
			if err != nil {
				response.Err(c, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE",
					"request body exceeds 64 KiB")
				c.Abort()
				return nil, false
			}
			c.Request.Body = io.NopCloser(bytes.NewBuffer(body))
			return body, true
		}

		// Portal license operations — verify the license belongs to the logged-in user
		portalLicenseGuard := func(c *gin.Context) {
			body, ok := readGuardBody(c)
			if !ok {
				return
			}
			var req struct {
				LicenseKey string `json:"license_key"`
			}
			if json.Unmarshal(body, &req) != nil || req.LicenseKey == "" {
				c.Next()
				return
			}
			email, _ := c.Get("email")
			emailStr, _ := email.(string)
			if emailStr == "" {
				response.Unauthorized(c, "unauthorized")
				c.Abort()
				return
			}
			// Check license belongs to user (by email or seat). Use
			// EqualFold so mixed-case email rows don't lock the owner
			// out of their own license — emails are case-insensitive
			// per RFC 5321 in practice, and admin-created licenses
			// may have stored mixed case.
			lic, err := db.FindLicenseByKey(c, req.LicenseKey)
			if err != nil {
				c.Next() // let handler return proper 404
				return
			}
			if !strings.EqualFold(lic.Email, emailStr) {
				// Check if user has a seat on this license. Return
				// the same 404 the handler returns when the license
				// doesn't exist — otherwise the differential between
				// 403 (exists but yours) and 404 (doesn't exist) is
				// a license-existence oracle. License keys are 32-
				// byte random so the keyspace defeats enumeration in
				// practice, but the oracle is gratuitous.
				if _, err := db.FindSeatByEmail(c, lic.ID, emailStr); err != nil {
					response.NotFound(c, "license not found")
					c.Abort()
					return
				}
			}
			c.Next()
		}
		// Seat mutation guard — only license owner or accepted admin seat can add/remove seats
		portalSeatMutationGuard := func(c *gin.Context) {
			body, ok := readGuardBody(c)
			if !ok {
				return
			}
			var req struct {
				LicenseKey string `json:"license_key"`
			}
			if json.Unmarshal(body, &req) != nil || req.LicenseKey == "" {
				c.Next()
				return
			}
			email, _ := c.Get("email")
			emailStr, _ := email.(string)
			// Defense in depth: a malformed JWT that produced an empty
			// email claim must not fall through to seat lookups with
			// "" — that would only match seats whose email column is
			// literally empty (forbidden by schema, but belt-and-
			// suspenders).
			if emailStr == "" {
				response.Unauthorized(c, "unauthorized")
				c.Abort()
				return
			}
			lic, err := db.FindLicenseByKey(c, req.LicenseKey)
			if err != nil {
				c.Next()
				return
			}
			// License owner can always manage seats. EqualFold for the
			// same case-folding reason as portalLicenseGuard.
			if strings.EqualFold(lic.Email, emailStr) {
				c.Next()
				return
			}
			// Seat-based access: only ACCEPTED admin can mutate. Seat
			// roles are 2-tier (admin / member) — the license owner is
			// implicit via license.email and was already matched above.
			// A still-pending admin invite confers no authority until
			// the invitee has explicitly claimed the seat — otherwise
			// possession of an unclaimed admin token would let an
			// OTP-logged-in user act as admin on the license without
			// going through the accept flow.
			//
			// Same shape as the 404 the handler returns when the
			// license doesn't exist, so this middleware doesn't act
			// as a "this license exists but isn't yours" oracle.
			seat, err := db.FindSeatByEmail(c, lic.ID, emailStr)
			if err != nil || seat.AcceptedAt == nil || seat.Role != "admin" {
				response.NotFound(c, "license not found")
				c.Abort()
				return
			}
			c.Next()
		}
		portal.POST("/usage", portalLicenseGuard, usageH.RecordUsage)
		portal.POST("/usage/status", portalLicenseGuard, usageH.GetQuotaStatus)
		portal.POST("/seats", portalLicenseGuard, seatH.ListSeats)
		portal.POST("/seats/add", portalSeatMutationGuard, seatH.AddSeat)
		portal.POST("/seats/remove", portalSeatMutationGuard, seatH.RemoveSeat)

		// Self-service activation management — solves the "I lost my
		// laptop, my activation slot is stuck" support ticket.
		portalActH := handler.NewPortalActivationsHandler(db)
		portal.GET("/licenses/:license_key/activations", portalActH.List)
		portal.DELETE("/licenses/:license_key/activations/:activation_id", portalActH.Delete)

		// Subscription self-service. The handlers already verify
		// email ownership of the target license against the session
		// (see stripe.go), so nesting under /portal makes the URL
		// match the trust model rather than reading like a system
		// endpoint sitting at the top level.
		portal.POST("/subscription/change-plan", stripeH.ChangePlan)
		portal.POST("/subscription/cancel", stripeH.CancelSubscription)
		portal.POST("/subscription/billing-portal", stripeH.CreatePortalSession)
		portal.GET("/subscription/invoices", stripeH.ListInvoices)
	}

	// Admin route layout: three groups under /admin, all sharing the
	// same Session-or-APIKey + rate-limit middlewares, differing only
	// in which scope(s) RequireScope accepts.
	//
	//   - admin: routes that only an interactive admin (or a key with
	//     the `admin` wildcard scope) can hit. Everything not in the
	//     other two buckets defaults here.
	//   - licWrite: license CRUD + lifecycle actions. Also reachable
	//     by API keys with `licenses:write`. Intended for merchant
	//     backends that provision licenses from their own checkout.
	//   - relWrite: release CRUD + artifact upload + publish/yank.
	//     Also reachable by `releases:write` keys for CI/CD that
	//     publishes builds.
	//
	// Signing key endpoints stay admin-only — rotating product
	// signing material is a one-off operator action, not a CI/CD
	// concern, and a leaked CI key shouldn't be able to swap the
	// product's release-signing identity.
	baseAdminMW := []gin.HandlerFunc{
		middleware.SessionOrAPIKey(cfg.JWTSecret, db, db.FindUserIsAdmin),
		middleware.RateLimitByIP(cfg.RateLimitAdmin, time.Minute),
	}
	adminMW := append([]gin.HandlerFunc{}, baseAdminMW...)
	adminMW = append(adminMW, middleware.RequireScope(model.ScopeAdmin))

	licWriteMW := append([]gin.HandlerFunc{}, baseAdminMW...)
	licWriteMW = append(licWriteMW, middleware.RequireScope(model.ScopeAdmin, model.ScopeLicensesWrite))

	relWriteMW := append([]gin.HandlerFunc{}, baseAdminMW...)
	relWriteMW = append(relWriteMW, middleware.RequireScope(model.ScopeAdmin, model.ScopeReleasesWrite))

	admin := v1.Group("/admin", adminMW...)
	licWrite := v1.Group("/admin", licWriteMW...)
	relWrite := v1.Group("/admin", relWriteMW...)
	{
		admin.GET("/stats", adminH.Stats)

		admin.GET("/products", adminH.ListProducts)
		admin.GET("/products/:id", adminH.GetProduct)
		admin.POST("/products", adminH.CreateProduct)
		admin.PUT("/products/:id", adminH.UpdateProduct)
		admin.DELETE("/products/:id", adminH.DeleteProduct)

		admin.GET("/plans", adminH.ListPlans)
		admin.GET("/plans/:id", adminH.GetPlan)
		admin.POST("/plans", adminH.CreatePlan)
		admin.PUT("/plans/:id", adminH.UpdatePlan)
		admin.DELETE("/plans/:id", adminH.DeletePlan)

		admin.POST("/entitlements", adminH.CreateEntitlement)
		admin.PUT("/entitlements/:id", adminH.UpdateEntitlement)
		admin.DELETE("/entitlements/:id", adminH.DeleteEntitlement)

		// ─── License CRUD + lifecycle: also reachable by licenses:write keys ───
		licWrite.GET("/licenses", adminH.ListLicenses)
		licWrite.GET("/licenses/export", adminH.ExportLicenses)
		licWrite.GET("/licenses/:id", adminH.GetLicense)
		licWrite.POST("/licenses", adminH.CreateLicense)
		licWrite.POST("/licenses/:id/refund", adminH.RefundLicense)
		licWrite.POST("/licenses/:id/revoke", adminH.RevokeLicense)
		licWrite.POST("/licenses/:id/suspend", adminH.SuspendLicense)
		licWrite.POST("/licenses/:id/reinstate", adminH.ReinstateLicense)
		licWrite.POST("/licenses/:id/valid-until", adminH.SetLicenseValidUntil)
		licWrite.POST("/licenses/:id/support-until", adminH.SetLicenseSupportUntil)
		licWrite.POST("/licenses/:id/change-plan", adminH.ChangeLicensePlan)
		licWrite.GET("/licenses/:id/usage", adminH.ListLicenseUsage)
		licWrite.POST("/licenses/:id/usage/reset", adminH.ResetLicenseUsage)
		licWrite.GET("/licenses/:id/seats", adminH.ListLicenseSeats)

		licWrite.GET("/licenses/:id/addons", adminH.ListLicenseAddons)
		licWrite.POST("/licenses/:id/addons", adminH.AddLicenseAddon)
		licWrite.DELETE("/licenses/:id/addons/:addon_id", adminH.RemoveLicenseAddon)
		licWrite.GET("/licenses/:id/floating", adminH.ListFloatingSessions)

		licWrite.DELETE("/activations/:id", adminH.DeleteActivation)

		admin.GET("/api-keys", adminH.ListAPIKeys)
		admin.POST("/api-keys", adminH.CreateAPIKey)
		admin.POST("/api-keys/:id/rotate", adminH.RotateAPIKey)
		admin.DELETE("/api-keys/:id", adminH.DeleteAPIKey)

		admin.GET("/webhooks", webhookAdminH.ListWebhooks)
		admin.POST("/webhooks", webhookAdminH.CreateWebhook)
		admin.PUT("/webhooks/:id", webhookAdminH.UpdateWebhook)
		admin.DELETE("/webhooks/:id", webhookAdminH.DeleteWebhook)
		admin.GET("/webhooks/:id/deliveries", webhookAdminH.ListDeliveries)
		admin.GET("/webhooks/:id/deliveries/:delivery_id", webhookAdminH.GetDelivery)
		admin.POST("/webhooks/:id/deliveries/:delivery_id/resend", webhookAdminH.ResendDelivery)
		admin.POST("/webhooks/:id/test", webhookAdminH.TestWebhook)

		admin.GET("/addons", adminH.ListAddons)
		admin.POST("/addons", adminH.CreateAddon)
		admin.PUT("/addons/:id", adminH.UpdateAddon)
		admin.DELETE("/addons/:id", adminH.DeleteAddon)

		admin.GET("/settings", adminH.GetSettings)
		admin.PUT("/settings", adminH.UpdateSettings)
		admin.POST("/settings/test-email", adminH.SendTestEmail)
		admin.POST("/system/run-expiry-checks", adminH.RunExpiryChecks)
		admin.POST("/system/run-metered-sync", adminH.RunMeteredSync)
		admin.GET("/email-templates", adminH.GetEmailTemplates)

		admin.GET("/team", adminH.ListTeamMembers)
		admin.POST("/team", adminH.InviteTeamMember)
		admin.DELETE("/team/:id", adminH.RemoveTeamMember)

		admin.GET("/system/update-check", systemH.CheckUpdate)
		admin.GET("/system/migrations", systemH.GetMigrationStatus)

		admin.GET("/analytics", adminH.ListAnalytics)
		admin.GET("/analytics/summary", adminH.AnalyticsSummary)
		admin.GET("/analytics/breakdown", adminH.AnalyticsBreakdown)
		admin.GET("/analytics/usage-top", adminH.AnalyticsUsageTop)
		admin.GET("/analytics/activation-trend", adminH.AnalyticsActivationTrend)
		admin.GET("/analytics/insights", adminH.AnalyticsInsights)
		admin.GET("/audit-logs", adminH.ListAuditLogs)
		admin.GET("/users", adminH.ListUsers)
		admin.GET("/users/:id", adminH.GetUserDetail)

		// ─── Releases (industry-standard bundle model) ───
		// Resource: release with multiple platform artifacts (mirrors
		// GitHub Releases / Keygen). Action endpoints under /actions/.
		// Reachable by `releases:write` keys so CI/CD can ship builds
		// without holding the admin wildcard.
		relWrite.GET("/releases", releaseAdminH.List)
		relWrite.GET("/releases/:id", releaseAdminH.Get)
		relWrite.POST("/releases", releaseAdminH.Create)
		relWrite.PATCH("/releases/:id", releaseAdminH.Update)
		// Backward-compat shim: old PATCH /releases/:id/notes — drop after one cycle.
		relWrite.PATCH("/releases/:id/notes", releaseAdminH.Update)
		relWrite.DELETE("/releases/:id", releaseAdminH.Delete)

		relWrite.POST("/releases/:id/artifacts", releaseAdminH.AddArtifact)
		relWrite.POST("/releases/:id/artifacts/:aid/finalize", releaseAdminH.FinalizeArtifact)
		relWrite.DELETE("/releases/:id/artifacts/:aid", releaseAdminH.DeleteArtifact)

		relWrite.POST("/releases/:id/actions/publish", releaseAdminH.Publish)
		relWrite.POST("/releases/:id/actions/yank", releaseAdminH.Yank)
		relWrite.POST("/releases/:id/actions/unyank", releaseAdminH.Unyank)

		// ─── Release signing keys (per product) ───
		admin.POST("/products/:id/signing-key", releaseSigningH.Generate)
		admin.POST("/products/:id/signing-key/rotate", releaseSigningH.Rotate)
		admin.DELETE("/products/:id/signing-key", releaseSigningH.Deactivate)
		admin.GET("/products/:id/signing-keys", releaseSigningH.List)
		admin.GET("/products/:id/signing-key/public.pem", releaseSigningH.DownloadPublicKey)
		admin.GET("/products/:id/signing-key/tauri-pubkey", releaseSigningH.DownloadPublicKeyTauri)

	}

	serveFrontend(r)

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: r,
	}

	go func() {
		log.Printf("Keygate starting on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	cancel() // cancel background context — stops all goroutines

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("server forced shutdown: %v", err)
	}
	log.Println("Server exited gracefully")
}

// stripHTMLTags removes all HTML tags from a string to prevent XSS.
func stripHTMLTags(s string) string {
	var result strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// serveFrontend serves the React SPA from web/dist if it exists.
func serveFrontend(r *gin.Engine) {
	distPath := "web/dist"
	if _, err := os.Stat(distPath); os.IsNotExist(err) {
		return
	}

	indexHTML, err := os.ReadFile(distPath + "/index.html")
	if err != nil {
		log.Printf("WARNING: web/dist exists but index.html not found: %v", err)
		return
	}

	r.Use(func(c *gin.Context) {
		path := c.Request.URL.Path

		// Let backend routes pass through.
		if strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/pay/") || path == "/health" || path == "/metrics" || path == "/docs" || strings.HasPrefix(path, "/docs/") {
			c.Next()
			return
		}

		// Try to serve a static file using path.Clean to prevent traversal.
		// Uses c.File() instead of c.FileFromFS() to avoid http.FileServer's
		// implicit 301 redirects (e.g. /index.html → /).
		if clean := filepath.Clean(path); clean != "/" && clean != "/index.html" {
			filePath := filepath.Join(distPath, clean)
			if info, err := os.Stat(filePath); err == nil && !info.IsDir() {
				c.File(filePath)
				c.Abort()
				return
			}
		}

		// SPA fallback: serve cached index.html for all other routes.
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML)
		c.Abort()
	})
}
