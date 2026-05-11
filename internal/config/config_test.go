package config

import (
	"testing"
)

func TestIsDevLoginAllowed(t *testing.T) {
	tests := []struct {
		env  string
		want bool
	}{
		{"development", true},
		{"Development", true},
		{"DEVELOPMENT", true},
		{" development ", true},
		{"production", false},
		{"staging", false},
		{"", false},
		{"dev", false},
	}
	for _, tt := range tests {
		c := &Config{Environment: tt.env}
		got := c.IsDevLoginAllowed()
		if got != tt.want {
			t.Errorf("IsDevLoginAllowed(%q) = %v, want %v", tt.env, got, tt.want)
		}
	}
}

func TestIsProduction(t *testing.T) {
	tests := []struct {
		env  string
		want bool
	}{
		{"production", true},
		{"development", false},
		{"staging", false},
		{"", false},
	}
	for _, tt := range tests {
		c := &Config{Environment: tt.env}
		if got := c.IsProduction(); got != tt.want {
			t.Errorf("IsProduction(%q) = %v, want %v", tt.env, got, tt.want)
		}
	}
}

func TestIsAdminEmail(t *testing.T) {
	c := &Config{AdminEmails: []string{"admin@keygate.dev", "boss@company.com"}}

	tests := []struct {
		email string
		want  bool
	}{
		{"admin@keygate.dev", true},
		{"ADMIN@KEYGATE.DEV", true},
		{"boss@company.com", true},
		{"user@other.com", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := c.IsAdminEmail(tt.email); got != tt.want {
			t.Errorf("IsAdminEmail(%q) = %v, want %v", tt.email, got, tt.want)
		}
	}
}

func TestValidateSecurityDefaults(t *testing.T) {
	t.Run("valid dev config", func(t *testing.T) {
		c := &Config{
			Environment: "development",
			JWTSecret:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			// 32-byte ed25519 seed in hex (64 chars).
			LicenseSigningKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		}
		warnings, fatal := c.ValidateSecurityDefaults()
		if len(fatal) > 0 {
			t.Errorf("unexpected fatal: %v", fatal)
		}
		// Should warn about dev login
		found := false
		for _, w := range warnings {
			if w == "SECURITY: dev-login is enabled (ENVIRONMENT=development) — do NOT use in production" {
				found = true
			}
		}
		if !found {
			t.Error("expected dev-login warning")
		}
	})

	t.Run("short JWT secret", func(t *testing.T) {
		c := &Config{
			Environment: "production",
			JWTSecret:   "short",
			// 32-byte ed25519 seed in hex (64 chars).
			LicenseSigningKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		}
		_, fatal := c.ValidateSecurityDefaults()
		if len(fatal) == 0 {
			t.Error("expected fatal for short JWT secret")
		}
	})

	t.Run("invalid environment", func(t *testing.T) {
		c := &Config{
			Environment: "typo",
			JWTSecret:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			// 32-byte ed25519 seed in hex (64 chars).
			LicenseSigningKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		}
		_, fatal := c.ValidateSecurityDefaults()
		if len(fatal) == 0 {
			t.Error("expected fatal for invalid environment")
		}
	})

	t.Run("production without admin emails", func(t *testing.T) {
		c := &Config{
			Environment: "production",
			JWTSecret:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			// 32-byte ed25519 seed in hex (64 chars).
			LicenseSigningKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		}
		warnings, _ := c.ValidateSecurityDefaults()
		found := false
		for _, w := range warnings {
			if w == "SECURITY: ADMIN_EMAILS is empty — no one can access the admin panel" {
				found = true
			}
		}
		if !found {
			t.Error("expected admin emails warning in production")
		}
	})
}

// TestStripeLivemodeDerivation pins the rules described in
// deriveLivemode. The webhook handler rejects events whose Livemode
// flag doesn't match this config field, so any bug here directly
// enables cross-environment forgery (a leaked test secret being
// replayed at the prod endpoint, or vice versa).
func TestStripeLivemodeDerivation(t *testing.T) {
	cases := []struct {
		name      string
		envVal    string
		envSet    bool
		secretKey string
		want      bool
	}{
		// Explicit env wins regardless of key prefix.
		{"env true overrides sk_test", "true", true, "sk_test_xxx", true},
		{"env TRUE case-insensitive", "TRUE", true, "", true},
		{"env 1 numeric", "1", true, "", true},
		{"env false overrides sk_live", "false", true, "sk_live_xxx", false},
		{"env 0 numeric", "0", true, "sk_live_xxx", false},
		{"env garbage → false (only true/1 accepted)", "yes_please", true, "sk_live_xxx", false},
		{"env empty string set → false (env wins, value 'unset live')", "", true, "sk_live_xxx", false},

		// Without env: derive from key prefix.
		{"sk_live_ → true", "", false, "sk_live_abc", true},
		{"sk_test_ → false", "", false, "sk_test_abc", false},
		{"rk_live_ → true (restricted)", "", false, "rk_live_abc", true},
		{"rk_test_ → false (restricted)", "", false, "rk_test_abc", false},
		{"empty key → false (safe default)", "", false, "", false},
		{"unknown prefix (pk_live_) → false", "", false, "pk_live_abc", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveLivemode(tc.envVal, tc.envSet, tc.secretKey)
			if got != tc.want {
				t.Errorf("deriveLivemode(env=%q, set=%v, key=%q) = %v, want %v",
					tc.envVal, tc.envSet, tc.secretKey, got, tc.want)
			}
		})
	}
}
