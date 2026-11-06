# 変更履歴 (Changelog)

**言語 / Language**: [English](./CHANGELOG.md) | [简体中文](./CHANGELOG.zh-CN.md) | [繁體中文](./CHANGELOG.zh-TW.md) | **日本語**

DBHarbor の主な変更履歴は本ファイルに記録され、GitHub リポジトリ [Willh92/DBHarbor](https://github.com/Willh92/DBHarbor.git) と同期されます。

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
