# 変更履歴 (Changelog)

**言語 / Language**: [English](./CHANGELOG.md) | [简体中文](./CHANGELOG.zh-CN.md) | [繁體中文](./CHANGELOG.zh-TW.md) | **日本語**

DBHarbor の主な変更履歴は本ファイルに記録され、GitHub リポジトリ [Willh92/DBHarbor](https://github.com/Willh92/DBHarbor.git) と同期されます。

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
