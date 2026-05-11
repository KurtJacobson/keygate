// Package storage abstracts blob storage for release artifacts.
//
// The interface is intentionally narrow — only the operations the release
// service needs. Implementations target Cloudflare R2, AWS S3, MinIO, or any
// S3-compatible API. Direct browser uploads use presigned PUT URLs;
// license-gated downloads use presigned GET URLs with a short TTL.
package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

// Storage is the minimal contract for an object store.
type Storage interface {
	// PresignedPut returns a URL the client can PUT a file to. The contentType
	// and expectedSize parameters are HINTS only — the AWS SDK's default
	// presigner does not include them in the SigV4 signed headers, so storage
	// will accept mismatched headers. Real validation happens at FinalizeUpload
	// (Head check) or via bucket-side policies.
	PresignedPut(ctx context.Context, key, contentType string, expectedSize int64, expires time.Duration) (string, error)

	// PresignedGet returns a license-gated download URL with a short TTL.
	// The optional filename hint is encoded into Content-Disposition so
	// browsers prompt with a sensible name regardless of the storage key.
	PresignedGet(ctx context.Context, key, filenameHint string, expires time.Duration) (string, error)

	// Head fetches the metadata (size, content-type, etag) for an object.
	// Returns ErrObjectNotFound if the key does not exist.
	Head(ctx context.Context, key string) (*ObjectInfo, error)

	// Exists reports whether the object exists. Returns (false, nil) when
	// the object is absent — distinguishing it from a transport error.
	Exists(ctx context.Context, key string) (bool, error)

	// Get streams the full object body. Caller MUST close the returned
	// ReadCloser. Returns ErrObjectNotFound on 404. Used by the release
	// signing pipeline which needs the artifact bytes server-side; do not
	// use it for license-gated downloads (use PresignedGet instead — that
	// path doesn't consume our bandwidth).
	Get(ctx context.Context, key string) (io.ReadCloser, error)

	// Delete removes an object. Returns nil if the object did not exist
	// (idempotent — matches S3 DeleteObject semantics).
	Delete(ctx context.Context, key string) error
}

// ObjectInfo is the subset of object metadata the release service consumes.
type ObjectInfo struct {
	Size        int64
	ContentType string
	ETag        string // opaque, used for client-side cache invalidation
}

// Sentinel errors. Implementations MUST return these for the documented
// conditions so the service layer can map them to user-facing errors.
var (
	ErrObjectNotFound  = errors.New("storage: object not found")
	ErrStorageDisabled = errors.New("storage: subsystem not configured")
)

// Disabled is a no-op implementation used when storage credentials are
// missing. All methods return ErrStorageDisabled. Wiring it up centrally
// avoids nil-check noise at call sites.
type Disabled struct{}

func (Disabled) PresignedPut(_ context.Context, _, _ string, _ int64, _ time.Duration) (string, error) {
	return "", ErrStorageDisabled
}
func (Disabled) PresignedGet(_ context.Context, _, _ string, _ time.Duration) (string, error) {
	return "", ErrStorageDisabled
}
func (Disabled) Head(_ context.Context, _ string) (*ObjectInfo, error) {
	return nil, ErrStorageDisabled
}
func (Disabled) Exists(_ context.Context, _ string) (bool, error) {
	return false, ErrStorageDisabled
}
func (Disabled) Get(_ context.Context, _ string) (io.ReadCloser, error) {
	return nil, ErrStorageDisabled
}
func (Disabled) Delete(_ context.Context, _ string) error {
	return ErrStorageDisabled
}

// Compile-time check.
var _ Storage = Disabled{}
