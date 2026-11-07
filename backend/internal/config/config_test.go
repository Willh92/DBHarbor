package config

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLoadUnixSocketFlagsAndShorthand(t *testing.T) {
	tmpData := t.TempDir()

	cfg, err := Load([]string{
		"-unix-socket", "/run/dbharbor/dbharbor.sock",
		"-unix-socket-perm", "0666",
		"-unix-socket-group", "www-data",
		"-use-os-data-dir=false",
		"-data-dir", tmpData,
	})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.UnixSocket != "/run/dbharbor/dbharbor.sock" {
		t.Fatalf("unexpected UnixSocket: %s", cfg.UnixSocket)
	}
	if cfg.UnixSocketPerm != 0o666 {
		t.Fatalf("expected perm 0666, got %04o", cfg.UnixSocketPerm)
	}
	if cfg.UnixSocketGroup != "www-data" {
		t.Fatalf("expected UnixSocketGroup www-data, got %q", cfg.UnixSocketGroup)
	}

	t.Setenv("DBHARBOR_UNIX_SOCKET_GROUP", "nginx")
	cfg2, err := Load([]string{
		"-host", "unix:/var/run/dbharbor.sock",
		"-data-dir", tmpData,
	})
	if err != nil {
		t.Fatalf("Load shorthand failed: %v", err)
	}
	if cfg2.UnixSocket != "/var/run/dbharbor.sock" {
		t.Fatalf("expected /var/run/dbharbor.sock, got %s", cfg2.UnixSocket)
	}
	if cfg2.UnixSocketPerm != 0o660 {
		t.Fatalf("expected default perm 0660, got %04o", cfg2.UnixSocketPerm)
	}
	if cfg2.UnixSocketGroup != "nginx" {
		t.Fatalf("expected env UnixSocketGroup nginx, got %q", cfg2.UnixSocketGroup)
	}
}

func TestDefaultDataDirNeverUsesWorkingDirectory(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	cwdData := filepath.Clean(filepath.Join(cwd, "data"))
	parentData := filepath.Clean(filepath.Join(filepath.Dir(cwd), "data"))

	dd := DefaultDataDir()
	if strings.TrimSpace(dd) == "" {
		t.Fatal("DefaultDataDir returned empty string")
	}
	cleanDD := filepath.Clean(dd)
	if cleanDD == cwdData || cleanDD == parentData || cleanDD == "data" || cleanDD == "./data" {
		t.Fatalf("DefaultDataDir must not point to working directory data folder, got %q (cwd=%q)", dd, cwd)
	}

	if !isWindowsSystemAccount("SYSTEM", `C:\Windows\System32\config\systemprofile`) {
		t.Fatal("expected SYSTEM to be detected as Windows service account")
	}
	if !isWindowsSystemAccount("NETWORK SERVICE", `C:\Windows\ServiceProfiles\NetworkService`) {
		t.Fatal("expected NETWORK SERVICE to be detected as Windows service account")
	}
	if !isWindowsSystemAccount("SERVER01$", `C:\Users\TEMP`) {
		t.Fatal("expected machine account ending with $ to be detected as Windows service account")
	}
	if isWindowsSystemAccount("alice", `C:\Users\alice`) {
		t.Fatal("did not expect regular user account to be detected as Windows service account")
	}

	if runtime.GOOS == "windows" {
		tmpLocal := t.TempDir()
		t.Setenv("DBHARBOR_DATA_DIR", "")
		t.Setenv("USERNAME", "regularuser")
		t.Setenv("USERPROFILE", filepath.Join(tmpLocal, "Users", "regularuser"))
		t.Setenv("LOCALAPPDATA", tmpLocal)

		loaded, err := Load([]string{})
		if err != nil {
			t.Fatalf("Load with OS default data dir failed: %v", err)
		}
		want := filepath.Join(tmpLocal, "DBHarbor", "data")
		if filepath.Clean(loaded.DataDir) != filepath.Clean(want) {
			t.Fatalf("expected DataDir %q, got %q", want, loaded.DataDir)
		}
	}
}

func TestPrepareListenerUnixDomainSocketHTTP(t *testing.T) {
	// Keep path well below ~104-byte sun_path limit across Linux/macOS/Windows
	sockPath := filepath.Join(os.TempDir(), fmt.Sprintf("dbh_uds_%d.sock", time.Now().UnixNano()%1_000_000_000))
	defer os.Remove(sockPath)

	cfg := &Config{
		UnixSocket:     sockPath,
		UnixSocketPerm: 0o660,
	}

	ln, cleanup, display, err := PrepareListener(cfg)
	if err != nil {
		t.Fatalf("PrepareListener unix failed: %v", err)
	}
	if !strings.HasPrefix(display, "unix:") {
		t.Fatalf("expected unix: display prefix, got %s", display)
	}

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("uds-ok:" + r.Header.Get("X-Real-IP")))
		}),
	}
	go func() {
		_ = srv.Serve(ln)
	}()

	// Verify active-socket collision guard rejects a second concurrent bind
	if _, _, _, dupErr := PrepareListener(cfg); dupErr == nil {
		t.Fatalf("expected second PrepareListener on active unix socket to fail")
	}

	// Send HTTP GET over Unix Domain Socket (simulating Nginx reverse proxy)
	udsClient := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sockPath)
			},
		},
	}
	req, _ := http.NewRequest(http.MethodGet, "http://unix/api/ping", nil)
	req.Header.Set("X-Real-IP", "203.0.113.42")
	resp, err := udsClient.Do(req)
	if err != nil {
		_ = srv.Close()
		cleanup()
		t.Fatalf("HTTP request over unix socket failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "uds-ok:203.0.113.42" {
		_ = srv.Close()
		cleanup()
		t.Fatalf("unexpected response over unix socket: status=%d body=%q", resp.StatusCode, string(body))
	}

	_ = srv.Close()
	cleanup()

	if _, statErr := os.Lstat(sockPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected cleanup() to remove unix socket file %s", sockPath)
	}
}

func TestLoadAdminBootstrapAndInitOnlyFlags(t *testing.T) {
	tmpData := t.TempDir()
	t.Setenv("DBHARBOR_ADMIN_USER", "envadmin")
	t.Setenv("DBHARBOR_ADMIN_PASS", "envpass123")

	cfgEnv, err := Load([]string{"-data-dir", tmpData, "-init-only"})
	if err != nil {
		t.Fatalf("Load env bootstrap failed: %v", err)
	}
	if !cfgEnv.InitOnly {
		t.Fatal("expected InitOnly to be true")
	}
	if cfgEnv.AdminUser != "envadmin" || cfgEnv.AdminPass != "envpass123" {
		t.Fatalf("unexpected env admin credentials: user=%q pass=%q", cfgEnv.AdminUser, cfgEnv.AdminPass)
	}

	cfgFlag, err := Load([]string{
		"-data-dir", tmpData,
		"-admin-user", "ops_root",
		"-admin-pass", "Strong#Pass99",
	})
	if err != nil {
		t.Fatalf("Load CLI bootstrap failed: %v", err)
	}
	if cfgFlag.InitOnly {
		t.Fatal("expected InitOnly to default to false")
	}
	if cfgFlag.AdminUser != "ops_root" || cfgFlag.AdminPass != "Strong#Pass99" {
		t.Fatalf("unexpected CLI admin credentials: user=%q pass=%q", cfgFlag.AdminUser, cfgFlag.AdminPass)
	}

	t.Setenv("DBHARBOR_ADMIN_USER", "")
	t.Setenv("DBHARBOR_ADMIN_PASS", "")
	cfgDeferred, err := Load([]string{"-data-dir", tmpData})
	if err != nil {
		t.Fatalf("Load deferred bootstrap failed: %v", err)
	}
	if cfgDeferred.AdminUser != "" || cfgDeferred.AdminPass != "" {
		t.Fatalf("expected empty default admin credentials for Web setup wizard, got user=%q pass=%q", cfgDeferred.AdminUser, cfgDeferred.AdminPass)
	}
}
