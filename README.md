# DBHarbor — Cross-Platform Multi-Database Web Workbench

**Language**: **English** | [简体中文](./README.zh-CN.md) | [繁體中文](./README.zh-TW.md) | [日本語](./README.ja.md)

[![License: Apache-2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](./LICENSE)
[![Version](https://img.shields.io/badge/Version-v1.0.0-1677ff.svg)](./CHANGELOG.md)
[![CGO_ENABLED=0](https://img.shields.io/badge/Go-CGO__ENABLED%3D0-00ADD8.svg)](https://github.com/Willh92/DBHarbor.git)

A zero-dependency, single-binary cross-platform Web Database Workbench combining **phpMyAdmin's intuitive visual table & data operations** with **modern multi-engine IDE power**.

- **Repository**: [https://github.com/Willh92/DBHarbor.git](https://github.com/Willh92/DBHarbor.git)
- **Changelog**: [CHANGELOG.md](./CHANGELOG.md)
- **License**: [Apache License 2.0](./LICENSE)

---

## Key Features

- **14 Built-in Database Engines (`CGO_ENABLED=0` Pure Go)**:
  - **Analytical / OLAP**: ClickHouse (Native TCP & HTTP protocols, `MergeTree` visual builder, partition governance), DuckDB
  - **Relational RDBMS**: MySQL, MariaDB, TiDB, OceanBase, PostgreSQL, KingbaseES, SQLite 3, SQL Server, Oracle (pure-Go without Instant Client), Dameng (DM8)
  - **Key-Value & Document NoSQL**: Redis (DB0–15 browser, 6 data structure editors, embedded CLI), MongoDB (JSON & Table views, BSON CRUD, index & `RunCommand` console)
- **Complete Visual Schema Designer, Privileges & SQL Operations**:
  - **Visual Table Structure, Routines, Triggers & DB Privilege Governance**: Add, modify, rename, or drop columns; alter data types, defaults, comments, and `NULL` constraints; manage indexes & foreign keys; visually create & edit functions, procedures, and triggers with dialect-smart DDL templates; manage database-level users & grants (`GRANT` / `REVOKE`, Redis ACL, MongoDB roles); rename/clone tables; create views; transactional lossless table rebuilds on SQLite; live DDL preview.
  - **Navicat-Style Full-Height Data Grid & Smart Multi-Tab SQL Console**: Full-height data grid with click-to-insert blank rows, batch column editing, single/multi-row TSV clipboard copy & paste (`Ctrl+C` / `Ctrl+V` compatible with Notepad & Excel), local DB file browser/upload (`SQLite 3` / `DuckDB`), plus an embedded tree-shaken Monaco Editor with schema-aware autocomplete, `Ctrl/Cmd + Enter` execution, `EXPLAIN` query plans, SQL formatting, query cancellation (`AbortController`), execution history, and saved snippets.
  - **Streaming Import, Export & SQL Runner**: Full-database & multi-table SQL Dump, single-table/filtered/custom-query/MongoDB/Redis streaming exports & imports (`SQL`, `CSV-BOM`, `CSV`, `JSON`, `JSONL`, `Markdown`), batched transactional file imports, and direct paste-to-execute multi-statement SQL scripts.
- **Enterprise Multi-User RBAC, Login Hardening & RFC 6238 TOTP 2FA**:
  - Web Server Edition provides 3-tier multi-user RBAC (`Admin` / `Read-Write` / `Read-Only`) with shared/private connection isolation, sliding-window IP/account anti-bruteforce rate limiting (`HTTP 429`), stateless HMAC SVG captcha, offline SVG QR code TOTP 2FA setup + 8 single-use recovery codes, revocable device sessions, security audit logs, and AES-256-GCM encrypted connection credentials.
- **Native Dark Window Theme, Mobile Responsive Workbench & Multi-Language UI**:
  - Native OS dark title bar synchronization on Desktop (`Win32 DWM`, `macOS DarkAqua`, `Linux GTK3`), plus a dedicated responsive mobile workbench (`<= 640px`) with bottom-sheet row editors and zero horizontal overflow.
  - Supports both **TCP** and **Unix Domain Socket (`AF_UNIX`)** listening (`-unix-socket`, `-unix-socket-perm`, `-unix-socket-group`).
  - Built-in i18n UI & documentation in **English (`en-US`)**, **Simplified Chinese (`zh-CN`)**, **Traditional Chinese (`zh-TW`)**, and **Japanese (`ja-JP`)**.
  - Online version update checker, configurable update reminder switch, and changelog sync powered by `Willh92/DBHarbor` on GitHub.

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

1. **Desktop GUI Edition (`dbharbor-desktop-<os>-<arch>` & `DBHarbor-darwin-<arch>.app`) — Pure Local Client**:
   - Double-click the executable (e.g. `dbharbor-desktop-windows-amd64.exe` or `DBHarbor-darwin-arm64.app`) to launch a native OS application window directly (`Win32 + Edge WebView2` on Windows without any console window, `Cocoa NSWindow + WKWebView` on macOS, `GTK3 + WebKit2GTK` on Linux).
   - **No Account / Login System**: Operates as a pure local desktop client — boots straight into the workbench with native OS Save As dialogs and an AES-256-GCM encrypted local vault isolated via same-site loopback protection.
2. **Web Server Edition (`dbharbor-<os>-<arch>`) — Self-Hosted / Team Service**:
   - Launch `./dist/dbharbor-<os>-<arch>` (or `-version` to inspect build info) and open `http://localhost:8787` (or bind a Unix Domain Socket via `-unix-socket`).
   - **Default initial credentials**: `admin` / `admin123` (change password and enable 2FA immediately via **Security / 2FA** in the top bar).

---

## Default OS Data Directories & CLI Flags

DBHarbor **never** writes runtime state into the current startup directory unless explicitly requested via `-data-dir`. By default, internal metadata (`dbharbor.db`) and the AES-256-GCM master key (`.secret.key`) are stored in standard OS application directories:

| Platform | User / Desktop Launch | System Service / Daemon |
| :--- | :--- | :--- |
| **Windows** | `%LOCALAPPDATA%\DBHarbor\data` | `%ProgramData%\DBHarbor\data` (`C:\ProgramData\DBHarbor\data`) |
| **macOS** | `~/Library/Application Support/DBHarbor/data` | `/Library/Application Support/DBHarbor/data` |
| **Linux** | `$XDG_DATA_HOME/dbharbor` (`~/.local/share/dbharbor`) | `/var/lib/dbharbor` |

### CLI Options & Environment Variables

| CLI Flag | Environment Variable | Default | Description |
| :--- | :--- | :--- | :--- |
| `-host` | `DBHARBOR_HOST` | `0.0.0.0` | TCP bind address (or `unix:/path/to/dbharbor.sock` shorthand) |
| `-port` | `DBHARBOR_PORT` | `8787` | HTTP TCP port |
| `-unix-socket` | `DBHARBOR_UNIX_SOCKET` | *(empty)* | Listen on Unix Domain Socket instead of TCP (e.g. `/run/dbharbor/dbharbor.sock`) |
| `-unix-socket-perm` | `DBHARBOR_UNIX_SOCKET_PERM` | `0660` | Octal file mode applied to the Unix Domain Socket |
| `-unix-socket-group` | `DBHARBOR_UNIX_SOCKET_GROUP` | *(empty)* | POSIX group name or GID assigned to the Unix Domain Socket (e.g. `www-data`, `nginx`) |
| `-data-dir` | `DBHARBOR_DATA_DIR` | *(OS standard)* | Custom directory for SQLite metadata store & encryption key |
| `-admin-user` | `DBHARBOR_ADMIN_USER` | `admin` | Initial admin username on first bootstrap |
| `-admin-pass` | `DBHARBOR_ADMIN_PASS` | `admin123` | Initial admin password on first bootstrap |
| `-idle-ttl` | — | `30` | Idle database connection pool eviction timeout (minutes) |
| `-open` | — | `false` | Open system browser automatically on TCP startup |
| `-version` | — | `false` | Print version, commit, and platform info |

> **Local File Security Boundary**: DBHarbor's internal metadata database (`dbharbor.db`) and encryption keys (`.secret.key`) are automatically hidden and protected from user database connections. In Web Server Edition, non-admin (`Read-Write`) accounts are strictly sandboxed within `<DataDir>` (defaulting to `<DataDir>/local_databases`) when browsing or creating SQLite / DuckDB files, while `Admin` accounts and Desktop GUI Edition retain full host directory access.

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

## Auto-Start Service Installation & Role / Group Configuration

Cross-platform service managers are provided in `scripts/` (and bundled into `dist/`):

### 1. Linux (`systemd` System / User & `OpenRC`)

```bash
chmod +x ./dist/install-service.sh

# Standard TCP system service with dedicated user & permission group (default: dbharbor:dbharbor)
sudo ./dist/install-service.sh install \
  --user dbharbor \
  --group dbharbor \
  --data-dir /var/lib/dbharbor \
  --data-dir-mode 0750 \
  --host 127.0.0.1 --port 8787

# Unix Domain Socket service granting socket group access to Nginx (www-data / nginx)
sudo ./dist/install-service.sh install \
  --user dbharbor \
  --group dbharbor \
  --socket-group www-data \
  --supplementary-groups www-data \
  --unix-socket /run/dbharbor/dbharbor.sock \
  --unix-socket-perm 0660

# Non-root user service (systemd --user)
./dist/install-service.sh install --user-mode --port 8787

# Lifecycle management
sudo ./dist/install-service.sh status
sudo ./dist/install-service.sh logs
sudo ./dist/install-service.sh restart
sudo ./dist/install-service.sh uninstall
```

### 2. macOS (`launchd` `LaunchDaemons` & `LaunchAgents`)

```bash
chmod +x ./dist/install-service.sh

# System-wide LaunchDaemon (with optional execution user & group)
sudo ./dist/install-service.sh install --host 127.0.0.1 --port 8787 --user _www --group _www

# Current logged-in user LaunchAgent (~/Library/LaunchAgents/io.dbharbor.server.plist)
./dist/install-service.sh install --user-mode --port 8787

# Check status / tail logs / uninstall
./dist/install-service.sh status
./dist/install-service.sh logs
./dist/install-service.sh uninstall --user-mode
```

### 3. Windows (`ScheduledTask` Service + `icacls` ACL Hardening)

```powershell
# Install auto-start background service (stores data in C:\ProgramData\DBHarbor\data)
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 `
  -Action Install `
  -ServiceAccount "NT AUTHORITY\LocalService" `
  -PermissionGroup "Administrators" `
  -HostAddr "127.0.0.1" -Port 8787

# Status / Restart / Uninstall
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 -Action Status
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 -Action Restart
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 -Action Uninstall
```

---

## Reverse Proxy Configuration (Nginx / Caddy / Apache)

Ready-to-use configuration templates are included in [`deploy/`](./deploy).

### Key Reverse Proxy Best Practices for DBHarbor
1. **Forward Real Client IP** (`X-Real-IP` and `X-Forwarded-For`): Ensures DBHarbor's anti-bruteforce rate limiter, active device session tracker, and security audit log identify real client IPs instead of the proxy loopback address.
2. **Disable Response Buffering** (`proxy_buffering off;` / `flush_interval -1`): Enables zero-memory streaming downloads for multi-gigabyte SQL Dump / CSV / JSONL exports.
3. **Increase Upload Body Limit** (`client_max_body_size 512m;`): Allows uploading large `.sql`, `.csv`, or `.json` files for batch import.

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
