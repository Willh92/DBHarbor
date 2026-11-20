package desktop

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDarkPreferenceRoundTrip(t *testing.T) {
	if LoadDarkPreference("") {
		t.Fatal("expected empty dataDir LoadDarkPreference to return false")
	}
	SaveDarkPreference("", true) // must not panic

	dir := t.TempDir()
	if LoadDarkPreference(dir) {
		t.Fatal("expected missing ui_prefs.json to return false")
	}
	if err := os.WriteFile(filepath.Join(dir, "ui_prefs.json"), []byte("{invalid-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if LoadDarkPreference(dir) {
		t.Fatal("expected malformed ui_prefs.json to return false")
	}

	SaveDarkPreference(dir, true)
	if !LoadDarkPreference(dir) {
		t.Fatal("expected LoadDarkPreference to return true after SaveDarkPreference(true)")
	}
	SaveDarkPreference(dir, false)
	if LoadDarkPreference(dir) {
		t.Fatal("expected LoadDarkPreference to return false after SaveDarkPreference(false)")
	}
}

func TestWindowConfigWithDefaults(t *testing.T) {
	zero := (&WindowConfig{}).withDefaults()
	if zero.Title != "DBHarbor" || zero.Width != 1440 || zero.Height != 900 {
		t.Fatalf("unexpected default WindowConfig: %+v", zero)
	}
	custom := (&WindowConfig{Title: "Custom", Width: 1200, Height: 800, DarkMode: true}).withDefaults()
	if custom.Title != "Custom" || custom.Width != 1200 || custom.Height != 800 || !custom.DarkMode {
		t.Fatalf("expected explicit WindowConfig values preserved: %+v", custom)
	}
}

func TestFindChromiumExecutables(t *testing.T) {
	paths := findChromiumExecutables()
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected returned Chromium path %q to exist: %v", p, err)
		}
	}
}

func TestWaitForShutdownSignalCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		waitForShutdownSignal(ctx)
		close(done)
	}()

	time.Sleep(15 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("waitForShutdownSignal did not return promptly on context cancellation")
	}
}
