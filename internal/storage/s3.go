package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// S3Config bundles the parameters needed to construct an S3-compatible client.
// It is decoupled from the application config struct so this package has no
// dependency on internal/config.
type S3Config struct {
	Endpoint       string // empty = AWS S3 default endpoint
	Region         string // R2 wants "auto"; AWS wants real region
	Bucket         string
	AccessKey      string
	SecretKey      string
	ForcePathStyle bool
}

// S3Storage is an S3-compatible object store implementation.
// It is safe for concurrent use; the underlying S3 client is goroutine-safe.
type S3Storage struct {
	client    *s3.Client
	presigner *s3.PresignClient
	bucket    string
}

const (
	// minPresignTTL is the floor for any presigned URL to avoid signing URLs
	// that expire before the client can plausibly use them.
	minPresignTTL = 30 * time.Second
	// maxPresignTTL is AWS SigV4's hard limit. R2 mirrors it.
	maxPresignTTL = 7 * 24 * time.Hour
)

// NewS3 constructs an S3Storage. It validates the config eagerly but does NOT
// reach out to the network — credentials are verified on first real call.
//
// Why no eager network probe: in production we want the server to start even
// if the storage backend is briefly down, surfacing errors per-request rather
// than failing fast at boot.
//
// Credentials: this constructor uses ONLY the static credentials passed via
// S3Config. We do not call awsconfig.LoadDefaultConfig because that would
// pull from env vars / IMDS / IAM role / shared profile, leading to confusing
// debug experiences when the runtime environment provides different creds
// than the operator intended.
func NewS3(_ context.Context, c S3Config) (*S3Storage, error) {
	if c.Bucket == "" {
		return nil, errors.New("storage: bucket is required")
	}
	if c.AccessKey == "" || c.SecretKey == "" {
		return nil, errors.New("storage: access_key and secret_key are required")
	}
	if err := validateEndpoint(c.Endpoint); err != nil {
		return nil, err
	}

	region := c.Region
	if region == "" {
		region = "auto"
	}

	cfg := aws.Config{
		Region:      region,
		Credentials: credentials.NewStaticCredentialsProvider(c.AccessKey, c.SecretKey, ""),
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if c.Endpoint != "" {
			o.BaseEndpoint = aws.String(c.Endpoint)
		}
		o.UsePathStyle = c.ForcePathStyle
	})

	return &S3Storage{
		client:    client,
		presigner: s3.NewPresignClient(client),
		bucket:    c.Bucket,
	}, nil
}

// PresignedPut returns a URL the client can PUT a file to.
//
// IMPORTANT: contentType and expectedSize are *hints*, not enforced limits.
// AWS SDK v2's default presigner does not include Content-Type or
// Content-Length in the SigV4 signed headers, so the storage backend will
// happily accept mismatched headers. Real size/type enforcement must happen
// at FinalizeUpload (Head check) or via bucket-side policies (e.g. R2
// max-object-size, S3 bucket policy with conditional Content-Length).
func (s *S3Storage) PresignedPut(ctx context.Context, key, contentType string, expectedSize int64, expires time.Duration) (string, error) {
	expires = clampPresignTTL(expires, time.Hour)
	if key == "" {
		return "", errors.New("storage: empty key")
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	input := &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	}
	if expectedSize > 0 {
		input.ContentLength = aws.Int64(expectedSize)
	}

	req, err := s.presigner.PresignPutObject(ctx, input, s3.WithPresignExpires(expires))
	if err != nil {
		return "", fmt.Errorf("storage: presign put: %w", err)
	}
	return req.URL, nil
}

// PresignedGet returns a short-TTL URL for license-gated downloads. The
// filenameHint, when non-empty, controls the Content-Disposition header so
// browsers save the file with a recognisable name.
func (s *S3Storage) PresignedGet(ctx context.Context, key, filenameHint string, expires time.Duration) (string, error) {
	expires = clampPresignTTL(expires, 10*time.Minute)
	if key == "" {
		return "", errors.New("storage: empty key")
	}

	input := &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}
	if filenameHint != "" {
		// RFC 5987 / 6266: the legacy `filename="..."` portion can only carry
		// ASCII; the modern `filename*=UTF-8''...` portion percent-encodes
		// any non-attr-char byte. We emit both for maximum browser compat.
		input.ResponseContentDisposition = aws.String(
			`attachment; filename="` + sanitizeASCII(filenameHint) +
				`"; filename*=UTF-8''` + rfc5987Escape(filenameHint),
		)
	}

	req, err := s.presigner.PresignGetObject(ctx, input, s3.WithPresignExpires(expires))
	if err != nil {
		return "", fmt.Errorf("storage: presign get: %w", err)
	}
	return req.URL, nil
}

// Head fetches object metadata. Returns ErrObjectNotFound on 404.
func (s *S3Storage) Head(ctx context.Context, key string) (*ObjectInfo, error) {
	if key == "" {
		return nil, errors.New("storage: empty key")
	}
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isS3NotFound(err) {
			return nil, ErrObjectNotFound
		}
		return nil, fmt.Errorf("storage: head: %w", err)
	}

	info := &ObjectInfo{}
	if out.ContentLength != nil {
		info.Size = *out.ContentLength
	}
	if out.ContentType != nil {
		info.ContentType = *out.ContentType
	}
	if out.ETag != nil {
		info.ETag = strings.Trim(*out.ETag, `"`)
	}
	return info, nil
}

// Exists reports whether the object exists. Equivalent to Head + presence
// check; provided as a convenience so callers don't allocate ObjectInfo
// just to discard it.
func (s *S3Storage) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.Head(ctx, key)
	if errors.Is(err, ErrObjectNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Get returns a streaming reader for the object body. Caller MUST close the
// returned ReadCloser. Used by the release signing pipeline — license-gated
// downloads should use PresignedGet instead so the bytes flow client → R2
// directly without consuming our bandwidth.
func (s *S3Storage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if key == "" {
		return nil, errors.New("storage: empty key")
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isS3NotFound(err) {
			return nil, ErrObjectNotFound
		}
		return nil, fmt.Errorf("storage: get: %w", err)
	}
	return out.Body, nil
}

// Delete removes an object. S3 DeleteObject is idempotent on AWS (calling
// on a missing key returns success). We normalise non-AWS backends that
// return NoSuchKey to match.
func (s *S3Storage) Delete(ctx context.Context, key string) error {
	if key == "" {
		return errors.New("storage: empty key")
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isS3NotFound(err) {
			return nil
		}
		return fmt.Errorf("storage: delete: %w", err)
	}
	return nil
}

// ─── Helpers ───

// clampPresignTTL applies the [minPresignTTL, maxPresignTTL] envelope.
// A non-positive input falls back to fallback rather than failing — most
// call sites pass a config-derived value that may be missing.
func clampPresignTTL(d, fallback time.Duration) time.Duration {
	if d <= 0 {
		d = fallback
	}
	if d < minPresignTTL {
		d = minPresignTTL
	}
	if d > maxPresignTTL {
		d = maxPresignTTL
	}
	return d
}

// validateEndpoint rejects misconfigured endpoints that would risk SSRF or
// plaintext credential transmission. Loopback HTTP is allowed for tests.
func validateEndpoint(endpoint string) error {
	if endpoint == "" {
		return nil // AWS S3 default — safe
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("storage: endpoint is not a valid URL: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return errors.New("storage: endpoint must use http or https")
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Host) {
		return errors.New("storage: endpoint must use https (only loopback may use http)")
	}
	return nil
}

func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// isS3NotFound recognises both the typed "NotFound" error (HeadObject) and
// the "NoSuchKey" code (GetObject / DeleteObject path). Both map to a
// missing object.
func isS3NotFound(err error) bool {
	if err == nil {
		return false
	}
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return true
	}
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NotFound", "NoSuchKey", "404":
			return true
		}
	}
	return false
}

// sanitizeASCII returns a filename safe for the legacy `filename="..."`
// portion of Content-Disposition. Non-ASCII runes are dropped; quotes and
// backslashes (which would break the quoted string) are stripped.
// The UTF-8 form (filename*=) preserves the original via percent-encoding.
func sanitizeASCII(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// rfc5987Escape percent-encodes a UTF-8 string per RFC 5987 attr-char rules.
// Used in the modern `filename*=UTF-8”<value>` portion of Content-Disposition.
//
// attr-char per RFC 5987: ALPHA / DIGIT / "!" / "#" / "$" / "&" / "+" /
//
//	"-" / "." / "^" / "_" / "`" / "|" / "~"
//
// Anything else (including reserved chars like ' ( ) * which url.PathEscape
// leaves unencoded) must be percent-encoded.
func rfc5987Escape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range []byte(s) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '!' || r == '#' || r == '$' || r == '&' || r == '+' ||
			r == '-' || r == '.' || r == '^' || r == '_' || r == '`' ||
			r == '|' || r == '~' {
			b.WriteByte(r)
		} else {
			fmt.Fprintf(&b, "%%%02X", r)
		}
	}
	return b.String()
}

// Compile-time check.
var _ Storage = (*S3Storage)(nil)
