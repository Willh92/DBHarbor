package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dbharbor/backend/internal/auth"
	"dbharbor/backend/internal/crypto"
	"dbharbor/backend/internal/driver"
	_ "dbharbor/backend/internal/driver/all"
	"dbharbor/backend/internal/driver/sqlbase"
	"dbharbor/backend/internal/pool"
	"dbharbor/backend/internal/store"
)

func TestV104SecurityAndConcurrencyRegressions(t *testing.T) {
	t.Run("sameEndpointAndSecurity blocks TLS downgrades while permitting TLS upgrades", func(t *testing.T) {
		pgStrict := driver.ConnectionConfig{
			Type:    "postgres",
			Host:    "db.example.com",
			Port:    5432,
			Options: map[string]string{"sslmode": "verify-full"},
		}
		pgDowngraded := driver.ConnectionConfig{
			Type:    "postgres",
			Host:    "db.example.com",
			Port:    5432,
			Options: map[string]string{"sslmode": "require"},
		}
		if sameEndpointAndSecurity(pgStrict, pgDowngraded) {
			t.Fatal("expected PostgreSQL verify-full -> require downgrade to be rejected")
		}
		pgPrefer := driver.ConnectionConfig{
			Type:    "postgres",
			Host:    "db.example.com",
			Port:    5432,
			Options: map[string]string{"sslmode": "prefer"},
		}
		if !sameEndpointAndSecurity(pgPrefer, pgStrict) {
			t.Fatal("expected PostgreSQL prefer -> verify-full upgrade to be allowed")
		}

		myStrict := driver.ConnectionConfig{
			Type:    "mysql",
			Host:    "db.example.com",
			Port:    3306,
			Options: map[string]string{"tls": "true"},
		}
		mySkip := driver.ConnectionConfig{
			Type:    "mysql",
			Host:    "db.example.com",
			Port:    3306,
			Options: map[string]string{"tls": "skip-verify"},
		}
		if sameEndpointAndSecurity(myStrict, mySkip) {
			t.Fatal("expected MySQL tls=true -> skip-verify downgrade to be rejected")
		}

		msVerified := driver.ConnectionConfig{
			Type:    "sqlserver",
			Host:    "db.example.com",
			Port:    1433,
			Options: map[string]string{"encrypt": "verified"},
		}
		msTrustCert := driver.ConnectionConfig{
			Type:    "sqlserver",
			Host:    "db.example.com",
			Port:    1433,
			Options: map[string]string{"encrypt": "true"},
		}
		if sameEndpointAndSecurity(msVerified, msTrustCert) {
			t.Fatal("expected SQL Server encrypt=verified -> encrypt=true (TrustServerCertificate) downgrade to be rejected")
		}

		uriVerified := driver.ConnectionConfig{
			Type: "mongodb",
			URI:  "mongodb+srv://admin:secret@cluster0.example.net/prod",
		}
		uriInsecure := driver.ConnectionConfig{
			Type: "mongodb",
			URI:  "mongodb+srv://admin:secret@cluster0.example.net/prod?tlsAllowInvalidCertificates=true",
		}
		if sameEndpointAndSecurity(uriVerified, uriInsecure) {
			t.Fatal("expected MongoDB SRV tlsAllowInvalidCertificates downgrade to be rejected")
		}
	})

	t.Run("LoginProtector RecordSuccess only subtracts that user's IP failures", func(t *testing.T) {
		lp := auth.NewLoginProtector([]byte("v104-test-secret-key-32-bytes-ok"))
		ip := "203.0.113.77"
		for i := 0; i < 4; i++ {
			lp.RecordFailure(ip, "attacker_target")
		}
		for i := 0; i < 2; i++ {
			lp.RecordFailure(ip, "own_acct")
		}
		// Signing into own_acct must subtract only own_acct's 2 failures, leaving 4 IP failures (>= captchaThreshold=3).
		lp.RecordSuccess(ip, "own_acct")
		chk := lp.Check(ip, "victim2")
		if !chk.RequiresCaptcha {
			t.Fatalf("expected IP failure tally to remain >= captchaThreshold after unrelated login success, got %+v", chk)
		}
	})

	t.Run("NewVaultFromDataDir refuses to overwrite corrupted .master.key", func(t *testing.T) {
		dir := t.TempDir()
		keyPath := filepath.Join(dir, ".master.key")
		corruptBytes := []byte("0123456789")
		if err := os.WriteFile(keyPath, corruptBytes, 0o600); err != nil {
			t.Fatalf("write corrupt key: %v", err)
		}
		if _, err := crypto.NewVaultFromDataDir(dir, ""); err == nil {
			t.Fatal("expected error on truncated 10-byte .master.key, got nil")
		}
		after, err := os.ReadFile(keyPath)
		if err != nil {
			t.Fatalf("read key after error: %v", err)
		}
		if !bytes.Equal(after, corruptBytes) {
			t.Fatalf("expected corrupted .master.key to remain untouched (%d bytes), got %d bytes", len(corruptBytes), len(after))
		}
	})

	t.Run("Store Verify2FA enforces atomic single-use CAS under concurrency", func(t *testing.T) {
		dir := t.TempDir()
		vault, err := crypto.NewVaultFromDataDir(dir, "")
		if err != nil {
			t.Fatalf("vault init: %v", err)
		}
		st, err := store.Open(dir, vault, "admin", "Admin#Pass123!")
		if err != nil {
			t.Fatalf("store open: %v", err)
		}
		defer st.Close()

		user, err := st.Authenticate("admin", "Admin#Pass123!")
		if err != nil {
			t.Fatalf("auth admin: %v", err)
		}

		// Stay clear of 30s TOTP boundary so setup & concurrent replay share the same window.
		if time.Now().Unix()%30 >= 28 {
			time.Sleep(2200 * time.Millisecond)
		}
		secret, _, _, err := st.BeginTOTPSetup(user.ID)
		if err != nil {
			t.Fatalf("BeginTOTPSetup: %v", err)
		}
		now := time.Now()
		setupCode, _, err := auth.GenerateTOTPCodeAt(secret, now)
		if err != nil {
			t.Fatalf("GenerateTOTPCodeAt setup: %v", err)
		}
		recoveryCodes, err := st.ConfirmTOTPSetup(user.ID, setupCode)
		if err != nil {
			t.Fatalf("ConfirmTOTPSetup: %v", err)
		}
		if len(recoveryCodes) == 0 {
			t.Fatal("expected backup recovery codes")
		}

		// 1) Concurrent TOTP replay on next valid window step (+1)
		nextStepCode, _, err := auth.GenerateTOTPCodeAt(secret, now.Add(30*time.Second))
		if err != nil {
			t.Fatalf("GenerateTOTPCodeAt +30s: %v", err)
		}
		const workers = 8
		var totpOK int32
		var startGate sync.WaitGroup
		var doneGate sync.WaitGroup
		startGate.Add(1)
		for i := 0; i < workers; i++ {
			doneGate.Add(1)
			go func() {
				defer doneGate.Done()
				startGate.Wait()
				if _, vErr := st.Verify2FA(user.ID, nextStepCode); vErr == nil {
					atomic.AddInt32(&totpOK, 1)
				}
			}()
		}
		startGate.Done()
		doneGate.Wait()
		if got := atomic.LoadInt32(&totpOK); got != 1 {
			t.Fatalf("expected exactly 1 concurrent TOTP verification to succeed, got %d", got)
		}

		// 2) Concurrent backup recovery code replay
		var recOK int32
		startGate.Add(1)
		for i := 0; i < workers; i++ {
			doneGate.Add(1)
			go func() {
				defer doneGate.Done()
				startGate.Wait()
				if usedRec, vErr := st.Verify2FA(user.ID, recoveryCodes[0]); vErr == nil && usedRec {
					atomic.AddInt32(&recOK, 1)
				}
			}()
		}
		startGate.Done()
		doneGate.Wait()
		if got := atomic.LoadInt32(&recOK); got != 1 {
			t.Fatalf("expected exactly 1 concurrent recovery-code verification to succeed, got %d", got)
		}
	})

	t.Run("Desktop HTTP router blocks unauthenticated index token leak and cross-origin cookie CSRF", func(t *testing.T) {
		dir := t.TempDir()
		vault, err := crypto.NewVaultFromDataDir(dir, "")
		if err != nil {
			t.Fatalf("vault init: %v", err)
		}
		st, err := store.OpenLocal(dir, vault)
		if err != nil {
			t.Fatalf("OpenLocal: %v", err)
		}
		defer st.Close()
		pm := pool.NewManager(st, 4)
		defer pm.CloseAll()

		const bootTicket = "boot-ticket-v104-secret"
		const cookieSecret = "desktop-cookie-secret-v104"
		srv := NewDesktopRouter(st, pm, DesktopConfig{
			Enabled:             true,
			ListenPort:          18910,
			DataDir:             dir,
			BootTicket:          bootTicket,
			SessionCookieSecret: cookieSecret,
		})

		// Unauthenticated GET / must return 401 without leaking cookieSecret
		r1 := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18910/", nil)
		w1 := httptest.NewRecorder()
		srv.ServeHTTP(w1, r1)
		if w1.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 on unauthenticated desktop GET /, got %d", w1.Code)
		}
		if strings.Contains(w1.Body.String(), cookieSecret) {
			t.Fatal("unauthenticated desktop response leaked SessionCookieSecret")
		}

		// Boot ticket exchange issues port-scoped HttpOnly cookie
		rBoot := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18910/__desktop_boot?ticket="+bootTicket, nil)
		wBoot := httptest.NewRecorder()
		srv.ServeHTTP(wBoot, rBoot)
		if wBoot.Code != http.StatusFound {
			t.Fatalf("expected 302 from /__desktop_boot, got %d", wBoot.Code)
		}
		cookies := wBoot.Result().Cookies()
		if len(cookies) == 0 {
			t.Fatal("expected desktop session cookie from /__desktop_boot")
		}

		// Cookie-only POST from another localhost port must be rejected with 403
		rCrossPort := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:18910/api/connections", strings.NewReader(`{"name":"x","type":"sqlite"}`))
		rCrossPort.Header.Set("Content-Type", "application/json")
		rCrossPort.Header.Set("Origin", "http://127.0.0.1:9999")
		for _, ck := range cookies {
			rCrossPort.AddCookie(ck)
		}
		wCrossPort := httptest.NewRecorder()
		srv.ServeHTTP(wCrossPort, rCrossPort)
		if wCrossPort.Code != http.StatusForbidden {
			t.Fatalf("expected 403 on cross-port Origin cookie POST, got %d", wCrossPort.Code)
		}

		// Cookie-only POST with simple form/text Content-Type must be rejected with 403
		rSimpleCT := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:18910/api/connections", strings.NewReader(`{"name":"x","type":"sqlite"}`))
		rSimpleCT.Header.Set("Content-Type", "text/plain")
		rSimpleCT.Header.Set("Origin", "http://127.0.0.1:18910")
		for _, ck := range cookies {
			rSimpleCT.AddCookie(ck)
		}
		wSimpleCT := httptest.NewRecorder()
		srv.ServeHTTP(wSimpleCT, rSimpleCT)
		if wSimpleCT.Code != http.StatusForbidden {
			t.Fatalf("expected 403 on text/plain cookie POST, got %d", wSimpleCT.Code)
		}
	})

	t.Run("ValidateClickHouseRestrictedWebSQL blocks external SSRF and file table functions", func(t *testing.T) {
		blocked := []string{
			"SELECT * FROM url('http://169.254.169.254/latest/meta-data/', CSV)",
			"SELECT * FROM file('/etc/passwd', LineAsString)",
			"CREATE TABLE t (x Int32) ENGINE = URL('http://127.0.0.1:8080/', CSV)",
		}
		for _, q := range blocked {
			if err := sqlbase.ValidateClickHouseRestrictedWebSQL(q); err == nil {
				t.Fatalf("expected restricted ClickHouse SQL guard to reject %q", q)
			}
		}
		if err := sqlbase.ValidateClickHouseRestrictedWebSQL("SELECT number FROM system.numbers LIMIT 5"); err != nil {
			t.Fatalf("unexpected error on safe ClickHouse query: %v", err)
		}
	})
}
