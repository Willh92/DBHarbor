package version

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.0", "v1.2.0", 0},
		{"v1.2.0", "v1.3.0", -1},
		{"v1.2.0", "v1.10.0", -1},
		{"v2.0.0", "v1.9.9", 1},
		{"1.2.0-beta.1", "1.2.0", -1},
		{"1.2.0", "1.2.0-rc.1", 1},
	}
	for _, tc := range cases {
		if got := CompareSemver(tc.a, tc.b); got != tc.want {
			t.Fatalf("CompareSemver(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestCheckerGitHubReleasesAndChangelog(t *testing.T) {
	if got := Info().License; got != "Apache-2.0" {
		t.Fatalf("expected License Apache-2.0, got %q", got)
	}

	assetFilename := fmt.Sprintf("dbharbor-%s-%s", runtime.GOOS, runtime.GOARCH)
	desktopAssetFilename := fmt.Sprintf("dbharbor-desktop-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		assetFilename += ".exe"
		desktopAssetFilename += ".exe"
	}

	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/Willh92/DBHarbor/releases"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"tag_name":     "v9.9.0",
					"name":         "DBHarbor v9.9.0",
					"body":         "### Highlights\n- Faster streaming SQL engine",
					"html_url":     "https://github.com/Willh92/DBHarbor/releases/tag/v9.9.0",
					"draft":        false,
					"prerelease":   false,
					"published_at": "2026-11-06T12:00:00Z",
					"assets": []map[string]any{
						{
							"name":                 desktopAssetFilename,
							"size":                 33000000,
							"browser_download_url": "https://github.com/Willh92/DBHarbor/releases/download/v9.9.0/" + desktopAssetFilename,
						},
						{
							"name":                 assetFilename,
							"size":                 32000000,
							"browser_download_url": "https://github.com/Willh92/DBHarbor/releases/download/v9.9.0/" + assetFilename,
						},
					},
				},
			})
		case r.URL.Path == "/Willh92/DBHarbor/HEAD/CHANGELOG.zh-CN.md":
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			_, _ = w.Write([]byte("# Changelog (zh-CN)\n\n## [v9.9.0] - 2026-11-06\n- Localized zh-CN changelog entry\n"))
		case r.URL.Path == "/Willh92/DBHarbor/HEAD/CHANGELOG.md":
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			_, _ = w.Write([]byte("# Changelog\n\n## [v9.9.0] - 2026-11-06\n- Remote GitHub changelog entry\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockSrv.Close()

	checker := NewChecker()
	checker.APIBaseURL = mockSrv.URL
	checker.RawBaseURL = mockSrv.URL

	res := checker.Check(context.Background(), true)
	if !res.OnlineCheckSuccess {
		t.Fatalf("expected OnlineCheckSuccess=true, got false (notice=%s)", res.Notice)
	}
	if !res.HasUpdate || res.LatestVersion != "v9.9.0" {
		t.Fatalf("expected HasUpdate=true and LatestVersion=v9.9.0, got %+v", res)
	}
	if res.MatchedAssetName != assetFilename {
		t.Fatalf("expected platform asset %s, got %s", assetFilename, res.MatchedAssetName)
	}
	if res.ChangelogSource != "github_changelog" || !strings.Contains(res.ChangelogMarkdown, "Remote GitHub changelog entry") {
		t.Fatalf("unexpected changelog result: source=%s, body=%s", res.ChangelogSource, res.ChangelogMarkdown)
	}

	// Verify cache hit on second non-forced call
	cachedRes := checker.Check(context.Background(), false)
	if !cachedRes.FromCache {
		t.Fatalf("expected second Check(force=false) to be served from cache")
	}

	// Verify per-locale fetch & fallback behavior
	zhRes := checker.CheckWithLocale(context.Background(), false, "zh-CN,zh;q=0.9")
	if zhRes.FromCache || !strings.Contains(zhRes.ChangelogMarkdown, "Localized zh-CN changelog entry") {
		t.Fatalf("expected fresh localized zh-CN changelog, got fromCache=%v body=%s", zhRes.FromCache, zhRes.ChangelogMarkdown)
	}
	if !checker.CheckWithLocale(context.Background(), false, "zh").FromCache {
		t.Fatalf("expected second zh CheckWithLocale call to hit zh-CN cache")
	}

	// Verify zh-TW falls back to CHANGELOG.md when CHANGELOG.zh-TW.md is 404
	twRes := checker.CheckWithLocale(context.Background(), false, "zh-TW")
	if !strings.Contains(twRes.ChangelogMarkdown, "Remote GitHub changelog entry") {
		t.Fatalf("expected zh-TW fallback to CHANGELOG.md, got %s", twRes.ChangelogMarkdown)
	}

	// Verify desktop edition matches desktop asset rather than server asset
	prevEdition := Edition
	Edition = "desktop"
	defer func() { Edition = prevEdition }()
	deskRes := checker.Check(context.Background(), true)
	if deskRes.MatchedAssetName != desktopAssetFilename {
		t.Fatalf("expected desktop asset %s, got %s", desktopAssetFilename, deskRes.MatchedAssetName)
	}
}

func TestReleaseVsPreviewChannelsAndRollingPreview(t *testing.T) {
	assetFilename := fmt.Sprintf("dbharbor-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		assetFilename += ".exe"
	}

	t.Run("versioned release vs preview channel", func(t *testing.T) {
		mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasPrefix(r.URL.Path, "/repos/Willh92/DBHarbor/releases"):
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode([]map[string]any{
					{
						"tag_name":     "v9.10.0-preview.1",
						"name":         "DBHarbor v9.10.0-preview.1 (Preview)",
						"body":         "Preview channel features",
						"html_url":     "https://github.com/Willh92/DBHarbor/releases/tag/v9.10.0-preview.1",
						"draft":        false,
						"prerelease":   true,
						"published_at": "2026-11-06T15:00:00Z",
						"assets": []map[string]any{
							{
								"name":                 assetFilename,
								"size":                 32000000,
								"browser_download_url": "https://github.com/Willh92/DBHarbor/releases/download/v9.10.0-preview.1/" + assetFilename,
							},
						},
					},
					{
						"tag_name":     "v9.9.0",
						"name":         "DBHarbor v9.9.0",
						"body":         "Stable release",
						"html_url":     "https://github.com/Willh92/DBHarbor/releases/tag/v9.9.0",
						"draft":        false,
						"prerelease":   false,
						"published_at": "2026-11-05T12:00:00Z",
						"assets": []map[string]any{
							{
								"name":                 assetFilename,
								"size":                 32000000,
								"browser_download_url": "https://github.com/Willh92/DBHarbor/releases/download/v9.9.0/" + assetFilename,
							},
						},
					},
				})
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer mockSrv.Close()

		checker := NewChecker()
		checker.APIBaseURL = mockSrv.URL
		checker.RawBaseURL = mockSrv.URL

		relRes := checker.CheckWithOptions(context.Background(), true, "en-US", "release")
		if relRes.Channel != "release" || relRes.LatestVersion != "v9.9.0" || relRes.LatestPrerelease {
			t.Fatalf("expected release channel to pick stable v9.9.0, got %+v", relRes)
		}

		preRes := checker.CheckWithOptions(context.Background(), false, "en-US", "preview")
		if preRes.Channel != "preview" || preRes.LatestVersion != "v9.10.0-preview.1" || !preRes.LatestPrerelease || !preRes.HasUpdate {
			t.Fatalf("expected preview channel to pick v9.10.0-preview.1, got %+v", preRes)
		}
	})

	t.Run("rolling preview tag published after v1.0.0 stable", func(t *testing.T) {
		prevVer, prevCh := Version, Channel
		Version = "1.0.0"
		Channel = "release"
		defer func() {
			Version = prevVer
			Channel = prevCh
		}()

		mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/repos/Willh92/DBHarbor/releases") {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode([]map[string]any{
					{
						"tag_name":     "preview",
						"name":         "DBHarbor Preview (v1.0.0-preview.20261106.abc1234)",
						"body":         "Rolling preview build",
						"html_url":     "https://github.com/Willh92/DBHarbor/releases/tag/preview",
						"draft":        false,
						"prerelease":   true,
						"published_at": "2026-11-06T16:00:00Z",
					},
					{
						"tag_name":     "v1.0.0",
						"name":         "DBHarbor v1.0.0",
						"body":         "Initial stable release",
						"html_url":     "https://github.com/Willh92/DBHarbor/releases/tag/v1.0.0",
						"draft":        false,
						"prerelease":   false,
						"published_at": "2026-11-05T10:00:00Z",
					},
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer mockSrv.Close()

		checker := NewChecker()
		checker.APIBaseURL = mockSrv.URL
		checker.RawBaseURL = mockSrv.URL

		stableCheck := checker.CheckWithOptions(context.Background(), true, "en-US", "release")
		if stableCheck.HasUpdate || stableCheck.LatestVersion != "v1.0.0" {
			t.Fatalf("expected stable v1.0.0 to have no update on release channel, got %+v", stableCheck)
		}

		previewCheck := checker.CheckWithOptions(context.Background(), true, "en-US", "preview")
		if !previewCheck.HasUpdate || previewCheck.LatestVersion != "v1.0.0-preview.20261106.abc1234" || !previewCheck.LatestPrerelease {
			t.Fatalf("expected newer rolling preview to surface on preview channel, got %+v", previewCheck)
		}
	})
}

func TestMatchPlatformAssetBundlesAndMacApp(t *testing.T) {
	var rel ghRelease
	raw := `{
		"tag_name": "v1.0.0",
		"assets": [
			{"name": "dbharbor-bundle-v1.0.0-darwin-arm64.tar.gz", "size": 70000000, "browser_download_url": "https://example.com/bundle"},
			{"name": "dbharbor-darwin-arm64", "size": 31000000, "browser_download_url": "https://example.com/server"},
			{"name": "dbharbor-desktop-darwin-arm64", "size": 31500000, "browser_download_url": "https://example.com/desktop-bin"},
			{"name": "DBHarbor-darwin-arm64.app.zip", "size": 31600000, "browser_download_url": "https://example.com/desktop-app"}
		]
	}`
	if err := json.Unmarshal([]byte(raw), &rel); err != nil {
		t.Fatal(err)
	}

	srvName, _ := matchPlatformAssetForEdition(&rel, "darwin", "arm64", "server")
	if srvName != "dbharbor-darwin-arm64" {
		t.Fatalf("expected server asset dbharbor-darwin-arm64, got %q", srvName)
	}

	deskName, _ := matchPlatformAssetForEdition(&rel, "darwin", "arm64", "desktop")
	if deskName != "DBHarbor-darwin-arm64.app.zip" {
		t.Fatalf("expected macOS desktop .app.zip asset, got %q", deskName)
	}
}
