# DBHarbor — 跨平台多数据库 Web 管理工作台

**语言切换**: [English](./README.md) | **简体中文** | [繁體中文](./README.zh-TW.md) | [日本語](./README.ja.md)

[![License: Apache-2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](./LICENSE)
[![Version](https://img.shields.io/badge/Version-v1.0.0-1677ff.svg)](./CHANGELOG.zh-CN.md)
[![CGO_ENABLED=0](https://img.shields.io/badge/Go-CGO__ENABLED%3D0-00ADD8.svg)](https://github.com/Willh92/DBHarbor.git)

一款融合 **phpMyAdmin 经典高效可视化操作体验** 与 **现代多引擎 IDE 能力** 的零外部依赖单文件跨平台 Web 数据库管理平台。

- **官方代码仓库**：[https://github.com/Willh92/DBHarbor.git](https://github.com/Willh92/DBHarbor.git)
- **版本更新日志**：[CHANGELOG.zh-CN.md](./CHANGELOG.zh-CN.md)
- **开源许可协议**：[Apache License 2.0](./LICENSE)

---

## 核心特性

- **14 种数据库驱动开箱即用 (`CGO_ENABLED=0` 纯 Go)**：
  - **OLAP 分析型**：ClickHouse（Native TCP / HTTP 双协议、`MergeTree` 可视化建表、分区治理）、DuckDB
  - **关系型 RDBMS**：MySQL、MariaDB、TiDB、OceanBase、PostgreSQL、人大金仓 (KingbaseES)、SQLite 3、SQL Server、Oracle（纯 Go 免 Instant Client）、达梦 DM8
  - **键值与文档 NoSQL**：Redis（DB0–15 浏览器、6 种数据结构编辑器、内嵌 CLI）、MongoDB（JSON / 二维表格双视图、BSON CRUD、索引与 `RunCommand` 终端）
- **完备的库表结构设计、数据库权限分配与 SQL 运维闭环**：
  - **可视化表结构、函数/触发器创建与数据库权限治理**：支持新增/修改/重命名/删除字段、调整数据类型/默认值/注释/`NULL` 约束、索引与外键管理、可视化新建与修改函数/存储过程/触发器（内置多方言智能模板）、数据库级账号与权限分配（`GRANT` / `REVOKE`、Redis ACL、MongoDB 角色）、重命名/克隆表、创建视图、SQLite 事务级无损重建表及实时 DDL 预览。
  - **Navicat 级铺满网格、空白行直插与多标签 SQL 智能终端**：数据浏览网格自动铺满工作区高度并支持点击底部空白行直接行内新增、多行批量改列、单行/多行整行 TSV 复制粘贴（`Ctrl+C` / `Ctrl+V` 兼容记事本与 Excel）、本地数据库文件浏览与上传（`SQLite 3` / `DuckDB`），内嵌轻量化 Monaco Editor 支持自动补全、`Ctrl/Cmd + Enter` 执行、`EXPLAIN` 执行计划、SQL 格式化、查询中止 (`AbortController`)、历史记录与脚本另存为。
  - **流式导入导出与批量表运维**：支持整库与多表勾选 SQL Dump 备份、单表/筛选结果/自定义查询/MongoDB 集合/Redis 键值流式导入导出（`SQL` / `CSV-BOM` / `CSV` / `JSON` / `JSONL` / `Markdown`）、大文件分批事务导入，以及直接粘贴执行多条 SQL 脚本。
- **Web 多成员三级 RBAC 权限、登录安全加固与双因素认证 (2FA)**：
  - Web 服务版支持三级多成员 RBAC（`Admin` / `Read-Write` / `Read-Only`）与共享/私有数据源授权隔离、滑动窗口 IP/账号防暴力破解限流 (`HTTP 429`)、无状态 HMAC SVG 图形验证码、标准 **RFC 6238 TOTP 双因素认证**（纯本地离线生成二维码 + 8 个一次性备用恢复码）、在线设备会话强制下线、安全审计日志与 AES-256-GCM 连接凭据加密。
- **原生深色标题栏同步、手机端专属自适应排版与多语言国际化 (i18n)**：
  - 桌面端支持系统级原生深色标题栏同步（`Win32 DWM`、`macOS DarkAqua`、`Linux GTK3`），同时针对手机端 (`<= 640px`) 专属重构紧凑顶栏、单胶囊标签切换器与底部抽屉行编辑器（零横向溢出）。
  - 同时支持 **TCP 端口监听** 与 **Unix Domain Socket (`AF_UNIX`) 监听**（`-unix-socket`、`-unix-socket-perm`、`-unix-socket-group`）。
  - 原生支持 **English (`en-US`)、简体中文 (`zh-CN`)、繁體中文 (`zh-TW`)、日本語 (`ja-JP`)** 实时切换，并支持连接 `Willh92/DBHarbor` 在线检测新版本、新版本主动提醒与提醒开关控制。

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

1. **原生桌面窗体版 (`dbharbor-desktop-<os>-<arch>` 与 `DBHarbor-darwin-<arch>.app`) — 纯本地客户端**：
   - 双击即可直接打开原生系统界面窗口（Windows 下如 `dbharbor-desktop-windows-amd64.exe` 采用 `Win32 + Edge WebView2` 无黑框直启，macOS 采用 `Cocoa NSWindow + WKWebView` 并提供 `.app` 包，Linux 采用 `GTK3 + WebKit2GTK`）。
   - **无账户体系**：作为纯本地桌面软件运行，免登录直达工作台，内置原生系统另存为文件对话框与本地 AES-256-GCM 加密连接库。
2. **Web 服务版 (`dbharbor-<os>-<arch>`) — 服务端部署 / 反向代理**：
   - 运行 `./dist/dbharbor-<os>-<arch>`（可通过 `-version` 查看版本信息），浏览器访问 `http://localhost:8787`（或通过 `-unix-socket` 监听 Unix Domain Socket）。
   - **默认初始管理员账号**：`admin` / `admin123`（首次登录后请立即在顶部栏 **Security / 2FA** 中修改密码并开启双因素认证）。

---

## 系统标准数据目录与启动参数

DBHarbor **绝不会把运行时数据散落在程序启动目录**（除非显式指定 `-data-dir`）。默认情况下，内部元数据库 (`dbharbor.db`) 与主密钥 (`.secret.key`) 自动存放在各操作系统的标准应用数据目录：

| 操作系统 | 普通用户 / 桌面启动默认目录 | 系统后台服务 (Daemon / Service) 默认目录 |
| :--- | :--- | :--- |
| **Windows** | `%LOCALAPPDATA%\DBHarbor\data` | `%ProgramData%\DBHarbor\data` (`C:\ProgramData\DBHarbor\data`) |
| **macOS** | `~/Library/Application Support/DBHarbor/data` | `/Library/Application Support/DBHarbor/data` |
| **Linux** | `$XDG_DATA_HOME/dbharbor` (`~/.local/share/dbharbor`) | `/var/lib/dbharbor` |

### 命令行参数与环境变量

| 命令行参数 | 环境变量 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `-host` | `DBHARBOR_HOST` | `0.0.0.0` | TCP 监听地址（也支持 `unix:/path/to/dbharbor.sock` 简写） |
| `-port` | `DBHARBOR_PORT` | `8787` | HTTP TCP 监听端口 |
| `-unix-socket` | `DBHARBOR_UNIX_SOCKET` | *(空)* | 启用 Unix Domain Socket 监听（如 `/run/dbharbor/dbharbor.sock`） |
| `-unix-socket-perm` | `DBHARBOR_UNIX_SOCKET_PERM` | `0660` | Unix 套接字文件的八进制权限掩码 |
| `-unix-socket-group` | `DBHARBOR_UNIX_SOCKET_GROUP` | *(空)* | Unix 套接字所属权限组名称或 GID（如 `www-data`、`nginx`） |
| `-data-dir` | `DBHARBOR_DATA_DIR` | *(系统标准目录)* | 自定义内部数据库与加密密钥目录 |
| `-admin-user` | `DBHARBOR_ADMIN_USER` | `admin` | 首次初始化管理员用户名 |
| `-admin-pass` | `DBHARBOR_ADMIN_PASS` | `admin123` | 首次初始化管理员密码 |
| `-idle-ttl` | — | `30` | 空闲数据库连接池自动回收超时（分钟） |
| `-open` | — | `false` | 启动后自动打开默认浏览器 |
| `-version` | — | `false` | 输出版本号、提交哈希与平台架构信息 |

> **本地数据库文件安全边界**：DBHarbor 内部元数据库（`dbharbor.db`）与主密钥（`.secret.key`）在全模式下自动隐藏并禁止作为业务数据源连接。在 Web 服务版中，非管理员（`Read-Write`）成员浏览、上传或新建 SQLite / DuckDB 本地文件时严格隔离在 `<DataDir>` 沙箱目录内（默认 `<DataDir>/local_databases`）；`Admin` 管理员与原生桌面窗体版则保留完整本机目录访问权限。

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

## 开机自启动服务安装及角色 / 权限组配置

`scripts/` 与构建产物 `dist/` 下内置全平台开机自启动及角色权限配置脚本：

### 1. Linux (`systemd` 系统服务 / 用户服务 & `OpenRC`)

```bash
chmod +x ./dist/install-service.sh

# 方式 A：标准 TCP 模式 + 自定义服务运行账号与主权限组（默认自动创建系统用户 dbharbor:dbharbor）
sudo ./dist/install-service.sh install \
  --user dbharbor \
  --group dbharbor \
  --data-dir /var/lib/dbharbor \
  --data-dir-mode 0750 \
  --host 127.0.0.1 --port 8787

# 方式 B：Unix Domain Socket 模式 + 授权 Nginx 所在组 (www-data 或 nginx) 访问套接字
sudo ./dist/install-service.sh install \
  --user dbharbor \
  --group dbharbor \
  --socket-group www-data \
  --supplementary-groups www-data \
  --unix-socket /run/dbharbor/dbharbor.sock \
  --unix-socket-perm 0660

# 方式 C：非 root 普通用户级自启服务 (systemd --user)
./dist/install-service.sh install --user-mode --port 8787

# 常用运维管理命令
sudo ./dist/install-service.sh status     # 查看状态
sudo ./dist/install-service.sh logs       # 实时日志
sudo ./dist/install-service.sh restart    # 重启服务
sudo ./dist/install-service.sh uninstall  # 卸载服务（保留数据目录）
```

### 2. macOS (`launchd` `LaunchDaemons` & `LaunchAgents`)

```bash
chmod +x ./dist/install-service.sh

# 系统级后台守护服务（支持通过 --user / --group 指定运行角色与权限组）
sudo ./dist/install-service.sh install --host 127.0.0.1 --port 8787 --user _www --group _www

# 当前登录用户级自启代理 (~/Library/LaunchAgents/io.dbharbor.server.plist)
./dist/install-service.sh install --user-mode --port 8787
```

### 3. Windows (开机自启服务计划任务 + `icacls` 数据目录权限加固)

```powershell
# 以管理员运行 PowerShell：安装至 C:\Program Files\DBHarbor，数据存放于 C:\ProgramData\DBHarbor\data
# 支持指定服务启动角色 (-ServiceAccount) 与目录访问权限组 (-PermissionGroup)
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 `
  -Action Install `
  -ServiceAccount "NT AUTHORITY\LocalService" `
  -PermissionGroup "Administrators" `
  -HostAddr "127.0.0.1" -Port 8787

# 状态查看 / 重启 / 卸载
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 -Action Status
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 -Action Restart
powershell -ExecutionPolicy Bypass -File .\dist\install-service.ps1 -Action Uninstall
```

---

## 反向代理配置指南 (Nginx / Caddy / Apache)

完整反代配置文件已随附于仓库 [`deploy/`](./deploy) 目录。

### DBHarbor 反向代理关键配置要点
1. **透传真实客户端 IP (`X-Real-IP` / `X-Forwarded-For`)**：确保登录防暴力破解限流、在线设备会话与安全审计日志准确记录访客真实 IP，而非反代本机地址。
2. **关闭响应缓冲 (`proxy_buffering off;` / `flush_interval -1`)**：保证整库 SQL Dump 与百万行级流式导出边查边传、零内存堆积。
3. **调大请求体限制 (`client_max_body_size 512m;`)**：支持大体积 `.sql` / `.csv` / `.json` 数据文件上传导入。

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
