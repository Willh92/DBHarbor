# DBHarbor — クロスプラットフォーム対応マルチデータベース Web 管理ツール

**言語切替**: [English](./README.md) | [简体中文](./README.zh-CN.md) | [繁體中文](./README.zh-TW.md) | **日本語**

[![License: Apache-2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](./LICENSE)
[![Version](https://img.shields.io/badge/Version-v1.0.0-1677ff.svg)](./CHANGELOG.ja.md)
[![CGO_ENABLED=0](https://img.shields.io/badge/Go-CGO__ENABLED%3D0-00ADD8.svg)](https://github.com/Willh92/DBHarbor.git)

**phpMyAdmin の直感的なビジュアル操作性**と**モダンなマルチエンジン IDE 機能**を融合した、外部依存ゼロ・単一バイナリの Web データベース管理プラットフォームです。

- **公式リポジトリ**: [https://github.com/Willh92/DBHarbor.git](https://github.com/Willh92/DBHarbor.git)
- **変更履歴**: [CHANGELOG.ja.md](./CHANGELOG.ja.md)
- **ライセンス**: [Apache License 2.0](./LICENSE)

---

## 主な特徴

- **14 種類のデータベースに標準対応 (`CGO_ENABLED=0` Pure Go)**:
  - **OLAP / 分析系**: ClickHouse（Native TCP / HTTP 両対応、`MergeTree` ビジュアル作成、パーティション管理）、DuckDB
  - **リレーショナル RDBMS**: MySQL、MariaDB、TiDB、OceanBase、PostgreSQL、KingbaseES、SQLite 3、SQL Server、Oracle（Instant Client 不要）、Dameng (DM8)
  - **Key-Value & ドキュメント NoSQL**: Redis（DB0–15 ブラウザ、6 データ構造エディタ、内蔵 CLI）、MongoDB（JSON / テーブル表示、BSON CRUD、インデックス & `RunCommand` コンソール）
- **ビジュアルスキーマ変更・DB権限管理 & SQL コンソール**:
  - **テーブル構造・関数/トリガー作成 & DB ユーザー権限管理**: カラムの追加・変更・リネーム・削除、型・デフォルト値・`NULL` 制約変更、インデックス/外部キー管理、関数・ストアドプロシージャ・トリガーのビジュアル新規作成＆編集、データベースユーザー＆権限管理（`GRANT` / `REVOKE`、Redis ACL、MongoDB ロール）、テーブル複製・リネーム、ビュー作成、SQLite トランザクション無損失再構築、DDL プレビュー。
  - **Navicat 風フルハイトグリッド・空白行直接追加 & スマート SQL コンソール**: フルハイト表示と空白行クリック直接挿入、一括カラム編集、メモ帳・Excel 互換の TSV コピー＆ペースト（`Ctrl+C` / `Ctrl+V`）、ローカル DB ファイル参照・アップロード（`SQLite 3` / `DuckDB`）、軽量 Monaco Editor、`Ctrl/Cmd + Enter` 実行、`EXPLAIN` 実行計画、DB 全体/複数テーブル SQL ダンプ、MongoDB / Redis JSON インポート＆エクスポート、SQL スクリプト直接実行。
- **Web マルチユーザー RBAC・認証保護 & TOTP 二要素認証 (2FA)**:
  - Web サーバー版は 3 階層マルチユーザー RBAC（`Admin` / `Read-Write` / `Read-Only`）と共有/プライベート接続分離、ブルートフォース防止レート制限 (`HTTP 429`)、オフライン HMAC SVG 画像認証、オフライン QR コードによる RFC 6238 TOTP 2FA + 8 個のリカバリコード、セッション強制終了、監査ログ、AES-256-GCM 接続情報暗号化に対応。
- **ネイティブダークタイトルバー同期・モバイル最適化 & 多言語対応 (i18n)**:
  - デスクトップ版の OS ネイティブダークタイトルバー同期（`Win32 DWM`、`macOS DarkAqua`、`Linux GTK3`）およびモバイル (`<= 640px`) 専用レスポンシブ UI に対応。
  - **TCP ポート**および **Unix ドメインソケット (`AF_UNIX`)** 待受に両対応（`-unix-socket`、`-unix-socket-perm`、`-unix-socket-group`）。
  - **English (`en-US`)・简体中文 (`zh-CN`)・繁體中文 (`zh-TW`)・日本語 (`ja-JP`)** のリアルタイム切替と、`Willh92/DBHarbor` からのオンライン更新通知・自動通知スイッチに対応。

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

- **デスクトップ GUI 版 (`dbharbor-desktop-<os>-<arch>` / `DBHarbor-darwin-<arch>.app`)**：ダブルクリックでネイティブウィンドウを直接起動（コンソール非表示・**アカウント/ログイン不要の純粋なローカルアプリ**）。
- **Web サーバー版 (`dbharbor-<os>-<arch>`)**：起動後、`http://localhost:8787` にアクセスします（初期アカウント: `admin` / `admin123` — 初回ログイン後にパスワード変更と 2FA 有効化を推奨します）。

---

## OS 標準データディレクトリ

DBHarbor はカレントディレクトリを汚染せず、各 OS の標準アプリケーションデータ領域にメタデータ (`dbharbor.db`) と暗号鍵 (`.secret.key`) を自動保存します（`-data-dir` で変更可能）：

| OS | 通常ユーザー起動時 | システムサービス / デーモン起動時 |
| :--- | :--- | :--- |
| **Windows** | `%LOCALAPPDATA%\DBHarbor\data` | `%ProgramData%\DBHarbor\data` (`C:\ProgramData\DBHarbor\data`) |
| **macOS** | `~/Library/Application Support/DBHarbor/data` | `/Library/Application Support/DBHarbor/data` |
| **Linux** | `$XDG_DATA_HOME/dbharbor` (`~/.local/share/dbharbor`) | `/var/lib/dbharbor` |

> **ローカル DB ファイルのセキュリティ境界**：DBHarbor の内部メタデータ DB（`dbharbor.db`）および暗号鍵（`.secret.key`）は全モードで自動的に非表示・保護され、接続対象として指定できません。Web サーバー版では非管理者（`Read-Write`）による SQLite / DuckDB ファイルの参照・作成は `<DataDir>` サンドボックス内（既定 `<DataDir>/local_databases`）に厳格に制限され、`Admin` 管理者およびデスクトップ GUI 版のみがホスト全体のディレクトリにアクセスできます。

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

## 自動起動サービス登録と実行ロール・権限グループ設定

### 1. Linux (`systemd` / `OpenRC`)

```bash
chmod +x ./dist/install-service.sh

# Unix ドメインソケット待受 + 実行ユーザー・グループおよび Nginx グループ (www-data / nginx) 権限付与
sudo ./dist/install-service.sh install \
  --user dbharbor \
  --group dbharbor \
  --socket-group www-data \
  --supplementary-groups www-data \
  --unix-socket /run/dbharbor/dbharbor.sock \
  --unix-socket-perm 0660

# ローカル TCP 待受
sudo ./dist/install-service.sh install --user dbharbor --group dbharbor --host 127.0.0.1 --port 8787
```

### 2. macOS (`launchd`)

```bash
chmod +x ./dist/install-service.sh
sudo ./dist/install-service.sh install --host 127.0.0.1 --port 8787 --user _www --group _www
# または非 root ユーザーサービス:
./dist/install-service.sh install --user-mode --port 8787
```

### 3. Windows (`ScheduledTask` + `icacls` アクセス権保護)

```powershell
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 `
  -Action Install `
  -ServiceAccount "NT AUTHORITY\LocalService" `
  -PermissionGroup "Administrators" `
  -HostAddr "127.0.0.1" -Port 8787
```

---

## リバースプロキシ設定ガイド (Nginx / Caddy / Apache)

設定テンプレート一式は [`deploy/`](./deploy) ディレクトリに同梱されています。

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
