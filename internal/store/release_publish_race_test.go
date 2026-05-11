package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/store"
)

// TestPublishRelease_RequireSignatures_Gate exercises the boolean gate
// without concurrency. It pins the four-state matrix:
//
//	requireSignatures | sig present | result
//	------------------|-------------|-------
//	true              | yes         | published
//	true              | no          | ErrReleaseArtifactsNotSigned
//	false             | yes         | published (we don't care about sig)
//	false             | no          | published
//
// These are the building blocks the publisher relies on.
func TestPublishRelease_RequireSignatures_Gate(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	cases := []struct {
		name            string
		setSignature    bool
		require         bool
		wantPublished   bool
		wantErrSentinel error
	}{
		{"signed+require=true → publish", true, true, true, nil},
		{"unsigned+require=true → reject", false, true, false, store.ErrReleaseArtifactsNotSigned},
		{"signed+require=false → publish", true, false, true, nil},
		{"unsigned+require=false → publish", false, false, true, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			relID, artID := setupSignedDraft(t, s, ctx, tc.setSignature)
			err := s.PublishRelease(ctx, relID, tc.require)
			if tc.wantErrSentinel != nil {
				if !errors.Is(err, tc.wantErrSentinel) {
					t.Fatalf("expected %v, got %v", tc.wantErrSentinel, err)
				}
				// Release must still be draft on rejection.
				rel, _ := s.FindReleaseByID(ctx, relID)
				if rel.Status != model.ReleaseStatusDraft {
					t.Fatalf("release flipped despite gate reject: status=%s", rel.Status)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected publish error: %v", err)
			}
			rel, _ := s.FindReleaseByID(ctx, relID)
			if rel.Status != model.ReleaseStatusPublished {
				t.Fatalf("expected published, got %s", rel.Status)
			}
			_ = artID
		})
	}
}

// TestPublishRelease_RaceUpdateArtifactFile is the closure-of-bug test.
// We race PublishRelease(requireSignatures=true) against
// UpdateArtifactFile (which clears ed25519_sig). The invariant: NO
// run may end with the release in 'published' state AND any artifact
// having empty ed25519_sig. Either publish wins (release published,
// sig still present) or upload wins (release still draft, sig cleared,
// publish returned ErrReleaseArtifactsNotSigned).
//
// Repeated N times to catch interleavings — a single iteration is rarely
// enough to land the goroutines on the same lock contention; 50 attempts
// across PG round-trips is typically sufficient.
func TestPublishRelease_RaceUpdateArtifactFile(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	const iterations = 50
	for i := range iterations {
		relID, artID := setupSignedDraft(t, s, ctx, true)

		var wg sync.WaitGroup
		var publishErr, uploadErr error
		wg.Add(2)

		// Goroutine A: publish with requireSignatures=true.
		go func() {
			defer wg.Done()
			publishErr = s.PublishRelease(ctx, relID, true)
		}()

		// Goroutine B: concurrent re-upload, clears ed25519_sig.
		go func() {
			defer wg.Done()
			uploadErr = s.UpdateArtifactFile(ctx, artID,
				"file-"+time.Now().Format("150405.000000"),
				123, "newsha256", "application/octet-stream")
		}()

		wg.Wait()

		rel, err := s.FindReleaseByID(ctx, relID)
		if err != nil {
			t.Fatalf("iter %d: load release: %v", i, err)
		}
		art, err := s.FindArtifact(ctx, artID)
		if err != nil {
			t.Fatalf("iter %d: load artifact: %v", i, err)
		}

		// The forbidden state: published with empty sig.
		if rel.Status == model.ReleaseStatusPublished && art.Ed25519Sig == "" {
			t.Fatalf("iter %d: FORBIDDEN STATE — release published with empty sig "+
				"(publishErr=%v, uploadErr=%v)", i, publishErr, uploadErr)
		}

		// Cross-check sentinels match the observed state.
		switch rel.Status {
		case model.ReleaseStatusPublished:
			// Publish won. Upload must have observed not-draft and bailed.
			if uploadErr == nil {
				t.Fatalf("iter %d: publish won but upload succeeded — both can't win", i)
			}
			if publishErr != nil {
				t.Fatalf("iter %d: published but publishErr non-nil: %v", i, publishErr)
			}
		case model.ReleaseStatusDraft:
			// Either neither raced, or upload won. publishErr must be either
			// nil (no race at all) — but that's impossible because we never
			// observe "draft + no publish error"; PublishRelease either flips
			// status or returns an error.
			if publishErr == nil {
				t.Fatalf("iter %d: draft state but publishErr nil — invariant broken", i)
			}
			// publishErr is one of: ErrReleaseArtifactsNotSigned (upload cleared
			// sig first) or ErrReleaseNotPublishable / NotReady (rare interleaving).
			// All acceptable.
		}
	}
}

// setupSignedDraft creates a product + plan + draft release + finalized
// artifact. If signed is true, the artifact also has a non-empty
// ed25519_sig and signing_key_id (just sentinel strings — we don't
// actually verify the sig in this test).
func setupSignedDraft(t *testing.T, s *store.Store, ctx context.Context, signed bool) (releaseID, artifactID string) {
	t.Helper()
	suffix := time.Now().Format("150405.000000000")

	prod := &model.Product{Name: "Rel " + suffix, Slug: "rel-" + suffix, Type: "desktop"}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatalf("create product: %v", err)
	}
	plan := &model.Plan{
		ProductID:    prod.ID,
		Name:         "P",
		Slug:         "p-" + suffix,
		LicenseType:  "perpetual",
		LicenseModel: "standard",
	}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	rel := &model.Release{
		ProductID: prod.ID,
		Version:   "1.0." + suffix[len(suffix)-3:],
		Channel:   "stable",
		Status:    model.ReleaseStatusDraft,
	}
	if err := s.CreateRelease(ctx, rel); err != nil {
		t.Fatalf("create release: %v", err)
	}
	art := &model.ReleaseArtifact{
		ReleaseID: rel.ID,
		Platform:  "darwin-arm64",
		FileKey:   "k-" + suffix,
		FileSize:  100,
		// 64-char lowercase hex literal — DB CHECK constraint enforces shape.
		SHA256:      "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		ContentType: "application/octet-stream",
	}
	if err := s.CreateArtifact(ctx, art); err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	if signed {
		// SigningKeyID FK → release_signing_keys.id, so we mint a real
		// key row before attaching it to the artifact.
		// DB CHECK: 60 <= octet_length(private_key_encrypted) <= 256.
		blob := make([]byte, 64)
		for i := range blob {
			blob[i] = byte(i + 1) // non-zero filler
		}
		// DB CHECK: 32 <= length(public_key) <= 128.
		key := &model.ReleaseSigningKey{
			ProductID:           prod.ID,
			PublicKey:           "pk-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-" + suffix,
			PrivateKeyEncrypted: blob,
			Active:              true,
		}
		if err := s.CreateSigningKey(ctx, key); err != nil {
			t.Fatalf("create signing key: %v", err)
		}
		if err := s.UpdateArtifactSignature(ctx, art.ID, "sig-"+suffix, key.ID); err != nil {
			t.Fatalf("set signature: %v", err)
		}
	}
	return rel.ID, art.ID
}
