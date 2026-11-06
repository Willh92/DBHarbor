package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dbharbor/backend/internal/api"
	"dbharbor/backend/internal/config"
	"dbharbor/backend/internal/crypto"
	"dbharbor/backend/internal/desktop"
	_ "dbharbor/backend/internal/driver/all"
	"dbharbor/backend/internal/pool"
	"dbharbor/backend/internal/store"
	"dbharbor/backend/internal/version"
)

func init() {
	version.Edition = "desktop"
}

func randomSecretHex(numBytes int) (string, error) {
	buf := make([]byte, numBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func main() {
	version.Edition = "desktop"

	fs := flag.NewFlagSet("dbharbor-desktop", flag.ContinueOnError)
	var (
		dataDir     string
		idleConnTTL int
		showVersion bool
	)
	fs.StringVar(&dataDir, "data-dir", os.Getenv("DBHARBOR_DATA_DIR"), "Directory for local encrypted connection store")
	fs.IntVar(&idleConnTTL, "idle-ttl", 30, "Idle database connection eviction timeout in minutes")
	fs.BoolVar(&showVersion, "version", false, "Print DBHarbor Desktop version and exit")

	// Ignore unknown OS launcher arguments (e.g. macOS -psn_0_... Finder Serial Number)
	filteredArgs := make([]string, 0, len(os.Args)-1)
	for _, a := range os.Args[1:] {
		if strings.HasPrefix(a, "-psn_") {
			continue
		}
		filteredArgs = append(filteredArgs, a)
	}
	if err := fs.Parse(filteredArgs); err != nil {
		desktop.ShowErrorDialog("DBHarbor Startup Error", err.Error())
		os.Exit(1)
	}

	if showVersion {
		info := version.Info()
		fmt.Printf("DBHarbor Desktop %s (commit=%s, %s/%s, %s, license=%s)\nRepository: %s\n",
			info.DisplayVersion, info.GitCommit, info.OS, info.Arch, info.GoVersion, info.License, info.GitURL)
		return
	}

	if strings.TrimSpace(dataDir) == "" {
		dataDir = config.DefaultDataDir()
	}
	if absDir, err := filepath.Abs(dataDir); err == nil {
		dataDir = absDir
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		desktop.ShowErrorDialog("DBHarbor Data Directory Error", fmt.Sprintf("Failed to prepare application data directory (%s):\n%v", dataDir, err))
		os.Exit(1)
	}

	vault, err := crypto.NewVaultFromDataDir(dataDir, os.Getenv("DBHARBOR_SECRET_KEY"))
	if err != nil {
		desktop.ShowErrorDialog("DBHarbor Vault Error", fmt.Sprintf("Failed to initialize local encryption vault:\n%v", err))
		os.Exit(1)
	}

	st, err := store.OpenLocal(dataDir, vault)
	if err != nil {
		desktop.ShowErrorDialog("DBHarbor Storage Error", fmt.Sprintf("Failed to open local metadata store:\n%v", err))
		os.Exit(1)
	}
	defer st.Close()

	pm := pool.NewManager(st, idleConnTTL)
	defer pm.CloseAll()

	bootTicket, err := randomSecretHex(32)
	if err != nil {
		desktop.ShowErrorDialog("DBHarbor Security Error", err.Error())
		os.Exit(1)
	}
	// Deterministic vault-derived local session secret so multi-window or restarted desktop instances
	// sharing 127.0.0.1 WebView2 cookies never invalidate each other's session.
	cookieSecret := hex.EncodeToString(vault.DeriveBytes("desktop-local-session-v1"))

	// Bind ephemeral loopback listener (127.0.0.1:0) first so we can scope cookies to the exact port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		desktop.ShowErrorDialog("DBHarbor Local Engine Error", fmt.Sprintf("Failed to bind local loopback engine:\n%v", err))
		os.Exit(1)
	}
	tcpAddr := ln.Addr().(*net.TCPAddr)
	darkPref := desktop.LoadDarkPreference(dataDir)

	router := api.NewDesktopRouter(st, pm, api.DesktopConfig{
		Enabled:             true,
		ListenPort:          tcpAddr.Port,
		DataDir:             dataDir,
		DarkMode:            darkPref,
		BootTicket:          bootTicket,
		SessionCookieSecret: cookieSecret,
		SaveFileDialog:      desktop.SaveFileDialog,
		OpenFileDialog:      desktop.OpenFileDialog,
		SetWindowDarkMode: func(dark bool) {
			desktop.SaveDarkPreference(dataDir, dark)
			desktop.SetWindowDarkMode(dark)
		},
	})

	srv := &http.Server{
		Handler:           router,
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		_ = srv.Serve(ln)
	}()

	bootURL := fmt.Sprintf("http://127.0.0.1:%d/__desktop_boot?ticket=%s", tcpAddr.Port, bootTicket)
	winTitle := fmt.Sprintf("DBHarbor %s", version.DisplayTag(version.Version))

	if err := desktop.RunWindow(desktop.WindowConfig{
		Title:    winTitle,
		BootURL:  bootURL,
		DataDir:  dataDir,
		Width:    1440,
		Height:   900,
		DarkMode: darkPref,
	}); err != nil {
		desktop.ShowErrorDialog("DBHarbor Window Error", err.Error())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
