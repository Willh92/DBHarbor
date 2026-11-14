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
	"strconv"
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

	if cfg.RecoverAdminCLI || cfg.Recovery.Enabled {
		trigger := "config.json"
		if cfg.RecoverAdminCLI {
			trigger = "cli (-recover-admin)"
		}
		recRes, recErr := st.ExecuteAdminRecovery(store.AdminRecoveryRequest{
			AdminUsername: cfg.Recovery.AdminUsername,
			NewPassword:   cfg.Recovery.NewPassword,
			Reset2FA:      cfg.Recovery.Reset2FA,
			Source:        trigger,
		})
		if recErr != nil {
			log.Fatalf("[DBHarbor] Emergency admin recovery failed: %v", recErr)
		}
		if !cfg.RecoverAdminCLI {
			if dErr := config.DisarmRecoveryConfigFile(cfg.ConfigFile); dErr != nil {
				log.Printf("[DBHarbor] WARNING: failed to disarm recovery block in %s: %v", cfg.ConfigFile, dErr)
			}
		}
		_, _ = os.Stdout.WriteString("\n============================================================\n")
		_, _ = os.Stdout.WriteString("  DBHarbor Emergency Admin Recovery Completed\n")
		_, _ = os.Stdout.WriteString("  Trigger         : " + trigger + "\n")
		_, _ = os.Stdout.WriteString("  Admin Username  : " + recRes.Username + " (created=" + strconv.FormatBool(recRes.CreatedNew) + ")\n")
		if recRes.IssuedBootstrapCode != "" {
			_, _ = os.Stdout.WriteString("  Bootstrap Code  : " + recRes.IssuedBootstrapCode + "\n")
			_, _ = os.Stdout.WriteString("  Notice          : Save this one-time code now and change it after sign-in!\n")
		} else {
			_, _ = os.Stdout.WriteString("  Credential State: Updated from recovery input\n")
		}
		_, _ = os.Stdout.WriteString("  2FA Reset       : " + strconv.FormatBool(recRes.Reset2FA) + "\n")
		_, _ = os.Stdout.WriteString("  Revoked Sessions: " + strconv.Itoa(recRes.RevokedSessions) + "\n")
		_, _ = os.Stdout.WriteString("  DB Backup       : " + recRes.BackupPath + "\n")
		_, _ = os.Stdout.WriteString("============================================================\n\n")
		if cfg.RecoverAdminCLI {
			return
		}
	}

	if cfg.InitOnly {
		if st.NeedsSetup() {
			fmt.Printf("[DBHarbor] Initialized vault and metadata database at %s (first-run Web administrator setup pending)\n", cfg.DataDir)
		} else {
			fmt.Printf("[DBHarbor] Initialized data directory and administrator store at %s\n", cfg.DataDir)
		}
		return
	}

	pm := pool.NewManager(st, cfg.IdleConnTTL)
	defer pm.CloseAll()

	tm := auth.NewTokenManager(vault.DeriveBytes("jwt-signing-v1"))
	router := api.NewRouterWithOptions(st, pm, tm, api.RouterOptions{
		ProtectorSecret:            vault.DeriveBytes("login-protector-hmac-v1"),
		MaxImportMB:                cfg.MaxImportMB,
		TrustedProxies:             cfg.TrustedProxies,
		CORSOrigins:                cfg.CORSOrigins,
		AllowPrivateNetForNonAdmin: cfg.Security.AllowPrivateNetworkForNonAdmin,
		DuckDBCliPath:              cfg.Security.DuckDBCliPath,
	})
	defer router.ClearAllSessionCreds()

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
	router.ClearAllSessionCreds()
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
	if err := cmd.Start(); err == nil {
		go func() { _ = cmd.Wait() }()
	}
}
