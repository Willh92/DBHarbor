package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SecurityConfig defines Web Server Edition security & sandboxing policies.
type SecurityConfig struct {
	AllowPrivateNetworkForNonAdmin bool   `json:"allowPrivateNetworkForNonAdmin"`
	DuckDBCliPath                  string `json:"duckdbCliPath"`
}

// RecoveryConfig defines the auto-disarming emergency administrator recovery block in config.json.
type RecoveryConfig struct {
	Enabled       bool   `json:"enabled"`
	AdminUsername string `json:"adminUsername"`
	NewPassword   string `json:"newPassword"`
	Reset2FA      bool   `json:"reset2FA"`
}

// FileConfig represents the persistent <DataDir>/config.json structure for DBHarbor Web Server Edition.
type FileConfig struct {
	Host               string         `json:"host"`
	Port               int            `json:"port"`
	UnixSocket         string         `json:"unixSocket"`
	UnixSocketPerm     string         `json:"unixSocketPerm"`
	UnixSocketGroup    string         `json:"unixSocketGroup"`
	IdleConnTTLMinutes int            `json:"idleConnTTLMinutes"`
	MaxImportMB        int            `json:"maxImportMB"`
	TrustedProxies     []string       `json:"trustedProxies"`
	CORSAllowedOrigins []string       `json:"corsAllowedOrigins"`
	Security           SecurityConfig `json:"security"`
	Recovery           RecoveryConfig `json:"recovery"`
}

// DefaultFileConfig returns the clean default configuration written to <DataDir>/config.json on first boot.
func DefaultFileConfig() FileConfig {
	return FileConfig{
		Host:               "0.0.0.0",
		Port:               8787,
		UnixSocket:         "",
		UnixSocketPerm:     "0660",
		UnixSocketGroup:    "",
		IdleConnTTLMinutes: 30,
		MaxImportMB:        256,
		TrustedProxies:     []string{},
		CORSAllowedOrigins: []string{},
		Security: SecurityConfig{
			AllowPrivateNetworkForNonAdmin: false,
			DuckDBCliPath:                  "",
		},
		Recovery: RecoveryConfig{
			Enabled:       false,
			AdminUsername: "",
			NewPassword:   "",
			Reset2FA:      false,
		},
	}
}

// EnsureAndLoadFileConfig loads configPath if present, or atomically writes DefaultFileConfig (mode 0600) if missing.
func EnsureAndLoadFileConfig(configPath string) (FileConfig, error) {
	def := DefaultFileConfig()
	if strings.TrimSpace(configPath) == "" {
		return def, nil
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			if writeErr := writeFileConfigAtomic(configPath, def); writeErr != nil {
				return def, fmt.Errorf("failed to create default config file %s: %w", configPath, writeErr)
			}
			return def, nil
		}
		return def, fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}

	// Tighten existing config file permissions best-effort
	_ = os.Chmod(configPath, 0o600)

	if len(strings.TrimSpace(string(raw))) == 0 {
		if writeErr := writeFileConfigAtomic(configPath, def); writeErr != nil {
			return def, writeErr
		}
		return def, nil
	}

	fc := def
	if err := json.Unmarshal(raw, &fc); err != nil {
		return def, fmt.Errorf("invalid JSON in config file %s: %w", configPath, err)
	}
	if fc.TrustedProxies == nil {
		fc.TrustedProxies = []string{}
	}
	if fc.CORSAllowedOrigins == nil {
		fc.CORSAllowedOrigins = []string{}
	}
	return fc, nil
}

// DisarmRecoveryConfigFile atomically resets recovery.enabled = false and wipes recovery.newPassword = "" on disk
// immediately after emergency recovery completes so plaintext credentials never persist.
func DisarmRecoveryConfigFile(configPath string) error {
	if strings.TrimSpace(configPath) == "" {
		return nil
	}
	fc, err := EnsureAndLoadFileConfig(configPath)
	if err != nil {
		return err
	}
	fc.Recovery.Enabled = false
	fc.Recovery.NewPassword = ""
	fc.Recovery.Reset2FA = false
	return writeFileConfigAtomic(configPath, fc)
}

func writeFileConfigAtomic(configPath string, fc FileConfig) error {
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if fc.TrustedProxies == nil {
		fc.TrustedProxies = []string{}
	}
	if fc.CORSAllowedOrigins == nil {
		fc.CORSAllowedOrigins = []string{}
	}
	encoded, err := json.MarshalIndent(fc, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')

	tmpPath := fmt.Sprintf("%s.tmp.%d", configPath, os.Getpid())
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(encoded); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	_ = f.Sync()
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	_ = os.Chmod(tmpPath, 0o600)
	if err := os.Rename(tmpPath, configPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	_ = os.Chmod(configPath, 0o600)
	return nil
}
