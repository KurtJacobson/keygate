package payment

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/store"
)

// TestNotifyPaymentRecovered_EpisodeScopedDedup is the regression
// guard for the past_due → active recovered-email dedup bug.
//
// Before the fix, the tag was hard-coded "payment_recovered" and
// the notifications (license_id, tag) UNIQUE constraint silently
// dropped every recovery after the first one for a given license.
// This test asserts the tag now carries the past_due_at epoch so
// each episode gets its own row.
//
// Skips when TEST_DATABASE_URL is unset (matches store integration
// tests).
func TestNotifyPaymentRecovered_EpisodeScopedDedup(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("skipping integration test: TEST_DATABASE_URL not set")
	}
	s, err := store.New(dsn)
	if err != nil {
		t.Skipf("skipping integration test: %v", err)
	}
	defer s.Close()
	if err := s.RunMigrations("../../db/migrations"); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	ctx := context.Background()

	suffix := time.Now().Format("150405.000")
	prod := &model.Product{Name: "Recovered Test", Slug: "rec-" + suffix, Type: "hybrid"}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatalf("create product: %v", err)
	}
	plan := &model.Plan{
		ProductID: prod.ID, Name: "Pro", Slug: "pro-" + suffix,
		LicenseType: "subscription", GraceDays: 7,
		LicenseModel: "standard",
	}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	lic := &model.License{
		ProductID: prod.ID, PlanID: plan.ID,
		Email:      "rec-" + suffix + "@example.com",
		LicenseKey: "KEY-rec-" + suffix,
		Status:     model.StatusActive,
	}
	if err := s.CreateLicense(ctx, lic); err != nil {
		t.Fatalf("create license: %v", err)
	}

	// Construct a handler with a nil email + webhook service — the
	// helper tolerates both being nil (sending is best-effort).
	h := &StripeHandler{Store: s}

	episode1 := int64(1700000000)
	episode2 := int64(1710000000)

	// Episode 1 — first recovery fires.
	h.notifyPaymentRecovered(ctx, lic, episode1)
	if !s.HasNotification(ctx, lic.ID, "payment_recovered:1700000000") {
		t.Fatal("expected notification for episode 1 to be recorded")
	}
	if s.HasNotification(ctx, lic.ID, "payment_recovered:1710000000") {
		t.Fatal("episode 2 notification should NOT exist yet")
	}

	// Same episode again — dedups. Sanity: still exactly one row
	// with this tag (UNIQUE constraint enforces it, but we want a
	// behavioural assertion in case the dedup branch ever drifts).
	h.notifyPaymentRecovered(ctx, lic, episode1)
	var ep1Count int
	if err := s.DB.NewRaw(
		`SELECT COUNT(*) FROM notifications WHERE license_id = ? AND tag = ?`,
		lic.ID, "payment_recovered:1700000000",
	).Scan(ctx, &ep1Count); err != nil {
		t.Fatalf("count ep1: %v", err)
	}
	if ep1Count != 1 {
		t.Fatalf("episode 1 dedup: expected 1 row, got %d", ep1Count)
	}

	// Episode 2 — second past_due → recovered cycle. Must fire.
	// This is the case that the pre-fix code silently dropped.
	h.notifyPaymentRecovered(ctx, lic, episode2)
	if !s.HasNotification(ctx, lic.ID, "payment_recovered:1710000000") {
		t.Fatal("episode 2 recovery should have been recorded")
	}

	// And the original tag (without episode suffix) is NOT used —
	// that was the broken behavior we're guarding against.
	if s.HasNotification(ctx, lic.ID, "payment_recovered") {
		t.Fatal("legacy un-suffixed tag must not appear: it would re-trigger the original silent-dedup bug")
	}
}

// TestNotifyPaymentRecovered_LegacyZeroEpisode covers the fallback
// branch: a license with no past_due_at (legacy row pre-migration)
// should still get exactly one recovered notification, not zero.
// We don't want the fix to accidentally silence the legacy case.
func TestNotifyPaymentRecovered_LegacyZeroEpisode(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("skipping integration test: TEST_DATABASE_URL not set")
	}
	s, err := store.New(dsn)
	if err != nil {
		t.Skipf("skipping integration test: %v", err)
	}
	defer s.Close()
	if err := s.RunMigrations("../../db/migrations"); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	ctx := context.Background()

	suffix := time.Now().Format("150405.000") + "-legacy"
	prod := &model.Product{Name: "Legacy Test", Slug: "legacy-" + suffix, Type: "hybrid"}
	_ = s.CreateProduct(ctx, prod)
	plan := &model.Plan{
		ProductID: prod.ID, Name: "Pro", Slug: "pro-" + suffix,
		LicenseType: "subscription", GraceDays: 7,
		LicenseModel: "standard",
	}
	_ = s.CreatePlan(ctx, plan)
	lic := &model.License{
		ProductID: prod.ID, PlanID: plan.ID,
		Email:      "legacy-" + suffix + "@example.com",
		LicenseKey: "KEY-legacy-" + suffix,
		Status:     model.StatusActive,
	}
	_ = s.CreateLicense(ctx, lic)

	h := &StripeHandler{Store: s}
	h.notifyPaymentRecovered(ctx, lic, 0)
	if !s.HasNotification(ctx, lic.ID, "payment_recovered") {
		t.Fatal("legacy zero-episode path must still record the bare tag once")
	}
	// Re-fire with zero — should dedup.
	h.notifyPaymentRecovered(ctx, lic, 0)
	var n int
	if err := s.DB.NewRaw(
		`SELECT COUNT(*) FROM notifications WHERE license_id = ? AND tag = 'payment_recovered'`,
		lic.ID,
	).Scan(ctx, &n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("legacy zero-episode: expected 1 row, got %d", n)
	}
}
