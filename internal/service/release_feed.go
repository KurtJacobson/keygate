package service

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/tabloy/keygate/internal/model"
)

// FeedFormat is the wire format requested by an auto-update client.
type FeedFormat string

const (
	FeedFormatSparkle  FeedFormat = "sparkle"
	FeedFormatVelopack FeedFormat = "velopack"
	FeedFormatTauri    FeedFormat = "tauri"
	FeedFormatJSON     FeedFormat = "json" // Keygate-native debug format
)

// IsValidFeedFormat reports whether f is one of the supported formats.
func IsValidFeedFormat(f FeedFormat) bool {
	switch f {
	case FeedFormatSparkle, FeedFormatVelopack, FeedFormatTauri, FeedFormatJSON:
		return true
	}
	return false
}

// FeedInput describes what the feed should contain.
// SignedDownloadURL is per-release: callers (handler) should produce a
// presigned GET URL for each release before passing the slice in. We do that
// outside the feed package so the storage layer is only consulted once per
// request rather than re-computed inside three different format renderers.
type FeedInput struct {
	ProductID   string
	ProductName string
	BaseURL     string // public origin, e.g. https://keygate.app — used for atom links
	Releases    []*FeedRelease

	// MinimumSupportedVersion: optional product-level version floor.
	// Embedded in formats that allow extension (Tauri). Sparkle's strict
	// XML schema and Velopack's array shape don't carry it; clients on
	// those formats consult the dedicated policy endpoint.
	MinimumSupportedVersion string
	MinimumSupportedMessage string
}

// FeedRelease is the marshal-ready view of one (release, artifact) pair —
// per platform, since the wire formats for Sparkle/Velopack/Tauri all carry
// per-platform binary metadata. The handler resolves which artifact within
// each release matches the caller's `?platform=` and bundles it here.
//
// SigningPublicKey is the raw base64 pubkey (32-byte ed25519) used to
// produce Artifact.Ed25519Sig. Required for Tauri's minisign-formatted
// signature envelope, which embeds an 8-byte key_id derived from the
// pubkey. Empty when the artifact is unsigned.
type FeedRelease struct {
	Release          *model.Release
	Artifact         *model.ReleaseArtifact
	DownloadURL      string
	SigningPublicKey string
}

// ─── Sparkle (appcast.xml) ───
// Spec: https://sparkle-project.org/documentation/publishing/

// sparkleAppcast is the root element of a Sparkle appcast feed.
type sparkleAppcast struct {
	XMLName      xml.Name       `xml:"rss"`
	Version      string         `xml:"version,attr"`
	XMLNSSparkle string         `xml:"xmlns:sparkle,attr"`
	XMLNSDC      string         `xml:"xmlns:dc,attr"`
	Channel      sparkleChannel `xml:"channel"`
}

type sparkleChannel struct {
	Title       string        `xml:"title"`
	Link        string        `xml:"link,omitempty"`
	Description string        `xml:"description,omitempty"`
	Language    string        `xml:"language,omitempty"`
	Items       []sparkleItem `xml:"item"`
}

type sparkleItem struct {
	Title           string             `xml:"title"`
	PubDate         string             `xml:"pubDate,omitempty"`
	SparkleVersion  string             `xml:"sparkle:version,omitempty"`
	SparkleShortVer string             `xml:"sparkle:shortVersionString,omitempty"`
	Description     sparkleDescription `xml:"description"`
	Enclosure       sparkleEnclosure   `xml:"enclosure"`
}

type sparkleDescription struct {
	XMLName xml.Name `xml:"description"`
	Body    string   `xml:",cdata"`
}

type sparkleEnclosure struct {
	XMLName      xml.Name `xml:"enclosure"`
	URL          string   `xml:"url,attr"`
	Length       int64    `xml:"length,attr"`
	Type         string   `xml:"type,attr"`
	SparkleEDSig string   `xml:"sparkle:edSignature,attr,omitempty"`
}

// sparkleSigPattern: raw base64 (no whitespace, no minisign envelope).
// 88 chars covers a padded 64-byte Ed25519 signature.
var sparkleSigPattern = regexp.MustCompile(`^[A-Za-z0-9+/]{86,88}={0,2}$`)

// RenderSparkle produces an appcast.xml body. Returns the bytes ready to
// write to the response.
//
// Sparkle clients fetch this XML, compare sparkle:version against the
// installed version, and download enclosure.url if newer.
//
// Signature handling: Sparkle's edSignature attribute MUST be raw base64
// of the 64-byte Ed25519 signature. If the model holds a value not matching
// that shape we omit the attribute rather than emit a malformed signature
// that would cause Sparkle to reject the entire item.
func RenderSparkle(in FeedInput) ([]byte, error) {
	feed := sparkleAppcast{
		Version:      "2.0",
		XMLNSSparkle: "http://www.andymatuschak.org/xml-namespaces/sparkle",
		XMLNSDC:      "http://purl.org/dc/elements/1.1/",
		Channel: sparkleChannel{
			Title:       in.ProductName + " Updates",
			Link:        in.BaseURL,
			Description: in.ProductName + " release feed",
			Language:    "en",
		},
	}

	for _, r := range in.Releases {
		if r == nil || r.Release == nil || r.Artifact == nil {
			continue
		}
		rel := r.Release
		a := r.Artifact
		pubDate := ""
		if rel.PublishedAt != nil {
			pubDate = rel.PublishedAt.UTC().Format(time.RFC1123Z)
		}
		sig := ""
		if sparkleSigPattern.MatchString(a.Ed25519Sig) {
			sig = a.Ed25519Sig
		}
		feed.Channel.Items = append(feed.Channel.Items, sparkleItem{
			Title:           rel.Version,
			PubDate:         pubDate,
			SparkleVersion:  rel.Version,
			SparkleShortVer: rel.Version,
			Description: sparkleDescription{
				Body: sanitizeCDATA(rel.ReleaseNotes),
			},
			Enclosure: sparkleEnclosure{
				URL:          r.DownloadURL,
				Length:       a.FileSize,
				Type:         feedContentType(a.ContentType),
				SparkleEDSig: sig,
			},
		})
	}

	body, err := xml.MarshalIndent(feed, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("sparkle: marshal: %w", err)
	}
	// Sparkle clients parse XML strictly; emit the standard prolog.
	return append([]byte(xml.Header), body...), nil
}

// sanitizeCDATA splits any literal "]]>" sequences across CDATA sections so
// admin-provided release notes can't accidentally (or maliciously) terminate
// the CDATA section early.
//
// The split trick: replace ]]> with ]]]]><![CDATA[> — the first ]] terminates
// the current CDATA, the next ]]> is split between two CDATA sections, and
// parsing resumes correctly.
func sanitizeCDATA(s string) string {
	return strings.ReplaceAll(s, "]]>", "]]]]><![CDATA[>")
}

// ─── Velopack (releases.json) ───
// Velopack consumes a JSON array of releases. The wire shape is opinionated:
//
//	[
//	  {
//	    "id": "v1.2.3",
//	    "type": "Full",
//	    "filename": "MyApp-1.2.3-full.nupkg",
//	    "size": 12345678,
//	    "sha256": "abc...",
//	    "url": "https://...",
//	    "notes": "...",
//	    "publishedAt": "2026-05-10T..."
//	  },
//	  ...
//	]
//
// Spec: https://docs.velopack.io/category/distributing
//
// Note: Velopack also supports delta releases. We only emit "Full" entries
// in Phase 1 — clients fall back to full downloads when no delta is present.

// VelopackEntry is one release in the Velopack feed.
type VelopackEntry struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	URL         string `json:"url"`
	Notes       string `json:"notes,omitempty"`
	PublishedAt string `json:"publishedAt,omitempty"`
}

// VelopackFeed is the JSON array Velopack expects.
type VelopackFeed []VelopackEntry

// BuildVelopack assembles the Velopack JSON-ready slice. Use json.Marshal on
// the return value to produce the wire bytes.
func BuildVelopack(in FeedInput) VelopackFeed {
	out := make(VelopackFeed, 0, len(in.Releases))
	for _, r := range in.Releases {
		if r == nil || r.Release == nil || r.Artifact == nil {
			continue
		}
		rel := r.Release
		a := r.Artifact
		entry := VelopackEntry{
			ID:       "v" + rel.Version,
			Type:     "Full",
			Filename: velopackFilename(rel, a),
			Size:     a.FileSize,
			SHA256:   strings.ToLower(a.SHA256),
			URL:      r.DownloadURL,
			Notes:    rel.ReleaseNotes,
		}
		if rel.PublishedAt != nil {
			entry.PublishedAt = rel.PublishedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, entry)
	}
	return out
}

// velopackFilename builds the filename Velopack expects for a "full" release.
// Extension comes from the artifact's storage key so Windows clients
// receiving a nupkg don't see a misleading .zip name.
func velopackFilename(rel *model.Release, a *model.ReleaseArtifact) string {
	name := rel.Name
	if name == "" {
		name = a.Platform
	}
	name = safeKeyComponent(name)
	ext := normalizeExt(a.FileKey)
	if ext == "" {
		ext = ".zip"
	}
	return fmt.Sprintf("%s-%s-full%s", name, rel.Version, ext)
}

// ─── Tauri ───
// Spec: https://tauri.app/v1/guides/distribution/updater
//
// Tauri's updater fetches a JSON document for ONE release at a time
// (the latest applicable). Schema:
//
//	{
//	  "version": "1.2.3",
//	  "pub_date": "2026-05-10T00:00:00Z",
//	  "url": "https://...",
//	  "signature": "untrusted comment...\nbase64sig",
//	  "notes": "..."
//	}

// TauriManifest is the single-release JSON Tauri's updater consumes.
//
// MinimumSupportedVersion + MinimumSupportedMessage are Keygate
// extensions: clients we ship with the official SDK refuse to run an
// installed build below this version. Tauri itself ignores unknown
// fields, so adding them is forward-compatible.
type TauriManifest struct {
	Version                 string `json:"version"`
	PubDate                 string `json:"pub_date,omitempty"`
	URL                     string `json:"url"`
	Signature               string `json:"signature,omitempty"`
	Notes                   string `json:"notes,omitempty"`
	MinimumSupportedVersion string `json:"minimum_supported_version,omitempty"`
	MinimumSupportedMessage string `json:"minimum_supported_message,omitempty"`
}

// BuildTauri returns the manifest for the FIRST entry in in.Releases (which
// is expected to be the latest after semver sort). Returns a zero value when
// no releases are present — callers should 204/404 in that case.
//
// The Signature field is wrapped in minisign envelope format because that
// is what Tauri's updater verifier consumes. Raw ed25519 base64 (the
// Sparkle convention we store) would fail Tauri's `signature[0..2] == "Ed"`
// + 8-byte key_id check.
func BuildTauri(in FeedInput) TauriManifest {
	for _, r := range in.Releases {
		if r == nil || r.Release == nil || r.Artifact == nil {
			continue
		}
		rel := r.Release
		a := r.Artifact
		m := TauriManifest{
			Version:                 rel.Version,
			URL:                     r.DownloadURL,
			Signature:               TauriSignatureEnvelope(a.Ed25519Sig, r.SigningPublicKey),
			Notes:                   rel.ReleaseNotes,
			MinimumSupportedVersion: in.MinimumSupportedVersion,
			MinimumSupportedMessage: in.MinimumSupportedMessage,
		}
		if rel.PublishedAt != nil {
			m.PubDate = rel.PublishedAt.UTC().Format(time.RFC3339)
		}
		return m
	}
	return TauriManifest{}
}

// TauriSignatureEnvelope wraps a raw-base64 ed25519 signature in the
// minisign-format string Tauri's updater expects:
//
//	untrusted comment: signature from keygate
//	<base64(2-byte algo "Ed" + 8-byte key_id + 64-byte raw sig)>
//
// Tauri's verifier:
//
//	let sig = base64::decode(signature.lines().nth(1)?)?;
//	if &sig[0..2] != b"Ed" { return Err }
//	ed25519::verify(&sig[10..74], msg, &pubkey[10..42])
//
// Returns "" if either rawSigB64 or rawPubKeyB64 is empty (unsigned
// artifact, or pubkey could not be resolved).
func TauriSignatureEnvelope(rawSigB64, rawPubKeyB64 string) string {
	if rawSigB64 == "" || rawPubKeyB64 == "" {
		return ""
	}
	rawSig, err := base64.StdEncoding.DecodeString(rawSigB64)
	if err != nil || len(rawSig) != 64 {
		return ""
	}
	rawPub, err := base64.StdEncoding.DecodeString(rawPubKeyB64)
	if err != nil || len(rawPub) != 32 {
		return ""
	}
	keyID := tauriKeyID(rawPub)
	blob := make([]byte, 0, 2+8+64)
	blob = append(blob, 'E', 'd')
	blob = append(blob, keyID[:]...)
	blob = append(blob, rawSig...)
	encoded := base64.StdEncoding.EncodeToString(blob)
	return "untrusted comment: signature from keygate\n" + encoded
}

// TauriPublicKey wraps a raw 32-byte ed25519 public key in the format
// Tauri's verifier expects (`base64(2-byte algo "Ed" + 8-byte key_id + 32-byte raw key)`).
// Use this when emitting public keys for Tauri devs to embed in their
// app's Tauri config — the raw Sparkle-shape pubkey will not work there.
func TauriPublicKey(rawPubKeyB64 string) string {
	if rawPubKeyB64 == "" {
		return ""
	}
	rawPub, err := base64.StdEncoding.DecodeString(rawPubKeyB64)
	if err != nil || len(rawPub) != 32 {
		return ""
	}
	keyID := tauriKeyID(rawPub)
	blob := make([]byte, 0, 2+8+32)
	blob = append(blob, 'E', 'd')
	blob = append(blob, keyID[:]...)
	blob = append(blob, rawPub...)
	return base64.StdEncoding.EncodeToString(blob)
}

// tauriKeyID derives the 8-byte minisign-style key_id deterministically
// from the public key. minisign uses random IDs but Tauri only checks
// that sig.key_id == pubkey.key_id, so a stable derivation is safe and
// avoids storing an extra column.
func tauriKeyID(rawPub []byte) [8]byte {
	h := sha256.Sum256(rawPub)
	var id [8]byte
	copy(id[:], h[:8])
	return id
}

// ─── Helpers ───

// feedContentType normalises content-type for feeds. Sparkle expects something
// like "application/octet-stream" or "application/x-apple-diskimage". Empty
// strings break some clients.
func feedContentType(ct string) string {
	if ct == "" {
		return "application/octet-stream"
	}
	return ct
}
