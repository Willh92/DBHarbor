# Changelog

**Language**: **English** | [简体中文](./CHANGELOG.zh-CN.md) | [繁體中文](./CHANGELOG.zh-TW.md) | [日本語](./CHANGELOG.ja.md)

All notable changes to DBHarbor are documented in this file and synchronized with [Willh92/DBHarbor](https://github.com/Willh92/DBHarbor.git).

## [v1.0.0] - 2026-11-07

### Initial Release
- **Dual Editions — Native Desktop GUI & Web Server (`CGO_ENABLED=0`)**:
  - **Desktop GUI Edition (`dbharbor-desktop-*` & macOS `DBHarbor.app`)**: Double-click native GUI window (`Win32 + Edge WebView2` on Windows with `-H=windowsgui` zero console window, `Cocoa NSWindow + WKWebView` on macOS, `GTK3 + WebKit2GTK` on Linux) operating in **pure local mode** without any account/login system, native dark OS title bar sync, native file dialogs for `.db/.sqlite/.duckdb` selection & Save As exports, multi-window local session isolation, and AES-256-GCM encrypted local connection vault.
  - **Web Server Edition (`dbharbor-*`)**: Standalone single-file web workbench supporting both TCP and native `AF_UNIX` Unix Domain Socket listening (`-unix-socket`, `-unix-socket-perm`, `-unix-socket-group`), automatic stale socket cleanup, `X-Forwarded-For` / `X-Real-IP` resolution, server-side local database directory browser & `.db/.sqlite/.duckdb` upload, and production reverse proxy templates for Nginx, Caddy, and Apache (`deploy/`).
- **14 Built-in Database Engines**: Pure-Go (`CGO_ENABLED=0`) drivers for ClickHouse (Native TCP & HTTP + partition governance), DuckDB, MySQL, MariaDB, TiDB, OceanBase, PostgreSQL, KingbaseES, SQLite 3, SQL Server, Oracle (no Instant Client required), Dameng DM8, Redis (DB0–15 browser, 6 data structures, JSON key backup/restore, CLI), and MongoDB (JSON/Table views, BSON CRUD, JSON/JSONL document import & export, indexes, `RunCommand`).
- **Full Visual Schema Designer, Navicat-Style Data Grid, Routines/Triggers & Streaming Transfer**:
  - Visual column add/modify/rename/drop, index create/modify/drop & foreign key management, SQL template generator, table rename/clone, `CREATE VIEW`, visual & dialect-smart `CREATE / EDIT PROCEDURE, FUNCTION & TRIGGER`, database-level user & privilege governance (`GRANT` / `REVOKE` / Redis ACL / MongoDB roles), transactional lossless SQLite table rebuilds, and live DDL preview.
  - Full-height spreadsheet-grade data grid with bottom-pinned horizontal scrollbar, click-blank-row direct insertion, multi-row batch column editing, single/multi-row TSV clipboard copy & paste (`Ctrl+C` / `Ctrl+V` compatible with Notepad & Excel), and multi-tab selective-feature Monaco SQL Console (`Ctrl/Cmd + Enter`, `EXPLAIN`, SQL formatter, `AbortController` cancellation, instant client-side result file save, history, and snippets).
  - Mobile-first (`<= 640px`) responsive workbench layout featuring a compact header, single-pill tab switcher, collapsible filter bar, sticky mutation/selection footer, `85dvh` bottom-sheet row editor, and `Keys | Value | CLI` Redis mobile switcher.
  - Streaming full-database SQL Dump, multi-table/filtered/query/MongoDB export (`SQL`, `CSV-BOM`, `CSV`, `JSON`, `JSONL`, `Markdown`), batched transactional file imports, and direct paste-to-execute SQL scripts.
- **Enterprise Web Multi-User RBAC, Security & Service Governance**:
  - Web Server Edition includes multi-user 3-tier RBAC (`Admin` / `Read-Write` / `Read-Only`) with shared/private connection isolation, sliding-window IP/account anti-bruteforce rate limiting (`HTTP 429`), stateless HMAC SVG captcha, offline SVG QR code RFC 6238 TOTP 2FA + 8 single-use recovery codes, revocable device sessions, and security audit logs.
  - Runtime metadata and `.secret.key` default to OS-standard application data directories (`%LOCALAPPDATA%\DBHarbor\data` / `%ProgramData%\DBHarbor\data` on Windows, `~/Library/Application Support/DBHarbor/data` on macOS, `$XDG_DATA_HOME/dbharbor` or `/var/lib/dbharbor` on Linux) rather than the working directory, with auto-start service installers (`install-service.sh`, `install-service.ps1`) supporting explicit user roles and permission groups.
- **English-First Codebase, Multi-Language UI & Online Update Reminder**:
  - Built-in real-time i18n UI & documentation in **English (`en-US`)**, **Simplified Chinese (`zh-CN`)**, **Traditional Chinese (`zh-TW`)**, and **Japanese (`ja-JP`)**.
  - Online version & changelog checker synced with `Willh92/DBHarbor` on GitHub with proactive update notification, auto-reminder toggle switch, per-version skip, automatic desktop vs server asset matching, and licensed under **Apache License 2.0 (`Apache-2.0`)**.
