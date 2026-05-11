package service

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	stripe "github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/billing/meterevent"

	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/store"
)

// MeteredBillingSyncer drains the metered_billing event log to
// Stripe's Billing Meter API.
//
// One row per RecordUsage call becomes one Stripe meter event. The
// row's identifier doubles as the Stripe event identifier so
// Stripe's own 24h-rolling dedup absorbs retries — we can call as
// many times as we want without overshooting the customer's bill.
//
// Persistent failures stick: attempts increments + last_error
// captures the reason. After MaxAttempts we leave the row stuck
// (visible in admin UI) rather than silently dropping it; operators
// decide whether to ack or fix the underlying configuration.
type MeteredBillingSyncer struct {
	store  *store.Store
	logger *slog.Logger
	// dispatch is the function that actually talks to Stripe. Real
	// callers wire stripeMeterEventDispatch; tests inject a fake to
	// assert request shape without hitting the network.
	dispatch    meterEventDispatcher
	maxAttempts int
}

// meterEventDispatcher is the seam between the sync engine and the
// Stripe SDK. Keeping it as a function value (rather than a global
// stripe.Client) makes the syncer testable end-to-end without
// requiring a live Stripe key.
type meterEventDispatcher func(ctx context.Context, in stripeMeterEvent) (externalID string, err error)

// stripeMeterEvent carries everything the dispatcher needs to push
// one meter event. customer_id is required; Stripe rejects events
// that can't be mapped to a subscription.
type stripeMeterEvent struct {
	EventName  string
	CustomerID string
	Quantity   int64
	Identifier string
	Timestamp  time.Time
}

const defaultMeteredMaxAttempts = 8

// NewMeteredBillingSyncer wires the production Stripe dispatcher.
// The Stripe-go SDK reads its API key from stripe.Key; main.go
// initialises that during config load.
func NewMeteredBillingSyncer(s *store.Store, logger *slog.Logger) *MeteredBillingSyncer {
	return &MeteredBillingSyncer{
		store:       s,
		logger:      logger,
		dispatch:    stripeMeterEventDispatch,
		maxAttempts: defaultMeteredMaxAttempts,
	}
}

// stripeMeterEventDispatch posts one meter event to Stripe. It is
// the only place this package talks to the network.
//
// Stripe-side semantics:
//   - identifier dedupes over a rolling 24h window. A second call
//     with the same identifier returns the original event.
//   - payload.value is a string (Stripe API quirk).
//   - timestamp must be within the past 35d or up to 5min ahead.
func stripeMeterEventDispatch(ctx context.Context, in stripeMeterEvent) (string, error) {
	params := &stripe.BillingMeterEventParams{
		EventName:  stripe.String(in.EventName),
		Identifier: stripe.String(in.Identifier),
		Timestamp:  stripe.Int64(in.Timestamp.Unix()),
		Payload: map[string]string{
			"stripe_customer_id": in.CustomerID,
			"value":              strconv.FormatInt(in.Quantity, 10),
		},
	}
	params.Context = ctx
	ev, err := meterevent.New(params)
	if err != nil {
		return "", err
	}
	if ev == nil {
		return "", nil
	}
	return ev.Identifier, nil
}

// RunOnce drains up to `limit` pending events. Returns the number
// of rows that were actually pushed to Stripe (excludes sentinel-
// drained rows like missing customer / cleared event_name / legacy
// no-identifier). Errors are logged + recorded per-row, never
// propagated — a poison-pill row should not block the rest of the
// queue.
func (m *MeteredBillingSyncer) RunOnce(ctx context.Context, limit int) (pushed int) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := m.store.ListUnsyncedMetered(ctx, limit)
	if err != nil {
		m.logger.Error("metered: list unsynced failed", "error", err)
		return 0
	}
	for _, row := range rows {
		if row.Attempts >= m.maxAttempts {
			// Hit the ceiling. Don't keep retrying forever; admin
			// inspection required. Skip silently — the row's
			// last_error already records the situation.
			continue
		}
		sent, err := m.syncOne(ctx, row)
		if err != nil {
			m.logger.Warn("metered: push failed",
				"id", row.ID, "license_id", row.LicenseID,
				"feature", row.Feature, "attempts", row.Attempts+1,
				"error", err)
			// Single source of attempts bump on failure. syncOne
			// does NOT touch attempts; RecordMeteredSyncFailure
			// owns it. Without this split the counter doubled.
			_ = m.store.RecordMeteredSyncFailure(ctx, row.ID, err.Error())
			continue
		}
		if sent {
			pushed++
		}
	}
	return pushed
}

// syncOne pushes a single event. Returns (sent, err):
//
//   - sent=true,  err=nil  → row was pushed to Stripe + marked synced
//   - sent=false, err=nil  → row was sentinel-drained (no customer,
//     no event_name, legacy identifier-missing) — counted toward
//     queue progress but NOT toward "actually billed"
//   - sent=false, err!=nil → dispatcher / store failure; caller
//     handles attempts + last_error
func (m *MeteredBillingSyncer) syncOne(ctx context.Context, row *model.MeteredBilling) (bool, error) {
	if row.Identifier == "" {
		// Legacy aggregate-style rows pre-migration; safer to skip
		// than to push without an idempotency key. Mark synced so
		// they stop blocking the queue.
		if err := m.store.MarkMeteredSynced(ctx, row.ID, "legacy_no_identifier"); err != nil {
			return false, fmt.Errorf("legacy skip mark: %w", err)
		}
		return false, nil
	}

	// Resolve customer + meter event_name in one license lookup.
	lic, err := m.store.FindLicenseByID(ctx, row.LicenseID)
	if err != nil {
		return false, fmt.Errorf("license lookup: %w", err)
	}
	if lic.StripeCustomerID == "" {
		// Self-hosted / non-Stripe license — we shouldn't have
		// enqueued the event in the first place, but tolerate it:
		// nothing to push, mark synced to avoid log churn.
		if err := m.store.MarkMeteredSynced(ctx, row.ID, "no_stripe_customer"); err != nil {
			return false, err
		}
		return false, nil
	}

	eventName := ""
	if lic.Plan != nil {
		for _, e := range lic.Plan.Entitlements {
			if e.Feature == row.Feature && e.StripeMeterEventName != "" {
				eventName = e.StripeMeterEventName
				break
			}
		}
	}
	if eventName == "" {
		// Entitlement reconfigured (event_name cleared) AFTER the
		// row was enqueued. There's nothing to push to; mark synced
		// so the queue drains.
		if err := m.store.MarkMeteredSynced(ctx, row.ID, "no_event_name"); err != nil {
			return false, err
		}
		return false, nil
	}

	externalID, err := m.dispatch(ctx, stripeMeterEvent{
		EventName:  eventName,
		CustomerID: lic.StripeCustomerID,
		Quantity:   row.Quantity,
		Identifier: row.Identifier,
		Timestamp:  row.CreatedAt,
	})
	if err != nil {
		return false, classifyStripeError(err)
	}
	if externalID == "" {
		externalID = row.Identifier
	}
	if err := m.store.MarkMeteredSynced(ctx, row.ID, externalID); err != nil {
		return false, err
	}
	return true, nil
}

// classifyStripeError keeps the error message compact for the
// last_error column. Stripe's raw errors can be multi-line JSON;
// we drop everything past the first line so the admin UI doesn't
// have to render wrapped paragraphs.
func classifyStripeError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	return fmt.Errorf("stripe: %s", msg)
}

// StartSyncLoop runs RunOnce on a fixed interval. interval should
// be in minutes-scale; sub-minute polling burns Stripe rate budget
// for no real benefit (Stripe aggregates server-side anyway).
func (m *MeteredBillingSyncer) StartSyncLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	// Initial drain on boot — picks up anything queued during the
	// last shutdown window.
	m.RunOnce(ctx, 200)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.RunOnce(ctx, 200)
		}
	}
}
