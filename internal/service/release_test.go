package service

import (
	"strings"
	"testing"
	"time"

	"github.com/tabloy/keygate/internal/model"
)

func TestValidateVersion(t *testing.T) {
	cases := []struct {
		v    string
		ok   bool
		desc string
	}{
		{"1.2.3", true, "plain semver"},
		{"0.0.1", true, "zero major/minor"},
		{"10.20.30", true, "multi-digit"},
		{"1.2.3-alpha", true, "prerelease"},
		{"1.2.3-alpha.1", true, "prerelease numeric"},
		{"1.2.3-rc.1+build.42", true, "prerelease + build"},
		{"1.2.3+build.42", true, "build only"},

		{"1", false, "missing minor/patch"},
		{"1.2", false, "missing patch"},
		{"v1.2.3", false, "v-prefix not accepted on input"},
		{"1.2.3 ", false, "trailing whitespace"},
		{" 1.2.3", false, "leading whitespace"},
		{"1.2.3.4", false, "four parts"},
		{"a.b.c", false, "non-numeric"},
		{"", false, "empty"},

		// SemVer 2.0 spec violations our regex would have accepted but
		// semver.IsValid correctly rejects:
		{"01.0.0", false, "leading zero in major (spec §2)"},
		{"1.02.0", false, "leading zero in minor"},
		{"1.0.03", false, "leading zero in patch"},
		{"1.0.0-01", false, "leading zero in numeric prerelease (spec §9)"},
		{"1.0.0-alpha.01", false, "leading zero in prerelease numeric ident"},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			err := validateVersion(c.v)
			if (err == nil) != c.ok {
				t.Fatalf("validateVersion(%q): got err=%v, want ok=%v", c.v, err, c.ok)
			}
		})
	}
}

func TestValidatePlatform(t *testing.T) {
	for _, ok := range allowedPlatforms {
		if err := validatePlatform(ok); err != nil {
			t.Errorf("expected %q to be valid, got %v", ok, err)
		}
	}
	for _, bad := range []string{"", "darwn-arm64", "windows", "DARWIN-ARM64", "darwin", "x64"} {
		if err := validatePlatform(bad); err == nil {
			t.Errorf("expected %q to be invalid", bad)
		}
	}
}

func TestSortBySemverDesc(t *testing.T) {
	rels := []*model.Release{
		{Version: "1.2.0"},
		{Version: "1.10.0"},
		{Version: "1.2.3-beta.1"},
		{Version: "1.2.3"},
		{Version: "0.9.9"},
		{Version: "2.0.0-rc.1"},
		{Version: "2.0.0"},
	}
	sortBySemverDesc(rels)
	got := make([]string, len(rels))
	for i, r := range rels {
		got[i] = r.Version
	}
	want := []string{
		"2.0.0",
		"2.0.0-rc.1",
		"1.10.0",
		"1.2.3",
		"1.2.3-beta.1",
		"1.2.0",
		"0.9.9",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sortBySemverDesc:\n  got  %v\n  want %v", got, want)
		}
	}
}

func TestSortBySemverDescPrereleaseOrdering(t *testing.T) {
	// Spec: 1.0.0-alpha < 1.0.0-alpha.1 < 1.0.0-beta < 1.0.0
	rels := []*model.Release{
		{Version: "1.0.0"},
		{Version: "1.0.0-beta"},
		{Version: "1.0.0-alpha.1"},
		{Version: "1.0.0-alpha"},
	}
	sortBySemverDesc(rels)
	got := []string{rels[0].Version, rels[1].Version, rels[2].Version, rels[3].Version}
	want := []string{"1.0.0", "1.0.0-beta", "1.0.0-alpha.1", "1.0.0-alpha"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("prerelease order:\n  got  %v\n  want %v", got, want)
		}
	}
}

func TestSortBySemverDescInvalidSinkToBottom(t *testing.T) {
	rels := []*model.Release{
		{Version: "garbage"},
		{Version: "1.0.0"},
		{Version: "also-bad"},
		{Version: "0.5.0"},
	}
	sortBySemverDesc(rels)
	if rels[0].Version != "1.0.0" || rels[1].Version != "0.5.0" {
		t.Fatalf("valid versions should sort first; got %v %v", rels[0].Version, rels[1].Version)
	}
	// last two are invalid; order between them is not guaranteed
}

func TestChannelFallbackChain(t *testing.T) {
	cases := map[string][]string{
		"stable": {"stable"},
		"beta":   {"beta", "stable"},
		"alpha":  {"alpha", "beta", "stable"},
		"dev":    {"dev", "alpha", "beta", "stable"},
		"":       {"stable"}, // unknown falls back to stable
		"weird":  {"stable"}, // truly unknown
	}
	for in, want := range cases {
		got := channelFallbackChain(in)
		if len(got) != len(want) {
			t.Errorf("channel %q: got %v want %v", in, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("channel %q[%d]: got %q want %q", in, i, got[i], want[i])
			}
		}
	}
}

func TestIsLicenseUsable(t *testing.T) {
	now := time.Now()
	future := now.Add(24 * time.Hour)
	past := now.Add(-24 * time.Hour)
	wayPast := now.Add(-30 * 24 * time.Hour)

	planGrace := &model.Plan{GraceDays: 7}

	cases := []struct {
		name string
		lic  *model.License
		want bool
	}{
		{
			"active perpetual (no expiry)",
			&model.License{Status: model.StatusActive},
			true,
		},
		{
			"active subscription before expiry",
			&model.License{Status: model.StatusActive, ValidUntil: &future},
			true,
		},
		{
			"active subscription within grace",
			&model.License{Status: model.StatusActive, ValidUntil: &past, Plan: planGrace},
			true, // 1 day past < 7-day grace
		},
		{
			"active subscription past grace",
			&model.License{Status: model.StatusActive, ValidUntil: &wayPast, Plan: planGrace},
			false,
		},
		{
			"trialing within window",
			&model.License{Status: model.StatusTrialing, ValidUntil: &future},
			true,
		},
		{
			"past_due within grace",
			&model.License{Status: model.StatusPastDue, ValidUntil: &past, Plan: planGrace},
			true,
		},
		{
			"canceled before period end",
			&model.License{Status: model.StatusCanceled, ValidUntil: &future},
			true,
		},
		{
			"canceled after period end",
			&model.License{Status: model.StatusCanceled, ValidUntil: &past},
			false,
		},
		{
			"suspended",
			&model.License{Status: model.StatusSuspended, ValidUntil: &future},
			false,
		},
		{
			"revoked",
			&model.License{Status: model.StatusRevoked, ValidUntil: &future},
			false,
		},
		{
			"expired",
			&model.License{Status: model.StatusExpired},
			false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsLicenseUsable(c.lic); got != c.want {
				t.Fatalf("IsLicenseUsable: got %v, want %v", got, c.want)
			}
		})
	}
}

func TestBuildFileKey(t *testing.T) {
	cases := []struct {
		name        string
		productSlug string
		platform    string
		version     string
		filename    string
		want        string
	}{
		{
			name:        "happy path with .dmg",
			productSlug: "myapp-pro",
			platform:    "darwin-arm64",
			version:     "1.2.3",
			filename:    "MyApp-1.2.3.dmg",
			want:        "releases/myapp-pro/1.2.3/darwin-arm64.dmg",
		},
		{
			name:        "compound .tar.gz preserved",
			productSlug: "myapp",
			platform:    "linux-x64",
			version:     "0.1.0",
			filename:    "myapp-0.1.0.tar.gz",
			want:        "releases/myapp/0.1.0/linux-x64.tar.gz",
		},
		{
			name:        "windows .exe",
			productSlug: "myapp",
			platform:    "windows-x64",
			version:     "2.0.0",
			filename:    "Setup.exe",
			want:        "releases/myapp/2.0.0/windows-x64.exe",
		},
		{
			name:        "unknown extension dropped",
			productSlug: "p",
			platform:    "linux-x64",
			version:     "1.0.0",
			filename:    "shady.exe.scr",
			want:        "releases/p/1.0.0/linux-x64",
		},
		{
			name:        "path traversal in filename ignored, only ext considered",
			productSlug: "p",
			platform:    "linux-x64",
			version:     "1.0.0",
			filename:    "../../etc/passwd.zip",
			want:        "releases/p/1.0.0/linux-x64.zip",
		},
		{
			name:        "special chars in version sanitized",
			productSlug: "p",
			platform:    "linux-x64",
			version:     "1.0.0-beta+sha.abc",
			filename:    "x.zip",
			want:        "releases/p/1.0.0-beta_sha.abc/linux-x64.zip",
		},
		{
			name:        "prerelease version preserved",
			productSlug: "myapp",
			platform:    "darwin-arm64",
			version:     "1.0.0-beta.1",
			filename:    "x.dmg",
			want:        "releases/myapp/1.0.0-beta.1/darwin-arm64.dmg",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildFileKey(c.productSlug, c.platform, c.version, c.filename)
			if got != c.want {
				t.Errorf("buildFileKey(%q,%q,%q,%q):\n  got  %q\n  want %q",
					c.productSlug, c.platform, c.version, c.filename, got, c.want)
			}
			if !strings.HasPrefix(got, "releases/") {
				t.Errorf("missing releases/ prefix: %q", got)
			}
			if strings.Contains(got, "..") {
				t.Errorf("path traversal sequence in %q", got)
			}
		})
	}
}

func TestDownloadFilename(t *testing.T) {
	cases := []struct {
		name string
		rel  *model.Release
		art  *model.ReleaseArtifact
		want string
	}{
		{
			name: "with display Name",
			rel:  &model.Release{Name: "MyApp Pro", Version: "1.2.3"},
			art:  &model.ReleaseArtifact{Platform: "darwin-arm64", FileKey: "releases/myapp-pro/1.2.3/darwin-arm64.dmg"},
			want: "MyApp_Pro-1.2.3-darwin-arm64.dmg",
		},
		{
			name: "without Name falls back to release-",
			rel:  &model.Release{Version: "1.0.0"},
			art:  &model.ReleaseArtifact{Platform: "linux-x64", FileKey: "releases/x/1.0.0/linux-x64.tar.gz"},
			want: "release-1.0.0-linux-x64.tar.gz",
		},
		{
			name: "no extension if FileKey has none",
			rel:  &model.Release{Name: "App", Version: "1.0.0"},
			art:  &model.ReleaseArtifact{Platform: "linux-x64", FileKey: "releases/x/1.0.0/linux-x64"},
			want: "App-1.0.0-linux-x64",
		},
		{
			name: "nil release returns empty",
			rel:  nil,
			art:  &model.ReleaseArtifact{Platform: "x", FileKey: "y"},
			want: "",
		},
		{
			name: "nil artifact returns empty",
			rel:  &model.Release{Version: "1.0.0"},
			art:  nil,
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DownloadFilename(c.rel, c.art)
			if got != c.want {
				t.Errorf("DownloadFilename:\n  got  %q\n  want %q", got, c.want)
			}
		})
	}
}

func TestNormalizeExt(t *testing.T) {
	cases := map[string]string{
		"app.tar.gz":  ".tar.gz",
		"app.tar.xz":  ".tar.xz",
		"app.tar.bz2": ".tar.bz2",
		"app.tar.zst": ".tar.zst",
		"app.zip":     ".zip",
		"app.DMG":     ".dmg",
		"app":         "",
		"app.tar":     ".tar",
		"":            "",
		"app.txt.tar": ".tar",
		"App-1.0.tgz": ".tgz",
	}
	for in, want := range cases {
		if got := normalizeExt(in); got != want {
			t.Errorf("normalizeExt(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSafeKeyComponent(t *testing.T) {
	cases := map[string]string{
		"simple":        "simple",
		"with space":    "with_space",
		"path/traverse": "path_traverse",
		// "../" → ".._" (slash becomes underscore; dots themselves are allowed
		// because dots without slashes can't traverse).
		"../../../etc": ".._.._.._etc",
		"foo!!bar":     "foo_bar",
		"a.b-c_d":      "a.b-c_d", // dots, hyphens, underscores allowed
		"emoji🎉here":   "emoji_here",
	}
	for in, want := range cases {
		if got := safeKeyComponent(in); got != want {
			t.Errorf("safeKeyComponent(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSafeKeyComponentNoPathTraversal verifies the practical security claim:
// no input can produce a / character in the output, so the resulting storage
// path can never escape its intended directory.
func TestSafeKeyComponentNoPathTraversal(t *testing.T) {
	for _, in := range []string{
		"a/b",
		"../../../etc/passwd",
		"x/../../y",
		"/absolute/path",
		`bad\path`,
		"a/b/c",
	} {
		got := safeKeyComponent(in)
		if strings.ContainsRune(got, '/') {
			t.Errorf("safeKeyComponent(%q) = %q must not contain /", in, got)
		}
	}
}
