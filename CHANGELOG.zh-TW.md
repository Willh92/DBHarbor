# 更新日誌 (Changelog)

**語言 / Language**: [English](./CHANGELOG.md) | [简体中文](./CHANGELOG.zh-CN.md) | **繁體中文** | [日本語](./CHANGELOG.ja.md)

DBHarbor 的所有重要變更皆記錄於本檔案，並與 GitHub 專案 [Willh92/DBHarbor](https://github.com/Willh92/DBHarbor.git) 同步。

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
