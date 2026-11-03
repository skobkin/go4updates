package selfupdate

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"

	updates "github.com/skobkin/go4updates"
)

func TestMatchAssetName(t *testing.T) {
	tests := []struct {
		name      string
		asset     string
		app       string
		goos      string
		goarch    string
		wantMatch bool
	}{
		// Canonical goreleaser-style layouts.
		{name: "canonical tar.gz", asset: "app_1.2.3_linux_amd64.tar.gz", app: "app", goos: "linux", goarch: "amd64", wantMatch: true},
		{name: "no version", asset: "app_linux_amd64.tar.gz", app: "app", goos: "linux", goarch: "amd64", wantMatch: true},
		{name: "raw linux", asset: "app_1.2.3_linux_amd64", app: "app", goos: "linux", goarch: "amd64", wantMatch: true},
		{name: "dash separators", asset: "app-1.2.3-linux-amd64.tar.gz", app: "app", goos: "linux", goarch: "amd64", wantMatch: true},
		{name: "zip", asset: "app_1.2.3_linux_amd64.zip", app: "app", goos: "linux", goarch: "amd64", wantMatch: true},
		{name: "tgz", asset: "app_1.2.3_linux_amd64.tgz", app: "app", goos: "linux", goarch: "amd64", wantMatch: true},
		{name: "tar", asset: "app_1.2.3_linux_amd64.tar", app: "app", goos: "linux", goarch: "amd64", wantMatch: true},
		{name: "gz", asset: "app_1.2.3_linux_amd64.gz", app: "app", goos: "linux", goarch: "amd64", wantMatch: true},

		// Version label placement and shapes.
		{name: "version after arch", asset: "app_linux_amd64_1.2.3.tar.gz", app: "app", goos: "linux", goarch: "amd64", wantMatch: true},
		{name: "v-prefixed version", asset: "app_v1.2.3_linux_amd64.tar.gz", app: "app", goos: "linux", goarch: "amd64", wantMatch: true},
		{name: "date version", asset: "app_20240101_linux_amd64.tar.gz", app: "app", goos: "linux", goarch: "amd64", wantMatch: true},
		{name: "rc suffix version", asset: "app_1.2.3-rc1_linux_amd64.tar.gz", app: "app", goos: "linux", goarch: "amd64", wantMatch: true},

		// OS aliases.
		{name: "darwin arm64", asset: "app_1.2.3_darwin_arm64.zip", app: "app", goos: "darwin", goarch: "arm64", wantMatch: true},
		{name: "macos alias", asset: "app_1.2.3_macos_amd64.zip", app: "app", goos: "darwin", goarch: "amd64", wantMatch: true},
		{name: "osx alias", asset: "app_1.2.3_osx_amd64.zip", app: "app", goos: "darwin", goarch: "amd64", wantMatch: true},
		{name: "wrong os", asset: "app_1.2.3_darwin_amd64.zip", app: "app", goos: "linux", goarch: "amd64", wantMatch: false},

		// Arch aliases.
		{name: "x86_64 alias", asset: "app_1.2.3_linux_x86_64.tar.gz", app: "app", goos: "linux", goarch: "amd64", wantMatch: true},
		{name: "x64 alias", asset: "app_1.2.3_linux_x64.zip", app: "app", goos: "linux", goarch: "amd64", wantMatch: true},
		{name: "i386 alias", asset: "app_1.2.3_linux_i386.tar.gz", app: "app", goos: "linux", goarch: "386", wantMatch: true},
		{name: "x86 alias", asset: "app_1.2.3_linux_x86.tar.gz", app: "app", goos: "linux", goarch: "386", wantMatch: true},
		{name: "aarch64 alias", asset: "app_1.2.3_linux_aarch64.tar.gz", app: "app", goos: "linux", goarch: "arm64", wantMatch: true},
		{name: "armv7 matches arm", asset: "app_1.2.3_linux_armv7.tar.gz", app: "app", goos: "linux", goarch: "arm", wantMatch: true},
		{name: "arm64 does not match arm", asset: "app_1.2.3_linux_arm64.tar.gz", app: "app", goos: "linux", goarch: "arm", wantMatch: false},
		{name: "wrong arch", asset: "app_1.2.3_linux_amd64.tar.gz", app: "app", goos: "linux", goarch: "386", wantMatch: false},

		// Windows raw requires .exe.
		{name: "windows exe", asset: "app_1.2.3_windows_amd64.exe", app: "app", goos: "windows", goarch: "amd64", wantMatch: true},
		{name: "windows zip", asset: "app_1.2.3_windows_amd64.zip", app: "app", goos: "windows", goarch: "amd64", wantMatch: true},
		{name: "windows raw without exe", asset: "app_1.2.3_windows_amd64", app: "app", goos: "windows", goarch: "amd64", wantMatch: false},

		// Never-installable names. These are rejected structurally — their
		// extensions and trailing tokens are not part of the convention —
		// not by scanning the name for blocked words.
		{name: "sha256 sibling", asset: "app_1.2.3_linux_amd64.tar.gz.sha256", app: "app", goos: "linux", goarch: "amd64", wantMatch: false},
		{name: "checksum manifest", asset: "checksums.txt", app: "app", goos: "linux", goarch: "amd64", wantMatch: false},
		{name: "signature", asset: "app_1.2.3_linux_amd64.tar.gz.sig", app: "app", goos: "linux", goarch: "amd64", wantMatch: false},
		{name: "sbom", asset: "app_1.2.3.sbom.json", app: "app", goos: "linux", goarch: "amd64", wantMatch: false},
		{name: "detached signature", asset: "app_1.2.3_linux_amd64.tar.gz.asc", app: "app", goos: "linux", goarch: "amd64", wantMatch: false},
		{name: "provenance file", asset: "app_1.2.3_linux_amd64.tar.gz.intoto.jsonl", app: "app", goos: "linux", goarch: "amd64", wantMatch: false},

		// Application names that merely contain blocked words must match:
		// the former whole-name substring scan rejected them permanently.
		{name: "app named signal", asset: "signal_2.0.0_linux_amd64.tar.gz", app: "signal", goos: "linux", goarch: "amd64", wantMatch: true},
		{name: "app named design", asset: "design_1.0.0_darwin_arm64.zip", app: "design", goos: "darwin", goarch: "arm64", wantMatch: true},
		{name: "app named digest", asset: "digest_2.1.0_windows_amd64.exe", app: "digest", goos: "windows", goarch: "amd64", wantMatch: true},

		// Unsupported archive extensions never match.
		{name: "xz", asset: "app_1.2.3_linux_amd64.tar.xz", app: "app", goos: "linux", goarch: "amd64", wantMatch: false},
		{name: "tar.bz2", asset: "app_1.2.3_linux_amd64.tar.bz2", app: "app", goos: "linux", goarch: "amd64", wantMatch: false},
		{name: "zst", asset: "app_1.2.3_linux_amd64.tar.zst", app: "app", goos: "linux", goarch: "amd64", wantMatch: false},

		// Unknown tokens make the scheme non-conventional.
		{name: "unknown extra token", asset: "app_1.2.3_linux_musl_amd64.tar.gz", app: "app", goos: "linux", goarch: "amd64", wantMatch: false},

		// A token following a version is only tolerated when it is itself a
		// version label: the former backward join swallowed any token
		// ("musl", "flavor"), accepting names the conservative match must
		// reject — and colliding glibc/musl builds then matched ambiguous.
		{name: "flavor token after version", asset: "app_1.2.3_musl_linux_amd64.tar.gz", app: "app", goos: "linux", goarch: "amd64", wantMatch: false},
		{name: "flavor token after version, dash style", asset: "app-1.2.3-musl-linux-amd64.tar.gz", app: "app", goos: "linux", goarch: "amd64", wantMatch: false},
		{name: "channel label after version", asset: "app_1.2.3_nightly_linux_amd64.tar.gz", app: "app", goos: "linux", goarch: "amd64", wantMatch: true},
		{name: "digit prerelease after version", asset: "app_1.2.3_beta2_linux_amd64.tar.gz", app: "app", goos: "linux", goarch: "amd64", wantMatch: true},
		{name: "dotted prerelease after version", asset: "app_1.2.3_alpha.1_linux_amd64.tar.gz", app: "app", goos: "linux", goarch: "amd64", wantMatch: true},

		// Unmapped platforms.
		{name: "unknown goos", asset: "app_1.2.3_plan9_amd64.tar.gz", app: "app", goos: "plan9", goarch: "amd64", wantMatch: false},
		{name: "unknown goarch", asset: "app_1.2.3_linux_riscv64.tar.gz", app: "app", goos: "linux", goarch: "riscv64", wantMatch: false},

		// Degenerate inputs.
		{name: "empty asset", asset: "", app: "app", goos: "linux", goarch: "amd64", wantMatch: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchAssetName(tt.asset, tt.app, tt.goos, tt.goarch)
			if got != tt.wantMatch {
				t.Fatalf("MatchAssetName(%q, %q, %q, %q) = %v, want %v",
					tt.asset, tt.app, tt.goos, tt.goarch, got, tt.wantMatch)
			}
		})
	}
}

// Regression: a token that is both the application name and a platform
// alias was claimed by the platform check, so the application token was
// never found and a conventional asset did not match. An application named
// like an alias keeps its token; the convention places the platform token
// after it anyway.
func TestMatchAssetNameAppNamedLikePlatformAlias(t *testing.T) {
	tests := []struct {
		asset     string
		app       string
		goos      string
		goarch    string
		wantMatch bool
	}{
		{asset: "mac_1.2.3_darwin_amd64.zip", app: "mac", goos: "darwin", goarch: "amd64", wantMatch: true},
		{asset: "linux_1.2.3_linux_amd64.tar.gz", app: "linux", goos: "linux", goarch: "amd64", wantMatch: true},
		{asset: "arm_1.2.3_linux_arm.tar.gz", app: "arm", goos: "linux", goarch: "arm", wantMatch: true},

		// The alias claim must not excuse a missing platform token.
		{asset: "mac_amd64.zip", app: "mac", goos: "darwin", goarch: "amd64", wantMatch: false},
	}

	for _, tt := range tests {
		t.Run(tt.asset, func(t *testing.T) {
			got := MatchAssetName(tt.asset, tt.app, tt.goos, tt.goarch)
			if got != tt.wantMatch {
				t.Fatalf("MatchAssetName(%q, %q, %q, %q) = %v, want %v",
					tt.asset, tt.app, tt.goos, tt.goarch, got, tt.wantMatch)
			}
		})
	}
}

// Application names containing the layout's token separators ("_" and "-")
// are matched as a whole against the opening of the file name, followed by
// a separator and the usual version/platform suffix. These are real-world
// shapes: hyphenated CLI names, "lite" suffixed tools, and underscore
// compound names.
func TestMatchAssetNameWithSeparatorsInAppName(t *testing.T) {
	tests := []struct {
		asset     string
		app       string
		goos      string
		goarch    string
		wantMatch bool
	}{
		// Hyphenated names, underscore- and dash-joined GoReleaser layouts.
		{asset: "example-app_1.2.3_linux_amd64.tar.gz", app: "example-app", goos: "linux", goarch: "amd64", wantMatch: true},
		{asset: "example-app-1.2.3-linux-amd64.tar.gz", app: "example-app", goos: "linux", goarch: "amd64", wantMatch: true},
		{asset: "example-app_1.2.3_windows_amd64.zip", app: "example-app", goos: "windows", goarch: "amd64", wantMatch: true},
		{asset: "example-app-1.2.3-darwin-arm64.zip", app: "example-app", goos: "darwin", goarch: "arm64", wantMatch: true},

		// Underscore compound names.
		{asset: "mesh_map_1.0.0_linux_amd64.tar.gz", app: "mesh_map", goos: "linux", goarch: "amd64", wantMatch: true},
		{asset: "mesh_map-2.0.0-windows-amd64.zip", app: "mesh_map", goos: "windows", goarch: "amd64", wantMatch: true},
		{asset: "my_tool_1.2.3_linux_amd64", app: "my_tool", goos: "linux", goarch: "amd64", wantMatch: true},

		// Reviewer-reported real application names.
		{asset: "meshmap-lite_1.0.0_windows_amd64.zip", app: "meshmap-lite", goos: "windows", goarch: "amd64", wantMatch: true},
		{asset: "meshmap-lite_2.0.0_darwin_arm64.tar.gz", app: "meshmap-lite", goos: "darwin", goarch: "arm64", wantMatch: true},
		{asset: "amdgputop-web_0.5.1_linux_amd64.tar.gz", app: "amdgputop-web", goos: "linux", goarch: "amd64", wantMatch: true},
		{asset: "amdgputop-web-0.5.1-linux-amd64.tar.gz", app: "amdgputop-web", goos: "linux", goarch: "amd64", wantMatch: true},

		// Wrong platform still fails for separator names.
		{asset: "example-app_1.2.3_linux_386.tar.gz", app: "example-app", goos: "linux", goarch: "amd64", wantMatch: false},
		{asset: "amdgputop-web_0.5.1_windows_amd64.zip", app: "amdgputop-web", goos: "linux", goarch: "amd64", wantMatch: false},

		// The name is never matched as a mere prefix: the character after the
		// application name must be a token separator, and a longer name with
		// an extra token is a different application's asset.
		{asset: "application_1.2.3_linux_amd64.tar.gz", app: "app", goos: "linux", goarch: "amd64", wantMatch: false},
		{asset: "example-app-next_1.2.3_linux_amd64.tar.gz", app: "example-app", goos: "linux", goarch: "amd64", wantMatch: false},
		{asset: "examples-app_1.2.3_linux_amd64.tar.gz", app: "example-app", goos: "linux", goarch: "amd64", wantMatch: false},
		{asset: "my-tool_1.2.3_linux_amd64.tar.gz", app: "my_tool", goos: "linux", goarch: "amd64", wantMatch: false},

		// Version labels between the application name and the platform
		// tokens stay tolerated, including pre-release suffixes.
		{asset: "example-app_v2.0.0-rc1_linux_amd64.tar.gz", app: "example-app", goos: "linux", goarch: "amd64", wantMatch: true},
		{asset: "amdgputop-web_0.5.1-nightly_linux_amd64.tar.gz", app: "amdgputop-web", goos: "linux", goarch: "amd64", wantMatch: true},

		// An unknown token after a separator name still fails conservatively.
		{asset: "example-app_1.2.3_musl_linux_amd64.tar.gz", app: "example-app", goos: "linux", goarch: "amd64", wantMatch: false},
	}

	for _, tt := range tests {
		t.Run(tt.asset, func(t *testing.T) {
			got := MatchAssetName(tt.asset, tt.app, tt.goos, tt.goarch)
			if got != tt.wantMatch {
				t.Fatalf("MatchAssetName(%q, %q, %q, %q) = %v, want %v",
					tt.asset, tt.app, tt.goos, tt.goarch, got, tt.wantMatch)
			}
		})
	}
}

// The convention is {app}[_{-}{version}]_{os}_{arch}: the application token
// first, the platform tokens last in os-then-arch order. Names shuffling the
// roles ("linux_app_amd64", "app_amd64_linux") are outside the convention
// and fail conservatively instead of matching whatever they happen to
// contain.
func TestMatchAssetNameEnforcesTokenOrder(t *testing.T) {
	for _, asset := range []string{
		"linux_app_amd64.zip",
		"app_amd64_linux.zip",
		"amd64_linux_app.tar.gz",
	} {
		if MatchAssetName(asset, "app", "linux", "amd64") {
			t.Errorf("MatchAssetName(%q) = true, want false: token order deviates from the convention", asset)
		}
	}

	// Conventional order keeps matching, version labels between the
	// application and platform tokens included.
	if !MatchAssetName("app_1.2.3_linux_amd64.tar.gz", "app", "linux", "amd64") {
		t.Error("MatchAssetName(app_1.2.3_linux_amd64.tar.gz) = false, want true")
	}

	if !MatchAssetName("app_linux_amd64.tar.gz", "app", "linux", "amd64") {
		t.Error("MatchAssetName(app_linux_amd64.tar.gz) = false, want true")
	}
}

func TestVersionish(t *testing.T) {
	tests := []struct {
		token string
		want  bool
	}{
		{"1.2.3", true},
		{"v2", true},
		{"1", true},
		{"1.0.0-rc1", true},
		{"1.2.3+build.5", true},
		{"20240101", true},
		{"v", false},
		{"", false},
		{"rc1", false},
		{"musl", false},
		{"linux", false},
		{"amd64", false},
	}

	for _, tt := range tests {
		if got := versionish(tt.token); got != tt.want {
			t.Errorf("versionish(%q) = %v, want %v", tt.token, got, tt.want)
		}
	}
}

func TestVersionSuffixish(t *testing.T) {
	tests := []struct {
		token string
		want  bool
	}{
		{"rc1", true},
		{"beta2", true},
		{"alpha.1", true},
		{"20240101", true},
		{"nightly", true},
		{"dev", true},
		{"", false},
		{"musl", false},
		{"flavor", false},
		{"linux", false},
	}

	for _, tt := range tests {
		if got := versionSuffixish(tt.token); got != tt.want {
			t.Errorf("versionSuffixish(%q) = %v, want %v", tt.token, got, tt.want)
		}
	}
}

func TestClassifyAssetName(t *testing.T) {
	tests := []struct {
		name     string
		asset    string
		wantKind archiveKind
		wantErr  error
	}{
		{asset: "app_1.0_linux_amd64", wantKind: kindRaw},
		{asset: "app_1.0_windows_amd64.exe", wantKind: kindRaw},
		{asset: "app_1.0_linux_amd64.zip", wantKind: kindZip},
		{asset: "app_1.0_linux_amd64.tar.gz", wantKind: kindTarGz},
		{asset: "app_1.0_linux_amd64.tgz", wantKind: kindTarGz},
		{asset: "app_1.0_linux_amd64.tar", wantKind: kindTar},
		{asset: "app_1.0_linux_amd64.gz", wantKind: kindGz},
		{asset: "app_1.0_linux_amd64.tar.xz", wantKind: kindUnknown, wantErr: ErrUnsupportedArchive},
		{asset: "app_1.0_linux_amd64.tar.bz2", wantKind: kindUnknown, wantErr: ErrUnsupportedArchive},
		{asset: "app_1.0_linux_amd64.xz", wantKind: kindUnknown, wantErr: ErrUnsupportedArchive},
		{asset: "app_1.0_linux_amd64.7z", wantKind: kindUnknown, wantErr: ErrUnsupportedArchive},
		{asset: "APP_1.0_LINUX_AMD64.ZIP", wantKind: kindZip},
	}

	for _, tt := range tests {
		t.Run(tt.asset, func(t *testing.T) {
			kind, err := classifyAssetName(tt.asset)

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("classifyAssetName(%q) error = %v, want %v", tt.asset, err, tt.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("classifyAssetName(%q) unexpected error: %v", tt.asset, err)
			}

			if kind != tt.wantKind {
				t.Fatalf("classifyAssetName(%q) = %v, want %v", tt.asset, kind, tt.wantKind)
			}
		})
	}
}

func TestSelectAsset(t *testing.T) {
	asset := func(name string) updates.Asset {
		return updates.Asset{Name: name, URL: "https://example.test/" + name}
	}

	t.Run("no assets", func(t *testing.T) {
		_, err := selectAsset(nil, func(updates.Asset) bool { return true })
		if !errors.Is(err, ErrNoMatchingAsset) {
			t.Fatalf("err = %v, want ErrNoMatchingAsset", err)
		}
	})

	t.Run("matcher matches nothing", func(t *testing.T) {
		_, err := selectAsset([]updates.Asset{asset("a.zip")}, func(updates.Asset) bool { return false })
		if !errors.Is(err, ErrNoMatchingAsset) {
			t.Fatalf("err = %v, want ErrNoMatchingAsset", err)
		}

		if !strings.Contains(err.Error(), "a.zip") {
			t.Errorf("error should list considered assets, got: %v", err)
		}
	})

	t.Run("single match", func(t *testing.T) {
		got, err := selectAsset([]updates.Asset{asset("app.zip")}, func(updates.Asset) bool { return true })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got.Name != "app.zip" {
			t.Fatalf("selected %q, want app.zip", got.Name)
		}
	})

	t.Run("format ranking", func(t *testing.T) {
		tests := []struct {
			name      string
			assets    []string
			wantPicks string
		}{
			{name: "raw over zip", assets: []string{"app.zip", "app"}, wantPicks: "app"},
			{name: "zip over tar.gz", assets: []string{"app.tar.gz", "app.zip"}, wantPicks: "app.zip"},
			{name: "tar.gz over tar", assets: []string{"app.tar", "app.tar.gz"}, wantPicks: "app.tar.gz"},
			{name: "tar over gz", assets: []string{"app.gz", "app.tar"}, wantPicks: "app.tar"},
			{name: "tgz ranks with tar.gz", assets: []string{"app.tgz", "app.gz"}, wantPicks: "app.tgz"},
			{
				name:      "full preference chain",
				assets:    []string{"app.gz", "app.tar", "app.tar.gz", "app.zip", "app"},
				wantPicks: "app",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				assets := make([]updates.Asset, 0, len(tt.assets))

				for _, a := range tt.assets {
					assets = append(assets, asset(a))
				}

				got, err := selectAsset(assets, func(updates.Asset) bool { return true })
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}

				if got.Name != tt.wantPicks {
					t.Fatalf("selected %q, want %q", got.Name, tt.wantPicks)
				}
			})
		}
	})

	t.Run("equal rank is ambiguous", func(t *testing.T) {
		_, err := selectAsset([]updates.Asset{asset("app-amd64.zip"), asset("app_x64.zip")},
			func(updates.Asset) bool { return true })

		if !errors.Is(err, ErrAmbiguousAsset) {
			t.Fatalf("err = %v, want ErrAmbiguousAsset", err)
		}

		for _, want := range []string{"app-amd64.zip", "app_x64.zip", "WithAssetMatcher"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error should mention %q, got: %v", want, err)
			}
		}
	})

	t.Run("better rank breaks the tie", func(t *testing.T) {
		got, err := selectAsset([]updates.Asset{asset("app.zip"), asset("app")},
			func(updates.Asset) bool { return true })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got.Name != "app" {
			t.Fatalf("selected %q, want app", got.Name)
		}
	})

	t.Run("unsupported custom pick is returned as-is", func(t *testing.T) {
		// A custom matcher deliberately selecting an unsupported format must
		// surface at extraction time, not be silently skipped here.
		got, err := selectAsset([]updates.Asset{asset("app.tar.xz")},
			func(a updates.Asset) bool { return a.Name == "app.tar.xz" })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got.Name != "app.tar.xz" {
			t.Fatalf("selected %q, want app.tar.xz", got.Name)
		}
	})
}

func TestDefaultAssetMatcher(t *testing.T) {
	matcher := DefaultAssetMatcher("app")

	canonical := fmt.Sprintf("app_1.0.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)

	if !matcher(updates.Asset{Name: canonical}) {
		t.Errorf("default matcher should accept %q on %s/%s", canonical, runtime.GOOS, runtime.GOARCH)
	}

	if matcher(updates.Asset{Name: "app_1.0.0_bogus_bogus.tar.gz"}) {
		t.Error("default matcher should reject an unknown platform name")
	}

	if matcher(updates.Asset{Name: "other_1.0.0_linux_amd64.tar.gz"}) && runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		t.Error("default matcher should reject a different application name")
	}
}
