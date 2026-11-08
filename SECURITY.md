# Security Policy

## Supported Versions

Security updates are provided for the latest minor release series on the `release` channel, as well as rolling fixes on the `preview` channel:

| Version | Supported | Notes |
| :--- | :---: | :--- |
| `1.0.x` (Latest: `v1.0.3`) | ✅ | Active security patches and hardening updates |
| `preview` channel | ✅ | Rolling pre-release builds tracking `main` / `preview` |
| `< 1.0.0` | ❌ | Pre-GA versions; please upgrade to the latest `1.0.x` release |

---

## Reporting a Vulnerability

Please **do not** report security vulnerabilities through public GitHub issues, discussions, or pull requests.

Instead, please report suspected vulnerabilities privately via **GitHub Private Vulnerability Reporting**:

- **Submit a Private Advisory**: https://github.com/Willh92/DBHarbor/security/advisories/new

### What to Include

To help us reproduce and remediate the issue quickly, please include:

1. Affected DBHarbor version, edition (**Web Server** vs. **Native Desktop GUI**), and OS/architecture.
2. Deployment configuration (e.g., reverse proxy, non-admin RBAC roles, `AllowPrivateNetForNonAdmin`, TLS mode).
3. Step-by-step reproduction instructions or a minimal proof-of-concept request/payload.
4. Assessment of the security impact (e.g., authentication bypass, privilege escalation, SSRF, path traversal, credential exposure, XSS).

### Response & Disclosure Timeline

- **Initial Acknowledgment**: Within **48 hours** of submission.
- **Triage & Severity Assessment**: Within **5 business days**.
- **Remediation & Coordinated Release**: Critical/High issues are prioritized for the next patch release, followed by a coordinated GitHub Security Advisory credit (unless anonymity is requested).

---

## Security Architecture & Deployment Scope

DBHarbor is distributed in two editions with distinct trust models:

### 1. Web Server Edition (`dbharbor`)
Designed for self-hosted, multi-user browser access over HTTPS or Unix domain sockets:
- **Authentication & Session Protection**: First-run administrator bootstrap, `bcrypt` password hashing, optional **RFC 6238 TOTP 2FA** with single-use hashed recovery codes, adaptive IP/account brute-force lockout + offline CAPTCHA, revocable server-side sessions, and short-lived single-use download tickets.
- **Credential Storage Policy**:
  - New database connections default to **Session-Only Credentials** (`RememberPassword = false`): database passwords and embedded URI secrets are kept only in server session memory (`sessionCreds`) and wiped upon sign-out, session revocation, or server shutdown.
  - When **Remember Password** is explicitly enabled by an authorized connection owner/administrator, secrets are encrypted at rest using **AES-256-GCM** backed by a `0600` `.master.key`.
- **RBAC & Network/Filesystem Sandboxing**:
  - Role-based access control (`admin`, `readwrite`, `readonly`) with per-connection visibility (`private` / `shared`) and allowlist scoping.
  - Non-admin Web accounts enforce dial-time **SSRF / private-network restrictions** (blocking loopback, RFC 1918, link-local/cloud IMDS `169.254.0.0/16`, CGNAT `100.64.0.0/10`, IPv6 ULA/NAT64/6to4, and DNS rebinding unless explicitly permitted by server policy).
  - Single-file databases (`SQLite 3`, `DuckDB`, `Parquet`) for non-admin Web users are strictly confined to per-user hashed subdirectories under `local_databases/` and `ephemeral_workspaces/`, with magic-byte header validation and SQL file-IO statement guards (`ATTACH`, `VACUUM INTO`, `COPY`, dot-commands).
- **Recommended Production Hardening**:
  - Always terminate **HTTPS / TLS** at a reverse proxy (Nginx, Caddy, Traefik) or bind DBHarbor to `127.0.0.1` / a Unix domain socket (`-unix-socket`).
  - Keep `data/` (`.master.key` and `dbharbor.db`) on restricted host storage (`0700` directory / `0600` files) and never commit or expose `data/` publicly.

### 2. Native Desktop GUI Edition (`dbharbor-desktop` / `DBHarbor.app`)
Designed as a single-user local workstation application:
- Binds an ephemeral loopback listener (`127.0.0.1:0`) protected by a per-launch random runtime bootstrap token and CSP script nonces.
- Operates with the local OS user's filesystem and network privileges by design; host filesystem access by the local user in Desktop mode is expected behavior rather than a privilege boundary.
