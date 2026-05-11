package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// VerifyToken is the offline-verifiable license payload returned by
// /license/activate and /license/verify.
//
// Signing: Ed25519. The server holds the private key; clients fetch
// the matching public key once (GET /api/v1/license/pubkey) and use
// it to verify tokens locally — no network call needed during the
// validity window.
//
// We intentionally do NOT use HMAC. HMAC is symmetric: a client that
// can verify can also sign. Shipping the shared secret inside a
// desktop binary makes it extractable, which lets an attacker forge
// arbitrary license tokens. Ed25519 puts the signing capability
// behind a key that never leaves the server.
type VerifyToken struct {
	LicenseID   string         `json:"lid"`
	ProductID   string         `json:"pid"`
	PlanID      string         `json:"pln"`
	Status      string         `json:"sts"`
	Identifier  string         `json:"did"`
	Features    map[string]any `json:"ftr,omitempty"`
	IssuedAt    int64          `json:"iat"`
	ExpiresAt   int64          `json:"exp"`
	GraceDays   int            `json:"grc"`
	Nonce       string         `json:"nce"`           // unique per-issuance to prevent replay
	Fingerprint string         `json:"fpr,omitempty"` // SHA256(identifier+product_id) for binding
}

// PrivateKeyFromHex parses a 32-byte ed25519 seed (64 hex chars) and
// derives the full private key. The seed is what operators rotate
// via their secret manager — it's short enough to fit in a single
// env var, unlike a full 64-byte raw private key.
//
// Generate one with: openssl rand -hex 32
func PrivateKeyFromHex(s string) (ed25519.PrivateKey, error) {
	seed, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("must be hex-encoded: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("must decode to %d bytes (got %d); generate with 'openssl rand -hex 32'",
			ed25519.SeedSize, len(seed))
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// PublicKey returns the verify key derived from the same seed. Used
// to serve the /license/pubkey endpoint without re-parsing the seed.
func PublicKey(priv ed25519.PrivateKey) ed25519.PublicKey {
	return priv.Public().(ed25519.PublicKey)
}

// Sign produces base64url(payload).base64url(signature). The signed
// region is the base64-encoded payload, NOT the raw JSON, so clients
// can verify by base64-decoding the signature half and running the
// payload-half bytes through ed25519.Verify.
func Sign(t *VerifyToken, priv ed25519.PrivateKey) (string, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("ed25519 private key not initialised")
	}
	if t.Nonce == "" {
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			return "", fmt.Errorf("nonce: %w", err)
		}
		t.Nonce = base64.RawURLEncoding.EncodeToString(nonce)
	}
	payload, err := json.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	b64 := base64.RawURLEncoding.EncodeToString(payload)
	sig := ed25519.Sign(priv, []byte(b64))
	return b64 + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Verify checks the signature with the server's public key and
// returns the decoded payload. ExpiresAt is checked here so callers
// don't accidentally trust stale tokens.
func Verify(raw string, pub ed25519.PublicKey) (*VerifyToken, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("ed25519 public key not initialised")
	}
	idx := strings.LastIndexByte(raw, '.')
	if idx < 0 {
		return nil, fmt.Errorf("invalid token format")
	}
	b64, sigB64 := raw[:idx], raw[idx+1:]
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return nil, fmt.Errorf("signature decode: %w", err)
	}
	if !ed25519.Verify(pub, []byte(b64), sig) {
		return nil, fmt.Errorf("invalid signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("payload decode: %w", err)
	}
	var t VerifyToken
	if err := json.Unmarshal(payload, &t); err != nil {
		return nil, fmt.Errorf("payload unmarshal: %w", err)
	}
	if t.ExpiresAt > 0 && time.Now().Unix() > t.ExpiresAt {
		return nil, fmt.Errorf("token expired")
	}
	return &t, nil
}

// Fingerprint binds a token to the (identifier, product_id) pair so
// a token leaked from one device can't be replayed on a sibling
// machine that knows the license key. SHA256 truncated to 8 bytes
// keeps the token small while staying collision-resistant for this
// scope (we only need uniqueness within one customer's devices).
func Fingerprint(identifier, productID string) string {
	h := sha256.Sum256([]byte(identifier + ":" + productID))
	return hex.EncodeToString(h[:8])
}
