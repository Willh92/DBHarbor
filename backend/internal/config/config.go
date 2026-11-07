package config

import (
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Config holds cross-platform runtime settings for DBHarbor Web Server Edition.
type Config struct {
	ConfigFile      string
	Host            string
	Port            int
	UnixSocket      string
	UnixSocketPerm  uint32
	UnixSocketGroup string
	DataDir         string
	AdminUser       string
	AdminPass       string
	SecretKeyHex    string
	OpenBrowser     bool
	UseOSDataDir    bool
	IdleConnTTL     int // minutes
	MaxImportMB     int
	TrustedProxies  []string
	CORSOrigins     []string
	Security        SecurityConfig
	Recovery        RecoveryConfig
	RecoverAdminCLI bool
	InitOnly        bool
	ShowVersion     bool
}

// normalizeCLIRecoverArgs allows both `-recover-admin` (boolean) and `-recover-admin <username>` shorthand.
func normalizeCLIRecoverArgs(args []string) []string {
	out := make([]string, 0, len(args)+2)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if (a == "-recover-admin" || a == "--recover-admin") && i+1 < len(args) {
			next := strings.TrimSpace(args[i+1])
			low := strings.ToLower(next)
			if next != "" && !strings.HasPrefix(next, "-") && low != "true" && low != "false" && low != "1" && low != "0" {
				out = append(out, "-recover-admin=true", "-recover-user", next)
				i++
				continue
			}
		}
		out = append(out, a)
	}
	return out
}

// Load parses CLI flags, DBHARBOR_* environment variables, and <DataDir>/config.json
// with precedence: Explicit CLI flags > Environment variables > config.json > Built-in defaults.
func Load(args []string) (*Config, error) {
	fs := flag.NewFlagSet("dbharbor", flag.ContinueOnError)

	var (
		unixPermStr  string
		recoverUser  string
		recoverPass  string
		reset2FAFlag bool
	)
	cfg := &Config{}
	fs.StringVar(&cfg.ConfigFile, "config", strings.TrimSpace(os.Getenv("DBHARBOR_CONFIG_FILE")), "Path to persistent server config.json (default: <data-dir>/config.json)")
	fs.StringVar(&cfg.Host, "host", "0.0.0.0", "Bind host address (or unix:/path/to/dbharbor.sock)")
	fs.IntVar(&cfg.Port, "port", 8787, "HTTP listen port")
	fs.StringVar(&cfg.UnixSocket, "unix-socket", "", "Unix domain socket path to listen on (disables TCP listener when set)")
	fs.StringVar(&unixPermStr, "unix-socket-perm", "0660", "Octal file permissions for Unix domain socket (e.g. 0660 or 0666)")
	fs.StringVar(&cfg.UnixSocketGroup, "unix-socket-group", "", "Group name or GID for Unix domain socket ownership")
	fs.StringVar(&cfg.DataDir, "data-dir", "", "Directory for internal database, config.json, and encryption key")
	fs.StringVar(&cfg.AdminUser, "admin-user", "", "Initial workbench admin username (first-run bootstrap)")
	fs.StringVar(&cfg.AdminPass, "admin-pass", "", "Initial workbench admin password (first-run bootstrap, min 8 chars)")
	fs.BoolVar(&cfg.OpenBrowser, "open", false, "Open default browser on startup")
	fs.BoolVar(&cfg.UseOSDataDir, "use-os-data-dir", true, "Use OS-standard application data directory (retained for backwards CLI compatibility)")
	fs.IntVar(&cfg.IdleConnTTL, "idle-ttl", 30, "Idle database connection eviction timeout in minutes")
	fs.BoolVar(&cfg.InitOnly, "init-only", false, "Initialize encryption vault, config.json, and initial admin account in data-dir, then exit")
	fs.BoolVar(&cfg.RecoverAdminCLI, "recover-admin", false, "Execute emergency administrator password/2FA recovery and exit")
	fs.StringVar(&recoverUser, "recover-user", "", "Target administrator username for -recover-admin (defaults to primary admin)")
	fs.StringVar(&recoverPass, "recover-pass", "", "New administrator password for -recover-admin (min 8 chars, or 'auto' to generate)")
	fs.BoolVar(&reset2FAFlag, "reset-2fa", false, "Reset TOTP 2FA and recovery codes during -recover-admin")
	fs.BoolVar(&cfg.ShowVersion, "version", false, "Print DBHarbor version and exit")

	if err := fs.Parse(normalizeCLIRecoverArgs(args)); err != nil {
		return nil, err
	}
	if cfg.ShowVersion {
		return cfg, nil
	}

	visited := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		visited[f.Name] = true
	})

	// 1. Resolve DataDir first (CLI -data-dir > DBHARBOR_DATA_DIR > DefaultDataDir)
	if !visited["data-dir"] {
		cfg.DataDir = envOr("DBHARBOR_DATA_DIR", "")
	}
	if strings.TrimSpace(cfg.DataDir) == "" {
		cfg.DataDir = DefaultDataDir()
	}
	if absDataDir, err := filepath.Abs(cfg.DataDir); err == nil {
		cfg.DataDir = absDataDir
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}

	// 2. Load (or auto-create) <DataDir>/config.json
	if strings.TrimSpace(cfg.ConfigFile) == "" {
		cfg.ConfigFile = filepath.Join(cfg.DataDir, "config.json")
	} else if absCfg, err := filepath.Abs(cfg.ConfigFile); err == nil {
		cfg.ConfigFile = absCfg
	}
	fileCfg, err := EnsureAndLoadFileConfig(cfg.ConfigFile)
	if err != nil {
		return nil, err
	}

	// 3. Apply 4-tier precedence: CLI > Env > config.json > Default
	if !visited["host"] {
		fallbackHost := strings.TrimSpace(fileCfg.Host)
		if fallbackHost == "" {
			fallbackHost = "0.0.0.0"
		}
		cfg.Host = envOr("DBHARBOR_HOST", fallbackHost)
	}
	if !visited["port"] {
		fallbackPort := fileCfg.Port
		if fallbackPort <= 0 {
			fallbackPort = 8787
		}
		cfg.Port = envIntOrAllowZero("DBHARBOR_PORT", fallbackPort)
	}
	if !visited["unix-socket"] {
		cfg.UnixSocket = envOr("DBHARBOR_UNIX_SOCKET", strings.TrimSpace(fileCfg.UnixSocket))
	}
	if !visited["unix-socket-perm"] {
		fallbackPerm := strings.TrimSpace(fileCfg.UnixSocketPerm)
		if fallbackPerm == "" {
			fallbackPerm = "0660"
		}
		unixPermStr = envOr("DBHARBOR_UNIX_SOCKET_PERM", fallbackPerm)
	}
	if !visited["unix-socket-group"] {
		cfg.UnixSocketGroup = envOr("DBHARBOR_UNIX_SOCKET_GROUP", strings.TrimSpace(fileCfg.UnixSocketGroup))
	}
	if !visited["idle-ttl"] {
		fallbackTTL := fileCfg.IdleConnTTLMinutes
		if fallbackTTL <= 0 {
			fallbackTTL = 30
		}
		cfg.IdleConnTTL = envIntOr("DBHARBOR_IDLE_TTL", fallbackTTL)
	}
	if !visited["admin-user"] {
		cfg.AdminUser = envOr("DBHARBOR_ADMIN_USER", "")
	}
	if !visited["admin-pass"] {
		cfg.AdminPass = envOr("DBHARBOR_ADMIN_PASS", "")
	}

	cfg.SecretKeyHex = strings.TrimSpace(os.Getenv("DBHARBOR_SECRET_KEY"))
	cfg.UnixSocketGroup = strings.TrimSpace(cfg.UnixSocketGroup)

	maxImport := fileCfg.MaxImportMB
	if maxImport <= 0 {
		maxImport = 256
	}
	cfg.MaxImportMB = envIntOr("DBHARBOR_MAX_IMPORT_MB", maxImport)
	cfg.TrustedProxies = cleanStringList(fileCfg.TrustedProxies)
	if envProxies := strings.TrimSpace(os.Getenv("DBHARBOR_TRUSTED_PROXIES")); envProxies != "" {
		cfg.TrustedProxies = cleanStringList(strings.Split(envProxies, ","))
	}
	cfg.CORSOrigins = cleanStringList(fileCfg.CORSAllowedOrigins)
	if envCORS := strings.TrimSpace(os.Getenv("DBHARBOR_CORS_ORIGINS")); envCORS != "" {
		cfg.CORSOrigins = cleanStringList(strings.Split(envCORS, ","))
	}

	cfg.Security = fileCfg.Security
	if envDuck := strings.TrimSpace(os.Getenv("DBHARBOR_DUCKDB_PATH")); envDuck != "" {
		cfg.Security.DuckDBCliPath = envDuck
	}
	if envAllowPriv := strings.TrimSpace(os.Getenv("DBHARBOR_ALLOW_PRIVATE_NET_NON_ADMIN")); envAllowPriv != "" {
		cfg.Security.AllowPrivateNetworkForNonAdmin = envAllowPriv == "1" || strings.EqualFold(envAllowPriv, "true")
	}

	if cfg.RecoverAdminCLI || visited["recover-pass"] || (visited["reset-2fa"] && reset2FAFlag) {
		cfg.RecoverAdminCLI = true
		cfg.Recovery = RecoveryConfig{
			Enabled:       true,
			AdminUsername: strings.TrimSpace(recoverUser),
			NewPassword:   recoverPass,
			Reset2FA:      reset2FAFlag,
		}
	} else {
		cfg.Recovery = fileCfg.Recovery
	}

	// Support shorthand -host unix:/run/dbharbor.sock or unix:///run/dbharbor.sock
	trimmedHost := strings.TrimSpace(cfg.Host)
	if cfg.UnixSocket == "" && strings.HasPrefix(strings.ToLower(trimmedHost), "unix:") {
		sock := trimmedHost[len("unix:"):]
		if strings.HasPrefix(sock, "//") && !strings.HasPrefix(sock, "///") {
			sock = sock[2:]
		} else if strings.HasPrefix(sock, "///") {
			sock = sock[2:] // keep single leading slash
		}
		cfg.UnixSocket = strings.TrimSpace(sock)
	}
	cfg.UnixSocket = strings.TrimSpace(cfg.UnixSocket)

	permVal, err := strconv.ParseUint(strings.TrimSpace(unixPermStr), 8, 32)
	if err != nil || permVal == 0 {
		return nil, fmt.Errorf("invalid -unix-socket-perm octal value %q: %w", unixPermStr, err)
	}
	cfg.UnixSocketPerm = uint32(permVal)

	return cfg, nil
}

func cleanStringList(items []string) []string {
	if len(items) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(items))
	for _, s := range items {
		t := strings.TrimSpace(s)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// DefaultDataDir selects the OS-standard application data directory (never relative to the working directory).
func DefaultDataDir() string {
	switch runtime.GOOS {
	case "windows":
		if isWindowsSystemAccount(os.Getenv("USERNAME"), os.Getenv("USERPROFILE")) {
			progData := strings.TrimSpace(os.Getenv("ProgramData"))
			if progData == "" {
				progData = `C:\ProgramData`
			}
			return filepath.Join(progData, "DBHarbor", "data")
		}
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			return filepath.Join(localAppData, "DBHarbor", "data")
		}
		if cfgDir, err := os.UserConfigDir(); err == nil && cfgDir != "" {
			return filepath.Join(cfgDir, "DBHarbor", "data")
		}
		return filepath.Join(os.TempDir(), "dbharbor-data")

	case "darwin":
		if os.Geteuid() == 0 {
			return "/Library/Application Support/DBHarbor/data"
		}
		if cfgDir, err := os.UserConfigDir(); err == nil && cfgDir != "" {
			return filepath.Join(cfgDir, "DBHarbor", "data")
		}
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, "Library", "Application Support", "DBHarbor", "data")
		}
		return filepath.Join(os.TempDir(), "dbharbor-data")

	default:
		if os.Geteuid() == 0 {
			return "/var/lib/dbharbor"
		}
		if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
			return filepath.Join(xdg, "dbharbor")
		}
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, ".local", "share", "dbharbor")
		}
		return "/var/lib/dbharbor"
	}
}

func isWindowsSystemAccount(username, userProfile string) bool {
	u := strings.TrimSpace(username)
	switch strings.ToUpper(u) {
	case "SYSTEM", "LOCAL SERVICE", "NETWORK SERVICE":
		return true
	}
	if strings.HasSuffix(u, "$") {
		return true
	}
	lp := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(userProfile), "/", `\`))
	if strings.Contains(lp, `\systemprofile`) || strings.Contains(lp, `\serviceprofiles`) {
		return true
	}
	return false
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func envIntOrAllowZero(key string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return fallback
}

// PrepareListener creates either a Unix Domain Socket listener (cleaning up stale socket files safely
// and applying octal permissions and optional group ownership) or a standard TCP listener, returning
// the listener, cleanup callback, and human-readable endpoint string.
func PrepareListener(cfg *Config) (net.Listener, func(), string, error) {
	if sockPath := strings.TrimSpace(cfg.UnixSocket); sockPath != "" {
		if absSock, err := filepath.Abs(sockPath); err == nil {
			sockPath = absSock
		}
		if err := os.MkdirAll(filepath.Dir(sockPath), 0o755); err != nil {
			return nil, nil, "", fmt.Errorf("create unix socket parent dir failed: %w", err)
		}

		if fi, err := os.Lstat(sockPath); err == nil {
			if fi.IsDir() {
				return nil, nil, "", fmt.Errorf("unix socket path %s is a directory", sockPath)
			}
			// Guard against clobbering a live running instance on the same socket
			if conn, dialErr := net.DialTimeout("unix", sockPath, 300*time.Millisecond); dialErr == nil {
				_ = conn.Close()
				return nil, nil, "", fmt.Errorf("unix socket %s is already in use by an active process", sockPath)
			}
			// Guard against accidentally deleting non-empty regular files
			if fi.Mode().IsRegular() && fi.Size() > 0 {
				return nil, nil, "", fmt.Errorf("refusing to overwrite non-empty regular file at %s", sockPath)
			}
			if rmErr := os.Remove(sockPath); rmErr != nil && !os.IsNotExist(rmErr) {
				return nil, nil, "", fmt.Errorf("remove stale unix socket %s failed: %w", sockPath, rmErr)
			}
		}

		ln, err := net.Listen("unix", sockPath)
		if err != nil {
			return nil, nil, "", fmt.Errorf("listen unix %s failed: %w", sockPath, err)
		}

		if grp := strings.TrimSpace(cfg.UnixSocketGroup); grp != "" {
			if err := applyUnixSocketGroup(sockPath, grp); err != nil {
				_ = ln.Close()
				_ = os.Remove(sockPath)
				return nil, nil, "", fmt.Errorf("apply unix socket group %q to %s failed: %w", grp, sockPath, err)
			}
		}

		perm := cfg.UnixSocketPerm
		if perm == 0 {
			perm = 0o660
		}
		_ = os.Chmod(sockPath, os.FileMode(perm))

		cleanup := func() {
			_ = os.Remove(sockPath)
		}
		if grp := strings.TrimSpace(cfg.UnixSocketGroup); grp != "" {
			return ln, cleanup, fmt.Sprintf("unix:%s (mode %04o, group %s)", sockPath, perm, grp), nil
		}
		return ln, cleanup, fmt.Sprintf("unix:%s (mode %04o)", sockPath, perm), nil
	}

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, "", fmt.Errorf("listen tcp %s failed: %w", addr, err)
	}
	return ln, func() {}, fmt.Sprintf("http://localhost:%d (bind %s)", cfg.Port, addr), nil
}
