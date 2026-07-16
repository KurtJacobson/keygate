package totp

import (
	"encoding/base32"
	"strings"
	"testing"
)

// rfcSecret is the RFC 6238 Appendix B test secret ("12345678901234567890").
var rfcSecret = base32.StdEncoding.WithPadding(base32.NoPadding).
	EncodeToString([]byte("12345678901234567890"))

// TestValidate_RFC6238Vectors pins the algorithm against the published
// SHA-1 test vectors. The RFC lists 8-digit codes; ours are the last 6.
func TestValidate_RFC6238Vectors(t *testing.T) {
	vectors := []struct {
		unix int64
		code string // last 6 digits of the RFC's 8-digit value
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	}
	for _, v := range vectors {
		if _, ok := Validate(rfcSecret, v.code, v.unix); !ok {
			t.Errorf("t=%d: code %s should validate", v.unix, v.code)
		}
	}
}

func TestValidate_AcceptsAdjacentSlots(t *testing.T) {
	// Code for t=59 lives in slot 1 (30–59s). It must validate at
	// t=30..89 (same slot ±1 period) and fail at t=120 (two slots on).
	if _, ok := Validate(rfcSecret, "287082", 89); !ok {
		t.Error("code should validate one period late")
	}
	if slot, ok := Validate(rfcSecret, "287082", 30); !ok || slot != 1 {
		t.Errorf("code should validate in its own slot, got slot=%d ok=%v", slot, ok)
	}
	if _, ok := Validate(rfcSecret, "287082", 120); ok {
		t.Error("code must NOT validate two periods late")
	}
}

func TestValidate_RejectsGarbage(t *testing.T) {
	for _, code := range []string{"", "12345", "1234567", "abcdef", "000 00"} {
		if _, ok := Validate(rfcSecret, code, 59); ok {
			t.Errorf("code %q should not validate", code)
		}
	}
	if _, ok := Validate("not-base32!!", "287082", 59); ok {
		t.Error("invalid secret should not validate")
	}
	if _, ok := Validate("", "287082", 59); ok {
		t.Error("empty secret should not validate")
	}
}

func TestGenerateSecret(t *testing.T) {
	a, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := GenerateSecret()
	if a == b {
		t.Error("secrets should be random")
	}
	if _, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(a); err != nil {
		t.Errorf("secret is not valid unpadded base32: %v", err)
	}
}

func TestProvisioningURI(t *testing.T) {
	uri := ProvisioningURI("Keygate", "admin@example.com", "ABC234")
	for _, want := range []string{
		"otpauth://totp/Keygate:admin%40example.com",
		"secret=ABC234",
		"issuer=Keygate",
		"digits=6",
		"period=30",
	} {
		if !strings.Contains(uri, want) {
			t.Errorf("URI missing %q: %s", want, uri)
		}
	}
}
