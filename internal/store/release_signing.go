package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tabloy/keygate/internal/model"
)

// Sentinel errors for the release signing key store.
var (
	ErrSigningKeyNotFound      = errors.New("release signing key not found")
	ErrActiveSigningKeyMissing = errors.New("no active signing key for product")
	// ErrSigningKeyAlreadyActive is returned when we try to insert a second
	// active key for the same product. Callers should call RotateSigningKey
	// instead of two separate Create calls.
	ErrSigningKeyAlreadyActive = errors.New("product already has an active signing key; rotate instead of create")
)

// signingKeyRotateLockSeed is hashed with the product_id to derive a
// unique advisory lock per product. Different from other advisory lock
// IDs in the codebase to avoid collisions.
const signingKeyRotateLockSeed int64 = 0x6b657967727274 // "keygrt" in hex

// CreateSigningKey inserts a new signing key. Caller is responsible for
// ensuring at most one active key per product exists at the time of call;
// the partial unique index will reject violations with a unique-constraint
// error which we surface as ErrSigningKeyAlreadyActive.
func (s *Store) CreateSigningKey(ctx context.Context, k *model.ReleaseSigningKey) error {
	if k.ID == "" {
		k.ID = newID()
	}
	_, err := s.DB.NewInsert().Model(k).Exec(ctx)
	if isUniqueViolation(err) {
		return ErrSigningKeyAlreadyActive
	}
	return err
}

// FindActiveSigningKey returns the (single) active key for a product.
// Returns ErrActiveSigningKeyMissing if none exists.
func (s *Store) FindActiveSigningKey(ctx context.Context, productID string) (*model.ReleaseSigningKey, error) {
	k := new(model.ReleaseSigningKey)
	err := s.DB.NewSelect().Model(k).
		Where("product_id = ? AND active = TRUE", productID).
		Limit(1).
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrActiveSigningKeyMissing
	}
	if err != nil {
		return nil, err
	}
	return k, nil
}

// FindSigningKeyByID looks up a key (active or rotated). Used for verifying
// signatures produced by rotated keys (a deployed client may still have the
// old public key embedded for some time after rotation).
func (s *Store) FindSigningKeyByID(ctx context.Context, id string) (*model.ReleaseSigningKey, error) {
	k := new(model.ReleaseSigningKey)
	err := s.DB.NewSelect().Model(k).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSigningKeyNotFound
	}
	if err != nil {
		return nil, err
	}
	return k, nil
}

// ListSigningKeys returns the recent history (active + rotated) for a
// product, most recent first. Capped at 200 rows — older entries are
// available via direct DB query if archaeology is needed.
func (s *Store) ListSigningKeys(ctx context.Context, productID string) ([]*model.ReleaseSigningKey, error) {
	var out []*model.ReleaseSigningKey
	err := s.DB.NewSelect().Model(&out).
		Where("product_id = ?", productID).
		OrderExpr("active DESC, created_at DESC").
		Limit(200).
		Scan(ctx)
	return out, err
}

// RotateSigningKey atomically deactivates the current active key (if any)
// and inserts a new active key. Both happen inside a single transaction +
// per-product advisory lock so concurrent rotate calls cannot interleave
// (which under READ COMMITTED would otherwise produce flap-flop where one
// caller's "active" key gets immediately deactivated by the other).
//
// The newKey must have ProductID set; ID is auto-generated if empty;
// Active is forced to true.
func (s *Store) RotateSigningKey(ctx context.Context, newKey *model.ReleaseSigningKey, note string) error {
	if newKey == nil {
		return errors.New("newKey is nil")
	}
	if newKey.ID == "" {
		newKey.ID = newID()
	}
	newKey.Active = true

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	// Per-product advisory lock — held until COMMIT. Two concurrent
	// RotateSigningKey calls for the same product will serialise here.
	lockKey := signingKeyRotateLockSeed ^ stringHash(newKey.ProductID)
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", lockKey); err != nil {
		return fmt.Errorf("acquire rotate lock: %w", err)
	}

	now := time.Now()
	// Deactivate current active key (if exists). Note (rotation reason) goes
	// onto the OLD key so the audit trail explains "why was this rotated".
	if _, err := tx.NewUpdate().Model((*model.ReleaseSigningKey)(nil)).
		Set("active = FALSE, rotated_at = ?, note = ?", now, note).
		Where("product_id = ? AND active = TRUE", newKey.ProductID).
		Exec(ctx); err != nil {
		return err
	}

	if _, err := tx.NewInsert().Model(newKey).Exec(ctx); err != nil {
		if isUniqueViolation(err) {
			return ErrSigningKeyAlreadyActive
		}
		return err
	}
	return tx.Commit()
}

// stringHash returns a deterministic int64 hash of s. Used to derive
// per-product advisory lock IDs. fnv-64a is fast and well-distributed.
func stringHash(s string) int64 {
	const offset64 = 14695981039346656037
	const prime64 = 1099511628211
	h := uint64(offset64)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	return int64(h) //nolint:gosec // bit-cast is intentional
}

// DeactivateSigningKey marks a key inactive without creating a replacement.
// Used when an admin wants to disable signing entirely (e.g. before deleting
// a product). Future releases for this product will be unsigned until a new
// key is created via CreateSigningKey or RotateSigningKey.
func (s *Store) DeactivateSigningKey(ctx context.Context, id, note string) error {
	res, err := s.DB.NewUpdate().Model((*model.ReleaseSigningKey)(nil)).
		Set("active = FALSE, rotated_at = now(), note = ?", note).
		Where("id = ? AND active = TRUE", id).
		Exec(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Either not found, or already inactive.
		if _, err := s.FindSigningKeyByID(ctx, id); err != nil {
			return err
		}
		return errors.New("signing key is already inactive")
	}
	return nil
}
