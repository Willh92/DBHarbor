# DBHarbor — 跨平台多資料庫 Web 管理工作台

**語言切換**: [English](./README.md) | [简体中文](./README.zh-CN.md) | **繁體中文** | [日本語](./README.ja.md)

[![License: Apache-2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](./LICENSE)
[![Version](https://img.shields.io/badge/Version-v1.0.1-1677ff.svg)](./CHANGELOG.zh-TW.md)
[![CGO_ENABLED=0](https://img.shields.io/badge/Go-CGO__ENABLED%3D0-00ADD8.svg)](https://github.com/Willh92/DBHarbor.git)

融合 **phpMyAdmin 經典直覺式資料表操作體驗** 與 **現代多引擎 IDE 能力** 的零外部相依性單一執行檔跨平台 Web 資料庫管理平台。

- **官方程式碼儲存庫**：[https://github.com/Willh92/DBHarbor.git](https://github.com/Willh92/DBHarbor.git)
- **版本更新日誌**：[CHANGELOG.zh-TW.md](./CHANGELOG.zh-TW.md)
- **開放原始碼授權**：[Apache License 2.0](./LICENSE)

---

## 核心特色

- **內建 14 種資料庫驅動程式 (`CGO_ENABLED=0` 純 Go)**：
  - **OLAP 分析型**：ClickHouse（Native TCP / HTTP 雙協定、`MergeTree` 視覺化建表、分區治理）、DuckDB
  - **關聯式 RDBMS**：MySQL、MariaDB、TiDB、OceanBase、PostgreSQL、KingbaseES、SQLite 3、SQL Server、Oracle（純 Go 免安裝 Instant Client）、達夢 DM8
  - **鍵值與文件 NoSQL**：Redis（DB0–15 瀏覽器、6 種資料結構編輯器、內嵌 CLI）、MongoDB（JSON / 表格雙檢視、BSON CRUD、索引與 `RunCommand` 終端機）
- **完整的視覺化結構設計、資料庫權限指派與 SQL 維運**：
  - **視覺化資料表結構、函式/觸發程序建立與資料庫權限治理**：新增/修改/重新命名欄位、變更型別與 `NULL` 約束、索引與外部索引鍵管理、視覺化新增與編輯函式/預存程序/觸發程序（內建多方言智慧範本）、資料庫級帳號與權限指派（`GRANT` / `REVOKE`、Redis ACL、MongoDB 角色）、複製/重新命名資料表、建立檢視表、SQLite 交易級無損重建資料表、SQLite 極速物件清單載入與即時 DDL 預覽。
  - **Navicat 級鋪滿資料網格、空白列直接新增與多頁籤智慧 SQL 終端機**：資料網格自動鋪滿工作區高度並支援點擊空白列直接行內新增、批次修改欄位、單列/多列整列 TSV 複製貼上（`Ctrl+C` / `Ctrl+V` 相容記事本與 Excel）、本機資料庫檔案瀏覽與上傳（`SQLite 3` / `DuckDB`），內嵌輕量化 Monaco Editor 支援自動補全、`Ctrl/Cmd + Enter` 執行、`EXPLAIN` 查詢計畫、SQL 排版、查詢中斷 (`AbortController`) 與歷史紀錄。
  - **串流匯入匯出與批次資料表維運**：支援整庫與多表勾選 SQL Dump、單表/篩選結果/自訂查詢/MongoDB 集合/Redis 鍵值串流匯入匯出（`SQL` / `CSV-BOM` / `CSV` / `JSON` / `JSONL` / `Markdown`）、DuckDB / MongoDB 串流記憶體上限保護、批次匯入與直接貼上執行 SQL 腳本。
- **Web 多成員三級 RBAC、登入品牌自訂、安全沙箱與管理員緊急復原 *(僅 Web 服務版)***：
  - **零預設弱密碼初始化、登入頁品牌自訂與防搜尋引擎收錄**：首次啟動開啟一次性**初始化管理員帳號精靈**（密碼至少 `8` 碼，移除預設 `admin123`）、零指紋登入介面、支援自訂瀏覽器 `<title>`、頂部圖示（上傳 `<= 128 KB` 圖示並同步 Favicon）與主副標題，內建 `/robots.txt`、`X-Robots-Tag` 與 `<meta name="robots">` 三重防爬蟲收錄保護。
  - **多成員 RBAC、2FA 與縱深安全強化**：三級 RBAC（`Admin` / `Read-Write` / `Read-Only`）、金鑰衍生單次有效圖形驗證碼、`trustedProxies` 可信反向代理限流 (`HTTP 429`)、離線 QR Code **RFC 6238 TOTP 雙重驗證** + 8 組一次性備用恢復碼 + 2FA 嘗試次數限制、嚴格 TLS 憑證驗證、非管理員 `<DataDir>/local_databases/` 沙箱（Web 模式停用 SQLite `ATTACH` 與 DuckDB 外部檔案存取）、非管理員內網 SSRF 防護、引用計數並行連線池安全回收、SQL Server 獨佔連線 `USE [db]` 隔離，以及 `<DataDir>/config.json` 持久化設定與**自動清除式管理員緊急復原**。
- **原生深色標題列同步、手機端全觸控工作台與多語言支援 (i18n)**：
  - 桌面端支援系統級原生深色標題列同步（`Win32 DWM`、`macOS DarkAqua`、`Linux GTK3`）；手機與觸控裝置支援原生 GPU CSS `position: sticky` 零閃爍固定表頭、手指拖曳調整欄寬 + 雙擊自動展開欄寬、側欄與表格觸控 `[⋮]` 及長按選單、手機 SQL 快捷符號列、點選檢視截斷儲存格、底部抽屜編輯器、左側導覽列 `keep-alive` 記憶體快取與全域彈出選單邊界防溢出定位。
  - 同時支援 **TCP 連接埠** 與 **Unix Domain Socket (`AF_UNIX`)** 監聽（`-unix-socket`、`-unix-socket-perm`、`-unix-socket-group`）。
  - 內建 **English (`en-US`)、简体中文 (`zh-CN`)、繁體中文 (`zh-TW`)、日本語 (`ja-JP`)** 介面切換，並支援連線 `Willh92/DBHarbor` 檢測最新版本與一鍵指令升級背景服務。

---

## 快速啟動與建置

```bash
# 1. 開發熱重載模式 (Go API :8787 + Vite :5173)
npm run dev

# 2. 編譯目前作業系統 Web 服務版 + 原生桌面視窗版執行檔 (輸出至 ./dist)
npm run build

# 3. 交叉編譯全部 6 大平台（服務版 + 桌面視窗版 + macOS .app 套件 + 服務安裝腳本 + 反向代理範本）
npm run build:all

# 4. 執行自動化測試
npm test
```

### 雙版本形態說明 (`dist/`)

1. **原生桌面視窗版 (`dbharbor-desktop-<os>-<arch>` / `DBHarbor-darwin-<arch>.app`) — 純本機免登入用戶端**：
   - 滑鼠雙擊直接開啟原生桌面視窗（Windows 無黑框直啟，**無使用者帳戶/登入體系**，純本機 AES-256-GCM 加密連線庫、完整本機檔案存取與原生另存新檔對話方塊）。伺服端專屬功能（`<DataDir>/config.json`、首次管理員初始化精靈、登入頁自訂、管理員緊急復原與 Web 本機檔案/內網沙箱）**不適用於桌面視窗版**。
2. **Web 服務版 (`dbharbor-<os>-<arch>`) — 伺服端部署 / 團隊共用服務**：
   - 啟動後於瀏覽器開啟 `http://localhost:8787`（無內建預設弱密碼——首次造訪自動開啟一次性**管理員初始化精靈**，密碼至少 `8` 碼，並可自訂登入頁標題與圖示；亦可透過 `-admin-user` / `-admin-pass` 預先建立管理員，並內建三重防搜尋引擎收錄）。

---

## 作業系統標準資料目錄、CLI 參數與持久化 `config.json`

DBHarbor **預設不會將資料寫入程式啟動目錄**，而是自動儲存於各作業系統標準應用資料路徑（可透過 `-data-dir` 自訂）：

| 作業系統 | 一般使用者 / 桌面啟動預設路徑 | 系統背景服務 (Daemon / Service) 預設路徑 |
| :--- | :--- | :--- |
| **Windows** | `%LOCALAPPDATA%\DBHarbor\data` | `%ProgramData%\DBHarbor\data` (`C:\ProgramData\DBHarbor\data`) |
| **macOS** | `~/Library/Application Support/DBHarbor/data` | `/Library/Application Support/DBHarbor/data` |
| **Linux** | `$XDG_DATA_HOME/dbharbor` (`~/.local/share/dbharbor`) | `/var/lib/dbharbor` |

### 命令列參數與環境變數 *(Web 服務版)*

設定優先順序：**`CLI 命令列參數 > DBHARBOR_* 環境變數 > <DataDir>/config.json > 內建預設值`**。

| 命令列參數 | 環境變數 | 預設值 | 說明 |
| :--- | :--- | :--- | :--- |
| `-config` | `DBHARBOR_CONFIG_FILE` | `<DataDir>/config.json` | Web 服務版持久化 JSON 設定檔路徑（`0600` 權限） |
| `-host` | `DBHARBOR_HOST` | `0.0.0.0` | TCP 監聽位址（支援 `unix:/path/to/dbharbor.sock` 簡寫） |
| `-port` | `DBHARBOR_PORT` | `8787` | HTTP TCP 連接埠 |
| `-unix-socket` | `DBHARBOR_UNIX_SOCKET` | *(空)* | 啟用 Unix Domain Socket 監聽（如 `/run/dbharbor/dbharbor.sock`） |
| `-unix-socket-perm` | `DBHARBOR_UNIX_SOCKET_PERM` | `0660` | Unix Socket 八進位檔案權限 |
| `-unix-socket-group` | `DBHARBOR_UNIX_SOCKET_GROUP` | *(空)* | Unix Socket 所屬群組名稱或 GID（如 `www-data`、`nginx`） |
| `-data-dir` | `DBHARBOR_DATA_DIR` | *(系統標準目錄)* | 自訂內部元資料庫、`config.json` 與加密金鑰目錄 |
| `-admin-user` | `DBHARBOR_ADMIN_USER` | *(空 — 網頁精靈)* | 首次無頭初始化管理員帳號（僅於使用者資料表為空時生效） |
| `-admin-pass` | `DBHARBOR_ADMIN_PASS` | *(空 — 網頁精靈)* | 首次無頭初始化管理員密碼（**至少 8 碼**；留空則使用網頁初始化精靈） |
| `-idle-ttl` | `DBHARBOR_IDLE_TTL` | `30` | 閒置資料庫連線池自動回收逾時（分鐘） |
| — | `DBHARBOR_MAX_IMPORT_MB` | `256` | 檔案匯入上傳體積上限（MB） |
| — | `DBHARBOR_TRUSTED_PROXIES` | *(本機迴路)* | 信任 `X-Forwarded-For` / `X-Real-IP` 的反向代理 IP/CIDR 清單 |
| — | `DBHARBOR_CORS_ORIGINS` | *(同源)* | 允許跨來源存取的 Origin 清單 |
| — | `DBHARBOR_DUCKDB_PATH` | *(自動偵測)* | 伺服端受控 DuckDB CLI 路徑 (`security.duckdbCliPath`) |
| — | `DBHARBOR_ALLOW_PRIVATE_NET_NON_ADMIN` | `false` | 是否允許非管理員連線私網/本機迴路位址 |
| `-init-only` | — | `false` | 初始化金鑰、`config.json` 與管理員帳號後立即結束 |
| `-recover-admin` | — | `false` | 執行單次管理員密碼 / 2FA 緊急重設後結束 |
| `-recover-user` | — | *(主管理員)* | 指定 `-recover-admin` 目標管理員帳號 |
| `-recover-pass` | — | *(空)* | 新管理員密碼（`>= 8` 碼）或 `auto`（隨機產生 16 碼強密碼） |
| `-reset-2fa` | — | `false` | 於 `-recover-admin` 時一併重設 TOTP 2FA 與備用恢復碼 |
| `-open` | — | `false` | 啟動後自動開啟預設瀏覽器 |
| `-version` | — | `false` | 顯示版本號、Commit 與平台資訊 |

### 持久化設定檔 (`<DataDir>/config.json`) 與管理員緊急復原 *(僅 Web 服務版)*

**Web 服務版**啟動時若 `<DataDir>/config.json` 不存在，會自動以 `0600` 權限建立設定檔（包含 `host`、`port`、`unixSocket`、`idleConnTTLMinutes`、`maxImportMB`、`trustedProxies`、`corsAllowedOrigins`、`security` 與 `recovery`）：

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

當遺忘管理員密碼或遺失 2FA 裝置時，可透過以下兩種方式復原：
1. **編輯 `<DataDir>/config.json` 並重啟服務**：將 `"recovery"` 設為 `{ "enabled": true, "adminUsername": "admin", "newPassword": "YourNewStrongPassword", "reset2FA": true }`（或 `"newPassword": "auto"` 自動產生 16 碼隨機密碼印出至啟動日誌）並重啟 DBHarbor。系統會自動備份 `dbharbor.db` 至 `<DataDir>/.backups/`、重設密碼/2FA、撤銷舊工作階段，並**立即自動將 `config.json` 重設為 `enabled: false, newPassword: ""`**，確保明文密碼不殘留於磁碟。
2. **單次 CLI 指令復原**：`dbharbor -data-dir /var/lib/dbharbor -recover-admin -recover-pass auto -reset-2fa`

> **Web 服務版本機檔案與網路安全邊界**：DBHarbor 內部元資料庫（`dbharbor.db`）、`config.json` 與主金鑰（`.secret.key`）在所有模式下皆自動隱藏保護。於 **Web 服務版**中，非管理員（`Read-Write`）成員操作 SQLite / DuckDB 本機檔案時嚴格限制於 `<DataDir>/local_databases/` 沙箱內（停用 SQLite `ATTACH`、強制伺服端控管 `security.duckdbCliPath` 與 DuckDB `enable_external_access = false`，並預設封鎖非管理員連線內網 SSRF 目標）；**原生桌面視窗版**運行於純本機免登入模式，保有完整本機目錄存取權限。

---

## 常用快捷鍵一覽

| 快捷鍵 | 適用範圍 | 功能說明 |
| :--- | :--- | :--- |
| `Alt + Shift + N` | 全域工作台 | 開啟 **新增資料來源連線** 視窗 |
| `Alt + T` | 全域工作台 | 為目前連線開啟新的 **SQL 終端機** 分頁 |
| `Alt + W` | 全域工作台 | 關閉目前使用中的工作區分頁 |
| `Alt + 1` … `Alt + 9` | 全域工作台 | 快速切換至第 `1`–`9` 個工作區分頁 |
| `Ctrl / Cmd + B` | 全域工作台 | 展開 / 收合左側資料來源與資料表導覽列 |
| `Alt + N` | 資料網格 / 儲存格編輯 | 於資料表瀏覽狀態觸發 **列內新增**；於儲存格輸入框內將目前值設為 `NULL` |
| `Ctrl / Cmd + S` | 資料網格 | 一鍵提交目前暫存的列內新增與儲存格修改 |
| `Ctrl / Cmd + C` / `Ctrl / Cmd + V` | 資料網格 | 複製 / 貼上單列或多列標準 TSV 文字（相容 Excel 與記事本） |
| `Ctrl / Cmd + Enter` | SQL 終端機 | 執行選取的 SQL 語句或整份指令碼 |

---

## 開機自啟動服務安裝、線上更新與初始管理員設定

`scripts/` 與建置產物 `dist/` 內建 **Web 服務版**跨平台服務管理指令碼，支援**自動從 GitHub Releases 下載並強制驗證 `SHA256SUMS.txt` + 覆寫前 `-version` 執行檔檢測**、鏡像加速（`--mirror` / `-Mirror`）、自訂安裝目錄（`--install-dir` / `-InstallDir`）、**首次部署初始化管理員帳號密碼**（`--admin-user` / `--admin-pass` `>= 8` 碼，初始化完成後自動抹除暫存憑證）與**防鎖定一鍵線上升級**（`upgrade` / `update` 與 `-Action Upgrade` / `-Action Update`，完整保留既有啟動旗標與 `DataDir`）：

### 1. Linux (`systemd` / `OpenRC`)

```bash
chmod +x ./dist/install-service.sh

# Unix Domain Socket 模式 + 指定執行角色、Nginx 權限群組 (www-data / nginx) 與初始管理員帳號（密碼至少 8 碼）
sudo ./dist/install-service.sh install \
  --user dbharbor \
  --group dbharbor \
  --data-dir /var/lib/dbharbor \
  --socket-group www-data \
  --supplementary-groups www-data \
  --unix-socket /run/dbharbor/dbharbor.sock \
  --unix-socket-perm 0660 \
  --admin-user ops_admin \
  --admin-pass 'StrongPass!2026'

# 本地 TCP 模式
sudo ./dist/install-service.sh install --user dbharbor --group dbharbor --host 127.0.0.1 --port 8787

# 一鍵從 GitHub Releases 線上更新並重新啟動服務（相容 update 別名；支援 --mirror <url> 與 --version <tag>）
sudo ./dist/install-service.sh upgrade
```

### 2. macOS (`launchd`)

```bash
chmod +x ./dist/install-service.sh
sudo ./dist/install-service.sh install \
  --host 127.0.0.1 --port 8787 \
  --user _www --group _www \
  --admin-user ops_admin --admin-pass 'StrongPass!2026'
# 或非 root 使用者模式：
./dist/install-service.sh install --user-mode --port 8787

# 線上更新（自動保留 plist 自訂安裝路徑）
sudo ./dist/install-service.sh upgrade
```

### 3. Windows (`ScheduledTask` + `icacls` 權限控管)

```powershell
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 `
  -Action Install `
  -ServiceAccount "NT AUTHORITY\LocalService" `
  -PermissionGroup "Administrators" `
  -AdminUser "ops_admin" `
  -AdminPass "StrongPass!2026" `
  -HostAddr "127.0.0.1" -Port 8787

# 一鍵線上更新並安全重啟服務（相容 -Action Update；支援 -Mirror <url>）
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 -Action Upgrade
```

---

## 反向代理設定說明 (Nginx / Caddy / Apache)

完整範本位於 [`deploy/`](./deploy) 目錄（包含 [`nginx-unix-socket.conf`](./deploy/nginx-unix-socket.conf)、[`nginx-tcp.conf`](./deploy/nginx-tcp.conf)、[`Caddyfile`](./deploy/Caddyfile)、[`apache-vhost.conf`](./deploy/apache-vhost.conf)；若反向代理非本機迴路位址，請將其 IP/CIDR 加入 `<DataDir>/config.json` 的 `trustedProxies`）：

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
    }
}
```

---

## 開放原始碼授權 (License)

本專案採用 **[Apache License, Version 2.0](./LICENSE)** (`Apache-2.0`) 授權發佈。
