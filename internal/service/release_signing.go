package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/tabloy/keygate/internal/crypto"
	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/storage"
	"github.com/tabloy/keygate/internal/store"
)

// ReleaseSigningService manages per-product Ed25519 keypairs and signs
// release artifacts at publish time.
//
// Security model:
//   - Private keys are AES-256-GCM encrypted at rest with the master key
//     supplied via RELEASE_KEY_ENCRYPTION_KEY. The aad binds each ciphertext
//     to its product_id so a leaked row cannot be moved to another product.
//   - Public keys are stored in plain (they're public).
//   - Private keys are NEVER returned by any API or appear in any log line.
//   - Decryption happens only inside SignArtifact, the result is held briefly
//     and then dropped.
//
// Concurrency: SignArtifact holds the full artifact bytes in memory (Ed25519
// requires the entire message). A bounded semaphore limits concurrent signs
// so a flood of publish calls cannot OOM the server.
type ReleaseSigningService struct {
	store       *store.Store
	storage     storage.Storage
	aead        *crypto.AESGCM
	logger      *slog.Logger
	maxSignSize int64
	signSlots   chan struct{} // semaphore; cap = max concurrent signs
}

// ReleaseSigningServiceConfig wires the dependencies. The master key
// must already be 32 raw bytes (caller decodes hex).
type ReleaseSigningServiceConfig struct {
	Store       *store.Store
	Storage     storage.Storage
	AEAD        *crypto.AESGCM
	Logger      *slog.Logger
	MaxSignSize int64 // bytes, defaults to 500MB if zero
	// MaxConcurrentSigns caps how many SignArtifact calls run at once.
	// Each in-flight sign holds up to MaxSignSize bytes, so the worst-case
	// memory ceiling is MaxConcurrentSigns × MaxSignSize. Defaults to 2.
	MaxConcurrentSigns int
}

// NewReleaseSigningService constructs a signing service. AEAD is required —
// pass nil only when explicitly disabling the signing subsystem (in which
// case the resulting service will fail every operation with ErrSigningDisabled).
func NewReleaseSigningService(c ReleaseSigningServiceConfig) *ReleaseSigningService {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Storage == nil {
		c.Storage = storage.Disabled{}
	}
	if c.MaxSignSize <= 0 {
		c.MaxSignSize = 500 * 1024 * 1024
	}
	slots := c.MaxConcurrentSigns
	if slots <= 0 {
		slots = 2
	}
	return &ReleaseSigningService{
		store:       c.Store,
		storage:     c.Storage,
		aead:        c.AEAD,
		logger:      c.Logger,
		maxSignSize: c.MaxSignSize,
		signSlots:   make(chan struct{}, slots),
	}
}

// ─── Sentinel errors ───

var (
	ErrSigningDisabled        = errors.New("release signing is not configured (RELEASE_KEY_ENCRYPTION_KEY missing)")
	ErrArtifactTooLargeToSign = errors.New("artifact exceeds the configured signing size limit")
	ErrSigningKeyMissing      = errors.New("product has no active signing key — generate one before signing")
	ErrArtifactNotInStorage   = errors.New("artifact bytes not found in storage; upload may not be finalized")
)

// ─── Key generation ───

// GenerateForProduct creates a fresh Ed25519 keypair, encrypts the private
// key, and stores both. Fails if the product already has an active key —
// callers should use Rotate instead.
//
// Memory hygiene: the ed25519 PrivateKey value is a 64-byte slice owned
// by the stdlib; we defer-zero it after encryption. priv.Seed() returns
// a fresh COPY (not the underlying buffer), so zeroing it has no effect —
// don't be misled by older versions of this code.
//
// Nil receiver: when storage isn't configured the service is wired as nil
// (see main.go). All public methods short-circuit to ErrSigningDisabled
// rather than panic, so handlers can map cleanly to a 503.
func (s *ReleaseSigningService) GenerateForProduct(ctx context.Context, productID string) (*model.ReleaseSigningKey, error) {
	if s == nil || s.aead == nil {
		return nil, ErrSigningDisabled
	}
	if productID == "" {
		return nil, errors.New("product_id is required")
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ed25519 generate: %w", err)
	}
	defer zeroBytes(priv) // 64-byte slice — zeroing is meaningful here

	// Encrypt the seed (32 bytes); decrypt path reconstructs priv via
	// ed25519.NewKeyFromSeed.
	seed := priv.Seed()
	defer zeroBytes(seed)

	// Bind the ciphertext to product_id so a leaked DB row cannot be
	// re-inserted under a different product.
	encrypted, err := s.aead.Encrypt(seed, []byte(productID))
	if err != nil {
		return nil, fmt.Errorf("encrypt private key: %w", err)
	}

	key := &model.ReleaseSigningKey{
		ProductID:           productID,
		PublicKey:           base64.StdEncoding.EncodeToString(pub),
		PrivateKeyEncrypted: encrypted,
		Active:              true,
	}
	if err := s.store.CreateSigningKey(ctx, key); err != nil {
		return nil, err
	}

	s.logger.Info("release signing key generated", "product_id", productID, "key_id", key.ID)
	return key, nil
}

// RotateForProduct generates a new keypair and atomically deactivates the
// previous active key. The note explains the rotation reason and is stored
// on the OLD key (which becomes inactive).
func (s *ReleaseSigningService) RotateForProduct(ctx context.Context, productID, note string) (*model.ReleaseSigningKey, error) {
	if s == nil || s.aead == nil {
		return nil, ErrSigningDisabled
	}
	if productID == "" {
		return nil, errors.New("product_id is required")
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ed25519 generate: %w", err)
	}
	defer zeroBytes(priv)

	seed := priv.Seed()
	defer zeroBytes(seed)

	encrypted, err := s.aead.Encrypt(seed, []byte(productID))
	if err != nil {
		return nil, fmt.Errorf("encrypt private key: %w", err)
	}

	key := &model.ReleaseSigningKey{
		ProductID:           productID,
		PublicKey:           base64.StdEncoding.EncodeToString(pub),
		PrivateKeyEncrypted: encrypted,
		Active:              true,
	}
	if err := s.store.RotateSigningKey(ctx, key, note); err != nil {
		return nil, err
	}

	s.logger.Info("release signing key rotated", "product_id", productID, "new_key_id", key.ID, "note", note)
	return key, nil
}

// ─── Public key access ───

// GetActivePublicKey returns the base64 public key for the product.
// Returns ErrSigningKeyMissing if no active key exists.
func (s *ReleaseSigningService) GetActivePublicKey(ctx context.Context, productID string) (string, error) {
	if s == nil || s.store == nil {
		return "", ErrSigningDisabled
	}
	k, err := s.store.FindActiveSigningKey(ctx, productID)
	if err != nil {
		if errors.Is(err, store.ErrActiveSigningKeyMissing) {
			return "", ErrSigningKeyMissing
		}
		return "", err
	}
	return k.PublicKey, nil
}

// ExportPublicKeyTauri returns the active public key in Tauri's
// updater-compatible format: base64("Ed" + 8-byte key_id + 32-byte raw key).
// Tauri's verifier requires this envelope; the raw 32-byte key Sparkle
// consumes fails Tauri's `pubkey.len() == 42` check.
func (s *ReleaseSigningService) ExportPublicKeyTauri(ctx context.Context, productID string) (string, error) {
	if s == nil || s.store == nil {
		return "", ErrSigningDisabled
	}
	k, err := s.store.FindActiveSigningKey(ctx, productID)
	if err != nil {
		if errors.Is(err, store.ErrActiveSigningKeyMissing) {
			return "", ErrSigningKeyMissing
		}
		return "", err
	}
	wrapped := TauriPublicKey(k.PublicKey)
	if wrapped == "" {
		return "", fmt.Errorf("stored public key has unexpected shape")
	}
	return wrapped, nil
}

// ExportPublicKeyPEM returns the active public key as a PEM-encoded
// document suitable for embedding in client apps (Sparkle, Tauri).
//
// Returns ErrSigningDisabled when the service was wired without storage
// (nil receiver) so handlers can return 503 cleanly.
//
// Format:
//
//	-----BEGIN PUBLIC KEY-----
//	<base64 of raw 32-byte key>
//	-----END PUBLIC KEY-----
//
// We use a custom block type rather than SPKI because Sparkle's edSignature
// docs recommend the raw 32-byte key, not the X.509 envelope.
func (s *ReleaseSigningService) ExportPublicKeyPEM(ctx context.Context, productID string) ([]byte, error) {
	if s == nil || s.store == nil {
		return nil, ErrSigningDisabled
	}
	k, err := s.store.FindActiveSigningKey(ctx, productID)
	if err != nil {
		if errors.Is(err, store.ErrActiveSigningKeyMissing) {
			return nil, ErrSigningKeyMissing
		}
		return nil, err
	}
	rawKey, err := base64.StdEncoding.DecodeString(k.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("decode stored public key: %w", err)
	}
	if len(rawKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("stored public key has wrong length: %d", len(rawKey))
	}
	pemBlock := &pem.Block{Type: "ED25519 PUBLIC KEY", Bytes: rawKey}
	return pem.EncodeToMemory(pemBlock), nil
}

// ─── Signing ───

// SignResult is the output of SignArtifact: the base64 Ed25519 signature
// AND the ID of the signing key that produced it. Callers MUST persist both
// atomically — a sig without a key_id is unverifiable after rotation.
type SignResult struct {
	Signature    string // base64-encoded raw 64-byte signature
	SigningKeyID string // FK to release_signing_keys.id
}

// SignArtifact signs the bytes of a single release artifact and returns
// the signature plus the signing key's ID. The pair is the only
// authoritative way to know which public key a client should verify
// against, especially after key rotation.
//
// rel provides product context (for active signing-key lookup, AAD); a is
// the specific platform binary to sign.
//
// Pure Ed25519 requires the entire message in memory. For artifacts larger
// than maxSignSize this returns ErrArtifactTooLargeToSign.
func (s *ReleaseSigningService) SignArtifact(ctx context.Context, rel *model.Release, a *model.ReleaseArtifact) (*SignResult, error) {
	if s == nil || s.aead == nil {
		return nil, ErrSigningDisabled
	}
	if rel == nil || a == nil {
		return nil, errors.New("release or artifact is nil")
	}
	if a.FileKey == "" {
		return nil, ErrArtifactNotInStorage
	}
	if a.FileSize > s.maxSignSize {
		return nil, fmt.Errorf("%w (size=%d, limit=%d)", ErrArtifactTooLargeToSign, a.FileSize, s.maxSignSize)
	}

	// Concurrency gate: one in-flight sign may use up to maxSignSize bytes,
	// so we cap worst-case memory at MaxConcurrentSigns × MaxSignSize.
	select {
	case s.signSlots <- struct{}{}:
		defer func() { <-s.signSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	keyRow, err := s.store.FindActiveSigningKey(ctx, rel.ProductID)
	if err != nil {
		if errors.Is(err, store.ErrActiveSigningKeyMissing) {
			return nil, ErrSigningKeyMissing
		}
		return nil, err
	}

	seed, err := s.aead.Decrypt(keyRow.PrivateKeyEncrypted, []byte(rel.ProductID))
	if err != nil {
		return nil, fmt.Errorf("decrypt private key: %w", err)
	}
	defer zeroBytes(seed)
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("decrypted seed has wrong length: %d", len(seed))
	}
	priv := ed25519.NewKeyFromSeed(seed)
	defer zeroBytes(priv)

	body, err := s.storage.Get(ctx, a.FileKey)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotFound) {
			return nil, ErrArtifactNotInStorage
		}
		return nil, fmt.Errorf("storage get: %w", err)
	}
	defer body.Close()
	defer func() { _, _ = io.Copy(io.Discard, body) }()

	limited := io.LimitReader(body, s.maxSignSize+1)
	buf := make([]byte, 0, a.FileSize)
	bufWriter := bytesBufferWriter(&buf, s.maxSignSize+1)
	if _, err := io.Copy(bufWriter, limited); err != nil {
		return nil, fmt.Errorf("read artifact: %w", err)
	}
	if int64(len(buf)) > s.maxSignSize {
		return nil, fmt.Errorf("%w (read=%d, limit=%d)", ErrArtifactTooLargeToSign, len(buf), s.maxSignSize)
	}

	sig := ed25519.Sign(priv, buf)
	return &SignResult{
		Signature:    base64.StdEncoding.EncodeToString(sig),
		SigningKeyID: keyRow.ID,
	}, nil
}

// bytesBufferWriter wraps a *[]byte as an io.Writer with a hard cap.
// We don't use bytes.Buffer because its grow semantics are exponential;
// the slice append path is more predictable for this workload.
type capWriter struct {
	dst *[]byte
	cap int64
}

func (w *capWriter) Write(p []byte) (int, error) {
	remaining := w.cap - int64(len(*w.dst))
	if remaining <= 0 {
		return 0, errors.New("capWriter: capacity exceeded")
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	*w.dst = append(*w.dst, p...)
	return len(p), nil
}

func bytesBufferWriter(dst *[]byte, cap int64) io.Writer {
	return &capWriter{dst: dst, cap: cap}
}

// VerifySignature verifies a base64 Ed25519 signature against artifact
// bytes using the given public key. Used by tests; clients verify in their
// own runtimes (Sparkle, Tauri) via their respective libraries.
func VerifySignature(publicKeyBase64 string, message []byte, signatureBase64 string) error {
	pub, err := base64.StdEncoding.DecodeString(publicKeyBase64)
	if err != nil {
		return fmt.Errorf("decode public key: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("public key has wrong length: %d", len(pub))
	}
	sig, err := base64.StdEncoding.DecodeString(signatureBase64)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	if !ed25519.Verify(pub, message, sig) {
		return errors.New("signature verification failed")
	}
	return nil
}

// ─── Helpers ───

// zeroBytes overwrites the given slice with zeros to scrub key material
// from memory. Go provides no guarantee that the underlying memory page
// won't be read after a buffer is dropped (GC, swap), so we do this on
// best-effort basis.
//
// Modern Go's escape analysis may keep these bytes on the stack and zeroing
// is a no-op for stack frames after function exit, but for heap-allocated
// slices (which most of these are) the explicit clear shrinks the window.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
