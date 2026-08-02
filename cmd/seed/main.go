// Command seed populates a local/dev database with demo data: two
// products (a desktop app and a SaaS app), plans with entitlements
// covering every value type (bool / int / string / quota), and a few
// licenses in different states. Safe to run repeatedly — it detects its
// own seed data by slug and exits early if already present.
//
// Usage:
//
//	DATABASE_URL=postgres://keygate:keygate@localhost:5432/keygate?sslmode=disable \
//	    go run ./cmd/seed
package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/store"
)

const seedProductSlug = "inventor-dxf"

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://keygate:keygate@localhost:5432/keygate?sslmode=disable"
	}

	db, err := store.New(dsn)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	// Idempotent — skips already-applied migrations. Lets `make seed`
	// work against a fresh DB without first starting the server.
	if err := db.RunMigrations("db/migrations"); err != nil {
		log.Fatalf("migrations: %v", err)
	}

	exists, err := db.DB.NewSelect().Model((*model.Product)(nil)).
		Where("slug = ?", seedProductSlug).Exists(ctx)
	if err != nil {
		log.Fatalf("check existing: %v", err)
	}
	if exists {
		log.Printf("seed data already present (product %q exists) — nothing to do", seedProductSlug)
		return
	}

	// ─── Product 1: desktop app with perpetual + subscription plans ───
	desktop := &model.Product{Name: "Inventor DXF FlatExport", Slug: seedProductSlug, Type: "desktop"}
	must("product", db.CreateProduct(ctx, desktop))

	free := &model.Plan{
		ProductID: desktop.ID, Name: "Free", Slug: "free",
		LicenseType: "perpetual", LicenseModel: "standard", MaxActivations: 1,
	}
	must("plan free", db.CreatePlan(ctx, free))
	seedEntitlements(ctx, db, free.ID,
		ent{"dxf_export", "bool", "true", "", ""},
		ent{"daily_exports", "quota", "20", "daily", "exports"},
	)

	pro := &model.Plan{
		ProductID: desktop.ID, Name: "Pro", Slug: "pro",
		LicenseType: "subscription", LicenseModel: "standard", MaxActivations: 3,
		GraceDays: 7, SupportDays: 365, BillingInterval: "month",
	}
	must("plan pro", db.CreatePlan(ctx, pro))
	seedEntitlements(ctx, db, pro.ID,
		ent{"dxf_export", "bool", "true", "", ""},
		ent{"batch_export", "bool", "true", "", ""},
		ent{"max_parts", "int", "1000", "", ""},
		ent{"tier", "string", "pro", "", ""},
		ent{"api_calls", "quota", "0", "monthly", "requests"}, // 0 = unlimited
	)

	// ─── Product 2: SaaS app with a team plan ───
	saas := &model.Product{Name: "Acme Cloud", Slug: "acme-cloud", Type: "saas"}
	must("product saas", db.CreateProduct(ctx, saas))

	team := &model.Plan{
		ProductID: saas.ID, Name: "Team", Slug: "team",
		LicenseType: "subscription", LicenseModel: "standard", MaxSeats: 5,
		GraceDays: 7, BillingInterval: "month",
	}
	must("plan team", db.CreatePlan(ctx, team))
	seedEntitlements(ctx, db, team.ID,
		ent{"sso", "bool", "true", "", ""},
		ent{"api_calls", "quota", "100000", "monthly", "requests"},
	)

	// ─── Licenses in a few states ───
	now := time.Now()
	inAYear := now.AddDate(1, 0, 0)
	trialEnd := now.AddDate(0, 0, 14)
	seedLicense(ctx, db, &model.License{
		ProductID: desktop.ID, PlanID: pro.ID, Email: "demo-pro@example.com",
		LicenseKey: "DEMO-PRO-0001", Status: "active", ValidUntil: &inAYear, SupportUntil: &inAYear,
	})
	seedLicense(ctx, db, &model.License{
		ProductID: desktop.ID, PlanID: free.ID, Email: "demo-free@example.com",
		LicenseKey: "DEMO-FREE-0001", Status: "active",
	})
	seedLicense(ctx, db, &model.License{
		ProductID: desktop.ID, PlanID: pro.ID, Email: "demo-trial@example.com",
		LicenseKey: "DEMO-TRIAL-0001", Status: "trialing", ValidUntil: &trialEnd,
	})
	seedLicense(ctx, db, &model.License{
		ProductID: saas.ID, PlanID: team.ID, Email: "demo-team@example.com",
		LicenseKey: "DEMO-TEAM-0001", Status: "active", ValidUntil: &inAYear,
	})

	log.Printf("seeded: 2 products, 3 plans, 4 licenses (keys DEMO-PRO-0001 / DEMO-FREE-0001 / DEMO-TRIAL-0001 / DEMO-TEAM-0001)")
}

type ent struct {
	feature, valueType, value, quotaPeriod, quotaUnit string
}

func seedEntitlements(ctx context.Context, db *store.Store, planID string, ents ...ent) {
	for _, e := range ents {
		must("entitlement "+e.feature, db.CreateEntitlement(ctx, &model.Entitlement{
			PlanID: planID, Feature: e.feature, ValueType: e.valueType, Value: e.value,
			QuotaPeriod: e.quotaPeriod, QuotaUnit: e.quotaUnit,
		}))
	}
}

func seedLicense(ctx context.Context, db *store.Store, l *model.License) {
	must("license "+l.LicenseKey, db.CreateLicense(ctx, l))
}

func must(what string, err error) {
	if err != nil {
		log.Fatalf("seed %s: %v", what, err)
	}
}
