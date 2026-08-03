// Command seed populates a local/dev database with demo data: three
// products (a desktop app, a SaaS app, and a hybrid toolkit), plans with
// entitlements covering every value type (bool / int / string / quota),
// and licenses across every lifecycle status, plus a few device
// activations. Names are deliberately generic ("Acme …") sample data.
//
// Safe to run repeatedly — it detects its own seed data by slug and
// exits early if already present.
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

const seedProductSlug = "acme-desktop"
const day = 24 * time.Hour

var (
	db  *store.Store
	ctx = context.Background()
	// planBySlug maps a plan slug to the created plan (carrying its
	// product id) so licenses can reference plans by a readable key.
	planBySlug = map[string]*model.Plan{}
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://keygate:keygate@localhost:5432/keygate?sslmode=disable"
	}

	var err error
	db, err = store.New(dsn)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer db.Close()

	// Idempotent — skips already-applied migrations so `make seed` works
	// against a fresh DB without first starting the server.
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

	// ─── Products ───
	desktop := product("Acme Desktop", seedProductSlug, "desktop")
	cloud := product("Acme Cloud", "acme-cloud", "saas")
	toolkit := product("Acme Toolkit", "acme-toolkit", "hybrid")

	// ─── Desktop plans (perpetual + subscription) ───
	plan(desktop, planSpec{name: "Free", slug: "desktop-free", licenseType: "perpetual", maxAct: 1, ents: []ent{
		{"editor", "bool", "true", "", ""},
		{"daily_exports", "quota", "20", "daily", "exports"},
	}})
	plan(desktop, planSpec{name: "Pro", slug: "desktop-pro", licenseType: "subscription", maxAct: 3, grace: 7, support: 365, billing: "month", ents: []ent{
		{"editor", "bool", "true", "", ""},
		{"batch_export", "bool", "true", "", ""},
		{"max_projects", "int", "50", "", ""},
		{"tier", "string", "pro", "", ""},
		{"exports", "quota", "0", "monthly", "exports"}, // 0 = unlimited
	}})
	plan(desktop, planSpec{name: "Studio", slug: "desktop-studio", licenseType: "perpetual", maxAct: 5, support: 365, ents: []ent{
		{"editor", "bool", "true", "", ""},
		{"batch_export", "bool", "true", "", ""},
		{"priority_support", "bool", "true", "", ""},
		{"max_projects", "int", "500", "", ""},
	}})

	// ─── Cloud plans (SaaS, seat-based) ───
	plan(cloud, planSpec{name: "Starter", slug: "cloud-starter", licenseType: "subscription", maxSeats: 3, grace: 7, billing: "month", ents: []ent{
		{"projects", "int", "5", "", ""},
		{"api_calls", "quota", "10000", "monthly", "requests"},
	}})
	plan(cloud, planSpec{name: "Team", slug: "cloud-team", licenseType: "subscription", maxSeats: 10, grace: 7, billing: "month", ents: []ent{
		{"sso", "bool", "true", "", ""},
		{"projects", "int", "50", "", ""},
		{"api_calls", "quota", "100000", "monthly", "requests"},
	}})
	plan(cloud, planSpec{name: "Business", slug: "cloud-business", licenseType: "subscription", maxSeats: 50, grace: 14, billing: "month", ents: []ent{
		{"sso", "bool", "true", "", ""},
		{"audit_log", "bool", "true", "", ""},
		{"api_calls", "quota", "1000000", "monthly", "requests"},
	}})

	// ─── Toolkit plan (hybrid: activations + seats) ───
	plan(toolkit, planSpec{name: "Standard", slug: "toolkit-standard", licenseType: "subscription", maxAct: 2, maxSeats: 5, grace: 7, billing: "month", ents: []ent{
		{"plugins", "int", "10", "", ""},
		{"api_calls", "quota", "50000", "monthly", "requests"},
	}})

	// ─── Licenses across every lifecycle status ───
	yr := plus(365 * day)
	mo := plus(30 * day)
	trial := plus(14 * day)
	for _, l := range []licSpec{
		{"desktop-pro", "alice@example.com", "DEMO-PRO-0001", "active", yr, yr, []string{"MBP-alice", "iMac-studio"}},
		{"desktop-free", "bob@example.com", "DEMO-FREE-0001", "active", nil, nil, []string{"thinkpad-bob"}},
		{"desktop-pro", "carol@example.com", "DEMO-TRIAL-0001", "trialing", trial, nil, nil},
		{"desktop-studio", "dave@example.com", "DEMO-STUDIO-0001", "active", nil, yr, []string{"workstation-dave"}},
		{"desktop-pro", "erin@example.com", "DEMO-SUSPENDED-01", "suspended", yr, yr, nil},
		{"desktop-pro", "frank@example.com", "DEMO-EXPIRED-0001", "expired", plus(-30 * day), plus(-30 * day), nil},
		{"cloud-starter", "grace@example.com", "DEMO-STARTER-0001", "active", yr, nil, nil},
		{"cloud-team", "heidi@example.com", "DEMO-TEAM-0001", "active", yr, nil, nil},
		{"cloud-team", "ivan@example.com", "DEMO-TRIAL-CLOUD1", "trialing", trial, nil, nil},
		{"cloud-team", "judy@example.com", "DEMO-PASTDUE-0001", "past_due", plus(-2 * day), nil, nil},
		{"cloud-business", "ken@example.com", "DEMO-BUSINESS-001", "active", yr, nil, nil},
		{"cloud-starter", "laura@example.com", "DEMO-CANCELED-001", "canceled", mo, nil, nil},
		{"toolkit-standard", "mike@example.com", "DEMO-TOOLKIT-0001", "active", yr, nil, []string{"ci-runner-1"}},
		{"toolkit-standard", "nina@example.com", "DEMO-REVOKED-0001", "revoked", nil, nil, nil},
	} {
		license(l)
	}

	log.Printf("seeded: 3 products, %d plans, 14 licenses across all statuses (keys prefixed DEMO-)", len(planBySlug))
}

// ─── helpers ───

func plus(d time.Duration) *time.Time { t := time.Now().Add(d); return &t }

func product(name, slug, typ string) *model.Product {
	p := &model.Product{Name: name, Slug: slug, Type: typ}
	must("product "+slug, db.CreateProduct(ctx, p))
	return p
}

type ent struct{ feature, valueType, value, quotaPeriod, quotaUnit string }

type planSpec struct {
	name, slug, licenseType, billing string
	maxAct, maxSeats, grace, support int
	ents                             []ent
}

func plan(p *model.Product, s planSpec) {
	pl := &model.Plan{
		ProductID: p.ID, Name: s.name, Slug: s.slug,
		LicenseType: s.licenseType, LicenseModel: "standard",
		MaxActivations: s.maxAct, MaxSeats: s.maxSeats,
		GraceDays: s.grace, SupportDays: s.support, BillingInterval: s.billing,
	}
	must("plan "+s.slug, db.CreatePlan(ctx, pl))
	for _, e := range s.ents {
		must("entitlement "+s.slug+"/"+e.feature, db.CreateEntitlement(ctx, &model.Entitlement{
			PlanID: pl.ID, Feature: e.feature, ValueType: e.valueType, Value: e.value,
			QuotaPeriod: e.quotaPeriod, QuotaUnit: e.quotaUnit,
		}))
	}
	planBySlug[s.slug] = pl
}

type licSpec struct {
	planSlug, email, key, status string
	validUntil, supportUntil     *time.Time
	activations                  []string
}

func license(s licSpec) {
	pl := planBySlug[s.planSlug]
	if pl == nil {
		log.Fatalf("seed license %s: unknown plan %q", s.key, s.planSlug)
	}
	lic := &model.License{
		ProductID: pl.ProductID, PlanID: pl.ID, Email: s.email,
		LicenseKey: s.key, Status: s.status,
		ValidUntil: s.validUntil, SupportUntil: s.supportUntil,
	}
	must("license "+s.key, db.CreateLicense(ctx, lic))
	for _, id := range s.activations {
		must("activation "+id, db.CreateActivation(ctx, &model.Activation{
			LicenseID: lic.ID, Identifier: id, IdentifierType: "device", Label: id,
		}))
	}
}

func must(what string, err error) {
	if err != nil {
		log.Fatalf("seed %s: %v", what, err)
	}
}
