package crypto

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestDeriveSubkeyDeterministic(t *testing.T) {
	master := make([]byte, MasterKeyBytes)
	if _, err := rand.Read(master); err != nil {
		t.Fatalf("rand: %v", err)
	}

	a, err := DeriveSubkey(master, "license-key")
	if err != nil {
		t.Fatalf("derive 1: %v", err)
	}
	b, err := DeriveSubkey(master, "license-key")
	if err != nil {
		t.Fatalf("derive 2: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("same master+purpose must produce identical subkey")
	}
	if len(a) != SubkeyBytes {
		t.Errorf("expected %d bytes, got %d", SubkeyBytes, len(a))
	}
}

func TestDeriveSubkeyPurposeIsolation(t *testing.T) {
	master := make([]byte, MasterKeyBytes)
	if _, err := rand.Read(master); err != nil {
		t.Fatalf("rand: %v", err)
	}

	licenseKey, _ := DeriveSubkey(master, "license-key")
	releaseKey, _ := DeriveSubkey(master, "release-signing-private-key")

	if bytes.Equal(licenseKey, releaseKey) {
		t.Error("different purposes must produce different subkeys")
	}

	// Encrypt under one, attempt to decrypt under the other — must fail
	// (this is the whole point of purpose isolation).
	a1, _ := NewAESGCM(licenseKey)
	a2, _ := NewAESGCM(releaseKey)

	ct, _ := a1.Encrypt([]byte("secret"), nil)
	if _, err := a2.Decrypt(ct, nil); err == nil {
		t.Error("ciphertext from one purpose must NOT decrypt under another")
	}
}

func TestDeriveSubkeyDifferentMasterDifferentSubkey(t *testing.T) {
	m1 := make([]byte, MasterKeyBytes)
	m2 := make([]byte, MasterKeyBytes)
	if _, err := rand.Read(m1); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if _, err := rand.Read(m2); err != nil {
		t.Fatalf("rand: %v", err)
	}

	a, _ := DeriveSubkey(m1, "p")
	b, _ := DeriveSubkey(m2, "p")
	if bytes.Equal(a, b) {
		t.Error("different masters must produce different subkeys")
	}
}

func TestDeriveSubkeyValidation(t *testing.T) {
	short := make([]byte, 8)
	if _, err := DeriveSubkey(short, "p"); err == nil {
		t.Error("expected error for short master")
	}

	good := make([]byte, MasterKeyBytes)
	if _, err := DeriveSubkey(good, ""); err == nil {
		t.Error("expected error for empty purpose")
	}
}

func TestMustDeriveAEAD(t *testing.T) {
	master := make([]byte, MasterKeyBytes)
	_, _ = rand.Read(master)

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("must not panic on valid input: %v", r)
		}
	}()
	a := MustDeriveAEAD(master, "license-key")
	if a == nil {
		t.Error("expected non-nil AEAD")
	}
}

func TestMustDeriveAEADPanicsOnBadInput(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on short master")
		}
	}()
	MustDeriveAEAD(make([]byte, 8), "license-key")
}
