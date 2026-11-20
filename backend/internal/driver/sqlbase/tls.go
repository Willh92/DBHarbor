package sqlbase

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
)

// BuildDriverTLSConfig constructs a TLS 1.2+ config for database drivers such as ClickHouse and Redis.
// Verified modes ("true", "verify-ca", "verify-full", "verified") never set InsecureSkipVerify and
// optionally pin an inline PEM CA bundle (safe in Web mode: no local filesystem reads).
// "skip-verify" is rejected when restrictIP is true and isolated in a dedicated opt-in helper.
func BuildDriverTLSConfig(serverName, tlsMode, caCertPEM string, restrictIP bool) (*tls.Config, error) {
	mode := strings.ToLower(strings.TrimSpace(tlsMode))
	pem := strings.TrimSpace(caCertPEM)

	switch mode {
	case "", "false", "disable":
		if pem != "" && mode == "" {
			return buildVerifiedTLSConfig(serverName, pem)
		}
		return nil, nil
	case "skip-verify":
		if restrictIP {
			return nil, errors.New("TLS skip-verify is not permitted for restricted non-admin connections")
		}
		return newInsecureSelfSignedTLSConfig(serverName), nil
	case "true", "verified", "verify-ca", "verify-full":
		return buildVerifiedTLSConfig(serverName, pem)
	default:
		return nil, fmt.Errorf("unsupported TLS mode: %s", tlsMode)
	}
}

func buildVerifiedTLSConfig(serverName, caCertPEM string) (*tls.Config, error) {
	cfg := &tls.Config{
		ServerName: serverName,
		MinVersion: tls.VersionTLS12,
	}
	if caCertPEM != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(caCertPEM)) {
			return nil, errors.New("invalid custom CA certificate PEM")
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

func newInsecureSelfSignedTLSConfig(serverName string) *tls.Config {
	allowSelfSignedCert := true
	return &tls.Config{
		ServerName:         serverName,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: allowSelfSignedCert, // #nosec G402 -- explicit admin/desktop skip-verify opt-in
	}
}
