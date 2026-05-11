package storage

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNewS3RequiresBucket(t *testing.T) {
	_, err := NewS3(context.Background(), S3Config{
		AccessKey: "a",
		SecretKey: "b",
	})
	if err == nil || !strings.Contains(err.Error(), "bucket") {
		t.Fatalf("expected bucket-required error, got %v", err)
	}
}

func TestNewS3RequiresCredentials(t *testing.T) {
	_, err := NewS3(context.Background(), S3Config{Bucket: "x"})
	if err == nil || !strings.Contains(err.Error(), "access_key") {
		t.Fatalf("expected credential error, got %v", err)
	}
}

func TestNewS3RejectsHTTPNonLoopback(t *testing.T) {
	_, err := NewS3(context.Background(), S3Config{
		Bucket:    "x",
		AccessKey: "a",
		SecretKey: "b",
		Endpoint:  "http://internal.example.com",
	})
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("expected https-required rejection, got %v", err)
	}
}

func TestNewS3AllowsHTTPLoopback(t *testing.T) {
	for _, ep := range []string{
		"http://localhost:9000",
		"http://127.0.0.1:9000",
		"http://[::1]:9000",
	} {
		_, err := NewS3(context.Background(), S3Config{
			Bucket:    "x",
			AccessKey: "a",
			SecretKey: "b",
			Endpoint:  ep,
		})
		if err != nil {
			t.Errorf("expected loopback %q to be accepted, got %v", ep, err)
		}
	}
}

func TestNewS3RejectsNonHTTPScheme(t *testing.T) {
	_, err := NewS3(context.Background(), S3Config{
		Bucket:    "x",
		AccessKey: "a",
		SecretKey: "b",
		Endpoint:  "ftp://example.com",
	})
	if err == nil {
		t.Fatalf("expected ftp:// to be rejected")
	}
}

func TestClampPresignTTL(t *testing.T) {
	cases := []struct {
		in       time.Duration
		fallback time.Duration
		want     time.Duration
	}{
		{0, time.Hour, time.Hour},
		{-1, time.Hour, time.Hour},
		{5 * time.Second, time.Hour, minPresignTTL}, // floor
		{minPresignTTL, time.Hour, minPresignTTL},
		{30 * time.Minute, time.Hour, 30 * time.Minute},
		{8 * 24 * time.Hour, time.Hour, maxPresignTTL}, // ceiling
		{maxPresignTTL, time.Hour, maxPresignTTL},
	}
	for _, c := range cases {
		got := clampPresignTTL(c.in, c.fallback)
		if got != c.want {
			t.Errorf("clampPresignTTL(%v, %v) = %v, want %v", c.in, c.fallback, got, c.want)
		}
	}
}

func TestSanitizeASCII(t *testing.T) {
	cases := map[string]string{
		"hello.dmg":       "hello.dmg",
		`with "quote"`:    "with quote",
		`with\backslash`:  "withbackslash",
		"unicode-中文":      "unicode-",
		"with\x00control": "withcontrol",
		"with\nnewline":   "withnewline",
		"":                "",
	}
	for in, want := range cases {
		if got := sanitizeASCII(in); got != want {
			t.Errorf("sanitizeASCII(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRFC5987Escape(t *testing.T) {
	cases := map[string]string{
		"hello":       "hello",
		"hello world": "hello%20world",
		"a.b-c_d":     "a.b-c_d",
		"中文":          "%E4%B8%AD%E6%96%87",
		"a'b":         "a%27b",
		"a*b":         "a%2Ab",
		"a(b)c":       "a%28b%29c",
		"":            "",
	}
	for in, want := range cases {
		if got := rfc5987Escape(in); got != want {
			t.Errorf("rfc5987Escape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, ok := range []string{
		"localhost", "localhost:9000",
		"127.0.0.1", "127.0.0.1:8080",
		"::1", "[::1]:9000",
	} {
		if !isLoopbackHost(ok) {
			t.Errorf("expected %q to be loopback", ok)
		}
	}
	for _, bad := range []string{
		"example.com", "192.168.1.1", "10.0.0.1",
		"8.8.8.8", "internal.dev", "169.254.169.254",
	} {
		if isLoopbackHost(bad) {
			t.Errorf("expected %q to NOT be loopback", bad)
		}
	}
}

func TestDisabledStorageReturnsErrors(t *testing.T) {
	d := Disabled{}
	ctx := context.Background()

	if _, err := d.PresignedPut(ctx, "k", "ct", 0, time.Minute); err != ErrStorageDisabled {
		t.Errorf("PresignedPut: %v", err)
	}
	if _, err := d.PresignedGet(ctx, "k", "", time.Minute); err != ErrStorageDisabled {
		t.Errorf("PresignedGet: %v", err)
	}
	if _, err := d.Head(ctx, "k"); err != ErrStorageDisabled {
		t.Errorf("Head: %v", err)
	}
	if _, err := d.Exists(ctx, "k"); err != ErrStorageDisabled {
		t.Errorf("Exists: %v", err)
	}
	if err := d.Delete(ctx, "k"); err != ErrStorageDisabled {
		t.Errorf("Delete: %v", err)
	}
}
