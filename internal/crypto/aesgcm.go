// Package crypto provides authenticated encryption helpers for storing
// secrets at rest. It is intentionally small — only the operations the
// release signing subsystem needs.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

// MasterKeyBytes is the required length of the AEAD master key in bytes.
// 32 bytes selects AES-256-GCM. Larger or smaller keys are rejected.
const MasterKeyBytes = 32

// nonceBytes is the standard AES-GCM nonce length.
const nonceBytes = 12

// gcmTagBytes is the standard AES-GCM authentication tag length.
const gcmTagBytes = 16

// MinCiphertextBytes is the smallest possible ciphertext produced by Encrypt
// (zero-length plaintext): just a nonce + tag, no payload.
const MinCiphertextBytes = nonceBytes + gcmTagBytes

// AESGCM is an authenticated encryption helper. Construction validates the
// key once so per-call overhead is minimal. The struct is safe for
// concurrent use — cipher.AEAD is goroutine-safe per Go stdlib docs.
type AESGCM struct {
	aead cipher.AEAD
}

// NewAESGCM constructs an AESGCM from a 32-byte raw key. Returns an error
// if the key length is wrong — never silently truncates or pads.
func NewAESGCM(key []byte) (*AESGCM, error) {
	if len(key) != MasterKeyBytes {
		return nil, fmt.Errorf("crypto: master key must be %d bytes, got %d", MasterKeyBytes, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: cipher init: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: gcm init: %w", err)
	}
	return &AESGCM{aead: aead}, nil
}

// NewAESGCMFromHex parses a hex-encoded master key and constructs the AEAD.
// 64 hex chars = 32 bytes = AES-256.
func NewAESGCMFromHex(hexKey string) (*AESGCM, error) {
	if len(hexKey) != MasterKeyBytes*2 {
		return nil, fmt.Errorf("crypto: hex master key must be %d chars, got %d", MasterKeyBytes*2, len(hexKey))
	}
	raw, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("crypto: master key hex decode: %w", err)
	}
	return NewAESGCM(raw)
}

// Encrypt produces nonce || ciphertext where ciphertext includes the GCM
// tag. The nonce is freshly generated per call from crypto/rand — never
// reused. Returns an error if the random source fails (extremely unlikely
// but we surface it rather than producing a deterministic nonce on a system
// with a broken RNG).
//
// Optional associated data (aad) is authenticated but not encrypted. Use it
// to bind the ciphertext to a context (e.g. the row's product_id) so an
// attacker cannot move ciphertext between rows. Pass nil when not needed.
func (a *AESGCM) Encrypt(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, nonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("crypto: nonce gen: %w", err)
	}
	// Seal appends ciphertext+tag to its first arg. We seed with nonce so
	// the output is self-contained: nonce || ciphertext || tag.
	out := a.aead.Seal(nonce, nonce, plaintext, aad)
	return out, nil
}

// Decrypt verifies and decrypts a value produced by Encrypt with the same
// key and aad. The returned plaintext is freshly allocated — callers may
// safely zero it via clear() once they're done.
func (a *AESGCM) Decrypt(envelope, aad []byte) ([]byte, error) {
	if len(envelope) < MinCiphertextBytes {
		return nil, errors.New("crypto: ciphertext too short")
	}
	nonce, ciphertext := envelope[:nonceBytes], envelope[nonceBytes:]
	plaintext, err := a.aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		// AEAD failure means tampering, wrong key, or wrong aad — don't
		// reveal which to the caller. Detailed cause goes to logs only.
		return nil, errors.New("crypto: decryption failed")
	}
	return plaintext, nil
}
