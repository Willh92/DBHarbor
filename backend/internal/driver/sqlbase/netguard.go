package sqlbase

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
	"unicode"
)

var (
	prefixThisNet     = netip.MustParsePrefix("0.0.0.0/8")
	prefixCGNAT       = netip.MustParsePrefix("100.64.0.0/10") // Includes Alibaba Cloud IMDS 100.100.100.200
	prefixIETF        = netip.MustParsePrefix("192.0.0.0/24")
	prefixTestNet1    = netip.MustParsePrefix("192.0.2.0/24")
	prefixBenchmark   = netip.MustParsePrefix("198.18.0.0/15")
	prefixTestNet2    = netip.MustParsePrefix("198.51.100.0/24")
	prefixTestNet3    = netip.MustParsePrefix("203.0.113.0/24")
	prefixReservedV4  = netip.MustParsePrefix("240.0.0.0/4")
	prefixNAT64WellKn = netip.MustParsePrefix("64:ff9b::/96")
	prefixNAT64Local  = netip.MustParsePrefix("64:ff9b:1::/48")
	prefix6to4        = netip.MustParsePrefix("2002::/16")
	prefixTeredo      = netip.MustParsePrefix("2001::/32")
)

// IsRestrictedAddr reports whether addr belongs to loopback, RFC1918/ULA private space,
// CGNAT/cloud metadata (169.254.0.0/16, 100.64.0.0/10), 0.0.0.0/8, multicast, reserved,
// or IPv6 transition prefixes embedding restricted IPv4 addresses.
func IsRestrictedAddr(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true
	}
	addr = addr.Unmap()
	if addr.IsUnspecified() ||
		addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() {
		return true
	}

	if addr.Is4() {
		return prefixThisNet.Contains(addr) ||
			prefixCGNAT.Contains(addr) ||
			prefixIETF.Contains(addr) ||
			prefixTestNet1.Contains(addr) ||
			prefixBenchmark.Contains(addr) ||
			prefixTestNet2.Contains(addr) ||
			prefixTestNet3.Contains(addr) ||
			prefixReservedV4.Contains(addr)
	}

	// Inspect IPv6 transition mechanisms that can tunnel to internal IPv4 targets.
	b := addr.As16()
	if prefixNAT64WellKn.Contains(addr) || prefixNAT64Local.Contains(addr) {
		v4 := netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
		return IsRestrictedAddr(v4)
	}
	if prefix6to4.Contains(addr) {
		v4 := netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]})
		return IsRestrictedAddr(v4)
	}
	if prefixTeredo.Contains(addr) {
		// Teredo obfuscates client IPv4 with ^0xff in the last 4 bytes, and server IPv4 in bytes 4..7.
		srvV4 := netip.AddrFrom4([4]byte{b[4], b[5], b[6], b[7]})
		cliV4 := netip.AddrFrom4([4]byte{b[12] ^ 0xff, b[13] ^ 0xff, b[14] ^ 0xff, b[15] ^ 0xff})
		return IsRestrictedAddr(srvV4) || IsRestrictedAddr(cliV4)
	}
	return false
}

// IsRestrictedNetIP adapts standard net.IP to IsRestrictedAddr.
func IsRestrictedNetIP(ip net.IP) bool {
	if len(ip) == 0 {
		return true
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	return IsRestrictedAddr(addr)
}

// ValidateStrictHostname validates a single host/IP value (not a full URL or host:port pair),
// rejecting path/query/userinfo/escape delimiters, whitespace, control characters, and IPv6 zones.
func ValidateStrictHostname(rawHost string) (string, error) {
	for i := 0; i < len(rawHost); i++ {
		if rawHost[i] < 0x20 || rawHost[i] == 0x7f {
			return "", fmt.Errorf("invalid host: control characters are not allowed")
		}
	}
	s := strings.TrimSpace(rawHost)
	if s == "" {
		return "", fmt.Errorf("target host is required")
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || unicode.IsSpace(r) {
			return "", fmt.Errorf("invalid host %q: whitespace and control characters are not allowed", rawHost)
		}
		switch r {
		case '/', '?', '#', '@', '\\', '%':
			return "", fmt.Errorf("invalid host %q: URL delimiters and socket paths are not allowed", rawHost)
		}
	}
	if strings.HasPrefix(s, "[") || strings.HasSuffix(s, "]") {
		if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
			return "", fmt.Errorf("invalid bracketed IPv6 host %q", rawHost)
		}
		inner := s[1 : len(s)-1]
		addr, err := netip.ParseAddr(inner)
		if err != nil || !addr.Is6() || addr.Zone() != "" {
			return "", fmt.Errorf("invalid bracketed IPv6 host %q", rawHost)
		}
		return inner, nil
	}
	if strings.Contains(s, ":") {
		addr, err := netip.ParseAddr(s)
		if err != nil || addr.Zone() != "" {
			return "", fmt.Errorf("invalid host %q: port numbers and colons are not allowed in host field", rawHost)
		}
		return s, nil
	}
	return s, nil
}

// ExtractHostname strips optional port or IPv6 brackets from a host or host:port authority token.
// Callers parsing full URLs should parse with net/url first rather than passing paths/queries here.
func ExtractHostname(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		return strings.Trim(strings.TrimSpace(host), "[]")
	}
	return strings.Trim(s, "[]")
}

func resolveValidatedPublicIPs(ctx context.Context, rawHost string) ([]netip.Addr, error) {
	host, err := ValidateStrictHostname(rawHost)
	if err != nil {
		return nil, err
	}
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if lower == "localhost" ||
		strings.HasSuffix(lower, ".localhost") ||
		strings.HasSuffix(lower, ".local") ||
		strings.HasSuffix(lower, ".internal") ||
		strings.HasSuffix(lower, ".sock") ||
		strings.HasPrefix(lower, "/") ||
		strings.HasPrefix(lower, `\`) {
		return nil, fmt.Errorf("loopback, unix-socket, and internal hostnames (%q) are restricted for non-administrator accounts", host)
	}

	if ipAddr, err := netip.ParseAddr(lower); err == nil {
		if IsRestrictedAddr(ipAddr) {
			return nil, fmt.Errorf("private, loopback, link-local, and cloud metadata IP ranges (%s) are restricted for non-administrator accounts", ipAddr.String())
		}
		return []netip.Addr{ipAddr.Unmap()}, nil
	}
	if stdIP := net.ParseIP(lower); stdIP != nil {
		if IsRestrictedNetIP(stdIP) {
			return nil, fmt.Errorf("private, loopback, link-local, and cloud metadata IP ranges (%s) are restricted for non-administrator accounts", stdIP.String())
		}
		if addr, ok := netip.AddrFromSlice(stdIP); ok {
			return []netip.Addr{addr.Unmap()}, nil
		}
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(lookupCtx, "ip", lower)
	if err != nil || len(addrs) == 0 {
		return nil, fmt.Errorf("unable to verify DNS address for %q (non-administrator connections require a resolvable public IP)", host)
	}
	for _, a := range addrs {
		if IsRestrictedAddr(a) {
			return nil, fmt.Errorf("hostname %q resolves to restricted internal IP %s", host, a.Unmap().String())
		}
	}
	return addrs, nil
}

// ValidateRemoteHost resolves and validates a remote hostname/IP against restricted internal ranges.
// It fails closed on invalid host syntax, DNS lookup errors, or empty answers.
func ValidateRemoteHost(ctx context.Context, rawHost string) error {
	_, err := resolveValidatedPublicIPs(ctx, rawHost)
	return err
}

// ResolveAndPinSinglePublicIP validates rawHost via ValidateRemoteHost rules and returns
// a single validated public literal IP string so drivers without custom DialContext hooks
// (e.g., Oracle go-ora and Dameng dm) cannot be tricked by DNS rebinding.
func ResolveAndPinSinglePublicIP(ctx context.Context, rawHost string) (string, error) {
	addrs, err := resolveValidatedPublicIPs(ctx, rawHost)
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		u := a.Unmap()
		if u.Is4() {
			return u.String(), nil
		}
	}
	return addrs[0].Unmap().String(), nil
}

var forbiddenMongoURIFileParams = map[string]bool{
	"tlscafile":                   true,
	"tlscertificatekeyfile":       true,
	"sslclientcertificatekeyfile": true,
	"sslcertificateauthorityfile": true,
}

// ValidateMongoURIForRestrictedWeb parses a MongoDB connection URI and rejects Unix domain
// socket hosts as well as local-filesystem TLS certificate/CA file query options.
func ValidateMongoURIForRestrictedWeb(rawURI string) error {
	for i := 0; i < len(rawURI); i++ {
		if rawURI[i] < 0x20 || rawURI[i] == 0x7f {
			return fmt.Errorf("invalid MongoDB URI: control characters are not allowed")
		}
	}
	uStr := strings.TrimSpace(rawURI)
	if uStr == "" {
		return nil
	}
	lower := strings.ToLower(uStr)
	var rest string
	switch {
	case strings.HasPrefix(lower, "mongodb://"):
		rest = uStr[len("mongodb://"):]
	case strings.HasPrefix(lower, "mongodb+srv://"):
		rest = uStr[len("mongodb+srv://"):]
	default:
		return fmt.Errorf("unsupported MongoDB URI scheme (expected mongodb:// or mongodb+srv://)")
	}
	parsed, err := url.Parse(uStr)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("invalid MongoDB URI host")
	}

	// Inspect raw authority section before '/', '?', '#' to catch percent-encoded Unix sockets (%2Ftmp%2Fmongo.sock).
	authEnd := strings.IndexAny(rest, "/?#")
	authority := rest
	if authEnd >= 0 {
		authority = rest[:authEnd]
	}
	if atIdx := strings.LastIndexByte(authority, '@'); atIdx >= 0 {
		authority = authority[atIdx+1:]
	}
	if strings.TrimSpace(authority) == "" {
		return fmt.Errorf("MongoDB URI host is required")
	}
	for _, part := range strings.Split(authority, ",") {
		hp := strings.TrimSpace(part)
		if hp == "" {
			return fmt.Errorf("invalid empty host in MongoDB URI")
		}
		unescaped, uErr := url.PathUnescape(hp)
		if uErr != nil {
			return fmt.Errorf("invalid percent-encoding in MongoDB URI host")
		}
		for _, candidate := range []string{hp, unescaped} {
			lowCand := strings.ToLower(candidate)
			if strings.Contains(lowCand, "/") || strings.Contains(lowCand, `\`) || strings.Contains(lowCand, ".sock") {
				return fmt.Errorf("Unix domain socket hosts are restricted for non-administrator MongoDB connections")
			}
		}
		hostOnly := hp
		if h, _, sErr := net.SplitHostPort(hp); sErr == nil {
			hostOnly = h
		}
		if _, hErr := ValidateStrictHostname(hostOnly); hErr != nil {
			return hErr
		}
	}

	// Inspect query parameters (supporting both '&' and legacy MongoDB ';' delimiters).
	if parsed.RawQuery != "" {
		normalizedQuery := strings.ReplaceAll(parsed.RawQuery, ";", "&")
		vals, qErr := url.ParseQuery(normalizedQuery)
		if qErr != nil {
			return fmt.Errorf("invalid MongoDB URI query parameters")
		}
		for k := range vals {
			kl := strings.ToLower(strings.TrimSpace(k))
			if forbiddenMongoURIFileParams[kl] {
				return fmt.Errorf("MongoDB URI parameter %q is restricted in Web Server mode for host filesystem isolation", k)
			}
		}
	}
	return nil
}

// ValidateMongoSRVHost resolves _mongodb._tcp.<host> SRV records and validates every target host.
// It fails closed if SRV resolution fails or returns zero targets.
func ValidateMongoSRVHost(ctx context.Context, rawHost string) error {
	host, err := ValidateStrictHostname(rawHost)
	if err != nil {
		return err
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	_, srvs, err := net.DefaultResolver.LookupSRV(lookupCtx, "mongodb", "tcp", strings.TrimSuffix(host, "."))
	if err != nil || len(srvs) == 0 {
		return fmt.Errorf("unable to resolve MongoDB SRV records for %q", host)
	}
	for _, rec := range srvs {
		target := strings.TrimSuffix(strings.TrimSpace(rec.Target), ".")
		if err := ValidateRemoteHost(ctx, target); err != nil {
			return err
		}
	}
	return nil
}

// NewRestrictedDialContext returns a DialContext function that verifies the resolved peer IP
// inside net.Dialer.Control right before the OS connect() syscall, defeating DNS rebinding.
func NewRestrictedDialContext(timeout time.Duration) func(ctx context.Context, network, address string) (net.Conn, error) {
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	dialer := &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			if strings.Contains(strings.ToLower(network), "unix") {
				return fmt.Errorf("unix domain sockets are restricted for non-administrator accounts")
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				host = address
			}
			host = strings.Trim(strings.TrimSpace(host), "[]")
			// Strip IPv6 zone if present
			if zIdx := strings.IndexByte(host, '%'); zIdx >= 0 {
				host = host[:zIdx]
			}
			addr, err := netip.ParseAddr(host)
			if err != nil {
				return fmt.Errorf("refusing connection to unverified peer address %q", address)
			}
			if IsRestrictedAddr(addr) {
				return fmt.Errorf("connection blocked: peer IP %s is in a restricted internal/metadata network", addr.Unmap().String())
			}
			return nil
		},
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		netLower := strings.ToLower(strings.TrimSpace(network))
		switch netLower {
		case "tcp", "tcp4", "tcp6":
		default:
			return nil, fmt.Errorf("network transport %q is restricted for non-administrator accounts", network)
		}
		return dialer.DialContext(ctx, netLower, address)
	}
}
