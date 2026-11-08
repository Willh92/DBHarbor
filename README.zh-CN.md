# DBHarbor — 跨平台多数据库 Web 管理工作台

**语言切换**: [English](./README.md) | **简体中文** | [繁體中文](./README.zh-TW.md) | [日本語](./README.ja.md)

[![License: Apache-2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](./LICENSE)
[![Version](https://img.shields.io/badge/Version-v1.0.3-1677ff.svg)](./CHANGELOG.zh-CN.md)
[![CGO_ENABLED=0](https://img.shields.io/badge/Go-CGO__ENABLED%3D0-00ADD8.svg)](https://github.com/Willh92/DBHarbor.git)

一款融合 **phpMyAdmin 经典高效可视化操作体验** 与 **现代多引擎 IDE 能力** 的零外部依赖单文件跨平台 Web 数据库管理平台。

- **官方代码仓库**：[https://github.com/Willh92/DBHarbor.git](https://github.com/Willh92/DBHarbor.git)
- **版本更新日志**：[CHANGELOG.zh-CN.md](./CHANGELOG.zh-CN.md)
- **开源许可协议**：[Apache License 2.0](./LICENSE)

---

## 核心特性

- **14 种数据库驱动开箱即用 (`CGO_ENABLED=0` 纯 Go，无损精简瘦身包体)**：
  - **OLAP 分析型**：ClickHouse（Native TCP / HTTP 双协议、`MergeTree` 可视化建表、投影 `Projections`、跳数索引 `Skipping Indexes`、物化视图、`TTL` 策略、外部字典、SQL UDF 与分区治理）、DuckDB（多 Schema 切换、序列 `Sequences`、标量/表宏 `Macros`）
  - **关系型 RDBMS**：MySQL、MariaDB、TiDB、OceanBase、PostgreSQL、人大金仓 (KingbaseES)、SQLite 3（`FTS5`/`RTree` 虚拟表与 `PRAGMA`/完整性体检面板）、SQL Server、Oracle（纯 Go 免 Instant Client，内置 236 张字符集表 Gzip 无损压缩覆盖层）、达梦 DM8
  - **键值与文档 NoSQL**：Redis（DB0–15 浏览器、6 种数据结构编辑器、Stream 消费者组管理、慢日志、运行参数 `CONFIG GET`、Pub/Sub 与内嵌 CLI）、MongoDB（JSON / 二维表格双视图、BSON CRUD、只读聚合视图 / 时序集合 / 固定大小集合、`$jsonSchema` 校验规则、TTL/Partial/Sparse 高级索引与 `RunCommand` 终端）
- **完备的 14 引擎特有对象 CRUD、可视化结构设计、权限分配与 SQL 运维闭环**：
  - **可视化表结构、各引擎特有对象管理与数据库权限治理**：支持新增/修改/重命名/删除字段（含生成列/计算列与 `CHECK` 约束）、索引/外键/表分区管理，可视化新建、编辑与实时预览 **定时事件 (`Events`)**（MySQL/MariaDB/OceanBase）、**序列 (`Sequences`)**（PG/Oracle/DM8/MSSQL/MariaDB/TiDB/DuckDB）、**物化视图及刷新**（PG/Oracle/DM8/ClickHouse）、**程序包 (`PACKAGE` 包头+包体)、同义词与数据库链接 (`DB Links`)**（Oracle/DM8）、**扩展插件与自定义类型 (`ENUM`/`DOMAIN`)**（PG/金仓）、**函数/存储过程/触发器**；支持数据库级账号与权限分配（`GRANT` / `REVOKE`、Redis ACL、MongoDB 角色）、SQLite 事务级无损重建表，以及左侧对象树**默认全折叠 + 一键全部展开/折叠**与跨连接/跨库/跨 Schema 严格状态隔离。
  - **Web 浏览器直开本地单文件库 (`SQLite 3` / `DuckDB`)、Navicat 级铺满网格与多标签 SQL 终端**：Web 服务版支持拖拽或一键 **“打开本地库 (`Open Local DB`)”** 直接载入本机 `.db`/`.sqlite`/`.duckdb` 文件，在 Chromium 浏览器支持通过 File System Access API **直接写回覆盖本机原文件**或一键下载最新二进制快照；全高铺满数据网格（`CellStateHub` 低内存架构）支持点击空白行直接新增、多行批量改列、单行/多行 TSV 复制粘贴（`Ctrl+C` / `Ctrl+V` 兼容记事本与 Excel），内嵌轻量化 Monaco Editor 支持自动补全、`Ctrl/Cmd + Enter` 执行、`EXPLAIN` 执行计划、SQL 格式化、查询中止 (`AbortController`)、历史记录与脚本另存为。
  - **流式导入导出与批量表运维**：支持整库与多表勾选 SQL Dump 备份、单表/筛选结果/自定义查询/MongoDB/Redis/DuckDB 流式导出与 `< 64 KiB` 内存流式分片上传（`SQL` / `CSV-BOM` / `CSV` / `JSON` / `JSONL` / `Markdown`）、大结果集流式内存上限保护、大文件分批事务导入，以及直接粘贴执行多条 SQL 脚本。
- **Web 多成员三级 RBAC、登录品牌定制、安全沙箱与应急找回 *(仅 Web 服务版)***：
  - **无默认弱口令初始化、登录页品牌定制与防搜索引擎收录**：首次启动自动引导**管理员初始化向导**（密码至少 `8` 位，移除内置 `admin123`）、零指纹登录界面、支持自定义浏览器 `<title>`、顶部图标（上传 `<= 128 KB` 图标并同步 Favicon）及主副标题，内置 `/robots.txt`、`X-Robots-Tag` 与 `<meta name="robots">` 三重防爬虫收录保护。
  - **默认不落盘的会话级数据库凭据策略与认证失败重输**：新建数据库连接**默认不勾选“记住密码”**（密码仅保存在当前登录会话内存中、磁盘零落盘，下次登录连接时自动弹窗提示输入；可选勾选以 `AES-256-GCM` 加密持久化保存，已记住密码的连接也可随时通过“编辑连接”关闭记住并立即清除本地密文）；当记住密码的连接因数据库账户或密码变更/错误导致连接或执行失败时，系统自动驱逐旧连接池并弹出凭据重输窗口，验证通过后无缝重试原请求。
  - **多成员 RBAC、2FA 与纵深安全加固**：三级 RBAC（`Admin` / `Read-Write` / `Read-Only`）与按账号独立隔离的工作区标签页/SQL 草稿、主密钥派生单次有效图形验证码、`trustedProxies` 可信反代限流 (`HTTP 429`)、离线二维码 **RFC 6238 TOTP 双因素认证** + 8 个一次性备用恢复码 + 2FA 尝试次数限制、驱动级严格 TLS 证书校验、非管理员 `<DataDir>/local_databases/u_<hash>/` 与 `<DataDir>/ephemeral_workspaces/` 按用户隔离沙箱（词法级拦截 SQLite `ATTACH`/文件函数、DuckDB `-safe` 模式 + `stdin` 传参 + `enable_external_access = false`、解包识别 MariaDB `/*M!...*/` 可执行注释）、全远程驱动拨号期防 DNS 重绑定 SSRF 拦截、引用计数并发连接池安全回收、SQL Server 独占连接 `USE [db]` 隔离，以及 `<DataDir>/config.json` 持久化配置与**自动擦除式管理员应急找回**。
- **原生深色标题栏同步、手机端全触控工作台与多语言国际化 (i18n)**：
  - 桌面端支持系统级原生深色标题栏同步（`Win32 DWM`、`macOS DarkAqua`、`Linux GTK3`）；手机端与触屏设备专属搭载 GPU 原生 `position: sticky` 零闪烁固定表头、手指拖拽调整列宽 + 双击自动撑开列宽、侧栏与表格触控 `[⋮]` 及长按菜单、手机 SQL 快捷符号栏、长文本单元格点击预览、`85dvh` 底部抽屉编辑器、左侧菜单 `keep-alive` 瞬时缓存与全局弹层视口防溢出夹紧。
  - 同时支持 **TCP 端口监听** 与 **Unix Domain Socket (`AF_UNIX`) 监听**（`-unix-socket`、`-unix-socket-perm`、`-unix-socket-group`）。
  - 原生支持 **English (`en-US`)、简体中文 (`zh-CN`)、繁體中文 (`zh-TW`)、日本語 (`ja-JP`)** 实时切换，支持连接 `Willh92/DBHarbor` 在线检测新版本与一键命令升级后台服务。

---

## 快速启动与构建

```bash
# 1. 开发热加载模式 (Go API :8787 + Vite 前端 :5173)
npm run dev

# 2. 编译当前系统 Web 服务版 + 原生桌面窗体版可执行程序 (输出至 ./dist)
npm run build

# 3. 交叉编译全部 6 大平台（服务版 + 桌面窗体版 + macOS .app 应用包 + 服务安装脚本 + 反代模板）
npm run build:all

# 4. 运行自动化单元、端到端与基准测试
npm test
```

### 双版本形态说明 (`dist/`)

1. **原生桌面窗体版 (`dbharbor-desktop-<os>-<arch>` 与 `DBHarbor-darwin-<arch>.app`) — 纯本地免登录客户端**：
   - 双击即可直接打开原生系统界面窗口（Windows 下如 `dbharbor-desktop-windows-amd64.exe` 采用 `Win32 + Edge WebView2` 无黑框直启，macOS 采用 `Cocoa NSWindow + WKWebView` 并提供 `.app` 包，Linux 采用 `GTK3 + WebKit2GTK`）。
   - **纯本地模式（无用户账户体系）**：免登录直达工作台，内置原生系统本地数据库选择与另存为文件对话框、完整本机文件访问权限及本地 AES-256-GCM 加密连接库。服务端专属机制（`<DataDir>/config.json`、首次管理员初始化向导、登录页定制、管理员应急找回以及 Web 本地文件/内网沙箱）**不适用于桌面窗体版**。
2. **Web 服务版 (`dbharbor-<os>-<arch>`) — 服务端部署 / 团队共享工作台**：
   - 运行 `./dist/dbharbor-<os>-<arch>`（可通过 `-version` 查看版本信息），浏览器访问 `http://localhost:8787`（或通过 `-unix-socket` 监听 Unix Domain Socket）。
   - **首次启动管理员初始化与防收录**：无内置弱默认密码——当用户库为空时，首次访问自动进入一次性**管理员初始化向导**（密码至少 `8` 位，可同步定制登录页标题与图标文案），也支持通过 `-admin-user` / `-admin-pass` 无头预置初始管理员；默认开启三重防搜索引擎爬虫收录。

---

## 系统标准数据目录、启动参数与持久化 `config.json`

DBHarbor **绝不会把运行时数据散落在程序启动目录**（除非显式指定 `-data-dir`）。默认情况下，内部元数据库 (`dbharbor.db`) 与主密钥 (`.secret.key`) 自动存放在各操作系统的标准应用数据目录：

| 操作系统 | 普通用户 / 桌面启动默认目录 | 系统后台服务 (Daemon / Service) 默认目录 |
| :--- | :--- | :--- |
| **Windows** | `%LOCALAPPDATA%\DBHarbor\data` | `%ProgramData%\DBHarbor\data` (`C:\ProgramData\DBHarbor\data`) |
| **macOS** | `~/Library/Application Support/DBHarbor/data` | `/Library/Application Support/DBHarbor/data` |
| **Linux** | `$XDG_DATA_HOME/dbharbor` (`~/.local/share/dbharbor`) | `/var/lib/dbharbor` |

### 命令行参数与环境变量 *(Web 服务版)*

配置生效优先级：**`显式命令行参数 > DBHARBOR_* 环境变量 > <DataDir>/config.json > 内置默认值`**。

| 命令行参数 | 环境变量 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `-config` | `DBHARBOR_CONFIG_FILE` | `<DataDir>/config.json` | Web 服务版持久化 JSON 配置文件路径（自动以 `0600` 权限创建） |
| `-host` | `DBHARBOR_HOST` | `0.0.0.0` | TCP 监听地址（也支持 `unix:/path/to/dbharbor.sock` 简写） |
| `-port` | `DBHARBOR_PORT` | `8787` | HTTP TCP 监听端口 |
| `-unix-socket` | `DBHARBOR_UNIX_SOCKET` | *(空)* | 启用 Unix Domain Socket 监听（如 `/run/dbharbor/dbharbor.sock`） |
| `-unix-socket-perm` | `DBHARBOR_UNIX_SOCKET_PERM` | `0660` | Unix 套接字文件的八进制权限掩码 |
| `-unix-socket-group` | `DBHARBOR_UNIX_SOCKET_GROUP` | *(空)* | Unix 套接字所属权限组名称或 GID（如 `www-data`、`nginx`） |
| `-data-dir` | `DBHARBOR_DATA_DIR` | *(系统标准目录)* | 自定义内部数据库、`config.json` 与加密密钥目录 |
| `-admin-user` | `DBHARBOR_ADMIN_USER` | *(空 — 网页向导)* | 可选首次无头初始化管理员用户名（仅在用户库为空时生效） |
| `-admin-pass` | `DBHARBOR_ADMIN_PASS` | *(空 — 网页向导)* | 可选首次无头初始化管理员密码（**至少 8 位**，留空则首次打开网页引导设置） |
| `-idle-ttl` | `DBHARBOR_IDLE_TTL` | `30` | 空闲数据库连接池自动回收超时（分钟） |
| — | `DBHARBOR_MAX_IMPORT_MB` | `256` | 数据文件上传导入最大体积上限（MB，也可在 `config.json` 配置） |
| — | `DBHARBOR_TRUSTED_PROXIES` | *(本机回环)* | 信任 `X-Forwarded-For` / `X-Real-IP` 的反代 IP/CIDR 列表（逗号分隔） |
| — | `DBHARBOR_CORS_ORIGINS` | *(同源)* | 允许跨域访问的前端 Origin 白名单（逗号分隔） |
| — | `DBHARBOR_DUCKDB_PATH` | *(自动探测)* | 服务端受控的 DuckDB CLI 可执行文件路径 (`security.duckdbCliPath`) |
| — | `DBHARBOR_ALLOW_PRIVATE_NET_NON_ADMIN` | `false` | 是否允许非管理员连接内网/本机回环数据库地址 |
| `-init-only` | — | `false` | 仅在 `-data-dir` 初始化密钥、`config.json` 与初始管理员后立即退出 |
| `-recover-admin` | — | `false` | 执行一次性管理员密码 / 2FA 应急重置后立即退出 |
| `-recover-user` | — | *(主管理员)* | 指定 `-recover-admin` 要重置的管理员用户名 |
| `-recover-pass` | — | *(空)* | 新管理员密码（`>= 8` 位）或 `auto`（随机生成 16 位强密码打印至终端） |
| `-reset-2fa` | — | `false` | 在 `-recover-admin` 时同步解绑重置 TOTP 2FA 与备用恢复码 |
| `-open` | — | `false` | 启动后自动打开默认浏览器 |
| `-version` | — | `false` | 输出版本号、提交哈希与平台架构信息 |

### 持久化服务端配置 (`<DataDir>/config.json`) 与管理员应急找回 *(仅 Web 服务版)*

**Web 服务版**首次启动时会自动在数据目录生成权限为 `0600` 的 `<DataDir>/config.json`：

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

当遗忘 Web 服务版管理员密码或丢失 2FA 手机验证器及备用恢复码时，拥有服务器宿主机权限的运维人员可通过以下任一方式安全找回：

1. **编辑 `<DataDir>/config.json` 并重启服务**：
   将 `"recovery"` 改为 `{ "enabled": true, "adminUsername": "admin", "newPassword": "YourNewStrongPassword", "reset2FA": true }`（新密码至少 8 位，或填写 `"newPassword": "auto"` 自动生成 16 位强随机密码输出到服务启动日志），然后重启 DBHarbor。启动时程序会自动将 `dbharbor.db` 快照备份至 `<DataDir>/.backups/`，重置管理员密码/2FA、强制下线全部旧会话、写入审计日志，并**立即自动将 `config.json` 回写归零（`"enabled": false, "newPassword": ""`）**，绝不在磁盘残留明文密码。
2. **一次性命令行应急重置**：
   ```bash
   dbharbor -data-dir /var/lib/dbharbor -recover-admin -recover-pass auto -reset-2fa
   ```

> **Web 服务版本地数据库与内网安全边界**：DBHarbor 内部元数据库（`dbharbor.db`）、`config.json` 与主密钥（`.secret.key`）在全模式下自动隐藏并禁止作为业务数据源连接。在 **Web 服务版**中，非管理员（`Read-Write`）成员浏览或新建服务端 SQLite / DuckDB 本地文件时按账号独立隔离在 `<DataDir>/local_databases/u_<hash>/` 子目录内，通过浏览器 **“打开本地库 (`Open Local DB`)”** 载入的临时单文件库则按用户隔离在 `0700` `<DataDir>/ephemeral_workspaces/` 目录内（强制校验文件头魔数、12 小时闲置自动清理、词法级禁用 SQLite `ATTACH`/文件读写函数、强制服务端受控 `security.duckdbCliPath` 与 DuckDB `-safe` 模式 + `stdin` 传参 + `enable_external_access = false`，并在拨号阶段拦截连接私网/回环地址的 DNS 重绑定与 SSRF 请求）；**原生桌面窗体版**运行于无用户账户体系的纯本地免登录模式，保留完整本机文件访问权限。

---

## 常用快捷键一览

| 快捷键 | 生效范围 | 功能说明 |
| :--- | :--- | :--- |
| `Alt + Shift + N` | 全局工作台 | 打开 **新建数据源连接** 弹窗 |
| `Alt + T` | 全局工作台 | 为当前连接新建 **SQL 终端** 标签页 |
| `Alt + W` | 全局工作台 | 关闭当前激活的工作区标签页 |
| `Alt + 1` … `Alt + 9` | 全局工作台 | 快速切换至第 `1`–`9` 个工作区标签页 |
| `Ctrl / Cmd + B` | 全局工作台 | 展开 / 收起左侧数据源与库表导航栏 |
| `Alt + N` | 数据表格 / 单元格编辑 | 在表格浏览态触发 **行内新增**；在单元格输入框内将当前值设为 `NULL` |
| `Ctrl / Cmd + S` | 数据表格 | 一键提交当前暂存的行内新增与单元格修改 |
| `Ctrl / Cmd + C` / `Ctrl / Cmd + V` | 数据表格 | 复制 / 粘贴单行或多行标准 TSV 制表符文本（无缝兼容 Excel 与记事本） |
| `Ctrl / Cmd + Enter` | SQL 终端 | 执行选中 SQL 语句或当前全部脚本 |

---

## 开机自启动服务安装、在线拉取升级与初始管理员配置

`scripts/` 与构建产物 `dist/` 下内置 **Web 服务版**全平台开机自启动服务管理脚本，支持：
- **GitHub Releases 在线下载与强制校验**：无本地二进制或升级时自动拉取对应架构程序（支持 `--version` 与 `--mirror` / `-Mirror` 加速），执行**强制 `SHA256SUMS.txt` 校验（失败即中止）**与覆盖前 `-version` 冒烟自检。
- **无头初始管理员安全预置**：`--admin-user` / `--admin-pass` 与 `-AdminUser` / `-AdminPass`（密码至少 `8` 位，初始化后立即擦除临时凭据，绝不在进程参数暴露密码）。
- **防锁冲突一键平滑热升级（`upgrade` / `update` 与 `-Action Upgrade` / `-Action Update`）**：自动保留已有服务的启动参数、带引号自定义安装路径与 `DataDir`；Windows 内置优雅停机轮询 + `.old` 原子重命名热替换。

### 1. Linux (`systemd` 系统服务 / 用户服务 & `OpenRC`)

```bash
chmod +x ./dist/install-service.sh

# 方式 A：标准 TCP 模式 + 首次初始化管理员账号密码（密码至少 8 位；默认安装至 /usr/local/bin/dbharbor，或 --install-dir /opt/dbharbor）
sudo ./dist/install-service.sh install \
  --user dbharbor \
  --group dbharbor \
  --data-dir /var/lib/dbharbor \
  --data-dir-mode 0750 \
  --admin-user ops_admin \
  --admin-pass 'StrongPass!2026' \
  --host 127.0.0.1 --port 8787

# 方式 B：Unix Domain Socket 模式 + 授权 Nginx 所在组 (www-data 或 nginx) 访问套接字
sudo ./dist/install-service.sh install \
  --user dbharbor \
  --group dbharbor \
  --socket-group www-data \
  --supplementary-groups www-data \
  --unix-socket /run/dbharbor/dbharbor.sock \
  --unix-socket-perm 0660 \
  --admin-user ops_admin \
  --admin-pass 'StrongPass!2026'

# 方式 C：非 root 普通用户级自启服务 (systemd --user)
./dist/install-service.sh install --user-mode --port 8787

# 一键从 GitHub Releases 在线升级（兼容 update 别名；强制校验 SHA256SUMS.txt 与 -version 自检；国内可加 --mirror https://ghfast.top）
sudo ./dist/install-service.sh upgrade

# 常用运维管理命令
sudo ./dist/install-service.sh status     # 查看状态
sudo ./dist/install-service.sh logs       # 实时日志
sudo ./dist/install-service.sh restart    # 重启服务
sudo ./dist/install-service.sh uninstall  # 卸载服务（保留数据目录）
```

### 2. macOS (`launchd` `LaunchDaemons` & `LaunchAgents`)

```bash
chmod +x ./dist/install-service.sh

# 系统级后台守护服务（支持通过 --user / --group 指定运行角色，--admin-user / --admin-pass 初始化管理员）
sudo ./dist/install-service.sh install \
  --host 127.0.0.1 --port 8787 \
  --user _www --group _www \
  --admin-user ops_admin --admin-pass 'StrongPass!2026'

# 当前登录用户级自启代理 (~/Library/LaunchAgents/io.dbharbor.server.plist)
./dist/install-service.sh install --user-mode --port 8787

# 在线升级（自动识别 plist 中的自定义安装路径）并重启服务
sudo ./dist/install-service.sh upgrade
```

### 3. Windows (开机自启服务计划任务 + `icacls` 数据目录权限加固)

```powershell
# 以管理员运行 PowerShell：安装至指定或默认目录（C:\Program Files\DBHarbor，数据存放于 C:\ProgramData\DBHarbor\data）
# 支持自动从 GitHub 下载并校验 SHA256SUMS.txt、指定服务角色 (-ServiceAccount)、权限组 (-PermissionGroup) 及初始管理员 (-AdminUser / -AdminPass >= 8 位)
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 `
  -Action Install `
  -ServiceAccount "NT AUTHORITY\LocalService" `
  -PermissionGroup "Administrators" `
  -AdminUser "ops_admin" `
  -AdminPass "StrongPass!2026" `
  -HostAddr "127.0.0.1" -Port 8787

# 一键在线升级（兼容 -Action Update；支持 -Version <tag> 与 -Mirror <url>） / 状态查看 / 重启 / 卸载
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 -Action Upgrade
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 -Action Status
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 -Action Restart
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 -Action Uninstall
```

---

## 反向代理配置指南 (Nginx / Caddy / Apache)

完整反代配置文件已随附于仓库 [`deploy/`](./deploy) 目录。

### DBHarbor 反向代理关键配置要点
1. **透传真实客户端 IP 并配置 `trustedProxies`**：透传 `X-Real-IP` / `X-Forwarded-For`（若反代与 DBHarbor 不在同一台机器，请将反代 IP/CIDR 加入 `<DataDir>/config.json` 的 `trustedProxies` 或 `DBHARBOR_TRUSTED_PROXIES`），确保防暴力破解限流、在线会话与安全审计日志准确且防伪造地识别访客真实 IP。
2. **关闭响应缓冲 (`proxy_buffering off;` / `flush_interval -1`)**：保证整库 SQL Dump 与百万行级流式导出边查边传、零内存堆积。
3. **调大请求体限制 (`client_max_body_size 512m;`)**：支持大体积 `.sql` / `.csv` / `.json` 数据文件上传导入（如需超过 256 MB 可同步调大 `config.json` 中的 `maxImportMB`）。

### 方案 A：Nginx 反代 Unix Domain Socket（同机部署首选，零暴露 TCP 端口）

参考 [`deploy/nginx-unix-socket.conf`](./deploy/nginx-unix-socket.conf)：

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
        proxy_send_timeout 600s;
    }
}
```

> **套接字权限提示**：安装服务时传入 `--socket-group www-data`（Debian/Ubuntu）或 `--socket-group nginx`（RHEL/CentOS/Alpine），或将 Nginx 工作用户加入 `dbharbor` 组：`sudo usermod -aG dbharbor www-data && sudo systemctl restart nginx`。

### 方案 B：Nginx 反代本地 TCP (`127.0.0.1:8787`)

将上方 `upstream` 中的 `server unix:/run/dbharbor/dbharbor.sock;` 替换为 `server 127.0.0.1:8787;` 即可（完整模板见 [`deploy/nginx-tcp.conf`](./deploy/nginx-tcp.conf)）。

### 方案 C：Caddy v2（自动签发 HTTPS 证书）

参考 [`deploy/Caddyfile`](./deploy/Caddyfile)：

```caddyfile
db.example.com {
    request_body {
        max_size 512MB
    }
    # Unix Domain Socket 反代（若使用本地 TCP 可改为 127.0.0.1:8787）
    reverse_proxy unix//run/dbharbor/dbharbor.sock {
        flush_interval -1
        header_up X-Real-IP {remote_host}
    }
}
```

### 方案 D：Apache httpd (`mod_proxy_http`)

参考 [`deploy/apache-vhost.conf`](./deploy/apache-vhost.conf)：

```apache
ProxyPreserveHost On
ProxyTimeout 600
RequestHeader set X-Forwarded-Proto "https"
RequestHeader set X-Real-IP "%{REMOTE_ADDR}s"

ProxyPass        / "unix:/run/dbharbor/dbharbor.sock|http://localhost/" nocanon
ProxyPassReverse / "unix:/run/dbharbor/dbharbor.sock|http://localhost/"
```

---

## 开源许可协议 (License)

本项目基于 **[Apache License, Version 2.0](./LICENSE)** (`Apache-2.0`) 开源协议发布。
