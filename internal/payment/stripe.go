package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v82"
	portalsession "github.com/stripe/stripe-go/v82/billingportal/session"
	"github.com/stripe/stripe-go/v82/checkout/session"
	stripecustomer "github.com/stripe/stripe-go/v82/customer"
	stripeinvoice "github.com/stripe/stripe-go/v82/invoice"
	stripeprice "github.com/stripe/stripe-go/v82/price"
	"github.com/stripe/stripe-go/v82/subscription"
	"github.com/stripe/stripe-go/v82/webhook"

	"github.com/tabloy/keygate/internal/license"
	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/service"
	"github.com/tabloy/keygate/internal/store"
	"github.com/tabloy/keygate/pkg/response"
)

type StripeHandler struct {
	Store         *store.Store
	WebhookSecret string // initial value from config (backward compat)
	BaseURL       string
	Email         *service.EmailService
	WebhookSvc    *service.WebhookService
	// Livemode is the environment this handler is configured for.
	// Every inbound webhook event whose Livemode differs is rejected
	// with 400 — guards against cross-environment delivery (test
	// secret leaking + replay into prod, or vice versa).
	Livemode bool

	mu            sync.RWMutex
	webhookSecret string // runtime-updatable, guarded by mu
}

// GetWebhookSecret returns the current webhook signing secret (thread-safe).
func (h *StripeHandler) GetWebhookSecret() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.webhookSecret
}

// SetWebhookSecret updates the webhook signing secret (thread-safe).
func (h *StripeHandler) SetWebhookSecret(secret string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.webhookSecret = secret
}

// isSameOrigin checks that a URL shares the same scheme+host as BaseURL
// and has no userinfo (to prevent https://evil.com@legit.com bypasses).
func (h *StripeHandler) isSameOrigin(raw string) bool {
	base, err := url.Parse(h.BaseURL)
	if err != nil {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Scheme == base.Scheme && u.Host == base.Host && u.User == nil
}

func (h *StripeHandler) CreateCheckoutSession(c *gin.Context) {
	var req struct {
		PriceID    string `json:"price_id" binding:"required"`
		Email      string `json:"email"`
		SuccessURL string `json:"success_url"`
		CancelURL  string `json:"cancel_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "price_id is required")
		return
	}

	plan, err := h.Store.FindPlanByStripePrice(c, req.PriceID)
	if err != nil || plan == nil {
		response.BadRequest(c, "invalid price_id")
		return
	}

	success := h.BaseURL + "/checkout/success?session_id={CHECKOUT_SESSION_ID}"
	if req.SuccessURL != "" && h.isSameOrigin(req.SuccessURL) {
		success = req.SuccessURL
	}
	cancel := h.BaseURL + "/pricing"
	if req.CancelURL != "" && h.isSameOrigin(req.CancelURL) {
		cancel = req.CancelURL
	}

	mode := string(stripe.CheckoutSessionModeSubscription)
	if plan.LicenseType == "perpetual" {
		mode = string(stripe.CheckoutSessionModePayment)
	}

	params := &stripe.CheckoutSessionParams{
		Mode: stripe.String(mode),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{Price: stripe.String(req.PriceID), Quantity: stripe.Int64(1)},
		},
		SuccessURL:          stripe.String(success),
		CancelURL:           stripe.String(cancel),
		AllowPromotionCodes: stripe.Bool(true),
	}
	params.Metadata = map[string]string{
		"plan_id":    plan.ID,
		"product_id": plan.ProductID,
	}
	if req.Email != "" {
		params.CustomerEmail = stripe.String(req.Email)
	}

	s, err := session.New(params)
	if err != nil {
		response.Internal(c)
		return
	}
	response.OK(c, gin.H{"url": s.URL, "session_id": s.ID})
}

// CheckoutByPlan handles GET /pay/:checkout_id — looks up plan by checkout_id,
// creates a Stripe Checkout Session, and redirects to Stripe.
func (h *StripeHandler) CheckoutByPlan(c *gin.Context) {
	checkoutID := c.Param("checkout_id")
	if len(checkoutID) != 8 {
		c.String(http.StatusBadRequest, "invalid checkout id")
		return
	}

	plan, err := h.Store.FindPlanByCheckoutID(c, checkoutID)
	if err != nil || plan == nil {
		c.String(http.StatusNotFound, "plan not found")
		return
	}

	if !plan.Active {
		c.String(http.StatusGone, "this plan is no longer available")
		return
	}

	if plan.StripePriceID == "" {
		c.String(http.StatusServiceUnavailable, "payment not configured for this plan")
		return
	}

	// Determine checkout mode from Stripe Price (source of truth, not local config)
	sp, err := stripeprice.Get(plan.StripePriceID, nil)
	if err != nil {
		slog.Error("stripe: failed to fetch price", "price_id", plan.StripePriceID, "error", err)
		c.String(http.StatusServiceUnavailable, "payment configuration error")
		return
	}
	mode := string(stripe.CheckoutSessionModeSubscription)
	if sp.Type == "one_time" {
		mode = string(stripe.CheckoutSessionModePayment)
	}

	params := &stripe.CheckoutSessionParams{
		Mode: stripe.String(mode),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{Price: stripe.String(plan.StripePriceID), Quantity: stripe.Int64(1)},
		},
		SuccessURL:          stripe.String(h.BaseURL + "/checkout/success?session_id={CHECKOUT_SESSION_ID}"),
		CancelURL:           stripe.String(h.BaseURL + "/pricing"),
		AllowPromotionCodes: stripe.Bool(true),
	}
	params.Metadata = map[string]string{
		"plan_id":    plan.ID,
		"product_id": plan.ProductID,
	}

	s, err := session.New(params)
	if err != nil {
		c.String(http.StatusInternalServerError, "checkout unavailable")
		return
	}

	c.Redirect(http.StatusTemporaryRedirect, s.URL)
}

func (h *StripeHandler) Webhook(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		response.BadRequest(c, "read failed")
		return
	}

	secret := h.GetWebhookSecret()
	if secret == "" {
		slog.Error("stripe webhook received but no signing secret configured")
		response.BadRequest(c, "webhook not configured")
		return
	}
	event, err := webhook.ConstructEvent(body, c.GetHeader("Stripe-Signature"), secret)
	if err != nil {
		slog.Error("stripe webhook verification failed", "error", err.Error())
		response.BadRequest(c, "invalid signature")
		return
	}

	// Livemode gate: even with a valid signature, a test-mode event
	// must not be processed by a live server (or vice versa). Stripe
	// allows separate webhook endpoints per mode, so the secret alone
	// doesn't carry environment info — we check the event flag
	// against our configured mode. Without this, a leaked test secret
	// could replay arbitrary forged events at the production endpoint.
	if event.Livemode != h.Livemode {
		slog.Error("stripe webhook livemode mismatch",
			"event_id", event.ID, "event_livemode", event.Livemode,
			"server_livemode", h.Livemode)
		response.BadRequest(c, "livemode mismatch")
		return
	}

	// Idempotency: atomically check+record to prevent race conditions
	if !h.Store.TryRecordProcessedEvent(c, "stripe", event.ID) {
		c.JSON(http.StatusOK, gin.H{"received": true, "skipped": true})
		return
	}

	ctx := c.Request.Context()
	slog.Info("stripe webhook received", "type", event.Type, "id", event.ID)

	switch event.Type {
	case "checkout.session.completed":
		h.onCheckoutCompleted(ctx, event.Data.Raw)
	case "invoice.paid":
		h.onInvoicePaid(ctx, event.Data.Raw)
	case "customer.subscription.updated":
		h.onSubscriptionUpdated(ctx, event.Data.Raw)
	case "customer.subscription.deleted":
		h.onSubscriptionDeleted(ctx, event.Data.Raw)
	case "invoice.payment_failed":
		h.onPaymentFailed(ctx, event.Data.Raw)
	case "charge.refunded":
		h.onChargeRefunded(ctx, event.Data.Raw)
	case "charge.dispute.created":
		h.onDisputeCreated(ctx, event.Data.Raw)
	case "charge.dispute.closed":
		h.onDisputeClosed(ctx, event.Data.Raw)
	case "invoice.payment_action_required":
		h.onPaymentActionRequired(ctx, event.Data.Raw)
	case "customer.subscription.paused":
		h.onSubscriptionPaused(ctx, event.Data.Raw)
	case "customer.subscription.resumed":
		h.onSubscriptionResumed(ctx, event.Data.Raw)
	case "customer.subscription.trial_will_end":
		h.onTrialWillEnd(ctx, event.Data.Raw)
	case "invoice.upcoming":
		h.onInvoiceUpcoming(ctx, event.Data.Raw)
	case "customer.updated":
		h.onCustomerUpdated(ctx, event.Data.Raw)
	default:
		slog.Warn("stripe webhook: unhandled event type", "type", event.Type)
	}

	c.JSON(http.StatusOK, gin.H{"received": true})
}

func (h *StripeHandler) onCheckoutCompleted(ctx context.Context, raw json.RawMessage) {
	var data struct {
		ID            string            `json:"id"`
		CustomerEmail string            `json:"customer_email"`
		Customer      string            `json:"customer"`
		Subscription  string            `json:"subscription"`
		PaymentIntent string            `json:"payment_intent"`
		Mode          string            `json:"mode"`
		Metadata      map[string]string `json:"metadata"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return
	}
	if data.Metadata == nil {
		data.Metadata = map[string]string{}
	}
	data.Metadata["session_id"] = data.ID
	h.fulfillCheckout(ctx, data.CustomerEmail, data.Customer, data.Subscription, data.Metadata, "webhook")
}

// fulfillCheckout creates a license for a completed checkout session.
// Idempotent: skips if an active license already exists for this email+product.
// Called by webhook, success page verification, and periodic sync.
func (h *StripeHandler) fulfillCheckout(ctx context.Context, email, customerID, subscriptionID string, metadata map[string]string, source string) {
	// Idempotency: use Stripe session ID if available to prevent duplicate processing
	if metadata != nil && metadata["session_id"] != "" {
		if !h.Store.TryRecordProcessedEvent(ctx, "stripe_fulfill", metadata["session_id"]) {
			return
		}
	}
	var plan *model.Plan

	if subscriptionID != "" {
		plan = h.resolvePlan(ctx, subscriptionID)
	}
	if plan == nil && metadata != nil && metadata["plan_id"] != "" {
		plan, _ = h.Store.FindPlanByID(ctx, metadata["plan_id"])
	}
	if plan == nil {
		slog.Warn("stripe checkout: could not resolve plan", "subscription_id", subscriptionID, "metadata", metadata, "source", source)
		return
	}

	// Resolve email from Stripe Customer (authoritative source)
	if customerID != "" {
		if cust, err := stripecustomer.Get(customerID, nil); err == nil && cust.Email != "" {
			email = cust.Email
		}
	}

	if email == "" {
		slog.Warn("stripe checkout: no customer email, skipping", "customer_id", customerID, "source", source)
		return
	}

	// Prevent duplicate
	{
		if existing := h.Store.FindActiveLicenseByEmailAndProduct(ctx, email, plan.ProductID); existing != nil {
			slog.Info("stripe checkout: license already exists",
				"email", email, "product_id", plan.ProductID, "existing_license", existing.ID, "source", source)
			return
		}
	}

	status := model.StatusActive
	if plan.LicenseType == "trial" {
		status = model.StatusTrialing
	}

	lic := &model.License{
		ProductID:        plan.ProductID,
		PlanID:           plan.ID,
		Email:            email,
		LicenseKey:       license.GenerateKey(""),
		PaymentProvider:  "stripe",
		StripeCustomerID: customerID,
		Status:           status,
	}

	if plan.LicenseType == "trial" && plan.TrialDays > 0 {
		until := time.Now().Add(time.Duration(plan.TrialDays) * 24 * time.Hour)
		lic.ValidUntil = &until
	}

	if subscriptionID != "" {
		lic.StripeSubscriptionID = subscriptionID
	}

	// Ensure user record exists so they appear in Customers
	_ = h.Store.UpsertUser(ctx, &model.User{Email: email})

	if err := h.Store.CreateLicenseWithSubscription(ctx, lic, plan); err != nil {
		slog.Error("stripe checkout: failed to create license", "email", email, "error", err)
		return
	}

	// Link license to user
	if u, err := h.Store.FindUserByEmail(ctx, email); err == nil {
		lic.UserID = u.ID
		_ = h.Store.UpdateLicenseUser(ctx, lic.ID, u.ID)
	}

	productName := h.productName(ctx, plan.ProductID)
	if email != "" {
		// Use DecryptLicenseKey for forward compatibility — Phase C will
		// drop the plaintext column and direct .LicenseKey reads will be empty.
		displayKey := h.Store.DecryptLicenseKey(lic)
		body := fmt.Sprintf(`<!DOCTYPE html>
<html><body style="font-family: -apple-system, sans-serif; max-width: 600px; margin: 0 auto; padding: 20px;">
<h2 style="color: #111;">Your %s License</h2>
<p>Your <strong>%s</strong> license is ready.</p>
<div style="background: #f4f4f5; border-radius: 8px; padding: 16px; margin: 16px 0; font-family: monospace; font-size: 18px; text-align: center; letter-spacing: 2px;">%s</div>
<p style="color: #666; font-size: 14px;">Keep this key safe. You'll need it to activate your software.</p>
</body></html>`, productName, plan.Name, displayKey)
		_ = h.Store.EnqueueEmail(ctx, email, "Your license for "+productName, body)
	}

	h.Store.Audit(ctx, &model.AuditLog{
		Entity: "license", EntityID: lic.ID, Action: "created",
		ActorType: source,
		Changes:   map[string]any{"provider": "stripe", "email": email, "plan": plan.Name},
	})

	if h.WebhookSvc != nil {
		h.WebhookSvc.Dispatch(ctx, lic.ProductID, "license.created", map[string]any{
			"license_id": lic.ID, "email": lic.Email, "plan_id": lic.PlanID,
		})
	}

	slog.Info("license created", "email", email, "plan", plan.Name, "source", source)
}

// VerifyCheckoutSession handles GET /api/v1/checkout/verify?session_id=xxx
// Called by the success page to verify payment and create license (webhook fallback).
func (h *StripeHandler) VerifyCheckoutSession(c *gin.Context) {
	sessionID := c.Query("session_id")
	if sessionID == "" {
		response.BadRequest(c, "session_id is required")
		return
	}

	sess, err := session.Get(sessionID, &stripe.CheckoutSessionParams{})
	if err != nil {
		response.BadRequest(c, "invalid session")
		return
	}

	if sess.PaymentStatus != "paid" && sess.PaymentStatus != "no_payment_required" {
		response.OK(c, gin.H{"status": "pending"})
		return
	}

	custID := ""
	if sess.Customer != nil {
		custID = sess.Customer.ID
	}
	subID := ""
	if sess.Subscription != nil {
		subID = sess.Subscription.ID
	}

	meta := sess.Metadata
	if meta == nil {
		meta = map[string]string{}
	}
	meta["session_id"] = sess.ID

	h.fulfillCheckout(
		c.Request.Context(),
		sess.CustomerEmail,
		custID,
		subID,
		meta,
		"verify",
	)

	response.OK(c, gin.H{"status": "ok", "email": sess.CustomerEmail})
}

// SyncRecentCheckouts scans Stripe for completed checkout sessions in the last
// interval and creates licenses for any that were missed by webhooks.
func (h *StripeHandler) SyncRecentCheckouts(ctx context.Context) {
	// List checkout sessions completed in the last 10 minutes
	cutoff := time.Now().Add(-10 * time.Minute).Unix()
	params := &stripe.CheckoutSessionListParams{
		Status: stripe.String("complete"),
	}
	params.Filters.AddFilter("created", "gte", fmt.Sprintf("%d", cutoff))
	params.Filters.AddFilter("limit", "", "50")

	iter := session.List(params)
	for iter.Next() {
		sess := iter.CheckoutSession()
		if sess.PaymentStatus != "paid" && sess.PaymentStatus != "no_payment_required" {
			continue
		}

		subID := ""
		if sess.Subscription != nil {
			subID = sess.Subscription.ID
		}
		custID := ""
		if sess.Customer != nil {
			custID = sess.Customer.ID
		}

		meta := sess.Metadata
		if meta == nil {
			meta = map[string]string{}
		}
		meta["session_id"] = sess.ID
		h.fulfillCheckout(ctx, sess.CustomerEmail, custID, subID, meta, "sync")
	}
	if err := iter.Err(); err != nil {
		slog.Error("stripe sync: failed to list sessions", "error", err)
	}
}

func (h *StripeHandler) onInvoicePaid(ctx context.Context, raw json.RawMessage) {
	var data struct {
		Subscription string `json:"subscription"`
		PeriodEnd    int64  `json:"period_end"`
	}
	if json.Unmarshal(raw, &data) != nil || data.Subscription == "" {
		return
	}

	lic, err := h.Store.FindLicenseByStripeSubscription(ctx, data.Subscription)
	if err != nil {
		return
	}
	wasPastDue := lic.Status == model.StatusPastDue
	// Capture the episode anchor BEFORE the write clears it.
	// Without this, notifyPaymentRecovered would always see a nil
	// PastDueAt and fall back to a license-wide tag — meaning the
	// second past_due → recovery cycle ever silently drops the email.
	var episode int64
	if lic.PastDueAt != nil {
		episode = lic.PastDueAt.Unix()
	}
	until := time.Unix(data.PeriodEnd, 0)
	lic.ValidUntil = &until
	lic.Status = model.StatusActive
	lic.PastDueAt = nil
	_ = h.Store.UpdateLicenseAndSubscription(ctx, lic, "valid_until", "status", "past_due_at")

	// Recovery notification — shares the dedup path with
	// onSubscriptionUpdated. Some flows emit invoice.paid without a
	// matching subscription.updated, others emit both; both call
	// this helper which fires at most once per cycle (per episode).
	if wasPastDue {
		h.notifyPaymentRecovered(ctx, lic, episode)
	}
}

func (h *StripeHandler) onSubscriptionUpdated(ctx context.Context, raw json.RawMessage) {
	var data struct {
		ID               string `json:"id"`
		Status           string `json:"status"`
		CurrentPeriodEnd int64  `json:"current_period_end"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return
	}

	lic, err := h.Store.FindLicenseByStripeSubscription(ctx, data.ID)
	if err != nil {
		return
	}

	// Capture the prior state BEFORE mutating — recovery side-effects
	// (clearing past_due_at, firing the recovered email) only run when
	// the transition is actually past_due → active.
	wasPastDue := lic.Status == model.StatusPastDue
	// Anchor for the episode-scoped recovered notification tag.
	// Captured here because the write below nulls past_due_at.
	var episode int64
	if lic.PastDueAt != nil {
		episode = lic.PastDueAt.Unix()
	}
	cols := []string{"status", "valid_until", "canceled_at", "past_due_at"}

	switch data.Status {
	case "active":
		lic.Status = model.StatusActive
		// Recovery: customer fixed the card. Clear the dunning
		// anchor so a fresh past_due episode in the future starts
		// the ladder from day 0, not from the original failure.
		lic.PastDueAt = nil
	case "past_due":
		// Idempotent entry: only stamp past_due_at on first entry
		// (or when re-entering after a recovery). Without this a
		// burst of repeated payment_failed webhooks would reset the
		// clock each time and the day-7 / day-14 reminders would
		// keep getting pushed out.
		lic.Status = model.StatusPastDue
		if lic.PastDueAt == nil {
			now := time.Now()
			lic.PastDueAt = &now
		}
	case "trialing":
		lic.Status = model.StatusTrialing
	case "canceled", "unpaid":
		lic.Status = model.StatusCanceled
		now := time.Now()
		lic.CanceledAt = &now
		lic.PastDueAt = nil
	}

	until := time.Unix(data.CurrentPeriodEnd, 0)
	lic.ValidUntil = &until
	_ = h.Store.UpdateLicenseAndSubscription(ctx, lic, cols...)

	// Recovery notification fires only on past_due → active. Routed
	// through notifyPaymentRecovered so concurrent webhooks (Stripe
	// sometimes sends invoice.paid + customer.subscription.updated
	// in parallel) collapse into one email + one webhook dispatch.
	if wasPastDue && lic.Status == model.StatusActive {
		h.notifyPaymentRecovered(ctx, lic, episode)
	}
}

// notifyPaymentRecovered fires the recovery email + webhook exactly
// once per past_due episode.
//
// `episode` is the past_due_at Unix epoch captured BEFORE the
// recovery write clears the column. It enters the notification tag
// so a license that lapses → recovers → lapses → recovers in the
// same year gets the email twice (once per episode), instead of
// being permanently silenced after the first recovery by the
// notifications (license_id, tag) UNIQUE constraint.
//
// episode == 0 is a defensive fallback for legacy rows that
// somehow lack past_due_at; we still dedupe on the bare tag in
// that case to avoid a double-send within the single missing
// episode.
func (h *StripeHandler) notifyPaymentRecovered(ctx context.Context, lic *model.License, episode int64) {
	tag := "payment_recovered"
	if episode > 0 {
		tag = fmt.Sprintf("payment_recovered:%d", episode)
	}
	productName := ""
	if p, err := h.Store.FindProductByID(ctx, lic.ProductID); err == nil {
		productName = p.Name
	}
	if h.Store.HasNotification(ctx, lic.ID, tag) {
		return
	}
	if h.Email != nil {
		h.Email.SendPaymentRecovered(lic.Email, productName)
	}
	h.Store.RecordNotification(ctx, lic.ID, tag)
	if h.WebhookSvc != nil {
		h.WebhookSvc.Dispatch(ctx, lic.ProductID, "license.payment_recovered", map[string]any{
			"license_id": lic.ID, "email": lic.Email,
		})
	}
}

func (h *StripeHandler) onSubscriptionDeleted(ctx context.Context, raw json.RawMessage) {
	var data struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return
	}

	lic, err := h.Store.FindLicenseByStripeSubscription(ctx, data.ID)
	if err != nil {
		return
	}
	lic.Status = model.StatusCanceled
	now := time.Now()
	lic.CanceledAt = &now
	lic.PastDueAt = nil
	_ = h.Store.UpdateLicenseAndSubscription(ctx, lic, "status", "canceled_at", "past_due_at")

	h.Store.Audit(ctx, &model.AuditLog{
		Entity: "license", EntityID: lic.ID, Action: "canceled",
		ActorType: "webhook", Changes: map[string]any{"provider": "stripe"},
	})

	if h.WebhookSvc != nil {
		h.WebhookSvc.Dispatch(ctx, lic.ProductID, "license.canceled", map[string]any{
			"license_id": lic.ID, "email": lic.Email, "reason": "subscription_deleted",
		})
	}
}

func (h *StripeHandler) onPaymentFailed(ctx context.Context, raw json.RawMessage) {
	var data struct {
		Subscription string `json:"subscription"`
	}
	if json.Unmarshal(raw, &data) != nil || data.Subscription == "" {
		return
	}

	lic, err := h.Store.FindLicenseByStripeSubscription(ctx, data.Subscription)
	if err != nil {
		return
	}

	if lic.Status == model.StatusActive {
		now := time.Now()
		lic.Status = model.StatusPastDue
		lic.PastDueAt = &now
		_ = h.Store.UpdateLicenseAndSubscription(ctx, lic, "status", "past_due_at")

		h.Store.Audit(ctx, &model.AuditLog{
			Entity: "license", EntityID: lic.ID, Action: "payment_failed",
			ActorType: "webhook", Changes: map[string]any{"provider": "stripe"},
		})

		if h.WebhookSvc != nil {
			h.WebhookSvc.Dispatch(ctx, lic.ProductID, "license.payment_failed", map[string]any{
				"license_id": lic.ID, "email": lic.Email,
			})
		}
	}
}

func (h *StripeHandler) onChargeRefunded(ctx context.Context, raw json.RawMessage) {
	var data struct {
		ID             string `json:"id"`
		Customer       string `json:"customer"`
		Amount         int64  `json:"amount"`
		AmountRefunded int64  `json:"amount_refunded"`
		Refunded       bool   `json:"refunded"`
		PaymentIntent  string `json:"payment_intent"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return
	}

	lic, err := h.Store.FindLicenseByStripeCustomer(ctx, data.Customer)
	if err != nil {
		return
	}

	if data.Refunded {
		lic.Status = model.StatusRevoked
		_ = h.Store.UpdateLicenseAndSubscription(ctx, lic, "status")

		h.Store.Audit(ctx, &model.AuditLog{
			Entity: "license", EntityID: lic.ID, Action: "revoked",
			ActorType: "webhook",
			Changes:   map[string]any{"reason": "full_refund", "provider": "stripe", "charge_id": data.ID},
		})
	} else if data.AmountRefunded > 0 {
		h.Store.Audit(ctx, &model.AuditLog{
			Entity: "license", EntityID: lic.ID, Action: "partial_refund",
			ActorType: "webhook",
			Changes:   map[string]any{"amount_refunded": data.AmountRefunded, "provider": "stripe"},
		})
	}
}

func (h *StripeHandler) CancelSubscription(c *gin.Context) {
	var req struct {
		LicenseID string `json:"license_id" binding:"required"`
		Immediate bool   `json:"immediate"` // false = cancel at period end (default)
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "license_id is required")
		return
	}

	lic, err := h.Store.FindLicenseByID(c, req.LicenseID)
	if err != nil {
		response.NotFound(c, "license not found")
		return
	}

	emailVal, _ := c.Get("email")
	if e, ok := emailVal.(string); !ok || lic.Email != e {
		response.Forbidden(c, "not your license")
		return
	}

	if lic.StripeSubscriptionID == "" {
		response.BadRequest(c, "no active subscription")
		return
	}

	if req.Immediate {
		_, err = subscription.Cancel(lic.StripeSubscriptionID, nil)
		if err != nil {
			response.Internal(c)
			return
		}
		now := time.Now()
		lic.Status = model.StatusCanceled
		lic.CanceledAt = &now
		lic.ValidUntil = &now
		_ = h.Store.UpdateLicense(c, lic, "status", "canceled_at", "valid_until")
	} else {
		sub, updateErr := subscription.Update(lic.StripeSubscriptionID, &stripe.SubscriptionParams{
			CancelAtPeriodEnd: stripe.Bool(true),
		})
		if updateErr != nil {
			response.Internal(c)
			return
		}
		// Set ValidUntil to when Stripe will cancel the subscription
		periodEnd := time.Unix(sub.CancelAt, 0)
		lic.ValidUntil = &periodEnd
		_ = h.Store.UpdateLicense(c, lic, "valid_until")
	}

	h.Store.Audit(c, &model.AuditLog{
		Entity: "license", EntityID: lic.ID, Action: "cancel_requested",
		ActorType: "user",
		Changes:   map[string]any{"immediate": req.Immediate},
	})

	productName := ""
	if lic.Product != nil {
		productName = lic.Product.Name
	} else if p, err := h.Store.FindProductByID(c, lic.ProductID); err == nil {
		productName = p.Name
	}
	if h.Email != nil {
		h.Email.SendSubscriptionCanceled(lic.Email, productName, req.Immediate)
	}

	response.OK(c, gin.H{
		"status":    "canceled",
		"immediate": req.Immediate,
	})
}

func (h *StripeHandler) ChangePlan(c *gin.Context) {
	var req struct {
		LicenseID  string `json:"license_id" binding:"required"`
		NewPriceID string `json:"new_price_id" binding:"required"`
		Prorate    *bool  `json:"prorate"` // default true
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "license_id and new_price_id are required")
		return
	}

	// Order matters: license + ownership check FIRST so an attacker
	// probing Bob's session against Alice's license_id can't
	// distinguish "wrong owner" from "bad price" via response codes.
	// Without this, a 400 on an unknown price_id leaks the existence
	// of Alice's license.
	lic, err := h.Store.FindLicenseByID(c, req.LicenseID)
	if err != nil {
		response.NotFound(c, "license not found")
		return
	}

	emailVal, _ := c.Get("email")
	if e, ok := emailVal.(string); !ok || lic.Email != e {
		response.Forbidden(c, "not your license")
		return
	}

	newPlan, err := h.Store.FindPlanByStripePrice(c, req.NewPriceID)
	if err != nil || newPlan == nil {
		response.BadRequest(c, "invalid new_price_id")
		return
	}

	if lic.StripeSubscriptionID == "" {
		response.BadRequest(c, "license has no Stripe subscription")
		return
	}

	if newPlan.ProductID != lic.ProductID {
		response.BadRequest(c, "new plan must belong to the same product")
		return
	}

	// Plan availability gates. Without these a leaked price_id for a
	// deprecated / non-subscription plan could be used to side-step
	// the merchant's pricing strategy:
	//   - inactive plans were taken off the public catalogue; honoring
	//     them on change-plan reopens a discontinued tier.
	//   - perpetual / trial plans aren't subscription-billable; Stripe
	//     would happily swap the subscription item but the resulting
	//     license_type wouldn't match the column semantics anywhere
	//     downstream (renewal email, dunning, expiry).
	if !newPlan.Active {
		response.Err(c, http.StatusBadRequest, "PLAN_INACTIVE",
			"target plan is no longer available")
		return
	}
	if newPlan.LicenseType != "subscription" {
		response.Err(c, http.StatusBadRequest, "NOT_SUBSCRIPTION_PLAN",
			"change-plan only accepts subscription plans")
		return
	}

	sub, err := subscription.Get(lic.StripeSubscriptionID, nil)
	if err != nil || len(sub.Items.Data) == 0 {
		response.Internal(c)
		return
	}

	prorationBehavior := "create_prorations"
	if req.Prorate != nil && !*req.Prorate {
		prorationBehavior = "none"
	}

	params := &stripe.SubscriptionParams{
		ProrationBehavior: stripe.String(prorationBehavior),
		Items: []*stripe.SubscriptionItemsParams{
			{
				ID:    stripe.String(sub.Items.Data[0].ID),
				Price: stripe.String(req.NewPriceID),
			},
		},
	}

	updatedSub, err := subscription.Update(lic.StripeSubscriptionID, params)
	if err != nil {
		response.Internal(c)
		return
	}

	oldPlanID := lic.PlanID
	lic.PlanID = newPlan.ID
	_ = h.Store.UpdateLicense(c, lic, "plan_id")

	if subRecord, err := h.Store.FindSubscriptionByLicense(c, lic.ID); err == nil {
		subRecord.PlanID = newPlan.ID
		_ = h.Store.UpdateSubscription(c, subRecord, "plan_id")
	}
	_ = updatedSub // used for audit context

	h.Store.Audit(c, &model.AuditLog{
		Entity: "license", EntityID: lic.ID, Action: "plan_changed",
		ActorType: "user",
		Changes: map[string]any{
			"old_plan_id": oldPlanID, "new_plan_id": newPlan.ID,
			"proration": prorationBehavior,
		},
	})

	response.OK(c, gin.H{
		"status":        "plan_changed",
		"new_plan_id":   newPlan.ID,
		"new_plan_name": newPlan.Name,
		"proration":     prorationBehavior,
	})
}

func (h *StripeHandler) onDisputeCreated(ctx context.Context, raw json.RawMessage) {
	var dispute struct {
		ID       string `json:"id"`
		Charge   string `json:"charge"`
		Reason   string `json:"reason"`
		Status   string `json:"status"`
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
	}
	if json.Unmarshal(raw, &dispute) != nil {
		return
	}

	h.Store.Audit(ctx, &model.AuditLog{
		Entity: "dispute", EntityID: dispute.ID, Action: "created",
		ActorType: "webhook",
		Changes: map[string]any{
			"charge_id": dispute.Charge,
			"reason":    dispute.Reason,
			"amount":    dispute.Amount,
			"status":    dispute.Status,
		},
	})
}

func (h *StripeHandler) onDisputeClosed(ctx context.Context, raw json.RawMessage) {
	var dispute struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal(raw, &dispute) != nil {
		return
	}

	h.Store.Audit(ctx, &model.AuditLog{
		Entity: "dispute", EntityID: dispute.ID, Action: "closed",
		ActorType: "webhook",
		Changes:   map[string]any{"status": dispute.Status, "reason": dispute.Reason},
	})
}

// CreatePortalSession creates a Stripe billing portal session for the user to manage payment methods.
func (h *StripeHandler) CreatePortalSession(c *gin.Context) {
	var req struct {
		LicenseID string `json:"license_id" binding:"required"`
		ReturnURL string `json:"return_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "license_id is required")
		return
	}

	lic, err := h.Store.FindLicenseByID(c, req.LicenseID)
	if err != nil {
		response.NotFound(c, "license not found")
		return
	}

	// Verify ownership
	emailVal, _ := c.Get("email")
	if e, ok := emailVal.(string); !ok || lic.Email != e {
		response.Forbidden(c, "not your license")
		return
	}

	if lic.StripeCustomerID == "" {
		response.BadRequest(c, "no Stripe customer associated")
		return
	}

	returnURL := req.ReturnURL
	if returnURL == "" {
		returnURL = h.BaseURL + "/portal"
	}

	params := &stripe.BillingPortalSessionParams{
		Customer:  stripe.String(lic.StripeCustomerID),
		ReturnURL: stripe.String(returnURL),
	}
	s, err := portalsession.New(params)
	if err != nil {
		response.Internal(c)
		return
	}

	response.OK(c, gin.H{"url": s.URL})
}

// ListInvoices returns invoice history for a license's Stripe customer.
func (h *StripeHandler) ListInvoices(c *gin.Context) {
	licenseID := c.Query("license_id")
	if licenseID == "" {
		response.BadRequest(c, "license_id is required")
		return
	}

	lic, err := h.Store.FindLicenseByID(c, licenseID)
	if err != nil {
		response.NotFound(c, "license not found")
		return
	}

	// Verify ownership
	emailVal, _ := c.Get("email")
	if e, ok := emailVal.(string); !ok || lic.Email != e {
		response.Forbidden(c, "not your license")
		return
	}

	if lic.StripeCustomerID == "" {
		response.OK(c, gin.H{"invoices": []any{}})
		return
	}

	params := &stripe.InvoiceListParams{
		Customer: stripe.String(lic.StripeCustomerID),
	}
	params.Filters.AddFilter("limit", "", "20")

	type invoiceItem struct {
		ID          string `json:"id"`
		Number      string `json:"number"`
		Status      string `json:"status"`
		AmountDue   int64  `json:"amount_due"`
		AmountPaid  int64  `json:"amount_paid"`
		Currency    string `json:"currency"`
		Created     int64  `json:"created"`
		PeriodStart int64  `json:"period_start"`
		PeriodEnd   int64  `json:"period_end"`
		InvoicePDF  string `json:"invoice_pdf"`
		HostedURL   string `json:"hosted_url"`
	}

	var invoices []invoiceItem
	iter := stripeinvoice.List(params)
	for iter.Next() {
		inv := iter.Invoice()
		invoices = append(invoices, invoiceItem{
			ID:          inv.ID,
			Number:      inv.Number,
			Status:      string(inv.Status),
			AmountDue:   inv.AmountDue,
			AmountPaid:  inv.AmountPaid,
			Currency:    string(inv.Currency),
			Created:     inv.Created,
			PeriodStart: inv.PeriodStart,
			PeriodEnd:   inv.PeriodEnd,
			InvoicePDF:  inv.InvoicePDF,
			HostedURL:   inv.HostedInvoiceURL,
		})
	}
	if err := iter.Err(); err != nil {
		response.Internal(c)
		return
	}

	response.OK(c, gin.H{"invoices": invoices})
}

func (h *StripeHandler) onPaymentActionRequired(ctx context.Context, raw json.RawMessage) {
	var data struct {
		Subscription     string `json:"subscription"`
		HostedInvoiceURL string `json:"hosted_invoice_url"`
	}
	if json.Unmarshal(raw, &data) != nil || data.Subscription == "" {
		return
	}
	lic, err := h.Store.FindLicenseByStripeSubscription(ctx, data.Subscription)
	if err != nil {
		return
	}
	// Notify user to complete 3DS/SCA authentication
	productName := h.productName(ctx, lic.ProductID)
	if h.Email != nil {
		h.Email.SendPaymentActionRequired(lic.Email, productName, data.HostedInvoiceURL)
	}
	h.Store.Audit(ctx, &model.AuditLog{
		Entity: "license", EntityID: lic.ID, Action: "payment_action_required",
		ActorType: "webhook", Changes: map[string]any{"provider": "stripe"},
	})
}

func (h *StripeHandler) onSubscriptionPaused(ctx context.Context, raw json.RawMessage) {
	var data struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return
	}
	lic, err := h.Store.FindLicenseByStripeSubscription(ctx, data.ID)
	if err != nil {
		return
	}
	lic.Status = model.StatusSuspended
	now := time.Now()
	lic.SuspendedAt = &now
	_ = h.Store.UpdateLicenseAndSubscription(ctx, lic, "status", "suspended_at")

	h.Store.Audit(ctx, &model.AuditLog{
		Entity: "license", EntityID: lic.ID, Action: "suspended",
		ActorType: "webhook", Changes: map[string]any{"reason": "subscription_paused", "provider": "stripe"},
	})
	if h.WebhookSvc != nil {
		h.WebhookSvc.Dispatch(ctx, lic.ProductID, "license.suspended", map[string]any{
			"license_id": lic.ID, "email": lic.Email, "reason": "subscription_paused",
		})
	}
}

func (h *StripeHandler) onSubscriptionResumed(ctx context.Context, raw json.RawMessage) {
	var data struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return
	}
	lic, err := h.Store.FindLicenseByStripeSubscription(ctx, data.ID)
	if err != nil {
		return
	}
	lic.Status = model.StatusActive
	lic.SuspendedAt = nil
	_ = h.Store.UpdateLicenseAndSubscription(ctx, lic, "status", "suspended_at")

	h.Store.Audit(ctx, &model.AuditLog{
		Entity: "license", EntityID: lic.ID, Action: "reinstated",
		ActorType: "webhook", Changes: map[string]any{"reason": "subscription_resumed", "provider": "stripe"},
	})
	if h.WebhookSvc != nil {
		h.WebhookSvc.Dispatch(ctx, lic.ProductID, "license.reinstated", map[string]any{
			"license_id": lic.ID, "email": lic.Email,
		})
	}
}

func (h *StripeHandler) onTrialWillEnd(ctx context.Context, raw json.RawMessage) {
	var data struct {
		ID       string `json:"id"`
		TrialEnd int64  `json:"trial_end"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return
	}
	lic, err := h.Store.FindLicenseByStripeSubscription(ctx, data.ID)
	if err != nil {
		return
	}
	productName := h.productName(ctx, lic.ProductID)
	trialEnd := time.Unix(data.TrialEnd, 0).Format("2006-01-02")
	if h.Email != nil {
		h.Email.SendTrialEnding(lic.Email, productName, trialEnd)
	}
}

func (h *StripeHandler) onInvoiceUpcoming(ctx context.Context, raw json.RawMessage) {
	var data struct {
		Customer     string `json:"customer"`
		Subscription string `json:"subscription"`
		AmountDue    int64  `json:"amount_due"`
		Currency     string `json:"currency"`
	}
	if json.Unmarshal(raw, &data) != nil || data.Subscription == "" {
		return
	}
	lic, err := h.Store.FindLicenseByStripeSubscription(ctx, data.Subscription)
	if err != nil {
		return
	}
	// Renewal reminder is handled by expiry checker, but this is a backup from Stripe
	// Just audit it
	h.Store.Audit(ctx, &model.AuditLog{
		Entity: "license", EntityID: lic.ID, Action: "invoice_upcoming",
		ActorType: "webhook", Changes: map[string]any{"amount_due": data.AmountDue, "currency": data.Currency},
	})
}

func (h *StripeHandler) onCustomerUpdated(ctx context.Context, raw json.RawMessage) {
	var data struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	if json.Unmarshal(raw, &data) != nil || data.ID == "" {
		return
	}
	// Update email on all licenses for this customer
	if data.Email != "" {
		h.Store.UpdateLicenseEmailByStripeCustomer(ctx, data.ID, data.Email)
	}
}

func (h *StripeHandler) productName(ctx context.Context, productID string) string {
	if p, err := h.Store.FindProductByID(ctx, productID); err == nil && p.Name != "" {
		return p.Name
	}
	slog.Warn("product name not found, using fallback", "product_id", productID)
	return "Your Software"
}

func (h *StripeHandler) resolvePlan(ctx context.Context, subID string) *model.Plan {
	sub, err := subscription.Get(subID, nil)
	if err != nil || len(sub.Items.Data) == 0 {
		return nil
	}
	plan, err := h.Store.FindPlanByStripePrice(ctx, sub.Items.Data[0].Price.ID)
	if err != nil {
		return nil
	}
	return plan
}
