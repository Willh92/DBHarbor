# 変更履歴 (Changelog)

**言語 / Language**: [English](./CHANGELOG.md) | [简体中文](./CHANGELOG.zh-CN.md) | [繁體中文](./CHANGELOG.zh-TW.md) | **日本語**

DBHarbor の主な変更履歴は本ファイルに記録され、GitHub リポジトリ [Willh92/DBHarbor](https://github.com/Willh92/DBHarbor.git) と同期されます。

## [v1.0.3] - 2026-11-08

### DB 接続パスワードのデフォルト非保存化（任意保存・編集時オフ消去）・CodeQL & 多層セキュリティ強化・ダークモード & モバイル最適化
- **DB 接続パスワードのデフォルト非保存化・編集時の保存解除消去・認証エラー時の対話型再入力 (`ConnectionModal` + `ConnectionAuthModal`)**：
  - **デフォルトでパスワードを保存しない（セッションメモリ限定）**：新規のネットワークおよび URI DB 接続（`MySQL`、`MariaDB`、`TiDB`、`OceanBase`、`PostgreSQL`、`KingbaseES`、`ClickHouse`、`SQL Server`、`Oracle`、`Dameng DM8`、`Redis`、`MongoDB`）で **「パスワードを記憶する」がデフォルトでオフ**（`RememberPassword = false`）になりました。パスワードや URI 認証情報はディスクに書き込まれず、上限付きのセッションメモリ（`POST /api/connections/{id}/unlock`）にのみ保持され、ログアウト・セッション期限切れ・接続ロック/削除・プロセス終了時に自動消去されます。
  - **任意の暗号化保存 & 編集からのワンクリック保存解除**：接続ごとに **「パスワードを記憶する」** をオンにして AES-256-GCM 暗号化ボールト（`conns.enc`）へ保存できるほか、保存済み接続の編集画面でチェックを外して保存するだけでディスク上の保存済みパスワードおよび URI 内蔵シークレットを即座に完全消去できます。
  - **未ロック解除接続および認証失敗時の自動再入力モーダル**：パスワード非保存の接続先を開く際（`DB_AUTH_REQUIRED`）や、DB 側のパスワード変更・アカウントロック等で認証エラーが発生した際（`DB_AUTH_FAILED`：全 12 認証ドライバの `ORA-01017` / `ORA-28000` / `ORA-28001`、PostgreSQL `28P01` / `28000`、MySQL `1045`、SQL Server `18456`、ClickHouse `516` / `401`、Redis `WRONGPASS` / `NOAUTH`、MongoDB `code 18` 等に対応）に **`ConnectionAuthModal`** を自動表示し、ユーザー名/パスワードの修正・記憶設定の切り替え・中断された操作（ストリーミングエクスポート事前確認や複数タブの自動再読込を含む）のシームレスな再試行を行えます。
- **セキュリティポリシー (`SECURITY.md`)・GitHub CodeQL 対応 & バックエンドメモリ/並行処理の強化**：
  - 公式の脆弱性報告ポリシー **`SECURITY.md`** を公開し、全 GitHub Actions ワークフロー（`ci.yml`、`release.yml`、`preview.yml`）をトップレベル最小権限 `contents: read` およびジョブ単位の書き込み権限へ厳格化しました。
  - Go / TypeScript の静的解析・セキュリティ監査項目を全面対応：接続編集/テスト時の保存済みシークレット再利用に対するホスト/URI 変更および TLS ダウングレード（`UseSSL` 無効化 / `SSLSkipVerify` 有効化）防止チェック（`sameEndpointAndSecurity`）、Web 版での他ユーザー所有シークレット複製防止、共有の非保存パスワード接続に対する一般権限ユーザーのセッション単位ロック解除対応、ClickHouse / Redis の共通 TLS 設定（SNI `ServerName` 補完および非管理者の `skipVerify` 制限）、カスタムブランド Favicon のラスター画像 `data:image/...;base64` 厳格検証、API パスの `encodeURIComponent` 徹底、`int32`/`uint32` 整数境界チェックを実装しました。
  - メモリ・ファイルロック安全性の向上：`sessionCreds` の上限設定（`4096` スコープ / 各 `256` 接続）と終了時ゼロクリア、ドライバプール退避の `(connID + sessionKey)` 複合キー化（`maxPools = 128`）、ストリーミング SQL/ドキュメントエクスポートにおけるクエリ開始成功後の `Content-Disposition: attachment` 遅延送出、DuckDB CLI の `32 MiB` 超出力パイプデッドロック防止および接続削除/更新時のファイルロック解放を実装しました。
- **ダークモード外観改善・起動時タブ復元ゲート & モバイル (`360px`) 互換性向上**：
  - ダークモード時のテーブルやモーダルに残っていた明るい灰色のスクロールバートラックを `transparent` に統一し、Monaco SQL/JSON エディタ初回マウント前の白背景フラッシュを解消、ライト（`#ffffff`）/ ダーク（`#141414`）テーマに応じた `<meta name="theme-color">` 動的同期を追加しました。
  - `/api/system/bootstrap` 完了を待つ起動ハイドレーションゲート（`bootstrapped`）を導入してリロード時の早期リクエスト競合やエラーバナーのちらつきを解消し、全 8 種類のワークスペースタブでロック解除成功後の自動データ再読込に対応しました。
  - `360x800` モバイル画面の操作性を改善：`ServerMonitorTab` ツールバーの折り返し対応による横スクロール溢れ解消、モバイル版 `ConnectionSidebar`（共有/個人）および `SchemaObjectModal` のコンパクトセレクタ化、モバイル `<Drawer>` 余白の最適化（`12px`）、ローカル単一ファイル DB 作成欄の縦積みレイアウト化、タッチ端末でのモーダル表示時における意図しないソフトウェアキーボード自動ポップアップ抑止を実装しました。

## [v1.0.2] - 2026-11-07

> **エディション適用範囲の注意**：**デスクトップ GUI 版 (`dbharbor-desktop-*` / `DBHarbor.app`)** は引き続き**ユーザーアカウント機能を持たないログイン不要の純粋なローカルモード**で動作し、ホスト上のローカルファイルへフルアクセスできます。ブラウザからの一時ローカル単一ファイル DB ワークスペース（`<DataDir>/ephemeral_workspaces/`）およびユーザー別 `<DataDir>/local_databases/u_<hash>/` サンドボックス制限は **Web サーバー版 (`dbharbor-*`) 専用**です。

### 14 DB エンジン固有オブジェクト CRUD・ブラウザからの一時ローカル DB 開設・サイドバー状態分離・セキュリティ＆バイナリサイズ最適化
- **ブラウザからの一時ローカル単一ファイル DB (`SQLite 3` & `DuckDB`) *(Web サーバー版のみ)***：
  - Web サーバー版のサイドバーおよび接続モーダルにドラッグ＆ドロップ対応の **「ローカル DB を開く (`Open Local DB`)」**（`POST /api/connections/ephemeral-singlefile`）を追加。サーバー側のパス権限なしで手元の `.db` / `.sqlite` / `.sqlite3` / `.duckdb` / `.ddb` ファイルをブラウザから直接開いてクエリ・編集できます。
  - ファイルはユーザー別の `0700` `<DataDir>/ephemeral_workspaces/` に分離保存され、マジックヘッダー検証（`SQLite format 3\0` / `DUCK`）を実施。エクスポート前に `WAL` / `CHECKPOINT` をフラッシュし、Chromium 系ブラウザでは File System Access API による**元のローカルファイルへの直接上書き保存**、全ブラウザで最新スナップショットのダウンロード（`GET /api/connections/{id}/download-file`）、並行安全な容量クォータ制限、12 時間アイドル自動クリーンアップを備えています。
- **14 種類の DB エンジン固有スキーマ要素 CRUD & NoSQL 高度管理 (`sqlmeta` + `SchemaObjectModal`)**：
  - **MySQL / MariaDB / OceanBase / TiDB**：**スケジュールイベント (`Scheduled Events`)**（`ONE TIME` / `EVERY`、有効/無効切替、`ON COMPLETION PRESERVE`）、**シーケンス (`Sequences`：MariaDB 10.3+ / TiDB)**、**テーブルパーティション (`ADD` / `DROP` / `TRUNCATE PARTITION`)**、**生成カラム (`VIRTUAL` / `STORED`)**、**`CHECK` 制約**のビジュアル作成・編集・削除と DDL プレビューに対応。
  - **PostgreSQL / KingbaseES**：**シーケンス**、**マテリアライズドビュー**（ワンクリック `REFRESH MATERIALIZED VIEW [CONCURRENTLY]` 対応）、**拡張機能 (`Extensions`)**、**カスタム型 (`ENUM` / `DOMAIN`)**、**宣言的パーティション (`ATTACH` / `DETACH`)**、**生成カラム**、**`CHECK` 制約**のビジュアル管理を追加。
  - **ClickHouse**：**プロジェクション (`Projections`：追加 / `MATERIALIZE` / クリア / 削除)**、**データスキップインデックス (`minmax`、`set`、`bloom_filter`、`tokenbf_v1`、`ngrambf_v1`)**、**マテリアライズドビュー**、**テーブル `TTL` 管理**、**外部辞書 (`Dictionaries`)**、**SQL UDF (`CREATE FUNCTION ... AS (...) -> expr`)** の完全 CRUD を追加。
  - **Oracle / Dameng DM8 & SQL Server**：**パッケージ (`PACKAGE` 仕様部 + `PACKAGE BODY` 本体部の 2 タブ編集＆再コンパイル)**、**シーケンス**、**マテリアライズドビュー (`DBMS_MVIEW.REFRESH`)**、**シノニム (`PRIVATE` / `PUBLIC`)**、**データベースリンク (`DB Links`、Web 版では管理者限定)**、**パーティション**、**仮想/計算カラム (`Computed Columns`)**、**`CHECK` 制約**に対応。
  - **DuckDB & SQLite 3**：DuckDB のマルチスキーマ切替・作成、**シーケンス**、**スカラー/テーブルマクロ (`Macros`)** に対応。SQLite 3 の **`FTS5` / `RTree` 仮想テーブル** 作成および読み取り専用の **`PRAGMA`・整合性インスペクタ**（`PRAGMA quick_check`、`foreign_key_check`）を追加。
  - **MongoDB & Redis**：MongoDB の通常コレクション・**読み取り専用ビュー (`Views`)**・**時系列コレクション (`Time-Series`)**・**Capped コレクション**作成、**`$jsonSchema` バリデータ (`collMod`)**、**TTL / Partial / Sparse / Unique インデックス**に対応。Redis の **Stream コンシューマーグループ管理**（`XINFO`、`XGROUP CREATE / SETID / DESTROY`、`XPENDING` + `XACK`）、**スローログ (`Slowlog`)**、**実行時設定 (`CONFIG GET`)**、**Pub/Sub チャンネル監視・配信**を追加。
- **サイドバーのデフォルト全折りたたみ・一括展開/折りたたみスイッチ & 完全な状態分離**：
  - 左サイドバーの全オブジェクトカテゴリを初回表示および接続/DB/スキーマ切替時に**デフォルトで全折りたたみ**とし、検索バー横に多言語対応の **「すべて展開 / すべて折りたたむ」** トグルボタンを追加。
  - 接続・データベース・スキーマ・ログインユーザー間でのサイドバー展開状態、シングルトンタブ、モーダル入力値、および `localStorage` のタブ/SQL 下書きの状態混入を完全に解消。
- **多層セキュリティ・並行安全性・メモリ使用量 & バイナリサイズの徹底最適化**：
  - **無損失バイナリ＆フロントエンド軽量化**：純 Go Oracle ドライバ `go-ora` の 236 文字セットテーブルをビルド時に Gzip 圧縮するオーバーレイ生成器（`cmd/gen-overlay`）を導入し、Oracle 文字セット互換性を 100% 維持したまま全 12 ターゲットバイナリと Docker イメージを **1 バイナリあたり `-11.20 MB` 削減**。フロントエンドの初期プリロード JS も **`-78.6%` 削減**。
  - **メモリ＆並行処理の強化**：マルチパートアップロードをストリーミング直書き化（RAM `< 64 KiB`）、`CellStateHub` によるデータグリッド行メモリ `-50%` 削減、DuckDB CLI のファイル単位ミューテックスとトランザクション更新・ストリーミング `COPY ... TO` エクスポート、PostgreSQL アイドル限定サブプール解放、アップロード中ディレクトリを保護するエフェメラル掃除機構を実装。
  - **サンドボックス・SSRF & ダークモード修正**：DuckDB Web サンドボックスでの `-safe` フラグ強制＋`stdin` SQL 入力によるドットコマンド遮断、SQLite Web サンドボックスの字句スキャナ化（コメント・引用符による `ATTACH` 回避と `?`/`#` DSN パラメータ注入を防止）、非管理者向け `<DataDir>/local_databases/u_<hash>/` ユーザー別隔離、MariaDB `/*M!...*/` 実行可能コメントの読み取り専用ガード対応、Oracle / Dameng 等リモートドライバへのダイヤル時 DNS リバインディング SSRF 検証＆IPv6 `net.JoinHostPort` 適用、およびダークモード時にモーダルを開くと背景の Monaco エディタがライトテーマに変わる不具合を修正。

## [v1.0.1] - 2026-11-07

> **エディション適用範囲の注意**：**デスクトップ GUI 版 (`dbharbor-desktop-*` / `DBHarbor.app`)** は引き続き**ユーザーアカウント機能を持たないログイン不要の純粋なローカルモード**で動作します。永続サーバー設定（`<DataDir>/config.json`）、初期管理者セットアップウィザード、ログイン画面のブランディング、緊急管理者リカバリモード（`recovery` / `-recover-admin`）、およびローカルファイル・ネットワークのサンドボックス制限は **Web サーバー版 (`dbharbor-*`) 専用**です。

### 永続サーバー設定 (`config.json`)・緊急管理者リカバリ・セキュリティ強化・モバイル操作性向上・オンライン更新
- **永続サーバー設定ファイル (`<DataDir>/config.json`) & 緊急管理者リカバリモード *(Web サーバー版のみ)***：
  - Web サーバー版の起動時に `<DataDir>/config.json`（権限 `0600`、`-config <path>` または `DBHARBOR_CONFIG_FILE` でパス変更可）を自動生成。優先順位 `CLI 引数 > DBHARBOR_* 環境変数 > config.json > デフォルト値` で `host`、`port`、`unixSocket`、`unixSocketPerm`、`unixSocketGroup`、`idleConnTTLMinutes`、`maxImportMB`、`trustedProxies`、`corsAllowedOrigins`、`security`（`allowPrivateNetworkForNonAdmin`、`duckdbCliPath`）、`recovery`（`enabled`、`adminUsername`、`newPassword`、`reset2FA`）を管理できます。
  - 管理者パスワードまたは 2FA デバイス紛失時の 2 通りの緊急復旧フローを追加：
    1. **`config.json` による復旧**：`<DataDir>/config.json` の `"recovery"` を `{ "enabled": true, "adminUsername": "admin", "newPassword": "YourNewStrongPassword", "reset2FA": true }`（8 文字以上、または `"auto"` で 16 文字のランダムパスワードを生成し起動ログに出力）に編集して再起動すると、`dbharbor.db` を `<DataDir>/.backups/` に自動バックアップした上でパスワード/2FA をリセット・既存セッションを失効させ、**即座に `config.json` を自動無害化（`enabled: false`、`newPassword: ""`）**します。
    2. **ワンショット CLI 復旧**：`dbharbor -data-dir <dir> -recover-admin [-recover-user <username>] -recover-pass <newPass|auto> [-reset-2fa]` を実行して即座にリセットして終了できます。
- **初期デフォルト認証情報の廃止・初回管理者セットアップ・ログイン画面カスタマイズ＆検索エンジン登録防止 *(Web サーバー版のみ)***：
  - 組み込みの `admin` / `admin123` を廃止し、Web セットアップ・CLI（`-admin-pass`）・サービスインストーラ全体で初期管理者パスワードを **最低 8 文字** に統一。ユーザーが空の場合は初回アクセス時に一回限りの**初期管理者セットアップウィザード**（`POST /api/auth/setup`）を表示し、作成後に永久ロックします。
  - 未認証時の情報露出ゼロ化（`admin` 事前入力なし、バージョン/エンジン/GitHub 非表示、最小化された `GET /api/health`、認証必須の `GET /api/version`）に加え、**セキュリティ / 2FA -> ログイン画面** から `<title>`・上部アイコン（`<= 128 KB` 画像アップロード＋Favicon 同期）・見出し・サブタイトルを自由に変更可能。`/robots.txt`、`X-Robots-Tag`、`<meta name="robots">` による三重のクローラ拒否を標準搭載。
- **セキュリティ・ドライバ信頼性 & パフォーマンス強化**：
  - **Web サーバー版のローカル DB & SSRF サンドボックス**：非管理者（`Read-Write`）のローカル SQLite / DuckDB ファイルを `<DataDir>/local_databases/` 内に厳格制限（シンボリックリンク脱出防止）、Web モードでの SQLite `ATTACH DATABASE` 無効化、サーバー管理の `security.duckdbCliPath` と DuckDB `enable_external_access = false` の強制、および非管理者によるプライベート/ループバック IP 接続をブロックする SSRF ガード（`security.allowPrivateNetworkForNonAdmin` で制御）を追加。
  - **リプレイ防止 CAPTCHA & 信頼プロキシ対応レート制限**：秘密鍵派生の使い捨て CAPTCHA トークン、`trustedProxies` に登録されたプロキシからのみ `X-Forwarded-For` / `X-Real-IP` を信頼するクライアント IP 制限、2FA 試行回数上限を導入。
  - **厳格な TLS 検証・プール並行安全性 & メモリ最適化**：各 DB ドライバの TLS 証明書検証オプション、SQL Server プール接続での `USE [db]` コンテキスト分離、`pool.Manager` の参照カウント方式リースによる実行中クエリの誤クローズ防止、DuckDB / MongoDB ストリーミングメモリ上限、起動時 SQLite メタデータ自動マイグレーション（`<DataDir>/.backups/` 自動退避）、SQLite オブジェクト一覧の即時取得を実装。
- **モバイルタッチワークベンチ & テーブル操作性の向上**：
  - モバイル・タッチ端末のデータグリッドを単一コンテナのネイティブ GPU CSS `position: sticky` ヘッダー（`.dbharbor-touch-sticky-grid`）へ刷新してちらつきを解消し、**タッチドラッグによる列幅変更**と**ダブルタップ自動幅調整**を追加。
  - サイドバーやグリッドへのタッチ用 `[⋮]` メニュー＆長押しメニュー、モバイル SQL クイック記号バー、省略セルのタップ全文確認、`34px〜40px` タップ領域と危険操作の分離、サイドバー `keep-alive` メモリキャッシュ、ポップアップの画面端はみ出し防止クランプを実装。
- **クロスプラットフォーム・サービスインストーラ & オンラインバイナリ更新 (`install-service.sh` / `install-service.ps1`)**：
  - `upgrade`（別名 `update`）/ `-Action Upgrade`（別名 `-Action Update`）による GitHub Releases からのオンライン更新、**フェイルクローズ方式の `SHA256SUMS.txt` 検証**、上書き前の `-version` スモークテスト、`--mirror` / `-Mirror` ミラー指定、`--admin-user` / `--admin-pass`（8 文字以上）初期化に対応。
  - 引用符付き `systemd` `ExecStart=` および macOS `launchd` `ProgramArguments` のカスタムパス引き継ぎ修正、Windows `irm | iex` 対応と `%TEMP%` 自動削除、終了待機ポーリング（`Stop-DBHarborServiceAndWait`）＋ `.old` アトミック置換による Windows `.exe` ロック競合解消を実施。

## [v1.0.0] - 2026-11-07

### 初回リリース
- **デュアルエディション — ネイティブデスクトップ GUI 版 & Web サーバー版 (`CGO_ENABLED=0`)**：
  - **デスクトップ GUI 版 (`dbharbor-desktop-*` / macOS `DBHarbor.app`)**：ダブルクリックでネイティブウィンドウを直接起動（Windows は `-H=windowsgui` コンソール非表示の `Win32 + Edge WebView2`、macOS は `Cocoa NSWindow + WKWebView`、Linux は `GTK3 + WebKit2GTK`）。**アカウント・ログイン不要の純粋なローカルモード**で動作し、OS ネイティブダークタイトルバー同期、ローカル DB ファイル選択＆エクスポート保存ダイアログ、マルチウィンドウ対応ローカルセッション、AES-256-GCM 暗号化ローカル接続ボールトを搭載。
  - **Web サーバー版 (`dbharbor-*`)**：外部依存ゼロのシングルバイナリ Web ワークベンチ。TCP およびネイティブ `AF_UNIX` Unix ドメインソケット待受（`-unix-socket`、`-unix-socket-perm`、`-unix-socket-group`）、古いソケットの自動削除、`X-Forwarded-For` / `X-Real-IP` クライアント IP 解決、サーバー側ローカル DB フォルダ参照・`.db/.sqlite/.duckdb` アップロード、Nginx / Caddy / Apache 用リバースプロキシ設定テンプレート（`deploy/`）を提供。
- **14 種類の組み込み純 Go データベースドライバ**：ClickHouse（Native TCP / HTTP 両対応・パーティション管理）、DuckDB、MySQL、MariaDB、TiDB、OceanBase、PostgreSQL、KingbaseES、SQLite 3、SQL Server、Oracle（Instant Client 不要）、Dameng DM8、Redis（DB0–15 ブラウザ・6 データ構造編集・JSON キー入出力・CLI）、MongoDB（JSON/テーブル表示・BSON CRUD・JSON/JSONL ドキュメント入出力・インデックス・`RunCommand`）に対応。
- **ビジュアルスキーマ設計・Navicat 風フルハイトグリッド・関数/トリガー作成・ストリーミング入出力**：
  - カラム追加/変更/名前変更/削除、インデックス作成/編集/削除・外部キー管理、SQL テンプレート生成、テーブル名変更/複製、`CREATE VIEW`、関数・プロシージャ・トリガー（`PROCEDURE` / `FUNCTION` / `TRIGGER`）のビジュアル新規作成＆編集、DB ユーザー＆権限管理（`GRANT` / `REVOKE` / Redis ACL / MongoDB ロール）、SQLite のトランザクション無損失テーブル再構築、リアルタイム DDL プレビュー。
  - 水平スクロールバーを下部固定したフルハイトデータグリッド（空白行クリックによる直接行追加、複数行一括カラム編集、メモ帳・Excel 互換の単一行/複数行 TSV コピー＆ペースト対応）と軽量化されたオフライン Monaco マルチタブ SQL コンソール（`Ctrl/Cmd + Enter`、`EXPLAIN`、SQL フォーマット、`AbortController` キャンセル、結果の直接ファイル保存、履歴・スニペット保存）。
  - モバイル画面（`<= 640px`）向けに再設計されたレスポンシブ UI：コンパクトヘッダー、シングルピル型タブ切替、折りたたみフィルタ、下部固定バッチ操作バー、`85dvh` ボトムシート行エディタ、Redis `キー一覧 | 値エディタ | CLI` 切替を搭載。
  - データベース全体の SQL ダンプ、複数テーブル選択/フィルタ条件/クエリ/MongoDB コレクションのストリーミングエクスポート、バッチインポート、SQL スクリプト直接実行。
- **Web マルチユーザー RBAC・セキュリティ強化・サービス権限管理**：
  - Web サーバー版は 3 階層マルチユーザー RBAC（`Admin` / `Read-Write` / `Read-Only`）と共有/プライベート接続分離、ブルートフォース対策レート制限（`HTTP 429`）、ステートレス HMAC SVG キャプチャ、オフライン QR コード RFC 6238 TOTP 二要素認証（2FA）＋リカバリコード 8 個、セッション強制失効、監査ログを搭載。
  - 実行時データはカレントディレクトリではなく OS 標準のアプリケーションデータディレクトリ（Windows: `%LOCALAPPDATA%\DBHarbor\data` / サービス時 `%ProgramData%\DBHarbor\data`、macOS: `~/Library/Application Support/DBHarbor/data`、Linux: `$XDG_DATA_HOME/dbharbor` または `/var/lib/dbharbor`）に保存。自動起動インストーラ（`install-service.sh`、`install-service.ps1`）で実行ユーザーと権限グループを指定可能。
- **英語ベースコード・多言語 UI・GitHub オンライン更新リマインダー**：
  - **English (`en-US`)**、**简体中文 (`zh-CN`)**、**繁體中文 (`zh-TW`)**、**日本語 (`ja-JP`)** のリアルタイム UI 切り替えと多言語ドキュメントを同梱。
  - GitHub `Willh92/DBHarbor` と連携したバージョン更新チェック（新バージョン自動通知、自動リマインダー切替スイッチ、バージョン別スキップ、デスクトップ版/サーバー版アセット自動判別）および **Apache License 2.0 (`Apache-2.0`)** ライセンスを採用。
