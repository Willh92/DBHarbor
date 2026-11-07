# 更新日誌 (Changelog)

**語言 / Language**: [English](./CHANGELOG.md) | [简体中文](./CHANGELOG.zh-CN.md) | **繁體中文** | [日本語](./CHANGELOG.ja.md)

DBHarbor 的所有重要變更皆記錄於本檔案，並與 GitHub 專案 [Willh92/DBHarbor](https://github.com/Willh92/DBHarbor.git) 同步。

## [v1.0.1] - 2026-11-07

> **版本形態邊界說明**：**原生桌面視窗版 (`dbharbor-desktop-*` / `DBHarbor.app`)** 維持**無使用者帳戶體系的純本機免登入模式**；以下持久化伺服端設定檔（`<DataDir>/config.json`）、首次管理員初始化精靈、登入頁品牌自訂、管理員緊急復原模式（`recovery` / `-recover-admin`）與本機檔案/內網沙箱隔離**僅適用於 Web 服務版 (`dbharbor-*`)**。

### 持久化設定檔、管理員緊急復原、安全與並行強化、手機觸控體驗升級與線上平滑更新
- **持久化伺服端設定檔 (`<DataDir>/config.json`) 與管理員密碼/2FA 緊急復原模式 *(僅 Web 服務版)***：
  - Web 服務版啟動時自動建立 `<DataDir>/config.json`（`0600` 權限，支援 `-config <路徑>` 或 `DBHARBOR_CONFIG_FILE` 覆寫路徑），遵循 `CLI 參數 > DBHARBOR_* 環境變數 > config.json > 預設值` 四層優先順序，涵蓋 `host`、`port`、`unixSocket`、`unixSocketPerm`、`unixSocketGroup`、`idleConnTTLMinutes`、`maxImportMB`、`trustedProxies`、`corsAllowedOrigins`、`security`（`allowPrivateNetworkForNonAdmin`、`duckdbCliPath`）及 `recovery`（`enabled`、`adminUsername`、`newPassword`、`reset2FA`）。
  - 提供雙重管理員緊急救援機制（遺忘管理員密碼或遺失 2FA 驗證裝置時使用）：
    1. **設定檔復原**：編輯 `<DataDir>/config.json` 設定 `"recovery": { "enabled": true, "adminUsername": "admin", "newPassword": "YourNewStrongPassword", "reset2FA": true }`（密碼至少 8 碼，或填寫 `"auto"` 自動產生 16 碼強隨機密碼輸出至日誌）並重啟服務——啟動時自動備份 `dbharbor.db` 至 `<DataDir>/.backups/`、重設密碼/2FA、撤銷舊工作階段，並**立即自動清除 `config.json` 中的復原狀態（`enabled: false`、`newPassword: ""`）**，避免明文密碼殘留於磁碟。
    2. **單次 CLI 指令復原**：執行 `dbharbor -data-dir <目錄> -recover-admin [-recover-user <帳號>] -recover-pass <新密碼|auto> [-reset-2fa]` 立即完成備份與重設並結束程式。
- **移除預設弱密碼、首次啟動管理員初始化精靈、登入頁自訂與三重防搜尋引擎收錄 *(僅 Web 服務版)***：
  - 徹底移除預設 `admin` / `admin123` 帳號，全面統一初始管理員密碼至少 **8 個字元**（含網頁初始化精靈、`-admin-pass` 與服務安裝腳本）；首次造訪自動開啟一次性**初始化管理員帳號精靈**（`POST /api/auth/setup`），建立後永久鎖定。
  - 零指紋登入介面與品牌自訂：未登入時不預填 `admin`、不洩漏版本號/引擎清單/GitHub 連結，`GET /api/health` 精簡為 `{"status":"ok"}`，`GET /api/version` 需驗證後存取；支援於 **安全 / 2FA -> 登入頁自訂** 設定 `<title>`、頂部圖示（上傳 `<= 128 KB` 圖示並同步 Favicon）、主副標題，並內建 `/robots.txt`、`X-Robots-Tag` 與 `<meta name="robots">` 防搜尋引擎收錄保護。
- **安全防護、驅動程式穩定性與效能強化**：
  - **Web 服務版本機資料庫與內網 SSRF 沙箱**：非管理員（`Read-Write`）本機 SQLite / DuckDB 檔案嚴格限制於 `<DataDir>/local_databases/`（阻擋符號連結跳脫），Web 模式停用 SQLite `ATTACH DATABASE`，強制伺服端控管 `security.duckdbCliPath` 與 DuckDB `enable_external_access = false`，並新增非管理員連線私網/迴路位址的 SSRF 防護（可由 `security.allowPrivateNetworkForNonAdmin` 調整）。
  - **防重送驗證碼與可信反向代理限流**：升級為金鑰衍生單次有效 CAPTCHA 驗證碼、僅信任 `trustedProxies` 代理清單的 `X-Forwarded-For` / `X-Real-IP` 用戶端 IP 限流，以及 2FA 驗證嘗試次數上限。
  - **嚴格 TLS 憑證驗證、連線池並行安全與記憶體最佳化**：補齊資料庫驅動標準 TLS 憑證驗證模式、修正 SQL Server 連線池 `USE [db]` 資料庫上下文隔離、`pool.Manager` 引用計數並行租約保護（防止閒置回收關閉執行中查詢）、DuckDB / MongoDB 串流記憶體上限、啟動期 SQLite 元資料庫自動遷移（`<DataDir>/.backups/` 備份），以及 SQLite 物件清單極速載入。
- **手機端觸控工作台與資料網格操作體驗升級**：
  - 手機與觸控螢幕改採單一捲動容器原生 GPU CSS `position: sticky` 固定表頭（`.dbharbor-touch-sticky-grid`）徹底消除捲動閃爍，並支援**觸控拖曳調整欄寬**與**雙擊欄分隔線自動展開欄寬**。
  - 左側導覽樹與資料表格新增觸控 `[⋮]` 操作按鈕與長按選單、手機 SQL 終端機快捷符號列、點選檢視截斷長文字儲存格、`34px–40px` 觸控按鈕與高風險刪除防誤觸隔離、左側選單 `keep-alive` 記憶體快取，以及全域彈出選單視窗邊界防溢出定位。
- **跨平台服務安裝腳本與一鍵線上二進位檔更新 (`install-service.sh` / `install-service.ps1`)**：
  - 支援 `upgrade`（相容 `update` 別名）/ `-Action Upgrade`（相容 `-Action Update`）從 GitHub Releases 線上更新、**強制驗證 `SHA256SUMS.txt`（失敗即中止）**、覆寫前 `-version` 執行檔自我檢測、`--mirror` / `-Mirror` 鏡像加速與 `--admin-user` / `--admin-pass`（`>= 8` 碼）無頭初始化。
  - 修正含空白/引號的 `systemd` `ExecStart=` 與 macOS `launchd` `ProgramArguments` 自訂安裝路徑識別，強化 Windows `irm | iex` 路徑保護與 `%TEMP%` 清理，並透過優雅停機輪詢（`Stop-DBHarborServiceAndWait`）+ `.old` 原子替換徹底解決 Windows `.exe` 檔案鎖定問題。

## [v1.0.0] - 2026-11-07

### 首發版本特性
- **雙形態發佈 — 原生桌面視窗版與 Web 服務版 (`CGO_ENABLED=0`)**：
  - **桌面視窗版 (`dbharbor-desktop-*` 與 macOS `DBHarbor.app`)**：滑鼠雙擊直接開啟原生系統視窗（Windows 採用 `Win32 + Edge WebView2` 並內建 `-H=windowsgui` 無黑框啟動、macOS 採用原生 `Cocoa NSWindow + WKWebView`、Linux 採用 `GTK3 + WebKit2GTK`），採用**無帳戶體系的純本機模式**（免登入直接進入工作台、原生深色視窗標題列同步、原生本機資料庫檔案瀏覽選擇與另存新檔串流匯出、多視窗本機工作階段相容、本機 AES-256-GCM 加密連線庫）。
  - **Web 服務版 (`dbharbor-*`)**：零外部相依單一執行檔 Web 工作台，支援 TCP 與原生 `AF_UNIX` Unix Domain Socket 監聽（`-unix-socket`、`-unix-socket-perm`、`-unix-socket-group`）、殘留 Socket 安全清理、反向代理真實用戶端 IP 辨識、伺服端本機資料庫目錄瀏覽與 `.db/.sqlite/.duckdb` 上傳，並提供 Nginx / Caddy / Apache 反向代理設定範本（`deploy/`）。
- **內建 14 種資料庫純 Go 驅動**：支援 ClickHouse（Native TCP / HTTP 雙協定與分區治理）、DuckDB、MySQL、MariaDB、TiDB、OceanBase、PostgreSQL、KingbaseES、SQLite 3、SQL Server、Oracle（免 Instant Client）、達夢 DM8、Redis（DB0–15 鍵值瀏覽、6 種結構編輯、JSON 備份匯入匯出與 CLI）以及 MongoDB（JSON/表格雙檢視、BSON 增刪改查、JSON/JSONL 文件匯入匯出、索引與 `RunCommand`）。
- **視覺化資料表結構設計、Navicat 級填滿資料網格、函式/觸發程序建立與串流匯入匯出**：
  - 支援欄位新增/修改/重新命名/刪除、索引新增/修改/刪除與外鍵管理、SQL 範本一鍵產生、資料表重新命名/複製、`CREATE VIEW`、視覺化建立與編輯函式/預存程序/觸發程序（`PROCEDURE` / `FUNCTION` / `TRIGGER`）、資料庫級使用者與權限指派治理（`GRANT` / `REVOKE` / Redis ACL / MongoDB 角色）、SQLite 交易級無損重建資料表與即時 DDL 預覽。
  - 全高填滿試算表級資料瀏覽網格（水平捲軸貼底、點擊下方空白列直接新增記錄、多列批次改值、單列/多列整列 TSV 複製貼上相容記事本與 Excel）與精簡版離線 Monaco 多分頁 SQL 終端機（`Ctrl/Cmd + Enter`、`EXPLAIN` 執行計畫、SQL 格式化、`AbortController` 查詢中斷、結果直接另存檔案、歷史紀錄與程式碼片段）。
  - 專為手機小螢幕（`<= 640px`）重新設計的響應式工作台排版：精簡頂欄、單膠囊分頁切換器、可折疊篩選面板、底部常駐批次提交列、`85dvh` 底部抽屜式列編輯器及 Redis `鍵列表 | 鍵值詳情 | CLI` 切換檢視。
  - 支援整庫 SQL Dump 備份、多表勾選/篩選條件/自訂查詢/MongoDB 集合串流匯出、檔案批次交易匯入與貼上 SQL 指令碼直接執行。
- **Web 多成員 RBAC 權限、安全強化與系統服務管理**：
  - Web 服務版內建三級多成員 RBAC 權限體系（`Admin` / `Read-Write` / `Read-Only`）與共享/私有連線授權隔離、IP/帳號雙維度防暴力破解限流（`HTTP 429`）、無狀態 HMAC 向量驗證碼、離線 QR Code RFC 6238 TOTP 雙重驗證（2FA）與 8 組一次性復原碼、工作階段撤銷與安全稽核日誌。
  - 預設資料目錄嚴格遵循各作業系統標準應用程式資料路徑（Windows 為 `%LOCALAPPDATA%\DBHarbor\data` 或服務模式 `%ProgramData%\DBHarbor\data`，macOS 為 `~/Library/Application Support/DBHarbor/data`，Linux 為 `$XDG_DATA_HOME/dbharbor` 或 `/var/lib/dbharbor`），絕不寫入啟動目錄；跨平台服務安裝腳本支援執行角色與權限群組設定。
- **全英文程式碼基底、多語系介面與 GitHub 線上更新提醒**：
  - 內建 **English (`en-US`)**、**简体中文 (`zh-CN`)**、**繁體中文 (`zh-TW`)**、**日本語 (`ja-JP`)** 即時切換與多語系文件。
  - 串接 GitHub `Willh92/DBHarbor` 線上檢查更新（支援新版本主動通知提醒、自動提醒開關、略過指定版本及自動配對桌面版/服務版產物）並同步更新日誌，採用 **Apache License 2.0 (`Apache-2.0`)** 開源授權。
