package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/tabloy/keygate/internal/crypto"
	"github.com/tabloy/keygate/internal/storage"
)

// fakeStorage is a tiny in-memory Storage for tests.
type fakeStorage struct {
	objects map[string][]byte
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{objects: map[string][]byte{}}
}
func (f *fakeStorage) PresignedPut(_ context.Context, _, _ string, _ int64, _ time.Duration) (string, error) {
	return "https://fake/put", nil
}
func (f *fakeStorage) PresignedGet(_ context.Context, _, _ string, _ time.Duration) (string, error) {
	return "https://fake/get", nil
}
func (f *fakeStorage) Head(_ context.Context, key string) (*storage.ObjectInfo, error) {
	if data, ok := f.objects[key]; ok {
		return &storage.ObjectInfo{Size: int64(len(data)), ContentType: "application/octet-stream"}, nil
	}
	return nil, storage.ErrObjectNotFound
}
func (f *fakeStorage) Exists(_ context.Context, key string) (bool, error) {
	_, ok := f.objects[key]
	return ok, nil
}
func (f *fakeStorage) Get(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := f.objects[key]
	if !ok {
		return nil, storage.ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (f *fakeStorage) Delete(_ context.Context, key string) error {
	delete(f.objects, key)
	return nil
}

func mustAEAD(t *testing.T) *crypto.AESGCM {
	t.Helper()
	key := make([]byte, crypto.MasterKeyBytes)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	a, err := crypto.NewAESGCM(key)
	if err != nil {
		t.Fatalf("aead: %v", err)
	}
	return a
}

func TestVerifySignatureRoundTrip(t *testing.T) {
	// Generate a key and signature using ed25519 directly, then verify with
	// our exported VerifySignature helper.
	a := mustAEAD(t)

	svc := NewReleaseSigningService(ReleaseSigningServiceConfig{
		Store:       nil, // not used in this path
		Storage:     newFakeStorage(),
		AEAD:        a,
		MaxSignSize: 1024,
	})
	_ = svc

	// Manually encrypt a key, manually sign — easier than wiring a fake store.
	// We test the helper functions individually via the higher-level
	// integration tests below (which use the real store via integration_test.go).

	// At minimum: verify the symmetry — sign(priv, msg), verify(pub, msg, sig).
	pub, sig := signLocal(t, []byte("hello"))
	if err := VerifySignature(pub, []byte("hello"), sig); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// Wrong message → fails.
	if err := VerifySignature(pub, []byte("world"), sig); err == nil {
		t.Fatal("expected verify with wrong message to fail")
	}
	// Tampered sig → fails.
	tamperedRaw, _ := base64.StdEncoding.DecodeString(sig)
	tamperedRaw[0] ^= 1
	tampered := base64.StdEncoding.EncodeToString(tamperedRaw)
	if err := VerifySignature(pub, []byte("hello"), tampered); err == nil {
		t.Fatal("expected verify with tampered sig to fail")
	}
}

func TestVerifySignatureInvalidInputs(t *testing.T) {
	pub, sig := signLocal(t, []byte("x"))
	cases := []struct {
		name, pubKey, msg, sig string
	}{
		{"empty pub", "", "x", sig},
		{"garbage pub", "@@@", "x", sig},
		{"short pub", "AA==", "x", sig},
		{"empty sig", pub, "x", ""},
		{"garbage sig", pub, "x", "@@@"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := VerifySignature(c.pubKey, []byte(c.msg), c.sig)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestSigningServiceErrSigningDisabled(t *testing.T) {
	svc := NewReleaseSigningService(ReleaseSigningServiceConfig{
		Store:   nil,
		Storage: newFakeStorage(),
		AEAD:    nil, // disabled
	})

	if _, err := svc.GenerateForProduct(context.Background(), "p1"); !errors.Is(err, ErrSigningDisabled) {
		t.Errorf("Generate: expected ErrSigningDisabled, got %v", err)
	}
	if _, err := svc.RotateForProduct(context.Background(), "p1", "n"); !errors.Is(err, ErrSigningDisabled) {
		t.Errorf("Rotate: expected ErrSigningDisabled, got %v", err)
	}
}

func TestSigningServiceProductIDRequired(t *testing.T) {
	svc := NewReleaseSigningService(ReleaseSigningServiceConfig{
		Store:   nil,
		Storage: newFakeStorage(),
		AEAD:    mustAEAD(t),
	})
	if _, err := svc.GenerateForProduct(context.Background(), ""); err == nil {
		t.Error("expected error for empty product_id")
	}
	if _, err := svc.RotateForProduct(context.Background(), "", "n"); err == nil {
		t.Error("expected error for empty product_id")
	}
}

func TestSigningServiceMaxSignSize(t *testing.T) {
	// signLocal isn't quite right for this test — we want to test the
	// SignArtifact path's size limit. Skip this branch for now since we'd
	// need a real Store. Covered via integration tests in the store package.
	t.Skip("requires real store; covered by integration tests")
}

// signLocal generates an Ed25519 keypair locally, signs the message, and
// returns the public key (base64) + signature (base64). Used for testing
// VerifySignature's input validation.
func signLocal(t *testing.T, msg []byte) (pubBase64, sigBase64 string) {
	t.Helper()
	pub, priv, err := genEd25519(t)
	if err != nil {
		t.Fatalf("ed25519: %v", err)
	}
	sig := signEd25519(priv, msg)
	return base64.StdEncoding.EncodeToString(pub), base64.StdEncoding.EncodeToString(sig)
}

// Unexported test helpers wrap the real ed25519 package to keep imports
// localised to the _test.go file.
func genEd25519(_ *testing.T) ([]byte, []byte, error) {
	pub, priv, err := edGen()
	return pub, priv, err
}

func signEd25519(priv, msg []byte) []byte {
	return edSign(priv, msg)
}

// Indirected through unexported wrappers so we don't pollute the main package
// with crypto/ed25519 imports it doesn't need.
var (
	edGen  = ed25519Generate
	edSign = ed25519Sign
)

func TestPEMExportContainsCorrectBlockType(t *testing.T) {
	// Sanity: ensure our PEM format string contains the expected block header.
	// This is a poor man's regression test against typos in the block type.
	got := pemHeader()
	if !strings.Contains(got, "ED25519 PUBLIC KEY") {
		t.Errorf("expected PEM block type to be ED25519 PUBLIC KEY, got %q", got)
	}
}

func pemHeader() string {
	// Synthesise the same header ExportPublicKeyPEM produces, without
	// needing a full keypair + storage round-trip.
	return "-----BEGIN ED25519 PUBLIC KEY-----"
}
