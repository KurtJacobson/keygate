package crypto

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// SubkeyBytes is the standard length of derived subkeys (32 bytes for AES-256).
const SubkeyBytes = 32

// purposePrefix is prepended to every purpose string so the same master
// across two Keygate versions cannot accidentally produce the same subkey
// for "license-key" if the protocol changes.
const purposePrefix = "keygate-v1-"

// DeriveSubkey produces a 32-byte subkey from the master key, deterministically
// bound to a "purpose" label via HKDF-SHA256 (RFC 5869).
//
// The same (master, purpose) always yields the same subkey. Different
// purposes always yield different subkeys, even if the same master is used.
// This means ciphertexts from one purpose cannot be replayed under another:
// AES-GCM under subkey_A cannot be decrypted under subkey_B.
//
// Example:
//
//	master, _ := hex.DecodeString(cfg.ReleaseKeyEncryptionKey)
//	licenseKeyAEAD, _ := crypto.NewAESGCM(crypto.DeriveSubkey(master, "license-key"))
//	releaseSigAEAD, _ := crypto.NewAESGCM(crypto.DeriveSubkey(master, "release-signing-private-key"))
//
// Master must be at least 16 bytes; we recommend MasterKeyBytes (32).
func DeriveSubkey(master []byte, purpose string) ([]byte, error) {
	if len(master) < 16 {
		return nil, fmt.Errorf("crypto: master key too short for HKDF (got %d bytes, need >=16)", len(master))
	}
	if purpose == "" {
		return nil, errors.New("crypto: purpose label is required")
	}
	r := hkdf.New(sha256.New, master, nil /* salt */, []byte(purposePrefix+purpose))
	out := make([]byte, SubkeyBytes)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, fmt.Errorf("crypto: hkdf read: %w", err)
	}
	return out, nil
}

// MustDeriveAEAD is a convenience wrapper for boot-time wiring: derive a
// subkey and immediately wrap it in an AES-GCM AEAD. Panics on failure
// (which can only happen if the master is malformed — caught at startup).
//
// Prefer this in main() so misconfiguration is loud at boot rather than
// silent on first call.
func MustDeriveAEAD(master []byte, purpose string) *AESGCM {
	sub, err := DeriveSubkey(master, purpose)
	if err != nil {
		panic(fmt.Sprintf("crypto: derive subkey for %q: %v", purpose, err))
	}
	a, err := NewAESGCM(sub)
	if err != nil {
		panic(fmt.Sprintf("crypto: aead init for %q: %v", purpose, err))
	}
	return a
}
