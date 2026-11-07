package version

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Build-time overridable metadata via -ldflags.
var (
	Version   = "1.0.1"
	Edition   = "server"  // "server" or "desktop"
	Channel   = "release" // "release" or "preview"
	GitCommit = "HEAD"
	BuildTime = ""
)

const (
	License       = "Apache-2.0"
	GitHubOwner   = "Willh92"
	GitHubRepo    = "DBHarbor"
	GitHubRepoURL = "https://github.com/Willh92/DBHarbor"
	GitHubGitURL  = "https://github.com/Willh92/DBHarbor.git"
)

//go:embed CHANGELOG.md
var embeddedChangelog string

//go:embed CHANGELOG.zh-CN.md
var embeddedChangelogZhCN string

//go:embed CHANGELOG.zh-TW.md
var embeddedChangelogZhTW string

//go:embed CHANGELOG.ja.md
var embeddedChangelogJA string

// EmbeddedChangelog returns the built-in English CHANGELOG.md bundled inside the binary.
func EmbeddedChangelog() string {
	return embeddedChangelog
}

// EmbeddedChangelogForLocale returns the localized built-in CHANGELOG matching locale (falling back to English).
func EmbeddedChangelogForLocale(locale string) string {
	switch normalizeLocaleKey(locale) {
	case "zh-CN":
		if strings.TrimSpace(embeddedChangelogZhCN) != "" {
			return embeddedChangelogZhCN
		}
	case "zh-TW":
		if strings.TrimSpace(embeddedChangelogZhTW) != "" {
			return embeddedChangelogZhTW
		}
	case "ja":
		if strings.TrimSpace(embeddedChangelogJA) != "" {
			return embeddedChangelogJA
		}
	}
	return embeddedChangelog
}

// EmbeddedChangelogsMap returns all built-in localized changelogs keyed by canonical UI locale code.
func EmbeddedChangelogsMap() map[string]string {
	return map[string]string{
		"en-US": embeddedChangelog,
		"zh-CN": EmbeddedChangelogForLocale("zh-CN"),
		"zh-TW": EmbeddedChangelogForLocale("zh-TW"),
		"ja-JP": EmbeddedChangelogForLocale("ja-JP"),
	}
}

func localeCodeFromKey(localeKey string) string {
	switch localeKey {
	case "zh-CN":
		return "zh-CN"
	case "zh-TW":
		return "zh-TW"
	case "ja":
		return "ja-JP"
	default:
		return "en-US"
	}
}

// NormalizeTag strips a leading "v"/"V" and whitespace for display or comparison.
func NormalizeTag(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	return v
}

// DisplayTag formats a version with a leading "v" prefix (e.g. "v1.2.0").
func DisplayTag(v string) string {
	clean := NormalizeTag(v)
	if clean == "" {
		return "v0.0.0"
	}
	return "v" + clean
}

// EffectiveChannel returns "preview" or "release" for the running binary.
func EffectiveChannel() string {
	ch := strings.ToLower(strings.TrimSpace(Channel))
	if ch == "preview" || strings.Contains(NormalizeTag(Version), "-") {
		return "preview"
	}
	return "release"
}

// BuildInfo describes the running binary's version and platform metadata.
type BuildInfo struct {
	Version        string `json:"version"`
	DisplayVersion string `json:"displayVersion"`
	Edition        string `json:"edition"`
	Channel        string `json:"channel"`
	License        string `json:"license"`
	GitCommit      string `json:"gitCommit"`
	BuildTime      string `json:"buildTime"`
	GoVersion      string `json:"goVersion"`
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	RepoURL        string `json:"repoUrl"`
	GitURL         string `json:"gitUrl"`
	ReleasesURL    string `json:"releasesUrl"`
}

// Info returns the current runtime BuildInfo.
func Info() BuildInfo {
	ed := strings.TrimSpace(Edition)
	if ed == "" {
		ed = "server"
	}
	return BuildInfo{
		Version:        NormalizeTag(Version),
		DisplayVersion: DisplayTag(Version),
		Edition:        ed,
		Channel:        EffectiveChannel(),
		License:        License,
		GitCommit:      GitCommit,
		BuildTime:      BuildTime,
		GoVersion:      runtime.Version(),
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		RepoURL:        GitHubRepoURL,
		GitURL:         GitHubGitURL,
		ReleasesURL:    GitHubRepoURL + "/releases",
	}
}

// ReleaseAsset represents a downloadable binary attached to a GitHub Release.
type ReleaseAsset struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"downloadUrl"`
}

// ReleaseItem represents a single version entry in the changelog / releases feed.
type ReleaseItem struct {
	TagName     string         `json:"tagName"`
	Name        string         `json:"name"`
	Body        string         `json:"body"`
	HTMLURL     string         `json:"htmlUrl"`
	PublishedAt string         `json:"publishedAt"`
	Prerelease  bool           `json:"prerelease"`
	Assets      []ReleaseAsset `json:"assets,omitempty"`
}

// UpdateCheckResult contains the version check outcome and changelog from GitHub.
type UpdateCheckResult struct {
	Channel            string        `json:"channel"`
	CurrentVersion     string        `json:"currentVersion"`
	LatestVersion      string        `json:"latestVersion"`
	LatestPrerelease   bool          `json:"latestPrerelease"`
	HasUpdate          bool          `json:"hasUpdate"`
	ReleaseURL         string        `json:"releaseUrl"`
	MatchedAssetURL    string        `json:"matchedAssetUrl,omitempty"`
	MatchedAssetName   string        `json:"matchedAssetName,omitempty"`
	PublishedAt        string        `json:"publishedAt,omitempty"`
	RepoURL            string        `json:"repoUrl"`
	GitURL             string        `json:"gitUrl"`
	ChangelogLang      string        `json:"changelogLang,omitempty"`
	ChangelogMarkdown  string        `json:"changelogMarkdown"`
	ChangelogSource    string        `json:"changelogSource"` // "github_changelog", "github_releases", "github_commits", "embedded"
	Releases           []ReleaseItem `json:"releases"`
	CheckedAt          string        `json:"checkedAt"`
	FromCache          bool          `json:"fromCache"`
	OnlineCheckSuccess bool          `json:"onlineCheckSuccess"`
	Notice             string        `json:"notice,omitempty"`
}

type cachedCheckEntry struct {
	result UpdateCheckResult
	at     time.Time
}

// Checker queries GitHub (Willh92/DBHarbor) for new versions and changelogs with per-locale TTL caching.
type Checker struct {
	APIBaseURL string // default: https://api.github.com
	RawBaseURL string // default: https://raw.githubusercontent.com
	Owner      string
	Repo       string
	CacheTTL   time.Duration
	HTTPClient *http.Client

	mu     sync.Mutex
	cached map[string]cachedCheckEntry
}

// NewChecker constructs a GitHub version & changelog checker for Willh92/DBHarbor.
func NewChecker() *Checker {
	return &Checker{
		APIBaseURL: "https://api.github.com",
		RawBaseURL: "https://raw.githubusercontent.com",
		Owner:      GitHubOwner,
		Repo:       GitHubRepo,
		CacheTTL:   15 * time.Minute,
		HTTPClient: &http.Client{Timeout: 8 * time.Second},
		cached:     make(map[string]cachedCheckEntry),
	}
}

// Check fetches or returns cached English update & changelog metadata from GitHub using the binary's default channel.
func (c *Checker) Check(ctx context.Context, force bool) UpdateCheckResult {
	return c.CheckWithOptions(ctx, force, "en-US", "")
}

// CheckWithLocale fetches or returns cached update & localized changelog metadata from GitHub using the binary's default channel.
func (c *Checker) CheckWithLocale(ctx context.Context, force bool, locale string) UpdateCheckResult {
	return c.CheckWithOptions(ctx, force, locale, "")
}

// CheckWithOptions fetches or returns cached update & localized changelog metadata for the requested channel ("release" or "preview").
func (c *Checker) CheckWithOptions(ctx context.Context, force bool, locale string, channel string) UpdateCheckResult {
	localeKey := normalizeLocaleKey(locale)
	channelKey := normalizeChannelKey(channel)
	cacheKey := localeKey + ":" + channelKey

	c.mu.Lock()
	if !force && c.cached != nil {
		if entry, ok := c.cached[cacheKey]; ok && time.Since(entry.at) < c.CacheTTL {
			cp := entry.result
			cp.FromCache = true
			c.mu.Unlock()
			return cp
		}
	}
	c.mu.Unlock()

	res := c.fetchOnline(ctx, localeKey, channelKey)

	c.mu.Lock()
	if c.cached == nil {
		c.cached = make(map[string]cachedCheckEntry)
	}
	c.cached[cacheKey] = cachedCheckEntry{
		result: res,
		at:     time.Now(),
	}
	c.mu.Unlock()

	return res
}

func normalizeChannelKey(channel string) string {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "preview", "prerelease", "beta", "nightly":
		return "preview"
	case "release", "stable":
		return "release"
	default:
		return EffectiveChannel()
	}
}

func normalizeLocaleKey(locale string) string {
	s := strings.TrimSpace(locale)
	if idx := strings.IndexAny(s, ",;"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	s = strings.ToLower(strings.ReplaceAll(s, "_", "-"))
	switch {
	case strings.HasPrefix(s, "zh-tw") || strings.HasPrefix(s, "zh-hk") || strings.HasPrefix(s, "zh-hant"):
		return "zh-TW"
	case strings.HasPrefix(s, "zh"):
		return "zh-CN"
	case strings.HasPrefix(s, "ja"):
		return "ja"
	default:
		return "en"
	}
}

func changelogCandidatesForLocale(localeKey string) []string {
	switch localeKey {
	case "zh-TW":
		return []string{"CHANGELOG.zh-TW.md", "CHANGELOG.md"}
	case "zh-CN":
		return []string{"CHANGELOG.zh-CN.md", "CHANGELOG.md"}
	case "ja":
		return []string{"CHANGELOG.ja.md", "CHANGELOG.md"}
	default:
		return []string{"CHANGELOG.md"}
	}
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []struct {
		Name               string `json:"name"`
		Size               int64  `json:"size"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

type ghTag struct {
	Name string `json:"name"`
}

type ghCommit struct {
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
	Commit  struct {
		Message string `json:"message"`
		Author  struct {
			Name string    `json:"name"`
			Date time.Time `json:"date"`
		} `json:"author"`
	} `json:"commit"`
}

func (c *Checker) fetchOnline(ctx context.Context, localeKey string, channelKey string) UpdateCheckResult {
	cur := DisplayTag(Version)
	wantPreview := channelKey == "preview"
	repoWebURL := fmt.Sprintf("https://github.com/%s/%s", c.Owner, c.Repo)
	localMD := EmbeddedChangelogForLocale(localeKey)
	result := UpdateCheckResult{
		Channel:           channelKey,
		CurrentVersion:    cur,
		LatestVersion:     cur,
		HasUpdate:         false,
		ReleaseURL:        repoWebURL + "/releases",
		RepoURL:           repoWebURL,
		GitURL:            repoWebURL + ".git",
		ChangelogLang:     localeCodeFromKey(localeKey),
		ChangelogMarkdown: localMD,
		ChangelogSource:   "embedded",
		Releases:          []ReleaseItem{},
		CheckedAt:         time.Now().Format(time.RFC3339),
	}

	var onlineOK bool
	var notices []string

	// 1. Query GitHub Releases for Willh92/DBHarbor
	releasesURL := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=20", strings.TrimRight(c.APIBaseURL, "/"), c.Owner, c.Repo)
	var rawReleases []ghRelease
	if status, err := c.getJSON(ctx, releasesURL, &rawReleases); err == nil {
		onlineOK = true
		var pickedLatest *ghRelease
		var pickedSemver string
		for i := range rawReleases {
			rel := &rawReleases[i]
			if rel.Draft {
				continue
			}
			assets := make([]ReleaseAsset, 0, len(rel.Assets))
			for _, a := range rel.Assets {
				assets = append(assets, ReleaseAsset{
					Name:        a.Name,
					Size:        a.Size,
					DownloadURL: a.BrowserDownloadURL,
				})
			}
			pubStr := ""
			if !rel.PublishedAt.IsZero() {
				pubStr = rel.PublishedAt.Format(time.RFC3339)
			}
			effVer := effectiveReleaseSemver(rel)
			displayTag := DisplayTag(rel.TagName)
			if effVer != "" && !isSemverLike(rel.TagName) {
				displayTag = DisplayTag(effVer)
			}
			title := strings.TrimSpace(rel.Name)
			if title == "" {
				title = displayTag
			}
			result.Releases = append(result.Releases, ReleaseItem{
				TagName:     displayTag,
				Name:        title,
				Body:        rel.Body,
				HTMLURL:     rel.HTMLURL,
				PublishedAt: pubStr,
				Prerelease:  rel.Prerelease,
				Assets:      assets,
			})

			if !wantPreview && rel.Prerelease {
				continue
			}
			if effVer == "" {
				continue
			}
			if pickedLatest == nil || compareReleaseOrder(pickedLatest, pickedSemver, rel, effVer) < 0 {
				pickedLatest = rel
				pickedSemver = effVer
			}
		}
		if pickedLatest != nil {
			hasUpdate := CompareSemver(Version, pickedSemver) < 0
			if !hasUpdate && wantPreview && isRollingPreviewTag(pickedLatest.TagName) &&
				sameCoreSemver(Version, pickedSemver) && EffectiveChannel() != "preview" {
				hasUpdate = true
			}
			result.LatestVersion = DisplayTag(pickedSemver)
			result.LatestPrerelease = pickedLatest.Prerelease
			result.HasUpdate = hasUpdate
			if pickedLatest.HTMLURL != "" {
				result.ReleaseURL = pickedLatest.HTMLURL
			}
			if !pickedLatest.PublishedAt.IsZero() {
				result.PublishedAt = pickedLatest.PublishedAt.Format(time.RFC3339)
			}
			assetName, assetURL := matchPlatformAsset(pickedLatest, runtime.GOOS, runtime.GOARCH)
			result.MatchedAssetName = assetName
			result.MatchedAssetURL = assetURL
		}
	} else if status == http.StatusNotFound {
		notices = append(notices, "GitHub repository has no published releases yet or is not public; showing built-in changelog")
	} else if err != nil {
		notices = append(notices, "GitHub Releases API request failed: "+err.Error())
	}

	// 2. Fallback to GitHub Tags if no Releases exist yet
	if len(result.Releases) == 0 {
		tagsURL := fmt.Sprintf("%s/repos/%s/%s/tags?per_page=15", strings.TrimRight(c.APIBaseURL, "/"), c.Owner, c.Repo)
		var tags []ghTag
		if _, err := c.getJSON(ctx, tagsURL, &tags); err == nil {
			onlineOK = true
			bestTag := ""
			for _, t := range tags {
				if !isSemverLike(t.Name) {
					continue
				}
				if !wantPreview && strings.Contains(NormalizeTag(t.Name), "-") {
					continue
				}
				if bestTag == "" || CompareSemver(bestTag, t.Name) < 0 {
					bestTag = t.Name
				}
			}
			if bestTag != "" {
				result.LatestVersion = DisplayTag(bestTag)
				result.LatestPrerelease = strings.Contains(NormalizeTag(bestTag), "-")
				result.HasUpdate = CompareSemver(Version, bestTag) < 0
				result.ReleaseURL = fmt.Sprintf("%s/releases/tag/%s", repoWebURL, bestTag)
			}
		}
	}

	// 3. Fetch remote localized CHANGELOG from GitHub HEAD (preserving built-in localized changelog unless remote has matching locale or newer version)
	candidates := changelogCandidatesForLocale(localeKey)
	var fetchedMD string
	var usedFallbackEN bool
	for idx, filename := range candidates {
		rawChangelogURL := fmt.Sprintf("%s/%s/%s/HEAD/%s", strings.TrimRight(c.RawBaseURL, "/"), c.Owner, c.Repo, filename)
		if md, err := c.getText(ctx, rawChangelogURL); err == nil && strings.TrimSpace(md) != "" {
			fetchedMD = md
			usedFallbackEN = idx > 0 && filename == "CHANGELOG.md" && localeKey != "en"
			break
		}
	}
	if fetchedMD != "" {
		onlineOK = true
		parsedVer := parseFirstVersionHeading(fetchedMD)
		if len(result.Releases) == 0 && !result.HasUpdate && parsedVer != "" {
			if CompareSemver(result.LatestVersion, parsedVer) < 0 {
				result.LatestVersion = DisplayTag(parsedVer)
			}
			result.HasUpdate = CompareSemver(Version, result.LatestVersion) < 0
		}
		// Only overwrite localized built-in changelog when GitHub HEAD has a strictly newer version than the local binary
		// (or when embedded changelog is empty), preventing older remote HEAD files from downgrading newer local builds.
		if strings.TrimSpace(result.ChangelogMarkdown) == "" || (parsedVer != "" && CompareSemver(Version, parsedVer) < 0) {
			result.ChangelogMarkdown = fetchedMD
			result.ChangelogSource = "github_changelog"
			if usedFallbackEN {
				result.ChangelogLang = "en-US"
			}
		}
	} else if len(result.Releases) > 0 {
		// Compose markdown from GitHub Releases bodies
		var sb strings.Builder
		for _, r := range result.Releases {
			datePart := ""
			if len(r.PublishedAt) >= 10 {
				datePart = " - " + r.PublishedAt[:10]
			}
			sb.WriteString(fmt.Sprintf("## [%s]%s\n\n%s\n\n---\n\n", r.TagName, datePart, strings.TrimSpace(r.Body)))
		}
		if strings.TrimSpace(sb.String()) != "" {
			result.ChangelogMarkdown = sb.String()
			result.ChangelogSource = "github_releases"
		}
	} else {
		// 4. Fallback to recent GitHub Commits log if repo has commits but no CHANGELOG.md or Releases yet
		commitsURL := fmt.Sprintf("%s/repos/%s/%s/commits?per_page=15", strings.TrimRight(c.APIBaseURL, "/"), c.Owner, c.Repo)
		var commits []ghCommit
		if _, err := c.getJSON(ctx, commitsURL, &commits); err == nil && len(commits) > 0 {
			onlineOK = true
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("# Recent GitHub Commits (%s/%s)\n\n", c.Owner, c.Repo))
			for _, cm := range commits {
				shortSHA := cm.SHA
				if len(shortSHA) > 7 {
					shortSHA = shortSHA[:7]
				}
				firstLine := strings.Split(strings.TrimSpace(cm.Commit.Message), "\n")[0]
				dateStr := ""
				if !cm.Commit.Author.Date.IsZero() {
					dateStr = cm.Commit.Author.Date.Format("2006-01-02")
				}
				sb.WriteString(fmt.Sprintf("- **`%s`** (%s) %s\n", shortSHA, dateStr, firstLine))
			}
			sb.WriteString("\n---\n\n" + localMD)
			result.ChangelogMarkdown = sb.String()
			result.ChangelogSource = "github_commits"
		}
	}

	result.OnlineCheckSuccess = onlineOK
	if len(notices) > 0 && !onlineOK {
		result.Notice = strings.Join(notices, "; ")
	} else if len(result.Releases) == 0 && onlineOK && result.ChangelogSource == "embedded" {
		result.Notice = "Connected to GitHub repository; no new release tags published yet"
	}

	return result
}

func (c *Checker) getJSON(ctx context.Context, url string, target any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "DBHarbor/"+NormalizeTag(Version))

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	return resp.StatusCode, json.Unmarshal(body, target)
}

func (c *Checker) getText(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "DBHarbor/"+NormalizeTag(Version))

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func matchPlatformAsset(rel *ghRelease, goos, goarch string) (string, string) {
	return matchPlatformAssetForEdition(rel, goos, goarch, Edition)
}

func matchPlatformAssetForEdition(rel *ghRelease, goos, goarch, edition string) (string, string) {
	needle1 := strings.ToLower(fmt.Sprintf("%s-%s", goos, goarch))
	needle2 := strings.ToLower(fmt.Sprintf("%s_%s", goos, goarch))
	wantDesktop := strings.EqualFold(strings.TrimSpace(edition), "desktop")

	var fallbackName, fallbackURL string
	for _, a := range rel.Assets {
		lower := strings.ToLower(a.Name)
		if strings.HasSuffix(lower, ".sha256") || strings.HasSuffix(lower, ".txt") || strings.HasSuffix(lower, ".sig") {
			continue
		}
		if !strings.Contains(lower, needle1) && !strings.Contains(lower, needle2) {
			continue
		}
		// Combined bundle packs contain both editions; keep only as a fallback if no edition-specific binary exists.
		if strings.Contains(lower, "bundle") || strings.Contains(lower, "pack") {
			if fallbackName == "" {
				fallbackName, fallbackURL = a.Name, a.BrowserDownloadURL
			}
			continue
		}
		isDesktopAsset := strings.Contains(lower, "desktop") || strings.Contains(lower, ".app.")
		if isDesktopAsset != wantDesktop {
			continue
		}
		// On macOS desktop, prefer the ready-to-launch .app.zip bundle when available
		if wantDesktop && goos == "darwin" && !strings.Contains(lower, ".app.") {
			if fallbackName == "" {
				fallbackName, fallbackURL = a.Name, a.BrowserDownloadURL
			}
			continue
		}
		return a.Name, a.BrowserDownloadURL
	}
	return fallbackName, fallbackURL
}

func isSemverLike(tag string) bool {
	clean := NormalizeTag(tag)
	return len(clean) >= 3 && clean[0] >= '0' && clean[0] <= '9' && strings.Contains(clean, ".")
}

// effectiveReleaseSemver extracts the semantic version of a GitHub Release, supporting both
// versioned tags ("v1.0.0", "v1.1.0-preview.1") and rolling preview tags ("preview" with version in title).
func effectiveReleaseSemver(rel *ghRelease) string {
	if isSemverLike(rel.TagName) {
		return NormalizeTag(rel.TagName)
	}
	for _, field := range []string{rel.Name, rel.Body} {
		for _, rawTok := range strings.Fields(field) {
			tok := strings.Trim(rawTok, "()[]*`'\",;:")
			if isSemverLike(tok) {
				return NormalizeTag(tok)
			}
		}
	}
	return ""
}

func isRollingPreviewTag(tag string) bool {
	t := strings.ToLower(strings.TrimSpace(tag))
	return t == "preview" || t == "nightly"
}

func sameCoreSemver(a, b string) bool {
	ac, _ := splitPrerelease(NormalizeTag(a))
	bc, _ := splitPrerelease(NormalizeTag(b))
	return CompareSemver(ac, bc) == 0
}

// compareReleaseOrder orders two releases consistently across both versioned tags and rolling preview tags.
func compareReleaseOrder(a *ghRelease, aVer string, b *ghRelease, bVer string) int {
	aCore, _ := splitPrerelease(NormalizeTag(aVer))
	bCore, _ := splitPrerelease(NormalizeTag(bVer))
	if coreCmp := CompareSemver(aCore, bCore); coreCmp != 0 {
		return coreCmp
	}
	if isRollingPreviewTag(a.TagName) != isRollingPreviewTag(b.TagName) &&
		!a.PublishedAt.IsZero() && !b.PublishedAt.IsZero() {
		if a.PublishedAt.Before(b.PublishedAt) {
			return -1
		}
		if a.PublishedAt.After(b.PublishedAt) {
			return 1
		}
	}
	return CompareSemver(aVer, bVer)
}

func parseFirstVersionHeading(md string) string {
	for _, line := range strings.Split(md, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "## ") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "## "))
		rest = strings.TrimPrefix(rest, "[")
		if idx := strings.IndexAny(rest, "] -"); idx > 0 {
			rest = rest[:idx]
		}
		clean := NormalizeTag(rest)
		if clean != "" && clean[0] >= '0' && clean[0] <= '9' {
			return clean
		}
	}
	return ""
}

// CompareSemver compares two semantic version strings (e.g. "v1.2.0" vs "1.10.0-beta.1"):
// returns -1 if a < b, 0 if a == b, +1 if a > b.
func CompareSemver(a, b string) int {
	aCore, aPre := splitPrerelease(NormalizeTag(a))
	bCore, bPre := splitPrerelease(NormalizeTag(b))

	aParts := parseNumericParts(aCore)
	bParts := parseNumericParts(bCore)
	maxLen := len(aParts)
	if len(bParts) > maxLen {
		maxLen = len(bParts)
	}
	for i := 0; i < maxLen; i++ {
		av, bv := 0, 0
		if i < len(aParts) {
			av = aParts[i]
		}
		if i < len(bParts) {
			bv = bParts[i]
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}

	// Stable release > pre-release for identical core version (1.2.0 > 1.2.0-rc.1)
	if aPre == "" && bPre != "" {
		return 1
	}
	if aPre != "" && bPre == "" {
		return -1
	}
	if aPre < bPre {
		return -1
	}
	if aPre > bPre {
		return 1
	}
	return 0
}

func splitPrerelease(v string) (string, string) {
	if plus := strings.IndexByte(v, '+'); plus >= 0 {
		v = v[:plus]
	}
	if dash := strings.IndexByte(v, '-'); dash >= 0 {
		return v[:dash], v[dash+1:]
	}
	return v, ""
}

func parseNumericParts(core string) []int {
	segs := strings.Split(core, ".")
	out := make([]int, 0, len(segs))
	for _, s := range segs {
		n, _ := strconv.Atoi(strings.TrimSpace(s))
		out = append(out, n)
	}
	return out
}
