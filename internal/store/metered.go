package store

import (
	"context"
	"time"

	"github.com/tabloy/keygate/internal/model"
)

// InsertMeteredEvent appends an event-log row to be pushed to Stripe
// by MeteredBillingSync. quantity is the DELTA from this single
// usage call (not the cumulative total).
//
// The identifier is the row's ID — opaque to callers, used as the
// Stripe meter event's identifier so retries dedupe within Stripe's
// rolling 24h window.
func (s *Store) InsertMeteredEvent(ctx context.Context, licenseID, feature, periodKey string, quantity int64) error {
	id := newID()
	m := &model.MeteredBilling{
		ID:         id,
		LicenseID:  licenseID,
		Feature:    feature,
		Quantity:   quantity,
		PeriodKey:  periodKey,
		Identifier: id,
	}
	_, err := s.DB.NewInsert().Model(m).Exec(ctx)
	return err
}

func (s *Store) ListUnsyncedMetered(ctx context.Context, limit int) ([]*model.MeteredBilling, error) {
	var out []*model.MeteredBilling
	err := s.DB.NewSelect().Model(&out).
		Where("synced = false").OrderExpr("created_at ASC").Limit(limit).Scan(ctx)
	return out, err
}

func (s *Store) MarkMeteredSynced(ctx context.Context, id, externalID string) error {
	now := time.Now()
	_, err := s.DB.NewUpdate().Model((*model.MeteredBilling)(nil)).
		Set("synced = true, synced_at = ?, external_id = ?, last_error = ''", now, externalID).
		Where("id = ?", id).Exec(ctx)
	return err
}

// RecordMeteredSyncFailure stamps the row with the failure reason
// and bumps the attempts counter. We DON'T flip synced=true — the
// next sync pass tries again. last_error is human-readable and
// surfaces in the admin UI so operators can investigate without
// log-diving.
func (s *Store) RecordMeteredSyncFailure(ctx context.Context, id, reason string) error {
	const maxLastErr = 512
	if len(reason) > maxLastErr {
		reason = reason[:maxLastErr]
	}
	_, err := s.DB.NewUpdate().Model((*model.MeteredBilling)(nil)).
		Set("attempts = attempts + 1, last_error = ?", reason).
		Where("id = ?", id).Exec(ctx)
	return err
}

// MarkMeteredAttempted bumps attempts without changing synced state.
// Called on every push regardless of outcome so retry budgets are
// visible. Successful pushes also call MarkMeteredSynced which
// already clears last_error.
func (s *Store) MarkMeteredAttempted(ctx context.Context, id string) error {
	_, err := s.DB.NewUpdate().Model((*model.MeteredBilling)(nil)).
		Set("attempts = attempts + 1").
		Where("id = ?", id).Exec(ctx)
	return err
}

func (s *Store) ListMeteredBilling(ctx context.Context, licenseID string) ([]*model.MeteredBilling, error) {
	var out []*model.MeteredBilling
	err := s.DB.NewSelect().Model(&out).
		Where("license_id = ?", licenseID).OrderExpr("created_at DESC").Scan(ctx)
	return out, err
}
