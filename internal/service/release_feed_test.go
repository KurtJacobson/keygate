package service

import (
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"github.com/tabloy/keygate/internal/model"
)

// validBase64Sig generates a syntactically valid 64-byte Ed25519 signature in
// raw base64 form for tests that need to verify Sparkle accepts it.
func validBase64Sig() string {
	raw := make([]byte, 64)
	for i := range raw {
		raw[i] = byte(i)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func mkRelease(version string, opts ...func(*model.Release, *model.ReleaseArtifact)) *FeedRelease {
	t := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	r := &model.Release{
		ID:           "rel-" + version,
		ProductID:    "prod-1",
		Version:      version,
		Channel:      model.ReleaseChannelStable,
		Name:         "MyApp",
		ReleaseNotes: "Bug fixes",
		Status:       model.ReleaseStatusPublished,
		PublishedAt:  &t,
	}
	a := &model.ReleaseArtifact{
		ID:          "art-" + version,
		ReleaseID:   r.ID,
		Platform:    "darwin-arm64",
		FileKey:     "releases/myapp/" + version + "/darwin-arm64.dmg",
		FileSize:    1024 * 1024,
		SHA256:      "abc123def456abc123def456abc123def456abc123def456abc123def456abcd",
		ContentType: "application/x-apple-diskimage",
	}
	for _, opt := range opts {
		opt(r, a)
	}
	return &FeedRelease{
		Release:     r,
		Artifact:    a,
		DownloadURL: "https://signed.example.com/" + version,
	}
}

func TestRenderSparkleHappyPath(t *testing.T) {
	in := FeedInput{
		ProductID:   "prod-1",
		ProductName: "MyApp",
		BaseURL:     "https://example.com",
		Releases: []*FeedRelease{
			mkRelease("1.2.3", func(_ *model.Release, a *model.ReleaseArtifact) { a.Ed25519Sig = validBase64Sig() }),
		},
	}
	body, err := RenderSparkle(in)
	if err != nil {
		t.Fatalf("RenderSparkle: %v", err)
	}
	s := string(body)
	if !strings.HasPrefix(s, xml.Header) {
		t.Errorf("missing XML prolog")
	}
	for _, want := range []string{
		"<rss version=\"2.0\"",
		"xmlns:sparkle=",
		"<title>MyApp Updates</title>",
		"<sparkle:version>1.2.3</sparkle:version>",
		`url="https://signed.example.com/1.2.3"`,
		`length="1048576"`,
		"sparkle:edSignature=",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("sparkle output missing %q\n--- output ---\n%s", want, s)
		}
	}

	// Must round-trip parse.
	var roundTrip sparkleAppcast
	if err := xml.Unmarshal(body, &roundTrip); err != nil {
		t.Errorf("sparkle output failed to re-parse: %v", err)
	}
	if len(roundTrip.Channel.Items) != 1 {
		t.Errorf("expected 1 item, got %d", len(roundTrip.Channel.Items))
	}
}

func TestRenderSparkleOmitsInvalidSignature(t *testing.T) {
	in := FeedInput{
		ProductName: "MyApp",
		Releases: []*FeedRelease{
			mkRelease("1.2.3", func(_ *model.Release, a *model.ReleaseArtifact) {
				a.Ed25519Sig = "untrusted comment: garbage\nBASE64_NOT_RIGHT_FORMAT"
			}),
		},
	}
	body, err := RenderSparkle(in)
	if err != nil {
		t.Fatalf("RenderSparkle: %v", err)
	}
	if strings.Contains(string(body), "sparkle:edSignature=") {
		t.Errorf("expected invalid sig to be omitted; got:\n%s", string(body))
	}
}

func TestSanitizeCDATAEscapesEndMarker(t *testing.T) {
	notes := "before ]]> middle ]]> after"
	in := FeedInput{
		ProductName: "X",
		Releases: []*FeedRelease{
			mkRelease("1.0.0", func(r *model.Release, _ *model.ReleaseArtifact) { r.ReleaseNotes = notes }),
		},
	}
	body, err := RenderSparkle(in)
	if err != nil {
		t.Fatalf("RenderSparkle: %v", err)
	}
	if strings.Contains(string(body), "<![CDATA[before ]]>") {
		t.Errorf("CDATA terminator was not escaped; output:\n%s", string(body))
	}
	// Round-trip should still parse despite the embedded ]]>
	var rt sparkleAppcast
	if err := xml.Unmarshal(body, &rt); err != nil {
		t.Fatalf("sparkle XML parse failed: %v", err)
	}
}

func TestBuildVelopack(t *testing.T) {
	in := FeedInput{
		ProductName: "MyApp",
		Releases: []*FeedRelease{
			mkRelease("1.2.3"),
			mkRelease("1.3.0", func(_ *model.Release, a *model.ReleaseArtifact) {
				a.FileKey = "releases/.../app-1.3.0.nupkg"
			}),
		},
	}
	feed := BuildVelopack(in)
	if len(feed) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(feed))
	}
	if feed[0].ID != "v1.2.3" {
		t.Errorf("expected v1.2.3 prefix, got %q", feed[0].ID)
	}
	if !strings.HasSuffix(feed[0].Filename, ".dmg") {
		t.Errorf("expected .dmg extension preserved, got %q", feed[0].Filename)
	}
	if !strings.HasSuffix(feed[1].Filename, ".nupkg") {
		t.Errorf("expected .nupkg extension preserved, got %q", feed[1].Filename)
	}
	if feed[0].Type != "Full" {
		t.Errorf("expected Type=Full, got %q", feed[0].Type)
	}

	// Marshalable to JSON.
	if _, err := json.Marshal(feed); err != nil {
		t.Errorf("BuildVelopack output not JSON-serialisable: %v", err)
	}
}

func TestBuildVelopackEmpty(t *testing.T) {
	feed := BuildVelopack(FeedInput{})
	if len(feed) != 0 {
		t.Errorf("expected empty slice, got %v", feed)
	}
	body, err := json.Marshal(feed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// MUST be `[]` not `null` — Velopack clients can't handle null.
	if string(body) != "[]" {
		t.Errorf("expected []; got %s", string(body))
	}
}

func TestBuildTauri(t *testing.T) {
	in := FeedInput{
		ProductName: "MyApp",
		Releases: []*FeedRelease{
			mkRelease("1.2.3"),
			mkRelease("1.3.0"), // Tauri picks first only
		},
	}
	m := BuildTauri(in)
	if m.Version != "1.2.3" {
		t.Errorf("Tauri picked wrong version: %q", m.Version)
	}
	if m.URL == "" {
		t.Errorf("Tauri URL empty")
	}
	if m.PubDate == "" {
		t.Errorf("Tauri pub_date empty")
	}
}

func TestBuildTauriEmpty(t *testing.T) {
	m := BuildTauri(FeedInput{})
	if m.Version != "" {
		t.Errorf("expected empty manifest, got %+v", m)
	}
}

// TestTauriSignatureEnvelopeMatchesVerifier verifies the wire shape
// against Tauri's actual verifier behavior:
//
//	signature.lines().nth(1) → base64
//	decoded[0..2]   == "Ed"
//	decoded[2..10]  == 8-byte key_id (= pubkey[2..10] for the same key)
//	decoded[10..74] == 64-byte ed25519 sig
//
// Anything else makes Tauri silently reject the update — and we'd see
// no signal in our tests until production users complained.
func TestTauriSignatureEnvelopeMatchesVerifier(t *testing.T) {
	rawSig := make([]byte, 64)
	for i := range rawSig {
		rawSig[i] = byte(i + 100)
	}
	rawPub := make([]byte, 32)
	for i := range rawPub {
		rawPub[i] = byte(i + 1)
	}
	sigB64 := base64.StdEncoding.EncodeToString(rawSig)
	pubB64 := base64.StdEncoding.EncodeToString(rawPub)

	envelope := TauriSignatureEnvelope(sigB64, pubB64)
	lines := strings.Split(envelope, "\n")
	if len(lines) < 2 {
		t.Fatalf("envelope must have at least 2 lines, got %d:\n%s", len(lines), envelope)
	}
	if !strings.HasPrefix(lines[0], "untrusted comment:") {
		t.Errorf("line 1 must start with 'untrusted comment:', got %q", lines[0])
	}
	decoded, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil {
		t.Fatalf("line 2 must decode as base64: %v", err)
	}
	if len(decoded) != 2+8+64 {
		t.Fatalf("decoded blob must be 74 bytes (algo+key_id+sig), got %d", len(decoded))
	}
	if string(decoded[0:2]) != "Ed" {
		t.Errorf("algo prefix must be 'Ed', got %q", decoded[0:2])
	}

	// Pubkey envelope's key_id MUST match the sig envelope's key_id.
	tauriPub := TauriPublicKey(pubB64)
	pubDecoded, err := base64.StdEncoding.DecodeString(tauriPub)
	if err != nil {
		t.Fatalf("tauri pubkey must decode: %v", err)
	}
	if len(pubDecoded) != 2+8+32 {
		t.Fatalf("pubkey blob must be 42 bytes, got %d", len(pubDecoded))
	}
	if string(decoded[2:10]) != string(pubDecoded[2:10]) {
		t.Errorf("sig key_id must equal pubkey key_id; sig=%x pub=%x",
			decoded[2:10], pubDecoded[2:10])
	}
	if string(decoded[10:74]) != string(rawSig) {
		t.Errorf("decoded[10:74] must equal raw signature bytes")
	}
	if string(pubDecoded[10:42]) != string(rawPub) {
		t.Errorf("decoded[10:42] of pubkey must equal raw public key bytes")
	}
}

func TestTauriEnvelopeEmptyOnUnsigned(t *testing.T) {
	if got := TauriSignatureEnvelope("", "anything"); got != "" {
		t.Errorf("unsigned artifact must produce empty envelope, got %q", got)
	}
	if got := TauriSignatureEnvelope("anything", ""); got != "" {
		t.Errorf("missing pubkey must produce empty envelope, got %q", got)
	}
}

func TestTauriEnvelopeRejectsMalformedInputs(t *testing.T) {
	// 32-byte sig (too short) → empty
	short := base64.StdEncoding.EncodeToString(make([]byte, 32))
	pub := base64.StdEncoding.EncodeToString(make([]byte, 32))
	if got := TauriSignatureEnvelope(short, pub); got != "" {
		t.Errorf("short sig must produce empty envelope, got %q", got)
	}
	// non-base64 → empty
	if got := TauriSignatureEnvelope("!!!not base64!!!", pub); got != "" {
		t.Errorf("malformed base64 must produce empty envelope, got %q", got)
	}
}

func TestIsValidFeedFormat(t *testing.T) {
	for _, ok := range []FeedFormat{FeedFormatSparkle, FeedFormatVelopack, FeedFormatTauri, FeedFormatJSON} {
		if !IsValidFeedFormat(ok) {
			t.Errorf("expected %q to be valid", ok)
		}
	}
	for _, bad := range []FeedFormat{"", "atom", "rss", "SPARKLE"} {
		if IsValidFeedFormat(bad) {
			t.Errorf("expected %q to be invalid", bad)
		}
	}
}
