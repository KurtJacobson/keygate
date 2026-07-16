package service

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/store"
)

type ExpiryChecker struct {
	store   *store.Store
	email   *EmailService
	webhook *WebhookService
	logger  *slog.Logger
}

func NewExpiryChecker(s *store.Store, email *EmailService, wh *WebhookService, logger *slog.Logger) *ExpiryChecker {
	return &ExpiryChecker{store: s, email: email, webhook: wh, logger: logger}
}

// StartExpiryLoop runs all lifecycle checks periodically.
func (c *ExpiryChecker) StartExpiryLoop(ctx context.Context) {
	// Run immediately on startup
	c.RunAll(ctx)

	ticker := time.NewTicker(1 * time.Hour) // check every hour, not daily
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.RunAll(ctx)
		}
	}
}

// RunAll executes all lifecycle checks.
func (c *ExpiryChecker) RunAll(ctx context.Context) {
	c.ExpireGracePeriodLicenses(ctx)
	c.ExpireTrials(ctx)
	c.MarkPastDueAsExpired(ctx)
	c.SendExpiryReminders(ctx)
	c.SendRenewalReminders(ctx)
	c.SendSupportReminders(ctx)
	c.SendPaymentFailureReminders(ctx)
	c.CleanupExpiredActivations(ctx)
	c.SyncSubscriptionStates(ctx)
}

// ExpireGracePeriodLicenses marks active/past_due licenses as expired
// when valid_until + grace_days has passed.
func (c *ExpiryChecker) ExpireGracePeriodLicenses(ctx context.Context) {
	licenses, err := c.store.FindLicensesForGraceExpiry(ctx)
	if err != nil {
		c.logger.Error("grace expiry check failed", "error", err)
		return
	}
	for _, lic := range licenses {
		graceDays := 7
		if lic.Plan != nil {
			graceDays = lic.Plan.GraceDays
		}
		graceEnd := lic.ValidUntil.Add(time.Duration(graceDays) * 24 * time.Hour)
		if time.Now().After(graceEnd) {
			lic.Status = model.StatusExpired
			if err := c.store.UpdateLicenseAndSubscription(ctx, lic, "status"); err != nil {
				c.logger.Error("expire license failed", "id", lic.ID, "error", err)
				continue
			}
			c.store.Audit(ctx, &model.AuditLog{
				Entity: "license", EntityID: lic.ID, Action: "expired",
				ActorType: "system",
				Changes:   map[string]any{"reason": "grace_period_ended"},
			})
			c.webhook.Dispatch(ctx, lic.ProductID, "license.expired", map[string]any{
				"license_id": lic.ID, "email": lic.Email, "reason": "grace_period_ended",
			})
			productName := ""
			if lic.Product != nil {
				productName = lic.Product.Name
			}
			c.email.SendLicenseExpired(lic.Email, productName)
			c.logger.Info("license expired (grace ended)", "id", lic.ID)
		}
	}
}

// ExpireTrials marks trialing licenses as expired when trial period ends.
func (c *ExpiryChecker) ExpireTrials(ctx context.Context) {
	licenses, err := c.store.FindExpiredTrials(ctx)
	if err != nil {
		c.logger.Error("trial expiry check failed", "error", err)
		return
	}
	for _, lic := range licenses {
		lic.Status = model.StatusExpired
		if err := c.store.UpdateLicenseAndSubscription(ctx, lic, "status"); err != nil {
			c.logger.Error("expire trial failed", "id", lic.ID, "error", err)
			continue
		}
		c.store.Audit(ctx, &model.AuditLog{
			Entity: "license", EntityID: lic.ID, Action: "expired",
			ActorType: "system",
			Changes:   map[string]any{"reason": "trial_ended"},
		})
		c.webhook.Dispatch(ctx, lic.ProductID, "license.expired", map[string]any{
			"license_id": lic.ID, "email": lic.Email, "reason": "trial_ended",
		})
		productName := ""
		if lic.Product != nil {
			productName = lic.Product.Name
		}
		c.email.SendTrialExpired(lic.Email, productName)
		c.logger.Info("trial expired", "id", lic.ID)
	}
}

// MarkPastDueAsExpired converts long-standing past_due licenses to expired.
// If past_due for more than 30 days, mark as expired.
func (c *ExpiryChecker) MarkPastDueAsExpired(ctx context.Context) {
	threshold := time.Now().Add(-30 * 24 * time.Hour)
	licenses, err := c.store.FindStalePastDueLicenses(ctx, threshold)
	if err != nil {
		c.logger.Error("past_due expiry check failed", "error", err)
		return
	}
	for _, lic := range licenses {
		lic.Status = model.StatusExpired
		if err := c.store.UpdateLicenseAndSubscription(ctx, lic, "status"); err != nil {
			continue
		}
		c.store.Audit(ctx, &model.AuditLog{
			Entity: "license", EntityID: lic.ID, Action: "expired",
			ActorType: "system",
			Changes:   map[string]any{"reason": "past_due_timeout"},
		})
		c.logger.Info("past_due expired", "id", lic.ID)
	}
}

// SendExpiryReminders sends notifications for upcoming expirations.
// Uses a notified_at tracking to prevent duplicate emails.
func (c *ExpiryChecker) SendExpiryReminders(ctx context.Context) {
	reminders := []struct {
		days int
		tag  string
	}{
		{7, "expiry_7d"},
		{3, "expiry_3d"},
		{1, "expiry_1d"},
	}

	for _, r := range reminders {
		from := time.Now()
		to := from.Add(time.Duration(r.days) * 24 * time.Hour)
		licenses, err := c.store.FindExpiringLicenses(ctx, from, to)
		if err != nil {
			c.logger.Error("expiry reminder check failed", "error", err)
			continue
		}
		for _, lic := range licenses {
			// Check if we already sent this reminder
			if c.store.HasNotification(ctx, lic.ID, r.tag) {
				continue
			}
			productName := ""
			if lic.Product != nil {
				productName = lic.Product.Name
			}
			expiresAt := ""
			if lic.ValidUntil != nil {
				expiresAt = lic.ValidUntil.Format("2006-01-02")
			}
			c.email.SendLicenseExpiring(lic.Email, productName, c.store.DecryptLicenseKey(lic), expiresAt)
			c.store.RecordNotification(ctx, lic.ID, r.tag)
			c.logger.Info("expiry reminder sent", "license_id", lic.ID, "days", r.days)
		}
	}
}

// SendSupportReminders emails licenses whose paid-support window is
// ending (30d / 7d ahead) or has just ended (scanned up to 7 days
// back so a server outage doesn't swallow the notice). Wider lead
// time than license expiry on purpose: support lapse is soft — the
// software keeps working, only updates stop — so this is renewal-
// sales mail, not an outage warning.
//
// Dedup tags encode support_until's epoch (like the dunning ladder
// encodes past_due_at): when the admin renews support to a new date,
// the next lapse gets a fresh tag namespace and a full new reminder
// cycle instead of being suppressed by last year's tags.
func (c *ExpiryChecker) SendSupportReminders(ctx context.Context) {
	reminders := []struct {
		days int
		tag  string
	}{
		{30, "support_30d"},
		{7, "support_7d"},
	}

	now := time.Now()
	for _, r := range reminders {
		to := now.Add(time.Duration(r.days) * 24 * time.Hour)
		licenses, err := c.store.FindSupportExpiringLicenses(ctx, now, to)
		if err != nil {
			c.logger.Error("support reminder check failed", "error", err)
			continue
		}
		for _, lic := range licenses {
			tag := r.tag + ":" + strconvI64(lic.SupportUntil.Unix())
			if c.store.HasNotification(ctx, lic.ID, tag) {
				continue
			}
			productName := ""
			if lic.Product != nil {
				productName = lic.Product.Name
			}
			c.email.SendSupportExpiring(lic.Email, productName,
				c.store.DecryptLicenseKey(lic), lic.SupportUntil.Format("2006-01-02"))
			c.store.RecordNotification(ctx, lic.ID, tag)
			c.logger.Info("support reminder sent", "license_id", lic.ID, "days", r.days)
		}
	}

	// "Support has ended" — window [now-7d, now] catches lapses even
	// if the checker was down when the date passed.
	lapsed, err := c.store.FindSupportExpiringLicenses(ctx, now.Add(-7*24*time.Hour), now)
	if err != nil {
		c.logger.Error("support lapse check failed", "error", err)
		return
	}
	for _, lic := range lapsed {
		tag := "support_ended:" + strconvI64(lic.SupportUntil.Unix())
		if c.store.HasNotification(ctx, lic.ID, tag) {
			continue
		}
		productName := ""
		if lic.Product != nil {
			productName = lic.Product.Name
		}
		c.email.SendSupportEnded(lic.Email, productName, c.store.DecryptLicenseKey(lic))
		c.store.RecordNotification(ctx, lic.ID, tag)
		c.webhook.Dispatch(ctx, lic.ProductID, "license.support_ended", map[string]any{
			"license_id": lic.ID, "email": lic.Email,
			"support_until": lic.SupportUntil.Format(time.RFC3339),
		})
		c.logger.Info("support ended notice sent", "license_id", lic.ID)
	}
}

// CleanupExpiredActivations removes activations for expired/revoked licenses.
func (c *ExpiryChecker) CleanupExpiredActivations(ctx context.Context) {
	count, err := c.store.DeleteExpiredActivations(ctx)
	if err != nil {
		c.logger.Error("cleanup activations failed", "error", err)
		return
	}
	if count > 0 {
		c.logger.Info("cleaned up expired activations", "count", count)
	}
}

// SendPaymentFailureReminders walks every past_due license up the
// dunning ladder. Anchored on lic.PastDueAt so unrelated row writes
// (audit, sync) don't reset the clock. Each step is gated by
// HasNotification + a unique tag per past_due_at epoch, so:
//
//   - the same email never fires twice for one episode, even if the
//     checker runs every hour;
//   - if the checker misses a window (server down) it still fires
//     the next time it wakes up — there's no narrow `>=N && <N+1`
//     window that can silently swallow a reminder;
//   - a customer who recovers and then lapses again later gets a
//     full fresh ladder for the second episode (the tag includes
//     past_due_at's epoch).
func (c *ExpiryChecker) SendPaymentFailureReminders(ctx context.Context) {
	var licenses []*model.License
	err := c.store.DB.NewSelect().Model(&licenses).
		Relation("Product").
		Where("license.status = 'past_due' AND license.past_due_at IS NOT NULL").
		Scan(ctx)
	if err != nil || len(licenses) == 0 {
		return
	}

	steps := []struct {
		minDays int
		tag     string
		send    func(to, productName string)
	}{
		// Highest threshold first — only one email per check per
		// license, and the "highest reached" wins. Without that,
		// a license that's been past_due for 20 days would receive
		// dunning_first + dunning_second + dunning_final on the
		// FIRST checker run after install.
		{14, "dunning_final", c.email.SendDunningFinal},
		{7, "dunning_second", c.email.SendDunningSecond},
		{1, "dunning_first", c.email.SendPaymentFailed},
	}

	now := time.Now()
	for _, lic := range licenses {
		if lic.PastDueAt == nil {
			continue
		}
		daysPastDue := int(now.Sub(*lic.PastDueAt).Hours() / 24)
		productName := ""
		if lic.Product != nil {
			productName = lic.Product.Name
		}
		// Tag scope: per-episode. Encoding past_due_at's Unix epoch
		// means the next past_due cycle for the same license gets a
		// brand-new tag namespace.
		episode := lic.PastDueAt.Unix()

		for _, step := range steps {
			if daysPastDue < step.minDays {
				continue
			}
			tag := step.tag + ":" + strconvI64(episode)
			if c.store.HasNotification(ctx, lic.ID, tag) {
				break // already sent the highest-tier; nothing lower will fire
			}
			step.send(lic.Email, productName)
			c.store.RecordNotification(ctx, lic.ID, tag)
			c.logger.Info("dunning email sent",
				"license_id", lic.ID, "tag", step.tag,
				"days_past_due", daysPastDue, "episode", episode)
			break // only one email per checker run per license
		}
	}
}

// strconvI64 keeps the small helper local so the expiry file stays
// self-contained. Tagging on epoch ints avoids any timezone / DST
// drift you'd get from a date-string anchor.
func strconvI64(n int64) string {
	return strconv.FormatInt(n, 10)
}

// SendRenewalReminders notifies users in the 24 hours leading up to
// renewal. We scan a wide [now, now+25h] window and dedup with the
// "renewal_24h" tag so a server outage during the narrow original
// 23-25h window doesn't silently skip the email.
func (c *ExpiryChecker) SendRenewalReminders(ctx context.Context) {
	from := time.Now()
	to := time.Now().Add(25 * time.Hour)
	licenses, err := c.store.FindExpiringLicenses(ctx, from, to)
	if err != nil {
		return
	}
	for _, lic := range licenses {
		if lic.Status != model.StatusActive {
			continue
		}
		// Only for subscription type
		if lic.Plan == nil || lic.Plan.LicenseType != "subscription" {
			continue
		}
		tag := "renewal_24h"
		if c.store.HasNotification(ctx, lic.ID, tag) {
			continue
		}
		productName := ""
		if lic.Product != nil {
			productName = lic.Product.Name
		}
		renewalDate := ""
		if lic.ValidUntil != nil {
			renewalDate = lic.ValidUntil.Format("2006-01-02")
		}
		c.email.SendRenewalReminder(lic.Email, productName, renewalDate)
		c.store.RecordNotification(ctx, lic.ID, tag)
	}
}

// SyncSubscriptionStates is a fallback that syncs subscription table with license status.
// This catches cases where Stripe webhooks were missed.
func (c *ExpiryChecker) SyncSubscriptionStates(ctx context.Context) {
	if err := c.store.SyncSubscriptionStatuses(ctx); err != nil {
		c.logger.Error("subscription sync failed", "error", err)
	}
}
