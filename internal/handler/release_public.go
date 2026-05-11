package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/service"
	"github.com/tabloy/keygate/internal/storage"
	"github.com/tabloy/keygate/internal/store"
	"github.com/tabloy/keygate/pkg/response"
)

// ReleasePublicHandler exposes the endpoints clients use:
//
//	POST /api/v1/license/download                              license-gated download URL
//	GET  /api/v1/releases/:product_slug/feed.xml               Sparkle appcast (per-platform)
//	GET  /api/v1/releases/:product_slug/feed.json              Velopack feed (per-platform)
//	GET  /api/v1/releases/:product_slug/upgrade.json           Tauri manifest (single latest)
//
// Auth model: feeds are PUBLIC for every channel — stable, beta,
// alpha, dev. Industry convention (Sparkle / Tauri / npm / GitHub
// Releases): trust = the artifact's Ed25519 signature, NOT URL secrecy.
// Genuinely private builds belong on internal CI/CD distribution
// (private R2 bucket, TestFlight, Firebase App Distribution); they
// don't ship through these feeds at all.
//
// License gating happens at app start (the SDK calls /license/verify),
// not at update time. License rotation never bricks installed clients.
type ReleasePublicHandler struct {
	svc         *service.ReleaseService
	store       *store.Store
	storage     storage.Storage
	logger      *slog.Logger
	baseURL     string
	downloadTTL time.Duration
}

type ReleasePublicConfig struct {
	Service     *service.ReleaseService
	Store       *store.Store
	Storage     storage.Storage
	Logger      *slog.Logger
	BaseURL     string
	DownloadTTL time.Duration
}

func NewReleasePublicHandler(c ReleasePublicConfig) *ReleasePublicHandler {
	if c.Storage == nil {
		c.Storage = storage.Disabled{}
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.DownloadTTL <= 0 {
		c.DownloadTTL = 10 * time.Minute
	}
	return &ReleasePublicHandler{
		svc:         c.Service,
		store:       c.Store,
		storage:     c.Storage,
		logger:      c.Logger,
		baseURL:     c.BaseURL,
		downloadTTL: c.DownloadTTL,
	}
}

// POST /api/v1/license/download
//
// Body: { license_key, platform, version?, channel? }
func (h *ReleasePublicHandler) Download(c *gin.Context) {
	var req struct {
		LicenseKey string `json:"license_key" binding:"required"`
		Platform   string `json:"platform" binding:"required"`
		Version    string `json:"version"`
		Channel    string `json:"channel"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "license_key and platform are required")
		return
	}
	productID, _ := c.Get("product_id")
	out, err := h.svc.GenerateDownload(c.Request.Context(), service.DownloadInput{
		LicenseKey: req.LicenseKey,
		ProductID:  str(productID),
		Version:    req.Version,
		Platform:   req.Platform,
		Channel:    req.Channel,
	})
	if err != nil {
		writeAppErr(c, err)
		return
	}
	response.OK(c, out)
}

// ─── Feed endpoints (one per format) ───

// feedRequest captures the validated context for a single feed request.
type feedRequest struct {
	product  *model.Product
	platform string
	channel  string
	limit    int
}

// parseFeedRequest validates path + query params. All channels (stable
// / beta / alpha / dev) are public — trust is established by the
// artifact's ed25519 signature, not by URL secrecy. On any error the
// response is already written and ok=false.
func (h *ReleasePublicHandler) parseFeedRequest(c *gin.Context) (req feedRequest, ok bool) {
	slug := strings.ToLower(strings.TrimSpace(c.Param("product_slug")))
	if slug == "" {
		response.BadRequest(c, "product_slug is required in the URL path")
		return
	}
	prod, err := h.store.FindProductBySlug(c.Request.Context(), slug)
	if err != nil {
		// Don't differentiate "no such product" from "other DB error" so
		// guesses at slugs leak nothing — 404 either way.
		response.NotFound(c, "product not found")
		return
	}
	// Capability gate: saas products don't ship installable binaries,
	// so the feed endpoints are off. Return 404 (same as "no product")
	// instead of 403 — the feed simply doesn't exist for this product.
	if !model.ProductSupports(prod.Type, model.CapReleases) {
		response.NotFound(c, "product not found")
		return
	}

	platform := c.Query("platform")
	if platform == "" {
		response.BadRequest(c, "platform is required")
		return
	}
	if !service.IsValidPlatform(platform) {
		response.BadRequest(c,
			"platform must be one of: "+strings.Join(service.AllowedPlatforms(), ", "))
		return
	}

	channel := c.DefaultQuery("channel", model.ReleaseChannelStable)
	if !model.IsValidReleaseChannel(channel) {
		response.BadRequest(c, "channel must be stable, beta, alpha, or dev")
		return
	}

	limit := 20
	if v := c.Query("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 100 {
			response.BadRequest(c, "limit must be an integer between 1 and 100")
			return
		}
		limit = n
	}
	return feedRequest{product: prod, platform: platform, channel: channel, limit: limit}, true
}

// fetchPublishedFeedReleases pulls releases + filters their artifacts to
// the requested platform. Returns the filtered slice + per-product feed
// metadata. ok=false means the response has already been written.
//
// Each artifact's signing public key is resolved (cached per-key-id
// within the loop) so renderers can build format-specific signature
// envelopes (Tauri minisign requires the pubkey to derive its key_id).
func (h *ReleasePublicHandler) fetchPublishedFeedReleases(c *gin.Context, req feedRequest) ([]*service.FeedRelease, bool) {
	releases, err := h.svc.ListForFeed(c.Request.Context(), req.product.ID, req.channel, req.platform, req.limit)
	if err != nil {
		writeAppErr(c, err)
		return nil, false
	}

	pubKeyCache := map[string]string{} // signing_key_id → base64 pubkey
	out := make([]*service.FeedRelease, 0, len(releases))
	for _, rel := range releases {
		// Pick the artifact for this platform. If the release has no
		// matching artifact (e.g. a stable release that didn't ship for
		// linux-armhf), skip it for this platform's feed.
		var artifact *model.ReleaseArtifact
		for _, a := range rel.Artifacts {
			if a.Platform == req.platform && a.IsUploaded() {
				artifact = a
				break
			}
		}
		if artifact == nil {
			continue
		}
		url, err := h.storage.PresignedGet(c.Request.Context(), artifact.FileKey,
			service.DownloadFilename(rel, artifact), h.downloadTTL)
		if err != nil {
			if errors.Is(err, storage.ErrStorageDisabled) {
				response.Err(c, http.StatusServiceUnavailable, "STORAGE_DISABLED",
					"release storage is not configured")
				return nil, false
			}
			h.logger.Error("feed: presign failed",
				"release_id", rel.ID, "artifact_id", artifact.ID, "error", err)
			continue
		}

		var signingPubKey string
		if artifact.SigningKeyID != "" {
			if cached, ok := pubKeyCache[artifact.SigningKeyID]; ok {
				signingPubKey = cached
			} else {
				k, err := h.store.FindSigningKeyByID(c.Request.Context(), artifact.SigningKeyID)
				if err == nil {
					signingPubKey = k.PublicKey
					pubKeyCache[artifact.SigningKeyID] = signingPubKey
				} else {
					// Key was deleted post-publish. Leave empty;
					// renderers will emit unsigned manifests.
					pubKeyCache[artifact.SigningKeyID] = ""
				}
			}
		}

		out = append(out, &service.FeedRelease{
			Release:          rel,
			Artifact:         artifact,
			DownloadURL:      url,
			SigningPublicKey: signingPubKey,
		})
	}
	return out, true
}

func (h *ReleasePublicHandler) feedInput(req feedRequest, releases []*service.FeedRelease) service.FeedInput {
	return service.FeedInput{
		ProductID:               req.product.ID,
		ProductName:             req.product.Name,
		BaseURL:                 h.baseURL,
		Releases:                releases,
		MinimumSupportedVersion: req.product.MinimumSupportedVersion,
		MinimumSupportedMessage: req.product.MinimumSupportedMessage,
	}
}

// GET /api/v1/releases/:product_slug/feed.xml — Sparkle appcast
func (h *ReleasePublicHandler) FeedSparkle(c *gin.Context) {
	req, ok := h.parseFeedRequest(c)
	if !ok {
		return
	}
	feedReleases, ok := h.fetchPublishedFeedReleases(c, req)
	if !ok {
		return
	}
	body, err := service.RenderSparkle(h.feedInput(req, feedReleases))
	if err != nil {
		h.logger.Error("feed: sparkle render failed", "error", err)
		response.Internal(c)
		return
	}
	h.writeFeedCacheHeaders(c, req.channel)
	c.Data(http.StatusOK, "application/xml; charset=utf-8", body)
}

// GET /api/v1/releases/:product_slug/feed.json — Velopack feed
func (h *ReleasePublicHandler) FeedVelopack(c *gin.Context) {
	req, ok := h.parseFeedRequest(c)
	if !ok {
		return
	}
	feedReleases, ok := h.fetchPublishedFeedReleases(c, req)
	if !ok {
		return
	}
	body, err := json.Marshal(service.BuildVelopack(h.feedInput(req, feedReleases)))
	if err != nil {
		h.logger.Error("feed: velopack marshal failed", "error", err)
		response.Internal(c)
		return
	}
	h.writeFeedCacheHeaders(c, req.channel)
	c.Data(http.StatusOK, "application/json; charset=utf-8", body)
}

// GET /api/v1/releases/:product_slug/upgrade.json — Tauri (single-release manifest)
func (h *ReleasePublicHandler) FeedTauri(c *gin.Context) {
	req, ok := h.parseFeedRequest(c)
	if !ok {
		return
	}
	// Tauri always wants exactly the latest. Pull only 1 release.
	req.limit = 1
	feedReleases, ok := h.fetchPublishedFeedReleases(c, req)
	if !ok {
		return
	}
	manifest := service.BuildTauri(h.feedInput(req, feedReleases))
	if manifest.Version == "" {
		// Even with no published release we want the policy fields to
		// reach the client so it can enforce minimum_supported_version.
		if req.product.MinimumSupportedVersion != "" {
			h.writeFeedCacheHeaders(c, req.channel)
			c.JSON(http.StatusOK, gin.H{
				"version":                   "",
				"minimum_supported_version": req.product.MinimumSupportedVersion,
				"minimum_supported_message": req.product.MinimumSupportedMessage,
			})
			return
		}
		c.Status(http.StatusNoContent)
		return
	}
	h.writeFeedCacheHeaders(c, req.channel)
	c.JSON(http.StatusOK, manifest)
}

// writeFeedCacheHeaders sets Cache-Control. All channels are public —
// CDNs / ISP proxies may safely serve them.
func (h *ReleasePublicHandler) writeFeedCacheHeaders(c *gin.Context, _ string) {
	c.Header("Cache-Control", "public, max-age=60")
}
