package desktop

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// WindowConfig holds parameters for launching the native DBHarbor desktop window.
type WindowConfig struct {
	Title    string
	BootURL  string
	DataDir  string
	Width    int
	Height   int
	DarkMode bool
}

type desktopUIPrefs struct {
	DarkMode bool `json:"darkMode"`
}

// LoadDarkPreference reads persisted window theme preference from dataDir/ui_prefs.json.
func LoadDarkPreference(dataDir string) bool {
	if dataDir == "" {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, "ui_prefs.json"))
	if err != nil {
		return false
	}
	var prefs desktopUIPrefs
	if err := json.Unmarshal(raw, &prefs); err != nil {
		return false
	}
	return prefs.DarkMode
}

// SaveDarkPreference persists window theme preference to dataDir/ui_prefs.json.
func SaveDarkPreference(dataDir string, dark bool) {
	if dataDir == "" {
		return
	}
	_ = os.MkdirAll(dataDir, 0o700)
	raw, err := json.Marshal(desktopUIPrefs{DarkMode: dark})
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dataDir, "ui_prefs.json"), raw, 0o600)
}

func (c *WindowConfig) withDefaults() WindowConfig {
	out := *c
	if out.Title == "" {
		out.Title = "DBHarbor"
	}
	if out.Width <= 0 {
		out.Width = 1440
	}
	if out.Height <= 0 {
		out.Height = 900
	}
	return out
}

// runChromelessAppWindowFallback launches an isolated chromeless browser app window (--app=...)
// and blocks until the user closes that window. Used only when native OS WebView runtime is unavailable.
func runChromelessAppWindowFallback(cfg WindowConfig) error {
	cfg = cfg.withDefaults()
	profileDir := filepath.Join(filepath.Dir(cfg.DataDir), "window-profile")
	_ = os.MkdirAll(profileDir, 0o700)

	candidates := findChromiumExecutables()
	for _, bin := range candidates {
		args := []string{
			"--app=" + cfg.BootURL,
			fmt.Sprintf("--window-size=%d,%d", cfg.Width, cfg.Height),
			"--user-data-dir=" + profileDir,
			"--no-first-run",
			"--no-default-browser-check",
			"--disable-features=TranslateUI,CalculateNativeWinOcclusion",
			"--disable-renderer-backgrounding",
			"--disable-backgrounding-occluded-windows",
		}
		cmd := exec.Command(bin, args...)
		if err := cmd.Start(); err == nil {
			_ = cmd.Wait()
			return nil
		}
	}

	// Final fallback: open system default browser and keep local engine running until interrupted
	if err := openSystemDefaultURL(cfg.BootURL); err != nil {
		return fmt.Errorf("no desktop WebView runtime or browser found: %w", err)
	}
	// Keep engine alive while user interacts in fallback browser
	for {
		time.Sleep(time.Hour)
	}
}

func findChromiumExecutables() []string {
	switch runtime.GOOS {
	case "windows":
		var paths []string
		roots := []string{
			os.Getenv("ProgramFiles(x86)"),
			os.Getenv("ProgramFiles"),
			os.Getenv("LOCALAPPDATA"),
		}
		rels := []string{
			`Microsoft\Edge\Application\msedge.exe`,
			`Google\Chrome\Application\chrome.exe`,
			`BraveSoftware\Brave-Browser\Application\brave.exe`,
		}
		for _, r := range roots {
			if r == "" {
				continue
			}
			for _, rel := range rels {
				p := filepath.Join(r, rel)
				if _, err := os.Stat(p); err == nil {
					paths = append(paths, p)
				}
			}
		}
		return paths

	case "darwin":
		candidates := []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
		var found []string
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				found = append(found, c)
			}
		}
		return found

	default:
		names := []string{
			"google-chrome",
			"google-chrome-stable",
			"chromium",
			"chromium-browser",
			"microsoft-edge",
			"microsoft-edge-stable",
			"brave-browser",
		}
		var found []string
		for _, n := range names {
			if p, err := exec.LookPath(n); err == nil {
				found = append(found, p)
			}
		}
		return found
	}
}

func openSystemDefaultURL(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
