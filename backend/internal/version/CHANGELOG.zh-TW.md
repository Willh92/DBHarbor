# 更新日誌 (Changelog)

**語言 / Language**: [English](./CHANGELOG.md) | [简体中文](./CHANGELOG.zh-CN.md) | **繁體中文** | [日本語](./CHANGELOG.ja.md)

DBHarbor 的所有重要變更皆記錄於本檔案，並與 GitHub 專案 [Willh92/DBHarbor](https://github.com/Willh92/DBHarbor.git) 同步。

## [v1.0.4] - 2026-11-14

### 全量 CodeQL 零告警與供應鏈強化、14 引擎並行/方言深度修正、深色模式防閃白與手機/Safari 相容性打磨
- **供應鏈安全、本機 CodeQL 全量清零與部署腳本強化**：
  - **GitHub Actions 與建置工具鏈**：將 `release.yml` 與 `preview.yml` 中所有第三方 Action 鎖定為不可變 40 字元 Commit SHA；於 `scripts/setup-toolchain.mjs` 新增 Go 官方壓縮檔 SHA-256 完整性驗證與日誌注入清理；消除 `scripts/build.mjs` 中的檔案系統 TOCTOU 競爭條件告警。
  - **容器與系統服務安裝腳本**：強化 `Dockerfile` 目錄權限（`/app` 與執行檔歸屬 `root:root` `0755` 唯讀，僅 `/app/data` 授權 `dbharbor:dbharbor` `0700`）；收緊 `scripts/install-service.ps1` 與 `scripts/install-service.sh` 的目錄 ACL 及 `systemd` / `launchd` / `OpenRC` 跳脫處理；於 `deploy/*` 反向代理範本補齊 `DBHARBOR_TRUSTED_PROXIES` 說明。
- **後端驗證、權限邊界、金鑰庫與桌面版跨連接埠隔離強化**：
  - **主金鑰保護、防暴力破解計數與原子級 2FA 防重送**：`.master.key` 僅於檔案不存在（`os.ErrNotExist`）時以 `O_CREATE|O_EXCL` 建立，防止損毀或異常金鑰檔遭靜默覆寫；`LoginProtector.RecordSuccess` 改為僅依目前 `(ip, username)` 扣減 IP 失敗次數，阻斷透過自建帳號登入歸零同 IP 暴力破解計數的繞過路徑；`Store.Verify2FA` 改採資料庫單一語句 CAS 原子條件更新，徹底阻斷高並行下同一組 TOTP 驗證碼或備援碼遭重複使用。
  - **全驅動 TLS 降級阻斷與匯入 SSRF 防護**：擴充 `sameEndpointAndSecurity`（`tlsSecurityLevel`）涵蓋 `PostgreSQL`、`KingbaseES`、`MySQL`、`MariaDB`、`TiDB`、`OceanBase`、`ClickHouse`、`SQL Server`、`Redis` 與 `MongoDB`（含 URI 參數），任何 TLS 安全等級下調皆強制重新輸入密碼；連線設定匯入補齊 `URI` SSRF 檢查、`2 MiB` / `500` 筆交易上限與管理員共用連線歸屬保留；共用且不記住密碼連線的 `autocomplete` 快取依工作階段隔離；Gzip 中介層保留上游 `Vary: Origin` 標頭。
  - **桌面版跨連接埠防護與子程序回收**：桌面版未攜帶啟動票券或有效本機工作階段 Cookie 存取首頁時回傳 `401`（絕不外洩 `desktopToken`），針對僅憑 Cookie 的寫入請求強制驗證精確本機連接埠 `Origin`、`Sec-Fetch-Site` 與 JSON `Content-Type`；修正桌面原生儲存匯出於 `MaxRows <= 0`（無上限）時誤截斷資料的問題；macOS `osascript` 改採 `argv` 傳遞參數，Linux 視窗啟動器補齊子程序 `Wait()` 回收，並統一收緊本機資料目錄 `0700`/`0600` 權限。
- **14 類資料庫驅動並行、記憶體與沙箱方言深度修正**：
  - **SQL 基礎層、ClickHouse、Oracle、達夢、SQL Server 與 MySQL**：識別字與字面值格式化自動剔除空位元組；新增綁定單一連線工作階段的 `ApplyMutationsConn`、`StreamExportConn`、`StreamMultiTableExportConn` 與 `BatchImportConn`；修正 `SQL Server` `BrowseData` 於單一連線池上限時與 `COUNT(*)` 死結問題，並將資料異動/匯入/匯出綁定至 `req.Database`；為 `Oracle` 與 `達夢 DM8` 接入受限網路撥號器（阻斷 TNS `REDIRECT` 內網探測）與 `Oracle.KillSession` 嚴格 `SID,SERIAL#` 驗證；全入口串接 `ValidateClickHouseRestrictedWebSQL` 逐句沙箱檢查；非管理員受限 Web 模式禁止 `skip-verify` 與 `TrustServerCertificate=true`。
  - **DuckDB、SQLite 與 MongoDB**：修正 `--` 與多行 `/* ... */` 註解內的單引號/行首點命令掃描，將 `:memory:` DuckDB 工作階段映射至受管暫存檔（`Close()` 自動清理）並支援單一程序多語句執行以維持工作階段與交易狀態，補齊 DuckDB `jsonl` 與 `markdown` 串流匯出；統一 `MongoDB` `resolveDBName` 預設資料庫解析，以 `bson.D` 保留純量 `_id` 精確比對，並支援分片叢集 `shard:opid` 工作階段終止。
- **深色模式防閃白、固定欄視覺、手機/Safari 相容性、記憶體與四語系國際化**：
  - **深色首屏零閃爍與資料表固定欄精修**：新增渲染前同步主題判定（`main.tsx` + `prefers-color-scheme` CSS 備援 + `<meta name="color-scheme" content="light dark" />`），修正深色模式下工作台（`#141414`）與彈窗/抽屜（`#1f1f1f`）固定欄背景透字、Hover 強調、填補列透底、暫存新增列勾選高亮及 `<Select>` 搜尋框自動填入遮罩問題。
  - **小螢幕手機 (`360px`)、瀏海螢幕安全區與 iOS Safari 輸入修正**：修正 `LoginPage` 於小螢幕或虛擬鍵盤彈出時頂部卡片遭裁切無法捲動的問題（`100dvh` + `margin: auto`）；工作台底部狀態列與行動版抽屜底欄完整支援 `env(safe-area-inset-bottom)`；行動版側邊抽屜與建立資料庫/Schema 視窗改為純 CSS 響應式寬度；iOS Safari 規則排除 Monaco `.ime-input` 並調整 `24px` 緊湊輸入框行高避免文字裁切。
  - **背景輪詢節能、連線狀態連動與多語系補齊**：瀏覽器分頁隱藏時自動暫停 `ServerMonitorTab` 輪詢並於恢復可見時立即更新；冷啟動還原分頁時略過未解鎖的「不記住密碼」連線；連線編輯或鎖定後自動失效側邊欄物件樹快取；修正 i18n `{var}` 變數替換對 `$` 字元的副作用，並補齊四語系（`en-US`、`zh-CN`、`zh-TW`、`ja-JP`）共 19 項介面翻譯。

## [v1.0.3] - 2026-11-08

### 資料庫連線密碼預設不落盤（可選記住與隨時關閉）、CodeQL 與縱深安全強化、深色模式及行動裝置體驗最佳化
- **資料庫連線密碼預設不記住、支援編輯隨時關閉與驗證失敗互動式重輸 (`ConnectionModal` + `ConnectionAuthModal`)**：
  - **預設不記住密碼（僅目前工作階段記憶體有效）**：所有新增網路與 URI 資料庫連線（`MySQL`、`MariaDB`、`TiDB`、`OceanBase`、`PostgreSQL`、`KingbaseES`、`ClickHouse`、`SQL Server`、`Oracle`、`達夢 DM8`、`Redis`、`MongoDB`）現在**預設關閉「記住密碼」**（`RememberPassword = false`），密碼與 URI 憑證不再寫入磁碟，僅暫存於受配額保護的工作階段記憶體（`POST /api/connections/{id}/unlock`），登出、工作階段逾時、鎖定/刪除連線或程式關閉時自動清除。
  - **可選加密記住與編輯一鍵關閉抹除**：使用者可依需求勾選 **「記住密碼」** 將憑證以 AES-256-GCM 加密儲存至本機保險箱（`conns.enc`）；針對已記住密碼的既有連線，亦可隨時透過 **「編輯連線」取消勾選「記住密碼」** 並儲存，立即抹除磁碟中已儲存的密碼與 URI 內嵌憑證。
  - **未解鎖連線與帳密錯誤自動彈窗重輸**：連線至未記住密碼的資料來源（`DB_AUTH_REQUIRED`），或因資料庫帳號密碼變更/鎖定導致驗證失敗時（`DB_AUTH_FAILED`，涵蓋 12 類認證驅動之 `ORA-01017` / `ORA-28000` / `ORA-28001`、PostgreSQL `28P01` / `28000`、MySQL `1045`、SQL Server `18456`、ClickHouse `516` / `401`、Redis `WRONGPASS` / `NOAUTH`、MongoDB `code 18` 等），自動開啟 **`ConnectionAuthModal`** 支援修正帳號密碼、切換是否記住密碼，並於驗證成功後無縫重試原操作（含串流匯出預檢與多分頁自動重載）。
- **安全揭露政策 (`SECURITY.md`)、GitHub CodeQL 掃描修補與後端記憶體/並行強化**：
  - 正式發佈 DBHarbor **`SECURITY.md`** 資安漏洞通報政策，並將所有 GitHub Actions 工作流程（`ci.yml`、`release.yml`、`preview.yml`）收斂為頂層最小權限 `contents: read` 與依 Job 精確授權寫入權限。
  - 全面修補 Go 與前端靜態安全掃描項目：於連線編輯/測試重用既有密文時新增 `sameEndpointAndSecurity` 檢查以阻斷變更主機/URI 或降級 TLS（關閉 `UseSSL` / 開啟 `SSLSkipVerify`）竊取原密碼，禁止跨擁有者複製私密憑證，修正 Web 服務版授權一般使用者解鎖共用「不記住密碼」連線之權限判斷，統一 ClickHouse 與 Redis TLS 設定（補齊 SNI `ServerName` 與非管理員 `skipVerify` 限制），嚴格驗證自訂品牌 Favicon 僅允許點陣圖 `data:image/...;base64`，補齊 API 路徑 `encodeURIComponent` 與 `int32`/`uint32` 整數邊界保護。
  - 記憶體與檔案鎖定治理：為工作階段憑證池 `sessionCreds` 加入雙層容量上限（`4096` 範圍 / 單範圍 `256` 連線）與關機自動歸零；連線池淘汰改採 `(connID + sessionKey)` 複合鍵（`maxPools = 128`）；串流 SQL/文件匯出延遲送出 `Content-Disposition: attachment` 標頭避免驗證失敗下載損毀檔案；修正 DuckDB CLI 超過 `32 MiB` 輸出管線死結與刪除/更新連線時之檔案鎖定釋放。
- **深色模式視覺打磨、啟動分頁水合門控與行動裝置 (`360px`) 相容性最佳化**：
  - 消除深色模式下資料表與視窗出現淺灰捲軸軌道的突兀感（統一改為 `transparent` 透明軌道），防止深色模式下 Monaco SQL/JSON 編輯器首次掛載前的白底閃爍，並隨淺色（`#ffffff`）/ 深色（`#141414`）主題同步更新瀏覽器 `<meta name="theme-color">`。
  - 新增啟動階段 `/api/system/bootstrap` 水合門控（`bootstrapped`），消除攜帶未記住密碼分頁重新整理時的搶跑請求與錯誤提示閃爍，並串接全部 8 類工作台分頁於密碼解鎖成功後的自動重新整理。
  - 最佳化 `360x800` 手機瀏覽體驗：`ServerMonitorTab` 頂部工具列自動換行消除水平溢位，行動版 `ConnectionSidebar`（共用/個人）與 `SchemaObjectModal` 自動切換緊湊下拉選單，抽屜內距調整為 `12px`，本機單檔資料庫建立表單改為垂直堆疊，並於觸控裝置抑制視窗開啟時虛擬鍵盤自動彈出遮擋畫面。

## [v1.0.2] - 2026-11-07

> **版本形態邊界說明**：**原生桌面視窗版 (`dbharbor-desktop-*` / `DBHarbor.app`)** 維持**無使用者帳戶體系的純本機免登入模式**並保有完整本機檔案存取權限；瀏覽器端臨時單一檔案資料庫工作區（`<DataDir>/ephemeral_workspaces/`）與非管理員依使用者隔離目錄（`<DataDir>/local_databases/u_<hash>/`）**僅適用於 Web 服務版 (`dbharbor-*`)**。

### 14 引擎專屬物件完整 CRUD、Web 瀏覽器直開本機單檔資料庫、側欄折疊與狀態隔離、深度安全與體積記憶體最佳化
- **Web 瀏覽器直開本機單一檔案資料庫 (`SQLite 3` & `DuckDB`) *(僅 Web 服務版)***：
  - Web 服務版側邊欄與新增連線視窗支援拖放及一鍵 **「開啟本機資料庫 (`Open Local DB`)」**（`POST /api/connections/ephemeral-singlefile`），無需伺服器目錄權限即可直接從瀏覽器載入本機 `.db` / `.sqlite` / `.sqlite3` / `.duckdb` / `.ddb` 檔案進行查詢與視覺化編輯。
  - 檔案依登入帳號隔離於 `0700` `<DataDir>/ephemeral_workspaces/` 沙箱並嚴格驗證二進位檔頭魔數（`SQLite format 3\0` / `DUCK`），匯出前自動執行 `WAL` / `CHECKPOINT` 寫入磁碟，於 Chromium 瀏覽器支援透過 File System Access API **直接寫回覆寫本機原始檔案**，全瀏覽器支援一鍵下載最新資料庫快照（`GET /api/connections/{id}/download-file`），並內建並行安全配額與 12 小時閒置自動清理。
- **14 類資料庫引擎專屬物件視覺化 CRUD 與 NoSQL 深度治理 (`sqlmeta` + `SchemaObjectModal`)**：
  - **MySQL / MariaDB / OceanBase / TiDB**：新增 **排程事件 (`Scheduled Events`)**（`ONE TIME` / `EVERY`、啟停狀態、`ON COMPLETION PRESERVE`）、**序列 (`Sequences`，MariaDB 10.3+ / TiDB)**、**資料表分區 (`ADD` / `DROP` / `TRUNCATE PARTITION`)**、**產生欄位 (`VIRTUAL` / `STORED`)** 與 **`CHECK` 約束** 視覺化管理與即時 DDL 預覽。
  - **PostgreSQL / KingbaseES**：新增 **序列 (`Sequences`)**、**實體化檢視表 (`Materialized Views` 含一鍵 `REFRESH [CONCURRENTLY]`)**、**擴充套件 (`Extensions`)**、**自訂型別 (`ENUM` / `DOMAIN`)**、**宣告式分區 (`ATTACH` / `DETACH`)**、**產生欄位** 與 **`CHECK` 約束** 視覺化管理。
  - **ClickHouse**：新增 **投影 (`Projections`：建立 / 實體化 `MATERIALIZE` / 清除 / 刪除)**、**跳數索引 (`Data Skipping Indexes`：`minmax`、`set`、`bloom_filter`、`tokenbf_v1`、`ngrambf_v1`)**、**實體化檢視表 (`Materialized Views`)**、**資料表 `TTL` 策略**、**外部字典 (`Dictionaries`)** 與 **SQL 自訂函式 (`UDFs`)** 管理。
  - **Oracle / 達夢 DM8 與 SQL Server**：新增 **套件 (`PACKAGE` 規格 + `PACKAGE BODY` 主體雙分頁編輯與重編譯)**、**序列 (`Sequences`)**、**實體化檢視表 (`DBMS_MVIEW.REFRESH`)**、**同義詞 (`PRIVATE` / `PUBLIC Synonyms`)**、**資料庫連結 (`DB Links`，Web 服務版僅限管理員)**、**資料表分區**、**虛擬/計算欄位 (`Computed Columns`)** 與 **`CHECK` 約束** 管理。
  - **DuckDB 與 SQLite 3**：DuckDB 支援多 Schema 切換與建立、**序列 (`Sequences`)** 及 **純量/資料表巨集 (`Scalar / Table Macros`)**；SQLite 3 支援 **`FTS5` / `RTree` 虛擬資料表** 建立與唯讀 **`PRAGMA` 診斷及完整性檢查面板**（`PRAGMA quick_check`、`foreign_key_check`）。
  - **MongoDB 與 Redis**：MongoDB 支援建立一般集合、**唯讀檢視表 (`Views`)**、**時間序列集合 (`Time-Series`)** 與 **固定大小集合 (`Capped`)**，支援 **`$jsonSchema` 驗證規則 (`collMod`)** 與 **TTL / Partial / Sparse / Unique 進階索引**；Redis 支援 **Stream 消費者群組治理**（`XINFO`、`XGROUP CREATE / SETID / DESTROY`、`XPENDING` + `XACK`）、**慢查詢日誌 (`Slowlog`)**、**執行期設定 (`CONFIG GET`)** 與 **Pub/Sub 頻道監測及發佈**。
- **左側導覽樹預設全折疊、一鍵展開/折疊開關與全工作台狀態嚴格隔離**：
  - 左側導覽列所有物件分類於首次載入及切換連線/資料庫/Schema 時**預設全部折疊**，並於篩選框旁提供多語系 **「全部展開 / 全部折疊」** 切換按鈕。
  - 徹底修正跨連線/跨資料庫折疊狀態串位問題，並全面隔離側邊欄快取、`OverviewTab`、`TableStructureTab`、`TableDataTab`、`SqlConsoleTab`、`DbUsersTab`、`RedisBrowserTab`、`MongoCollectionTab`、視窗狀態與多帳號切換時 `localStorage` 分頁/SQL 草稿狀態。
- **縱深安全強化、並行可靠性、記憶體佔用與安裝檔體積瘦身**：
  - **無損執行檔與前端資源瘦身**：新增建置期純 Go Oracle 驅動 `go-ora` 236 張字元集表 Gzip 壓縮覆寫器（`cmd/gen-overlay`，100% 保留 Oracle 字元集相容性並使全平台 12 個執行檔與 Docker 映像**單檔縮減 `-11.20 MB`**），同時拆分前端大型模組（首屏預載入 JS 縮減 **`-78.6%`**）。
  - **記憶體峰值與並行安全最佳化**：檔案上傳改採串流 `MultipartReader` 直寫磁碟（記憶體 `< 64 KiB`），資料網格導入 `CellStateHub` 儲存格訂閱機制（列記憶體降低 `-50%`），DuckDB CLI 新增依檔案互斥鎖、交易化 `ApplyRowMutations` 與串流 `COPY ... TO` 匯出，PostgreSQL 子連線池僅回收閒置連線，臨時工作區清理器加入上傳中保護鎖。
  - **沙箱、SSRF 與深色模式修正**：DuckDB Web 沙箱強制啟用 `-safe` 模式並改由 `stdin` 傳遞 SQL 阻擋點指令跳脫；SQLite Web 沙箱升級為詞法掃描器阻擋註解/引號繞過的 `ATTACH` 與檔案函式並封鎖 `?`/`#` DSN 參數注入；Web 非管理員本機資料庫依帳號雜湊子目錄 `<DataDir>/local_databases/u_<hash>/` 嚴格隔離；唯讀 SQL 守衛解開辨識 MariaDB `/*M!...*/` 可執行註解；Oracle / 達夢等遠端驅動全面套用連線期防 DNS 重新綁定 SSRF 檢查與 IPv6 `net.JoinHostPort`；修正深色模式下開啟物件編輯視窗導致全域 Monaco 編輯器變為淺色的問題。

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
