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

// Config holds cross-platform runtime settings for DBHarbor.
type Config struct {
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
	ShowVersion     bool
}

// Load parses CLI flags and environment variables using OS-standard application data directories.
func Load(args []string) (*Config, error) {
	fs := flag.NewFlagSet("dbharbor", flag.ContinueOnError)

	defaultHost := envOr("DBHARBOR_HOST", "0.0.0.0")
	defaultPort := envIntOr("DBHARBOR_PORT", 8787)
	defaultUnixSock := envOr("DBHARBOR_UNIX_SOCKET", "")
	defaultUnixPerm := envOr("DBHARBOR_UNIX_SOCKET_PERM", "0660")
	defaultUnixGroup := envOr("DBHARBOR_UNIX_SOCKET_GROUP", "")
	defaultDataDir := envOr("DBHARBOR_DATA_DIR", "")
	defaultAdminUser := envOr("DBHARBOR_ADMIN_USER", "admin")
	defaultAdminPass := envOr("DBHARBOR_ADMIN_PASS", "admin123")
	defaultSecret := envOr("DBHARBOR_SECRET_KEY", "")

	var unixPermStr string
	cfg := &Config{}
	fs.StringVar(&cfg.Host, "host", defaultHost, "Bind host address (or unix:/path/to/dbharbor.sock)")
	fs.IntVar(&cfg.Port, "port", defaultPort, "HTTP listen port")
	fs.StringVar(&cfg.UnixSocket, "unix-socket", defaultUnixSock, "Unix domain socket path to listen on (disables TCP listener when set)")
	fs.StringVar(&unixPermStr, "unix-socket-perm", defaultUnixPerm, "Octal file permissions for Unix domain socket (e.g. 0660 or 0666)")
	fs.StringVar(&cfg.UnixSocketGroup, "unix-socket-group", defaultUnixGroup, "Group name or GID for Unix domain socket ownership")
	fs.StringVar(&cfg.DataDir, "data-dir", defaultDataDir, "Directory for internal database and encryption key")
	fs.StringVar(&cfg.AdminUser, "admin-user", defaultAdminUser, "Initial workbench admin username")
	fs.StringVar(&cfg.AdminPass, "admin-pass", defaultAdminPass, "Initial workbench admin password")
	fs.BoolVar(&cfg.OpenBrowser, "open", false, "Open default browser on startup")
	fs.BoolVar(&cfg.UseOSDataDir, "use-os-data-dir", true, "Use OS-standard application data directory (retained for backwards CLI compatibility)")
	fs.IntVar(&cfg.IdleConnTTL, "idle-ttl", 30, "Idle database connection eviction timeout in minutes")
	fs.BoolVar(&cfg.ShowVersion, "version", false, "Print DBHarbor version and exit")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if cfg.ShowVersion {
		return cfg, nil
	}
	cfg.SecretKeyHex = strings.TrimSpace(defaultSecret)
	cfg.UnixSocketGroup = strings.TrimSpace(cfg.UnixSocketGroup)

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

	if strings.TrimSpace(cfg.DataDir) == "" {
		cfg.DataDir = DefaultDataDir()
	}

	absDataDir, err := filepath.Abs(cfg.DataDir)
	if err == nil {
		cfg.DataDir = absDataDir
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}

	return cfg, nil
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
