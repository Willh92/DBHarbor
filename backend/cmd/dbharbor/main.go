package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"dbharbor/backend/internal/api"
	"dbharbor/backend/internal/auth"
	"dbharbor/backend/internal/config"
	"dbharbor/backend/internal/crypto"
	"dbharbor/backend/internal/driver"
	_ "dbharbor/backend/internal/driver/all"
	"dbharbor/backend/internal/pool"
	"dbharbor/backend/internal/store"
	"dbharbor/backend/internal/version"
)

func main() {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		log.Fatalf("[DBHarbor] Failed to parse configuration: %v", err)
	}
	if cfg.ShowVersion {
		info := version.Info()
		fmt.Printf("DBHarbor %s (commit=%s, %s/%s, %s, license=%s)\nRepository: %s\n",
			info.DisplayVersion, info.GitCommit, info.OS, info.Arch, info.GoVersion, info.License, info.GitURL)
		return
	}

	vault, err := crypto.NewVaultFromDataDir(cfg.DataDir, cfg.SecretKeyHex)
	if err != nil {
		log.Fatalf("[DBHarbor] Failed to initialize encryption vault: %v", err)
	}

	st, err := store.Open(cfg.DataDir, vault, cfg.AdminUser, cfg.AdminPass)
	if err != nil {
		log.Fatalf("[DBHarbor] Failed to initialize metadata store: %v", err)
	}
	defer st.Close()

	if cfg.InitOnly {
		fmt.Printf("[DBHarbor] Initialized data directory and administrator store at %s\n", cfg.DataDir)
		return
	}

	pm := pool.NewManager(st, cfg.IdleConnTTL)
	defer pm.CloseAll()

	tm := auth.NewTokenManager(vault.DeriveBytes("jwt-signing-v1"))
	router := api.NewRouter(st, pm, tm)

	ln, cleanupListener, endpointDisplay, err := config.PrepareListener(cfg)
	if err != nil {
		log.Fatalf("[DBHarbor] Failed to bind listener: %v", err)
	}
	defer cleanupListener()

	srv := &http.Server{
		Handler:           router,
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		fmt.Printf("\n============================================================\n")
		fmt.Printf("  DBHarbor %s - Cross-Platform Multi-DB Web Workbench\n", version.DisplayTag(version.Version))
		fmt.Printf("  OS / Arch  : %s / %s\n", runtime.GOOS, runtime.GOARCH)
		fmt.Printf("  Drivers    : %d registered engines\n", len(driver.ListMetas()))
		fmt.Printf("  Data Dir   : %s\n", cfg.DataDir)
		fmt.Printf("  GitHub     : %s\n", version.GitHubGitURL)
		fmt.Printf("  Listen     : %s\n", endpointDisplay)
		fmt.Printf("============================================================\n\n")

		if cfg.OpenBrowser && cfg.UnixSocket == "" {
			openBrowserCrossPlatform(fmt.Sprintf("http://localhost:%d", cfg.Port))
		}
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[DBHarbor] HTTP server terminated unexpectedly: %v", err)
		}
	}()

	quitCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-quitCtx.Done()

	log.Println("[DBHarbor] Gracefully shutting down server and database connection pools...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func openBrowserCrossPlatform(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
