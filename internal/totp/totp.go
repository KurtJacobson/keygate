// Package totp implements RFC 6238 time-based one-time passwords using
// only the standard library. Scope is deliberately minimal — SHA-1,
// 6 digits, 30-second period — because that is the interoperable subset
// every authenticator app (Google Authenticator, Authy, 1Password,
// Bitwarden, ...) actually supports.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 mandates HMAC-SHA1; not used for collision resistance
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
)

const (
	// Period is the code rotation interval in seconds.
	Period = 30
	// Digits is the code length.
	Digits = 6
	// secretBytes is the shared-secret size. RFC 4226 recommends at
	// least 128 bits; 20 bytes (160 bits) matches the SHA-1 block.
	secretBytes = 20
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateSecret returns a new random shared secret, base32-encoded
// without padding (the alphabet authenticator apps expect).
func GenerateSecret() (string, error) {
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("totp: generating secret: %w", err)
	}
	return b32.EncodeToString(buf), nil
}

// ProvisioningURI renders the otpauth:// URI that enrollment QR codes
// encode. issuer and account are display strings shown in the app.
func ProvisioningURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprintf("%d", Digits))
	q.Set("period", fmt.Sprintf("%d", Period))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// Slot returns the counter value for a unix timestamp. Exposed so the
// caller can persist the last accepted slot and reject replays.
func Slot(unix int64) int64 { return unix / Period }

// codeForSlot computes the 6-digit code for one counter value.
func codeForSlot(secret []byte, slot int64) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(slot))
	mac := hmac.New(sha1.New, secret)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000)
}

// Validate checks a submitted code against the secret at the given unix
// time, accepting ±1 period of clock skew. It returns the slot the code
// matched so the caller can enforce single use; ok is false when the
// code or secret is invalid.
func Validate(secretB32, code string, unix int64) (matchedSlot int64, ok bool) {
	code = strings.TrimSpace(code)
	if len(code) != Digits {
		return 0, false
	}
	secret, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secretB32)))
	if err != nil || len(secret) == 0 {
		return 0, false
	}
	now := Slot(unix)
	// Constant-shape loop: always compare all three slots so timing
	// doesn't reveal which slot (if any) matched.
	matched := int64(0)
	found := false
	for _, slot := range []int64{now, now - 1, now + 1} {
		expected := codeForSlot(secret, slot)
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 && !found {
			matched, found = slot, true
		}
	}
	return matched, found
}
