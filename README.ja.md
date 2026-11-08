# DBHarbor — クロスプラットフォーム対応マルチデータベース Web 管理ツール

**言語切替**: [English](./README.md) | [简体中文](./README.zh-CN.md) | [繁體中文](./README.zh-TW.md) | **日本語**

[![License: Apache-2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](./LICENSE)
[![Version](https://img.shields.io/badge/Version-v1.0.3-1677ff.svg)](./CHANGELOG.ja.md)
[![CGO_ENABLED=0](https://img.shields.io/badge/Go-CGO__ENABLED%3D0-00ADD8.svg)](https://github.com/Willh92/DBHarbor.git)

**phpMyAdmin の直感的なビジュアル操作性**と**モダンなマルチエンジン IDE 機能**を融合した、外部依存ゼロ・単一バイナリの Web データベース管理プラットフォームです。

- **公式リポジトリ**: [https://github.com/Willh92/DBHarbor.git](https://github.com/Willh92/DBHarbor.git)
- **変更履歴**: [CHANGELOG.ja.md](./CHANGELOG.ja.md)
- **ライセンス**: [Apache License 2.0](./LICENSE)

---

## 主な特徴

- **14 種類のデータベースに標準対応 (`CGO_ENABLED=0` Pure Go・無損失スリムバイナリ)**:
  - **OLAP / 分析系**: ClickHouse（Native TCP / HTTP 両対応、`MergeTree` ビジュアル作成、プロジェクション、データスキップインデックス、マテリアライズドビュー、テーブル `TTL`、外部辞書、SQL UDF、パーティション管理）、DuckDB（マルチスキーマ切替、シーケンス、スカラー/テーブルマクロ）
  - **リレーショナル RDBMS**: MySQL、MariaDB、TiDB、OceanBase、PostgreSQL、KingbaseES、SQLite 3（`FTS5`/`RTree` 仮想テーブル & `PRAGMA`/整合性インスペクタ）、SQL Server、Oracle（Instant Client 不要・236 文字セット Gzip 無損失圧縮オーバーレイ内蔵）、Dameng (DM8)
  - **Key-Value & ドキュメント NoSQL**: Redis（DB0–15 ブラウザ、6 データ構造エディタ、Stream コンシューマーグループ管理、Slowlog、`CONFIG GET`、Pub/Sub、内蔵 CLI）、MongoDB（JSON / テーブル表示、BSON CRUD、View / Time-Series / Capped コレクション、`$jsonSchema` バリデータ、TTL/Partial/Sparse インデックス & `RunCommand` コンソール）
- **14 DB 固有オブジェクト CRUD・ビジュアルスキーマ変更・権限管理 & SQL コンソール**:
  - **テーブル構造・DB 固有オブジェクト作成 & ユーザー権限管理**: カラムの追加・変更・リネーム・削除（生成/計算カラム・`CHECK` 制約対応）、インデックス/外部キー/パーティション管理、**スケジュールイベント**（MySQL/MariaDB/OceanBase）、**シーケンス**（PG/Oracle/DM8/MSSQL/MariaDB/TiDB/DuckDB）、**マテリアライズドビュー＆リフレッシュ**（PG/Oracle/DM8/ClickHouse）、**パッケージ (`Spec` + `Body`)・シノニム・DB リンク**（Oracle/DM8）、**拡張機能・カスタム型 (`ENUM`/`DOMAIN`)**（PG/KingbaseES）、**関数・プロシージャ・トリガー**のビジュアル作成＆編集、DB ユーザー＆権限管理（`GRANT` / `REVOKE`、Redis ACL、MongoDB ロール）、SQLite 無損失テーブル再構築、サイドバーの**デフォルト全折りたたみ＋一括展開/折りたたみスイッチ**と接続/DB/スキーマ間の完全な状態分離。
  - **ブラウザからの一時ローカル単一ファイル DB (`SQLite 3` / `DuckDB`)・フルハイトグリッド & スマート SQL コンソール**: Web サーバー版で手元の `.db`/`.sqlite`/`.duckdb` ファイルをドラッグ＆ドロップまたは **「ローカル DB を開く (`Open Local DB`)」** で直接開き、Chromium ブラウザでの**元ファイルへの直接上書き保存**および最新スナップショットのダウンロードに対応。省メモリ `CellStateHub` フルハイトグリッド（空白行クリック直接挿入、一括カラム編集、Excel・メモ帳互換の TSV コピー＆ペースト）、軽量 Monaco Editor、`Ctrl/Cmd + Enter` 実行、`EXPLAIN` 実行計画、DB 全体/複数テーブルのストリーミング入出力（アップロード RAM `< 64 KiB`）、SQL スクリプト直接実行。
- **Web マルチユーザー RBAC・ログイン画面ブランディング・セキュリティ保護 & 緊急リカバリ *(Web サーバー版のみ)***:
  - **初期デフォルト認証情報ゼロ・ブランディング設定 & インデックス登録防止**: 初回起動時の**初期管理者セットアップウィザード**（パスワード最低 `8` 文字、`admin123` 廃止）、未認証時の情報漏洩ゼロ化、ブラウザ `<title>`・上部アイコン（`<= 128 KB` カスタム画像＋Favicon 同期）・見出し・サブタイトルの自由変更、および `/robots.txt`・`X-Robots-Tag`・`<meta name="robots">` による三重の検索エンジンクローラ拒否を搭載。
  - **セッション優先の DB パスワード非保存ポリシー & 認証エラー時の再入力プロンプト**: 新規データベース接続はデフォルトで **「パスワードを保存」がオフ**（パスワードは現在のログインセッションのメモリ内でのみ保持されディスクには一切保存されず、次回ログイン時に認証情報の入力を要求。チェック時は `AES-256-GCM` で暗号化保存され、保存済みの接続も「接続を編集」からオフにしてディスク上の暗号化パスワードを即座に消去可能）。また、パスワード保存済みの接続でも DB 側のアカウントやパスワード変更・不一致でエラーになった際は、古いプールを破棄して再入力モーダルを自動表示し、認証成功後に元のリクエストを透過的に再試行します。
  - **3 階層 RBAC・TOTP 2FA & 多層サンドボックス**: 3 階層 RBAC（`Admin` / `Read-Write` / `Read-Only`）とユーザー別のタブ/SQL 下書き分離、秘密鍵派生の使い捨て CAPTCHA、`trustedProxies` 対応レート制限 (`HTTP 429`)、オフライン QR コードによる RFC 6238 TOTP 2FA + 8 個のリカバリコード + 2FA 試行回数制限、厳格な TLS 証明書検証、非管理者向け `<DataDir>/local_databases/u_<hash>/` ＆ `<DataDir>/ephemeral_workspaces/` ユーザー別サンドボックス（字句スキャナによる SQLite `ATTACH`/ファイル関数遮断、サーバー管理の `duckdbCliPath` と DuckDB `-safe` モード + `stdin` 入力 + `enable_external_access = false`、MariaDB `/*M!...*/` 読み取り専用ガード）、全リモートドライバのダイヤル時 DNS リバインディング SSRF 検証、参照カウント式コネクションプール、SQL Server `USE [db]` 接続固定、永続設定 `<DataDir>/config.json` と**自動無害化機能付き緊急管理者リカバリ**に対応。
- **ネイティブダークタイトルバー同期・モバイルタッチ最適化 & 多言語対応 (i18n)**:
  - デスクトップ版の OS ネイティブダークタイトルバー同期（`Win32 DWM`、`macOS DarkAqua`、`Linux GTK3`）に加え、モバイル・タッチ端末向けにちらつきゼロのネイティブ GPU CSS `position: sticky` ヘッダー、タッチドラッグ列幅調整＋ダブルタップ自動幅拡張、サイドバー/テーブルの `[⋮]` ＆長押しメニュー、モバイル SQL クイック記号バー、省略セルのタップ確認、サイドバー `keep-alive` メモリキャッシュ、ポップアップの画面端クランプを搭載。
  - **TCP ポート**および **Unix ドメインソケット (`AF_UNIX`)** 待受に両対応（`-unix-socket`、`-unix-socket-perm`、`-unix-socket-group`）。
  - **English (`en-US`)・简体中文 (`zh-CN`)・繁體中文 (`zh-TW`)・日本語 (`ja-JP`)** のリアルタイム切替、およびオンライン更新通知・ワンコマンドサービス更新に対応。

---

## クイックスタート & ビルド

```bash
# 1. 開発モード起動 (Go API :8787 + Vite :5173)
npm run dev

# 2. 現在の OS/Arch 向け Web サーバー版 + ネイティブデスクトップ GUI 版をビルド (./dist に出力)
npm run build

# 3. 全 6 プラットフォーム向けクロスコンパイル (サーバー版 + デスクトップ GUI 版 + macOS .app + サービススクリプト)
npm run build:all

# 4. 自動テストスイート実行
npm test
```

### 2 つのエディション (`dist/`)

1. **デスクトップ GUI 版 (`dbharbor-desktop-<os>-<arch>` / `DBHarbor-darwin-<arch>.app`) — ログイン不要の純粋なローカルクライアント**：
   - ダブルクリックでネイティブウィンドウを直接起動（コンソール非表示・**ユーザーアカウント機能を持たない純粋なローカルモード**。OS ネイティブのファイル選択・名前を付けて保存ダイアログ、ホスト全ディレクトリへのアクセス、AES-256-GCM 暗号化接続ボールトを搭載）。`<DataDir>/config.json`、初期管理者セットアップ、ログイン画面設定、緊急リカバリモード、Web 用ファイル/ネットワーク制限は**デスクトップ GUI 版には適用されません**。
2. **Web サーバー版 (`dbharbor-<os>-<arch>`) — セルフホスト / チーム共有サービス**：
   - 起動後、`http://localhost:8787` にアクセスします（固定の初期パスワードはありません — 初回アクセス時に**初期管理者セットアップウィザード**（最低 `8` 文字）が表示され、ログイン画面のタイトル・アイコン設定も可能です。CLI の `-admin-user` / `-admin-pass` でも初期設定可能）。

---

## OS 標準データディレクトリ・CLI オプション & 永続 `config.json`

DBHarbor はカレントディレクトリを汚染せず、各 OS の標準アプリケーションデータ領域にメタデータ (`dbharbor.db`) と暗号鍵 (`.secret.key`) を自動保存します（`-data-dir` で変更可能）：

| OS | 通常ユーザー起動時 | システムサービス / デーモン起動時 |
| :--- | :--- | :--- |
| **Windows** | `%LOCALAPPDATA%\DBHarbor\data` | `%ProgramData%\DBHarbor\data` (`C:\ProgramData\DBHarbor\data`) |
| **macOS** | `~/Library/Application Support/DBHarbor/data` | `/Library/Application Support/DBHarbor/data` |
| **Linux** | `$XDG_DATA_HOME/dbharbor` (`~/.local/share/dbharbor`) | `/var/lib/dbharbor` |

### CLI オプション & 環境変数 *(Web サーバー版)*

設定の優先順位：**`CLI フラグ > DBHARBOR_* 環境変数 > <DataDir>/config.json > デフォルト値`**。

| CLI フラグ | 環境変数 | デフォルト | 説明 |
| :--- | :--- | :--- | :--- |
| `-config` | `DBHARBOR_CONFIG_FILE` | `<DataDir>/config.json` | Web サーバー版の永続 JSON 設定ファイルパス（権限 `0600`） |
| `-host` | `DBHARBOR_HOST` | `0.0.0.0` | TCP 待受アドレス（`unix:/path/to/dbharbor.sock` 短縮表記も可） |
| `-port` | `DBHARBOR_PORT` | `8787` | HTTP TCP ポート番号 |
| `-unix-socket` | `DBHARBOR_UNIX_SOCKET` | *(空)* | Unix ドメインソケット待受パス（例: `/run/dbharbor/dbharbor.sock`） |
| `-unix-socket-perm` | `DBHARBOR_UNIX_SOCKET_PERM` | `0660` | Unix ソケットの 8 進数パーミッション |
| `-unix-socket-group` | `DBHARBOR_UNIX_SOCKET_GROUP` | *(空)* | Unix ソケットの所有グループ名または GID（`www-data`、`nginx` など） |
| `-data-dir` | `DBHARBOR_DATA_DIR` | *(OS 標準)* | メタデータ DB・`config.json`・`.secret.key` の保存先ディレクトリ |
| `-admin-user` | `DBHARBOR_ADMIN_USER` | *(空 — Web 設定)* | 初回ヘッドレス初期化用の管理者ユーザー名（ユーザー未登録時のみ有効） |
| `-admin-pass` | `DBHARBOR_ADMIN_PASS` | *(空 — Web 設定)* | 初回ヘッドレス初期化用の管理者パスワード（**最低 8 文字**） |
| `-idle-ttl` | `DBHARBOR_IDLE_TTL` | `30` | アイドル DB 接続プールの自動解放タイムアウト（分） |
| — | `DBHARBOR_MAX_IMPORT_MB` | `256` | インポートファイルの最大アップロードサイズ（MB） |
| — | `DBHARBOR_TRUSTED_PROXIES` | *(ループバック)* | `X-Forwarded-For` / `X-Real-IP` を信頼するプロキシ IP/CIDR（カンマ区切り） |
| — | `DBHARBOR_CORS_ORIGINS` | *(同一オリジン)* | 許可する CORS オリジン一覧 |
| — | `DBHARBOR_DUCKDB_PATH` | *(自動検出)* | サーバー管理の DuckDB CLI パス (`security.duckdbCliPath`) |
| — | `DBHARBOR_ALLOW_PRIVATE_NET_NON_ADMIN` | `false` | 非管理者によるプライベート/ループバック網への接続許可 |
| `-init-only` | — | `false` | 暗号鍵・`config.json`・初期管理者を初期化して即座に終了 |
| `-recover-admin` | — | `false` | 緊急管理者パスワード / 2FA リカバリを実行して即座に終了 |
| `-recover-user` | — | *(主管理者)* | `-recover-admin` の対象管理者ユーザー名 |
| `-recover-pass` | — | *(空)* | 新しいパスワード（`>= 8` 文字）または `auto`（16 文字ランダム生成） |
| `-reset-2fa` | — | `false` | `-recover-admin` 実行時に TOTP 2FA とリカバリコードもリセット |
| `-open` | — | `false` | TCP 起動時に標準ブラウザを自動で開く |
| `-version` | — | `false` | バージョン・コミット・プラットフォーム情報を出力 |

### 永続サーバー設定 (`<DataDir>/config.json`) & 緊急管理者リカバリ *(Web サーバー版のみ)*

**Web サーバー版**は起動時に `<DataDir>/config.json`（パーミッション `0600`）を自動生成します：

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

管理者パスワードや 2FA デバイスを紛失した場合は、以下の 2 通りの方法で安全に復旧できます：
1. **`<DataDir>/config.json` を編集して再起動**：`"recovery"` を `{ "enabled": true, "adminUsername": "admin", "newPassword": "YourNewStrongPassword", "reset2FA": true }`（8 文字以上、または `"newPassword": "auto"` で 16 文字のランダムパスワードを生成して起動ログに出力）に設定し、DBHarbor を再起動します。起動時に `dbharbor.db` を `<DataDir>/.backups/` へ自動退避し、パスワード/2FA のリセットと既存セッション失効を行った後、**即座に `config.json` を自動無害化（`"enabled": false, "newPassword": ""`）**して平文パスワードをディスクから消去します。
2. **ワンショット CLI 復旧**：`dbharbor -data-dir /var/lib/dbharbor -recover-admin -recover-pass auto -reset-2fa`

> **Web サーバー版のローカルファイル & ネットワーク境界**：DBHarbor の内部メタデータ DB（`dbharbor.db`）・`config.json`・暗号鍵（`.secret.key`）は全モードで自動的に非表示・保護されます。**Web サーバー版**では、非管理者（`Read-Write`）によるサーバー保存 SQLite / DuckDB ファイル操作はユーザー別の `<DataDir>/local_databases/u_<hash>/` サンドボックス内に制限され、ブラウザから **「ローカル DB を開く (`Open Local DB`)」** で開いた一時単一ファイル DB はユーザー別の `0700` `<DataDir>/ephemeral_workspaces/` に分離されます（マジックヘッダー検証、12 時間アイドル自動クリーンアップ、字句スキャナによる SQLite `ATTACH`/ファイル関数遮断、サーバー管理の `security.duckdbCliPath` と DuckDB `-safe` モード + `stdin` 入力 + `enable_external_access = false`、およびダイヤル時の DNS リバインディング・プライベート網 SSRF ガードを適用）。**デスクトップ GUI 版**はユーザーアカウント機能を持たないログイン不要のローカルモードでホスト全体のファイルにアクセス可能です。

---

## キーボードショートカット

| ショートカット | 対象範囲 | 動作 |
| :--- | :--- | :--- |
| `Alt + Shift + N` | ワークベンチ全体 | **新規データソース接続** モーダルを開く |
| `Alt + T` | ワークベンチ全体 | 現在の接続で新規 **SQL コンソール** タブを開く |
| `Alt + W` | ワークベンチ全体 | アクティブなタブを閉じる |
| `Alt + 1` … `Alt + 9` | ワークベンチ全体 | タブ `1`〜`9` へ直接切り替え |
| `Ctrl / Cmd + B` | ワークベンチ全体 | 左サイドバーの表示 / 非表示を切り替え |
| `Alt + N` | データグリッド / セル編集 | グリッド表示中は **インライン行追加**、セル入力中は値を `NULL` に設定 |
| `Ctrl / Cmd + S` | データグリッド | 保留中の行追加・セル変更を一括コミット |
| `Ctrl / Cmd + C` / `Ctrl / Cmd + V` | データグリッド | 単一行・複数行の TSV コピー＆ペースト（Excel・メモ帳互換） |
| `Ctrl / Cmd + Enter` | SQL コンソール | 選択範囲またはスクリプト全体の SQL を実行 |

---

## 自動起動サービス登録・オンライン更新・初期管理者設定

`scripts/` および `dist/` 同梱の **Web サーバー版**サービス管理スクリプトは、**GitHub Releases からの自動ダウンロード・フェイルクローズ方式の `SHA256SUMS.txt` 検証・置換前の `-version` スモークチェック**、ミラー指定（`--mirror` / `-Mirror`）、インストール先指定（`--install-dir` / `-InstallDir`）、**初回デプロイ時の管理者初期化**（`--admin-user` / `--admin-pass` 最低 `8` 文字 — 初期化後に一時認証情報を自動消去）、および既存のサービス引数と `DataDir` を維持した **ロック安全なワンコマンドオンライン更新**（`upgrade` / `update` および `-Action Upgrade` / `-Action Update`）に対応しています。

### 1. Linux (`systemd` / `OpenRC`)

```bash
chmod +x ./dist/install-service.sh

# Unix ドメインソケット待受 + 実行ロール・Nginx グループ権限 (www-data / nginx)・初期管理者アカウント設定（8 文字以上）
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

# ローカル TCP 待受
sudo ./dist/install-service.sh install --user dbharbor --group dbharbor --host 127.0.0.1 --port 8787

# GitHub Releases から最新版へオンライン更新＆再起動（別名 update、--mirror <url>、--version <tag> に対応）
sudo ./dist/install-service.sh upgrade
```

### 2. macOS (`launchd`)

```bash
chmod +x ./dist/install-service.sh
sudo ./dist/install-service.sh install \
  --host 127.0.0.1 --port 8787 \
  --user _www --group _www \
  --admin-user ops_admin --admin-pass 'StrongPass!2026'
# または非 root ユーザーサービス:
./dist/install-service.sh install --user-mode --port 8787

# オンライン更新（plist 内のカスタムパスを自動維持）＆再起動
sudo ./dist/install-service.sh upgrade
```

### 3. Windows (`ScheduledTask` + `icacls` アクセス権保護)

```powershell
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 `
  -Action Install `
  -ServiceAccount "NT AUTHORITY\LocalService" `
  -PermissionGroup "Administrators" `
  -AdminUser "ops_admin" `
  -AdminPass "StrongPass!2026" `
  -HostAddr "127.0.0.1" -Port 8787

# オンライン更新＆ロック安全再起動（別名 -Action Update、-Mirror <url> に対応）
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 -Action Upgrade
```

---

## リバースプロキシ設定ガイド (Nginx / Caddy / Apache)

設定テンプレート一式は [`deploy/`](./deploy) ディレクトリに同梱されています（リバースプロキシが別ホストにある場合は `<DataDir>/config.json` の `trustedProxies` にプロキシの IP/CIDR を追加してください）。

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

## オープンソースライセンス

DBHarbor は **[Apache License, Version 2.0](./LICENSE)** (`Apache-2.0`) のもとで公開されています。
