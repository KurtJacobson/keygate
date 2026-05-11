package crypto

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
)

func mustKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, MasterKeyBytes)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return k
}

func TestNewAESGCMRejectsWrongKeyLength(t *testing.T) {
	for _, n := range []int{0, 1, 16, 31, 33, 64} {
		_, err := NewAESGCM(make([]byte, n))
		if err == nil {
			t.Errorf("expected error for key length %d", n)
		}
	}
}

func TestNewAESGCMFromHex(t *testing.T) {
	good := strings.Repeat("ab", MasterKeyBytes)
	if _, err := NewAESGCMFromHex(good); err != nil {
		t.Errorf("expected ok for valid hex key, got %v", err)
	}

	for _, bad := range []string{
		"",
		"abcd",
		strings.Repeat("z", MasterKeyBytes*2), // valid length, invalid hex
		strings.Repeat("a", MasterKeyBytes*2-1),
		strings.Repeat("a", MasterKeyBytes*2+1),
	} {
		if _, err := NewAESGCMFromHex(bad); err == nil {
			t.Errorf("expected error for hex key %q", bad)
		}
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	a, err := NewAESGCM(mustKey(t))
	if err != nil {
		t.Fatalf("ctor: %v", err)
	}

	cases := [][]byte{
		nil,                               // empty
		[]byte("hello"),                   // small
		bytes.Repeat([]byte{42}, 1024*64), // 64KB
	}
	for _, plaintext := range cases {
		ct, err := a.Encrypt(plaintext, nil)
		if err != nil {
			t.Fatalf("encrypt: %v", err)
		}
		if len(ct) < MinCiphertextBytes {
			t.Errorf("ciphertext too short: %d", len(ct))
		}
		pt, err := a.Decrypt(ct, nil)
		if err != nil {
			t.Fatalf("decrypt: %v", err)
		}
		if !bytes.Equal(pt, plaintext) {
			t.Errorf("round-trip mismatch")
		}
	}
}

func TestEncryptProducesUniqueNonces(t *testing.T) {
	a, _ := NewAESGCM(mustKey(t))
	plaintext := []byte("same input each call")

	const N = 100
	seen := make(map[string]struct{}, N)
	for range N {
		ct, err := a.Encrypt(plaintext, nil)
		if err != nil {
			t.Fatalf("encrypt: %v", err)
		}
		nonce := string(ct[:12])
		if _, dup := seen[nonce]; dup {
			t.Fatal("nonce reused — RNG broken or implementation bug")
		}
		seen[nonce] = struct{}{}
	}
}

func TestDecryptFailsOnTamper(t *testing.T) {
	a, _ := NewAESGCM(mustKey(t))
	plaintext := []byte("secret data")
	ct, _ := a.Encrypt(plaintext, nil)

	// Flip a bit in the ciphertext body.
	tampered := make([]byte, len(ct))
	copy(tampered, ct)
	tampered[len(tampered)-1] ^= 1

	if _, err := a.Decrypt(tampered, nil); err == nil {
		t.Fatal("expected decrypt to fail on tampered ciphertext")
	}

	// Tamper the nonce.
	tampered2 := make([]byte, len(ct))
	copy(tampered2, ct)
	tampered2[0] ^= 1
	if _, err := a.Decrypt(tampered2, nil); err == nil {
		t.Fatal("expected decrypt to fail on tampered nonce")
	}
}

func TestDecryptFailsOnWrongKey(t *testing.T) {
	a1, _ := NewAESGCM(mustKey(t))
	a2, _ := NewAESGCM(mustKey(t))

	ct, _ := a1.Encrypt([]byte("hello"), nil)
	if _, err := a2.Decrypt(ct, nil); err == nil {
		t.Fatal("expected decrypt with wrong key to fail")
	}
}

func TestDecryptFailsOnShortInput(t *testing.T) {
	a, _ := NewAESGCM(mustKey(t))
	for _, n := range []int{0, 1, 11, MinCiphertextBytes - 1} {
		if _, err := a.Decrypt(make([]byte, n), nil); err == nil {
			t.Errorf("expected error for ciphertext length %d", n)
		}
	}
}

func TestAADBinding(t *testing.T) {
	a, _ := NewAESGCM(mustKey(t))
	plaintext := []byte("private key bytes")
	aad := []byte("product_id=prod_abc")

	ct, err := a.Encrypt(plaintext, aad)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Same aad → succeeds.
	pt, err := a.Decrypt(ct, aad)
	if err != nil || !bytes.Equal(pt, plaintext) {
		t.Errorf("decrypt with same aad failed: %v", err)
	}

	// Different aad → must fail (cross-row replay protection).
	if _, err := a.Decrypt(ct, []byte("product_id=prod_xyz")); err == nil {
		t.Fatal("expected decrypt with wrong aad to fail")
	}

	// Nil aad after encrypting with non-nil → must fail.
	if _, err := a.Decrypt(ct, nil); err == nil {
		t.Fatal("expected decrypt with nil aad (after encrypt with aad) to fail")
	}
}

func TestKeyHexFormat(t *testing.T) {
	// Sanity: well-known fixed key produces deterministic structure (length only,
	// since nonces are random). Helps catch encoding regressions.
	keyHex := strings.Repeat("00", MasterKeyBytes)
	a, err := NewAESGCMFromHex(keyHex)
	if err != nil {
		t.Fatalf("ctor: %v", err)
	}
	ct, err := a.Encrypt([]byte("x"), nil)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	// 12-byte nonce + 1-byte plaintext + 16-byte tag = 29 bytes.
	if len(ct) != 12+1+16 {
		t.Errorf("unexpected ciphertext shape: %d (hex=%s)", len(ct), hex.EncodeToString(ct))
	}
}
