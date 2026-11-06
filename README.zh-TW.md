# DBHarbor — 跨平台多資料庫 Web 管理工作台

**語言切換**: [English](./README.md) | [简体中文](./README.zh-CN.md) | **繁體中文** | [日本語](./README.ja.md)

[![License: Apache-2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](./LICENSE)
[![Version](https://img.shields.io/badge/Version-v1.0.0-1677ff.svg)](./CHANGELOG.zh-TW.md)
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
  - **視覺化資料表結構、函式/觸發程序建立與資料庫權限治理**：新增/修改/重新命名欄位、變更型別與 `NULL` 約束、索引與外部索引鍵管理、視覺化新增與編輯函式/預存程序/觸發程序（內建多方言智慧範本）、資料庫級帳號與權限指派（`GRANT` / `REVOKE`、Redis ACL、MongoDB 角色）、複製/重新命名資料表、建立檢視表、SQLite 交易級無損重建資料表與即時 DDL 預覽。
  - **Navicat 級鋪滿資料網格、空白列直接新增與多頁籤智慧 SQL 終端機**：資料網格自動鋪滿工作區高度並支援點擊空白列直接行內新增、批次修改欄位、單列/多列整列 TSV 複製貼上（`Ctrl+C` / `Ctrl+V` 相容記事本與 Excel）、本機資料庫檔案瀏覽與上傳（`SQLite 3` / `DuckDB`），內嵌輕量化 Monaco Editor 支援自動補全、`Ctrl/Cmd + Enter` 執行、`EXPLAIN` 查詢計畫、SQL 排版、查詢中斷 (`AbortController`) 與歷史紀錄。
  - **串流匯入匯出與批次資料表維運**：支援整庫與多表勾選 SQL Dump、單表/篩選結果/自訂查詢/MongoDB 集合/Redis 鍵值串流匯入匯出（`SQL` / `CSV-BOM` / `CSV` / `JSON` / `JSONL` / `Markdown`）、批次匯入與直接貼上執行 SQL 腳本。
- **Web 多成員三級 RBAC 權限、登入安全防護與雙重驗證 (2FA)**：
  - Web 服務版支援三級多成員 RBAC（`Admin` / `Read-Write` / `Read-Only`）與共享/私有連線授權隔離、滑動視窗防暴力破解速率限制 (`HTTP 429`)、無狀態 HMAC SVG 圖形驗證碼、RFC 6238 TOTP 雙重驗證（純離線產生 QR Code + 8 組一次性備用恢復碼）、裝置工作階段強制登出、安全稽核日誌與 AES-256-GCM 憑證加密。
- **原生深色標題列同步、手機端專屬自適應排版與多語言支援 (i18n)**：
  - 桌面端支援系統級原生深色標題列同步（`Win32 DWM`、`macOS DarkAqua`、`Linux GTK3`），並針對手機端 (`<= 640px`) 專屬重構緊湊頂欄與底部抽屜編輯器。
  - 同時支援 **TCP 連接埠** 與 **Unix Domain Socket (`AF_UNIX`)** 監聽（`-unix-socket`、`-unix-socket-perm`、`-unix-socket-group`）。
  - 內建 **English (`en-US`)、简体中文 (`zh-CN`)、繁體中文 (`zh-TW`)、日本語 (`ja-JP`)** 介面切換，並支援連線 `Willh92/DBHarbor` 檢測最新版本、主動更新提醒與提醒開關控制。

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

- **原生桌面視窗版 (`dbharbor-desktop-<os>-<arch>` / `DBHarbor-darwin-<arch>.app`)**：滑鼠雙擊直接開啟原生桌面視窗（無黑框、**無帳戶登入體系**，純本機加密連線庫與原生另存新檔對話方塊）。
- **Web 服務版 (`dbharbor-<os>-<arch>`)**：啟動後於瀏覽器開啟 `http://localhost:8787`（預設管理員帳號密碼：`admin` / `admin123`，首次登入後請立即修改密碼並啟用雙重驗證）。

---

## 作業系統標準資料目錄

DBHarbor **預設不會將資料寫入程式啟動目錄**，而是自動儲存於各作業系統標準應用資料路徑（可透過 `-data-dir` 自訂）：

| 作業系統 | 一般使用者 / 桌面啟動預設路徑 | 系統背景服務 (Daemon / Service) 預設路徑 |
| :--- | :--- | :--- |
| **Windows** | `%LOCALAPPDATA%\DBHarbor\data` | `%ProgramData%\DBHarbor\data` (`C:\ProgramData\DBHarbor\data`) |
| **macOS** | `~/Library/Application Support/DBHarbor/data` | `/Library/Application Support/DBHarbor/data` |
| **Linux** | `$XDG_DATA_HOME/dbharbor` (`~/.local/share/dbharbor`) | `/var/lib/dbharbor` |

> **本機資料庫檔案安全邊界**：DBHarbor 內部元資料庫（`dbharbor.db`）與主金鑰（`.secret.key`）在所有模式下皆自動隱藏並禁止作為業務資料來源連線。於 Web 服務版中，非管理員（`Read-Write`）成員瀏覽、上傳或建立 SQLite / DuckDB 本機檔案時嚴格限制於 `<DataDir>` 沙箱目錄內（預設 `<DataDir>/local_databases`）；`Admin` 管理員與原生桌面視窗版則保有完整本機目錄存取權限。

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

## 開機自啟動服務安裝與角色 / 權限群組設定

### 1. Linux (`systemd` / `OpenRC`)

```bash
chmod +x ./dist/install-service.sh

# Unix Domain Socket 模式 + 指定執行角色與 Nginx 權限群組 (www-data / nginx)
sudo ./dist/install-service.sh install \
  --user dbharbor \
  --group dbharbor \
  --socket-group www-data \
  --supplementary-groups www-data \
  --unix-socket /run/dbharbor/dbharbor.sock \
  --unix-socket-perm 0660

# 本地 TCP 模式
sudo ./dist/install-service.sh install --user dbharbor --group dbharbor --host 127.0.0.1 --port 8787
```

### 2. macOS (`launchd`)

```bash
chmod +x ./dist/install-service.sh
sudo ./dist/install-service.sh install --host 127.0.0.1 --port 8787 --user _www --group _www
# 或非 root 使用者模式：
./dist/install-service.sh install --user-mode --port 8787
```

### 3. Windows (`ScheduledTask` + `icacls` 權限控管)

```powershell
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 `
  -Action Install `
  -ServiceAccount "NT AUTHORITY\LocalService" `
  -PermissionGroup "Administrators" `
  -HostAddr "127.0.0.1" -Port 8787
```

---

## 反向代理設定說明 (Nginx / Caddy / Apache)

完整範本位於 [`deploy/`](./deploy) 目錄（包含 [`nginx-unix-socket.conf`](./deploy/nginx-unix-socket.conf)、[`nginx-tcp.conf`](./deploy/nginx-tcp.conf)、[`Caddyfile`](./deploy/Caddyfile)、[`apache-vhost.conf`](./deploy/apache-vhost.conf)）：

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
