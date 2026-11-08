# DBHarbor — Cross-Platform Multi-Database Web Workbench

**Language**: **English** | [简体中文](./README.zh-CN.md) | [繁體中文](./README.zh-TW.md) | [日本語](./README.ja.md)

[![License: Apache-2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](./LICENSE)
[![Version](https://img.shields.io/badge/Version-v1.0.3-1677ff.svg)](./CHANGELOG.md)
[![CGO_ENABLED=0](https://img.shields.io/badge/Go-CGO__ENABLED%3D0-00ADD8.svg)](https://github.com/Willh92/DBHarbor.git)

A zero-dependency, single-binary cross-platform Web Database Workbench combining **phpMyAdmin's intuitive visual table & data operations** with **modern multi-engine IDE power**.

- **Repository**: [https://github.com/Willh92/DBHarbor.git](https://github.com/Willh92/DBHarbor.git)
- **Changelog**: [CHANGELOG.md](./CHANGELOG.md)
- **License**: [Apache License 2.0](./LICENSE)

---

## Key Features

- **14 Built-in Database Engines (`CGO_ENABLED=0` Pure Go, Lossless Slim Binaries)**:
  - **Analytical / OLAP**: ClickHouse (Native TCP & HTTP protocols, `MergeTree` visual builder, Projections, Data Skipping Indexes, Materialized Views, Table TTL, Dictionaries, SQL UDFs & partition governance), DuckDB (multi-schema switching, Sequences, Scalar/Table Macros)
  - **Relational RDBMS**: MySQL, MariaDB, TiDB, OceanBase, PostgreSQL, KingbaseES, SQLite 3 (`FTS5`/`RTree` virtual tables & `PRAGMA`/integrity inspector), SQL Server, Oracle (pure-Go without Instant Client, lossless gzip-compressed 236-charset NLS overlay), Dameng (DM8)
  - **Key-Value & Document NoSQL**: Redis (DB0–15 browser, 6 data structure editors, Stream Consumer Groups, Slowlog, `CONFIG GET`, Pub/Sub & embedded CLI), MongoDB (JSON & Table views, BSON CRUD, Views / Time-Series / Capped collections, `$jsonSchema` validators, TTL/Partial/Sparse indexes & `RunCommand` console)
- **Complete Visual Schema Designer, 14-Engine Characteristic Elements, Privileges & SQL Operations**:
  - **Visual Table Structure, Characteristic Objects & DB Privilege Governance**: Add, modify, rename, or drop columns (including Generated/Computed Columns & `CHECK` constraints); manage indexes, foreign keys & table partitions; visually create, edit & preview DDL for **Scheduled Events** (MySQL/MariaDB/OceanBase), **Sequences** (PG/Oracle/DM8/MSSQL/MariaDB/TiDB/DuckDB), **Materialized Views & Refresh** (PG/Oracle/DM8/ClickHouse), **Packages (`Spec` + `Body`), Synonyms & DB Links** (Oracle/DM8), **Extensions & Custom Types (`ENUM`/`DOMAIN`)** (PG/KingbaseES), **Functions, Procedures & Triggers**; manage database-level users & grants (`GRANT` / `REVOKE`, Redis ACL, MongoDB roles); transactional lossless table rebuilds on SQLite; default-collapsed sidebar categories with one-click **Expand All / Collapse All** and strict cross-connection/DB/schema state isolation.
  - **Browser Ephemeral Local Single-File DBs (`SQLite 3` & `DuckDB`), Navicat-Style Data Grid & Smart SQL Console**: Drag-and-drop or pick local `.db`/`.sqlite`/`.duckdb` files directly in Web Server Edition (**`Open Local DB`**) with Chromium File System Access API **direct save-back to the local file** and one-click binary snapshot downloads; full-height data grid (`CellStateHub` low-memory rendering) with click-to-insert blank rows, batch column editing, single/multi-row TSV clipboard copy & paste (`Ctrl+C` / `Ctrl+V` compatible with Notepad & Excel), plus an embedded tree-shaken Monaco Editor with schema-aware autocomplete, `Ctrl/Cmd + Enter` execution, `EXPLAIN` query plans, SQL formatting, query cancellation (`AbortController`), execution history, and saved snippets.
  - **Streaming Import, Export & SQL Runner**: Full-database & multi-table SQL Dump, single-table/filtered/custom-query/MongoDB/Redis/DuckDB streaming exports & `< 64 KiB` RAM streaming multipart uploads (`SQL`, `CSV-BOM`, `CSV`, `JSON`, `JSONL`, `Markdown`), memory-capped streaming result readers, batched transactional file imports, and direct paste-to-execute multi-statement SQL scripts.
- **Enterprise Web Server Security, RBAC, Login Branding & Emergency Recovery *(Web Server Edition Only)***:
  - **Zero-Default-Credential Setup, Login Branding & Anti-Indexing**: First-run **Initial Administrator Setup** wizard (`>= 8` char password, no weak default `admin123`), zero-leakage Sign-In page, customizable browser `<title>`, top emblem (`<= 128 KB` custom logo synced to Favicon), heading & subtitle, plus triple anti-search-engine indexing (`/robots.txt`, `X-Robots-Tag`, `<meta name="robots">`).
  - **Session-First Database Credential Policy & Auth-Failure Re-Prompt**: New credentialed database connections default to **not remembering passwords on disk** (`Remember password` unchecked — credentials stay strictly in per-session memory and prompt on next login, while checking `Remember password` persists them with `AES-256-GCM` encryption; existing remembered connections can also turn off `Remember password` via Edit Connection to immediately wipe stored secrets from disk). Whenever a connection or query fails due to a changed or invalid database username/password, DBHarbor automatically evicts the stale pool and prompts to re-enter credentials with transparent request retry.
  - **Multi-User RBAC, TOTP 2FA & Hardened Sandboxing**: 3-tier RBAC (`Admin` / `Read-Write` / `Read-Only`) with per-user workspace/tab isolation, vault-derived single-use CAPTCHA tokens, `trustedProxies`-aware rate limiting (`HTTP 429`), offline SVG QR code RFC 6238 TOTP 2FA + 8 single-use recovery codes + bounded 2FA attempt limits, verified TLS certificate modes, per-user `<DataDir>/local_databases/u_<hash>/` & `<DataDir>/ephemeral_workspaces/` sandboxing (with lexical SQLite `ATTACH`/file-IO blocking, DuckDB `-safe` mode + `stdin` piping + `enable_external_access = false`, and MariaDB `/*M!...*/` read-only SQL unwrapping), dial-time DNS-rebinding SSRF guard across all remote drivers, reference-counted concurrent connection pool leases, connection-pinned SQL Server `USE [db]`, and persistent `<DataDir>/config.json` with auto-disarming **Emergency Admin Recovery**.
- **Native Dark Window Theme, Mobile Touch Workbench & Multi-Language UI**:
  - Native OS dark title bar synchronization on Desktop (`Win32 DWM`, `macOS DarkAqua`, `Linux GTK3`), plus a dedicated touch-first mobile workbench (`<= 640px`) featuring flicker-free GPU CSS `position: sticky` headers, touch column width drag-resizing & double-tap auto-expand, sidebar/table `[⋮]` & long-press menus, mobile SQL quick-symbol bar, tap-to-inspect cells, bottom-sheet row editors, `keep-alive` sidebar object caching, and global viewport popup clamping.
  - Supports both **TCP** and **Unix Domain Socket (`AF_UNIX`)** listening (`-unix-socket`, `-unix-socket-perm`, `-unix-socket-group`).
  - Built-in i18n UI & documentation in **English (`en-US`)**, **Simplified Chinese (`zh-CN`)**, **Traditional Chinese (`zh-TW`)**, and **Japanese (`ja-JP`)**, plus online update checking & one-command service binary upgrades.

---

## Quick Start & Build

```bash
# 1. Start development servers (Go API :8787 + Vite HMR :5173)
npm run dev

# 2. Build both Web Server & Native Desktop GUI executables for current OS/Arch (output -> ./dist)
npm run build

# 3. Cross-compile all 6 OS/Arch Server + Desktop GUI binaries + macOS .app bundles + service installers
npm run build:all

# 4. Run automated unit, E2E, and benchmark test suites
npm test
```

### Two Editions (`dist/`)

1. **Desktop GUI Edition (`dbharbor-desktop-<os>-<arch>` & `DBHarbor-darwin-<arch>.app`) — Pure Zero-Login Local Client**:
   - Double-click the executable (e.g. `dbharbor-desktop-windows-amd64.exe` or `DBHarbor-darwin-arm64.app`) to launch a native OS application window directly (`Win32 + Edge WebView2` on Windows without any console window, `Cocoa NSWindow + WKWebView` on macOS, `GTK3 + WebKit2GTK` on Linux).
   - **Pure Local Mode (NO User Account System)**: Boots straight into the workbench with zero login wall, native OS file/Save As dialogs, full local file access, and an AES-256-GCM encrypted local connection vault isolated via same-site loopback protection. Server-only features (`<DataDir>/config.json`, Initial Setup Wizard, Login Branding, Admin Recovery Mode, and Web Server local-file/network sandboxing) do **not** apply to Desktop GUI Edition.
2. **Web Server Edition (`dbharbor-<os>-<arch>`) — Self-Hosted / Team Service**:
   - Launch `./dist/dbharbor-<os>-<arch>` (or `-version` to inspect build info) and open `http://localhost:8787` (or bind a Unix Domain Socket via `-unix-socket`).
   - **Initial Administrator Setup & Anti-Indexing**: No hardcoded weak default credentials — on first visit when the user store is empty, DBHarbor opens a one-time **Initial Administrator Setup** wizard (`>= 8` char password) where you create the primary administrator and optionally customize login page branding (or pass `-admin-user` / `-admin-pass` for headless bootstrap). Built-in `/robots.txt`, `X-Robots-Tag`, and `<meta name="robots">` automatically block search engine indexing.

---

## Default OS Data Directories, CLI Flags & Persistent `config.json`

DBHarbor **never** writes runtime state into the current startup directory unless explicitly requested via `-data-dir`. By default, internal metadata (`dbharbor.db`) and the AES-256-GCM master key (`.secret.key`) are stored in standard OS application directories:

| Platform | User / Desktop Launch | System Service / Daemon |
| :--- | :--- | :--- |
| **Windows** | `%LOCALAPPDATA%\DBHarbor\data` | `%ProgramData%\DBHarbor\data` (`C:\ProgramData\DBHarbor\data`) |
| **macOS** | `~/Library/Application Support/DBHarbor/data` | `/Library/Application Support/DBHarbor/data` |
| **Linux** | `$XDG_DATA_HOME/dbharbor` (`~/.local/share/dbharbor`) | `/var/lib/dbharbor` |

### CLI Options & Environment Variables *(Web Server Edition)*

Configuration precedence: **`Explicit CLI flags > DBHARBOR_* environment variables > <DataDir>/config.json > Built-in defaults`**.

| CLI Flag | Environment Variable | Default | Description |
| :--- | :--- | :--- | :--- |
| `-config` | `DBHARBOR_CONFIG_FILE` | `<DataDir>/config.json` | Path to persistent Web Server JSON configuration file (`0600`) |
| `-host` | `DBHARBOR_HOST` | `0.0.0.0` | TCP bind address (or `unix:/path/to/dbharbor.sock` shorthand) |
| `-port` | `DBHARBOR_PORT` | `8787` | HTTP TCP port |
| `-unix-socket` | `DBHARBOR_UNIX_SOCKET` | *(empty)* | Listen on Unix Domain Socket instead of TCP (e.g. `/run/dbharbor/dbharbor.sock`) |
| `-unix-socket-perm` | `DBHARBOR_UNIX_SOCKET_PERM` | `0660` | Octal file mode applied to the Unix Domain Socket |
| `-unix-socket-group` | `DBHARBOR_UNIX_SOCKET_GROUP` | *(empty)* | POSIX group name or GID assigned to the Unix Domain Socket (e.g. `www-data`, `nginx`) |
| `-data-dir` | `DBHARBOR_DATA_DIR` | *(OS standard)* | Custom directory for SQLite metadata store, `config.json`, & `.secret.key` |
| `-admin-user` | `DBHARBOR_ADMIN_USER` | *(empty — Web Setup)* | Optional headless initial admin username (only used when users table is empty) |
| `-admin-pass` | `DBHARBOR_ADMIN_PASS` | *(empty — Web Setup)* | Optional headless initial admin password (**min 8 chars**; omit to use Web Setup Wizard) |
| `-idle-ttl` | `DBHARBOR_IDLE_TTL` | `30` | Idle database connection pool eviction timeout (minutes) |
| — | `DBHARBOR_MAX_IMPORT_MB` | `256` | Maximum file import upload body size in MB (also configurable in `config.json`) |
| — | `DBHARBOR_TRUSTED_PROXIES` | *(loopback)* | Comma-separated proxy IPs/CIDRs trusted for `X-Forwarded-For` / `X-Real-IP` |
| — | `DBHARBOR_CORS_ORIGINS` | *(same-origin)* | Comma-separated allowed cross-origin origins |
| — | `DBHARBOR_DUCKDB_PATH` | *(auto)* | Server-controlled DuckDB CLI binary path (`security.duckdbCliPath`) |
| — | `DBHARBOR_ALLOW_PRIVATE_NET_NON_ADMIN` | `false` | Allow non-admin Web users to connect to private/loopback network targets |
| `-init-only` | — | `false` | Initialize encryption vault, `config.json`, & initial admin account in `-data-dir`, then exit |
| `-recover-admin` | — | `false` | Run one-shot emergency administrator password/2FA recovery and exit |
| `-recover-user` | — | *(primary admin)* | Target admin username for `-recover-admin` |
| `-recover-pass` | — | *(empty)* | New password (`>= 8` chars) or `auto` (random 16-char password) for `-recover-admin` |
| `-reset-2fa` | — | `false` | Also clear TOTP 2FA & recovery codes during `-recover-admin` |
| `-open` | — | `false` | Open system browser automatically on TCP startup |
| `-version` | — | `false` | Print version, commit, and platform info |

### Persistent Server Configuration (`<DataDir>/config.json`) & Emergency Admin Recovery *(Web Server Edition Only)*

On startup, **Web Server Edition** automatically generates `<DataDir>/config.json` with `0600` permissions if it does not exist:

```json
{
  "host": "0.0.0.0",
  "port": 8787,
  "unixSocket": "",
  "unixSocketPerm": "0660",
  "unixSocketGroup": "",
  "idleConnTTLMinutes": 30,
  "maxImportMB": 256,
  "trustedProxies": ["127.0.0.1/32", "::1/128"],
  "corsAllowedOrigins": [],
  "security": {
    "allowPrivateNetworkForNonAdmin": false,
    "duckdbCliPath": ""
  },
  "recovery": {
    "enabled": false,
    "adminUsername": "admin",
    "newPassword": "",
    "reset2FA": false
  }
}
```

If an administrator forgets their password or loses their 2FA device & recovery codes, **Web Server Edition** provides two host-verified recovery paths:

1. **Edit `<DataDir>/config.json` & Restart Service**:
   Set `"recovery": { "enabled": true, "adminUsername": "admin", "newPassword": "YourNewStrongPassword", "reset2FA": true }` (minimum 8 characters, or `"newPassword": "auto"` to generate a random 16-character password printed to server startup logs) and restart DBHarbor. On boot, DBHarbor snapshots `dbharbor.db` to `<DataDir>/.backups/`, resets the password/2FA, revokes all active sessions for that admin, records an audit event, and **immediately disarms `config.json` (`"enabled": false`, `"newPassword": ""`)** so plaintext passwords never remain on disk.
2. **One-Shot CLI Recovery**:
   ```bash
   dbharbor -data-dir /var/lib/dbharbor -recover-admin -recover-pass auto -reset-2fa
   ```

> **Web Server Local File & Network Security Boundary**: DBHarbor's internal metadata (`dbharbor.db`), `config.json`, and encryption keys (`.secret.key`) are always hidden and blocked from database connections. In **Web Server Edition**, non-admin (`Read-Write`) accounts are strictly isolated per user within `<DataDir>/local_databases/u_<hash>/` when browsing or creating server-stored SQLite / DuckDB files, and browser-opened **`Open Local DB`** single-file workspaces are isolated per user in `0700` `<DataDir>/ephemeral_workspaces/` with binary magic-header verification and 12h idle auto-cleanup (with lexical SQLite `ATTACH`/file-IO blocking, DuckDB `-safe` mode + `stdin` SQL piping + `enable_external_access = false` & server-controlled `security.duckdbCliPath`, and dial-time private-network SSRF protection unless `security.allowPrivateNetworkForNonAdmin` is enabled). **Desktop GUI Edition** operates in pure zero-login local mode with full host file access.

---

## Keyboard Shortcuts

| Shortcut | Scope | Action |
| :--- | :--- | :--- |
| `Alt + Shift + N` | Global Workbench | Open **New Data Source Connection** modal |
| `Alt + T` | Global Workbench | Open a new **SQL Console** tab for the active connection |
| `Alt + W` | Global Workbench | Close the active workspace tab |
| `Alt + 1` … `Alt + 9` | Global Workbench | Switch directly to workspace tab `1`–`9` |
| `Ctrl / Cmd + B` | Global Workbench | Toggle left connection/object sidebar |
| `Alt + N` | Data Grid / Cell Input | **Add Row (Inline)** in Data Grid, or set current cell to `NULL` while editing |
| `Ctrl / Cmd + S` | Data Grid | Commit all staged inline row insertions & cell edits |
| `Ctrl / Cmd + C` / `Ctrl / Cmd + V` | Data Grid | Copy / paste single or multi-row TSV records (compatible with Excel & Notepad) |
| `Ctrl / Cmd + Enter` | SQL Console | Execute selected SQL or full script |

---

## Auto-Start Service Installation, Online Upgrade & Initial Admin Bootstrap

Cross-platform service installers for **Web Server Edition** are provided in `scripts/` (and bundled into `dist/`). They support:
- **Online GitHub Releases Download & Mandatory Checksum Verification**: Auto-downloads matching OS/Arch binaries (`--download`, `--version`, `--mirror` / `-Mirror`) with **fail-closed `SHA256SUMS.txt` verification** and a pre-install `-version` binary smoke test before touching any running service.
- **Headless Initial Admin Bootstrap**: `--admin-user` / `--admin-pass` and `-AdminUser` / `-AdminPass` (`>= 8` characters; wiped immediately after initialization without leaking credentials in process arguments).
- **Lock-Safe One-Command Online Upgrades (`upgrade` / `update` & `-Action Upgrade` / `-Action Update`)**: Preserves existing service flags, quoted `ExecStart` / `launchd` `ProgramArguments` paths, and `DataDir`, with graceful shutdown polling + atomic `.old` hot-replacement on Windows.

### 1. Linux (`systemd` System / User & `OpenRC`)

```bash
chmod +x ./dist/install-service.sh

# Standard TCP system service + initial admin bootstrap (>= 8 char password)
# (Defaults to /usr/local/bin/dbharbor & /var/lib/dbharbor, or pass --install-dir /opt/dbharbor)
sudo ./dist/install-service.sh install \
  --user dbharbor \
  --group dbharbor \
  --data-dir /var/lib/dbharbor \
  --data-dir-mode 0750 \
  --admin-user ops_admin \
  --admin-pass 'StrongPass!2026' \
  --host 127.0.0.1 --port 8787

# Unix Domain Socket service granting socket group access to Nginx (www-data / nginx)
sudo ./dist/install-service.sh install \
  --user dbharbor \
  --group dbharbor \
  --socket-group www-data \
  --supplementary-groups www-data \
  --unix-socket /run/dbharbor/dbharbor.sock \
  --unix-socket-perm 0660 \
  --admin-user ops_admin \
  --admin-pass 'StrongPass!2026'

# Non-root user service (systemd --user)
./dist/install-service.sh install --user-mode --port 8787

# One-command online upgrade (or 'update') with SHA256SUMS.txt verification & -version preflight
# (Supports --version <tag> and --mirror <url>, e.g. --mirror https://ghfast.top)
sudo ./dist/install-service.sh upgrade

# Lifecycle management
sudo ./dist/install-service.sh status
sudo ./dist/install-service.sh logs
sudo ./dist/install-service.sh restart
sudo ./dist/install-service.sh uninstall
```

### 2. macOS (`launchd` `LaunchDaemons` & `LaunchAgents`)

```bash
chmod +x ./dist/install-service.sh

# System-wide LaunchDaemon (with execution user/group & initial admin bootstrap)
sudo ./dist/install-service.sh install \
  --host 127.0.0.1 --port 8787 \
  --user _www --group _www \
  --admin-user ops_admin --admin-pass 'StrongPass!2026'

# Current logged-in user LaunchAgent (~/Library/LaunchAgents/io.dbharbor.server.plist)
./dist/install-service.sh install --user-mode --port 8787

# Online upgrade (preserves custom LaunchDaemon / LaunchAgent binary path) / status / logs / uninstall
sudo ./dist/install-service.sh upgrade
./dist/install-service.sh status
./dist/install-service.sh logs
./dist/install-service.sh uninstall --user-mode
```

### 3. Windows (`ScheduledTask` Service + `icacls` ACL Hardening)

```powershell
# Install auto-start background service (auto-downloads & verifies SHA256SUMS.txt if needed; data in C:\ProgramData\DBHarbor\data)
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 `
  -Action Install `
  -ServiceAccount "NT AUTHORITY\LocalService" `
  -PermissionGroup "Administrators" `
  -AdminUser "ops_admin" `
  -AdminPass "StrongPass!2026" `
  -HostAddr "127.0.0.1" -Port 8787

# Online Upgrade (or -Action Update; supports -Version <tag> and -Mirror <url>) / Status / Restart / Uninstall
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 -Action Upgrade
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 -Action Status
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 -Action Restart
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 -Action Uninstall
```

---

## Reverse Proxy Configuration (Nginx / Caddy / Apache)

Ready-to-use configuration templates are included in [`deploy/`](./deploy).

### Key Reverse Proxy Best Practices for DBHarbor
1. **Forward Real Client IP & Configure `trustedProxies`**: Forward `X-Real-IP` and `X-Forwarded-For` (and add non-loopback proxy IPs/CIDRs to `trustedProxies` in `<DataDir>/config.json` or `DBHARBOR_TRUSTED_PROXIES`) so DBHarbor's anti-bruteforce rate limiter, active device session tracker, and security audit log identify real client IPs securely.
2. **Disable Response Buffering** (`proxy_buffering off;` / `flush_interval -1`): Enables zero-memory streaming downloads for multi-gigabyte SQL Dump / CSV / JSONL exports.
3. **Increase Upload Body Limit** (`client_max_body_size 512m;`): Allows uploading large `.sql`, `.csv`, or `.json` files for batch import (adjust `maxImportMB` in `config.json` if needed).

### Option A: Nginx + Unix Domain Socket (Recommended Same-Host Setup)

See [`deploy/nginx-unix-socket.conf`](./deploy/nginx-unix-socket.conf):

```nginx
upstream dbharbor_uds {
    server unix:/run/dbharbor/dbharbor.sock;
    keepalive 32;
}

server {
    listen 443 ssl http2;
    server_name db.example.com;

    ssl_certificate     /etc/ssl/certs/db.example.com.pem;
    ssl_certificate_key /etc/ssl/private/db.example.com.key;

    client_max_body_size 512m;

    location / {
        proxy_pass http://dbharbor_uds;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        proxy_buffering off;
        proxy_request_buffering off;
        proxy_read_timeout 600s;
        proxy_send_timeout 600s;
    }
}
```

> **Socket Permission Tip**: Install DBHarbor with `--socket-group www-data` (Debian/Ubuntu) or `--socket-group nginx` (RHEL/CentOS/Alpine), or add the proxy worker user to the `dbharbor` group: `sudo usermod -aG dbharbor www-data && sudo systemctl restart nginx`.

### Option B: Nginx + Local TCP (`127.0.0.1:8787`)

Replace `server unix:/run/dbharbor/dbharbor.sock;` in the `upstream` block with `server 127.0.0.1:8787;` (see [`deploy/nginx-tcp.conf`](./deploy/nginx-tcp.conf)).

### Option C: Caddy v2 (Automatic HTTPS)

See [`deploy/Caddyfile`](./deploy/Caddyfile):

```caddyfile
db.example.com {
    request_body {
        max_size 512MB
    }
    # Unix Domain Socket (or replace with 127.0.0.1:8787 for TCP)
    reverse_proxy unix//run/dbharbor/dbharbor.sock {
        flush_interval -1
        header_up X-Real-IP {remote_host}
    }
}
```

### Option D: Apache httpd (`mod_proxy_http`)

See [`deploy/apache-vhost.conf`](./deploy/apache-vhost.conf):

```apache
ProxyPreserveHost On
ProxyTimeout 600
RequestHeader set X-Forwarded-Proto "https"
RequestHeader set X-Real-IP "%{REMOTE_ADDR}s"

ProxyPass        / "unix:/run/dbharbor/dbharbor.sock|http://localhost/" nocanon
ProxyPassReverse / "unix:/run/dbharbor/dbharbor.sock|http://localhost/"
```

---

## Open Source License

DBHarbor is licensed under the **[Apache License, Version 2.0](./LICENSE)** (`Apache-2.0`).
