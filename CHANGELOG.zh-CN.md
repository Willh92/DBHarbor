# 更新日志 (Changelog)

**语言 / Language**: [English](./CHANGELOG.md) | **简体中文** | [繁體中文](./CHANGELOG.zh-TW.md) | [日本語](./CHANGELOG.ja.md)

DBHarbor 的所有重要版本变更均记录于此文件，并与 GitHub 仓库 [Willh92/DBHarbor](https://github.com/Willh92/DBHarbor.git) 保持同步。

## [v1.0.1] - 2026-11-07

> **版本形态边界说明**：**原生桌面窗体版 (`dbharbor-desktop-*` / `DBHarbor.app`)** 始终采用**无用户账户体系的纯本地免登录模式**；下方持久化服务端配置（`<DataDir>/config.json`）、首次管理员初始化向导、登录页品牌定制、管理员应急找回模式（`recovery` / `-recover-admin`）以及本地文件与内网沙箱隔离**仅适用于 Web 服务版 (`dbharbor-*`)**。

### 持久化配置文件、管理员应急找回、安全与并发加固、手机触控交互升级与在线平滑更新
- **持久化服务端配置 (`<DataDir>/config.json`) 与管理员密码/2FA 应急找回模式 *(仅 Web 服务版)***：
  - Web 服务版启动时自动在 `<DataDir>/config.json` 生成持久化配置文件（`0600` 权限，支持 `-config <路径>` 或 `DBHARBOR_CONFIG_FILE` 自定义路径），采用 `命令行参数 > DBHARBOR_* 环境变量 > config.json > 默认值` 四级优先级，统一管理 `host`、`port`、`unixSocket`、`unixSocketPerm`、`unixSocketGroup`、`idleConnTTLMinutes`、`maxImportMB`、`trustedProxies`、`corsAllowedOrigins`、`security`（`allowPrivateNetworkForNonAdmin`、`duckdbCliPath`）与 `recovery`（`enabled`、`adminUsername`、`newPassword`、`reset2FA`）。
  - 提供双通道管理员应急恢复机制（遗忘管理员密码或丢失 2FA 验证器/备用恢复码时使用）：
    1. **配置文件找回**：编辑 `<DataDir>/config.json` 将 `"recovery"` 设为 `{ "enabled": true, "adminUsername": "admin", "newPassword": "YourNewStrongPassword", "reset2FA": true }`（密码至少 8 位，或设为 `"auto"` 自动生成 16 位强随机密码输出至启动日志）并重启 DBHarbor 服务——程序启动时自动备份 `dbharbor.db` 至 `<DataDir>/.backups/`，重置密码/2FA、吊销该账号全部历史会话，并**立即自动回写清空 `config.json`（`enabled: false`、`newPassword: ""`）**，杜绝明文密码残留磁盘。
    2. **一次性命令行找回**：执行 `dbharbor -data-dir <目录> -recover-admin [-recover-user <用户名>] -recover-pass <新密码|auto> [-reset-2fa]` 即可一键完成备份、重置密码/2FA 并退出。
- **取消默认弱口令、首次启动管理员初始化向导、登录页品牌定制与三重防搜索引擎收录 *(仅 Web 服务版)***：
  - 彻底移除内置默认 `admin` / `admin123` 兜底账号，统一强制初始管理员密码至少 **8 位**（涵盖网页向导、`-admin-pass` 与服务安装脚本）；未传入初始凭据且用户库为空时，首次打开网页自动进入一次性**管理员初始化向导**（`POST /api/auth/setup`），创建完成后接口永久锁定。
  - 全新零指纹登录页：不预填 `admin`、无默认密码提示、未登录态隐藏版本号/14 引擎列表/GitHub 链接，`GET /api/health` 精简为 `{"status":"ok"}`，`GET /api/version` 收敛至登录鉴权后访问；支持在 **安全 / 2FA -> 登录页定制** 中修改浏览器 `<title>`、顶部图标（盾牌/数据库/应用图标/上传 `<= 128 KB` 自定义图标并同步 Favicon/隐藏）、主副标题，并内置 `/robots.txt`、`X-Robots-Tag` 与 `<meta name="robots">` 三重防爬虫收录保护。
- **安全边界、驱动稳定性与高并发性能加固**：
  - **Web 服务版本地数据库与内网 SSRF 沙箱**：非管理员（`Read-Write`）在 Web 服务版中操作本地 SQLite / DuckDB 文件严格锁定在 `<DataDir>/local_databases/` 目录内（拦截符号链接越界），Web 模式禁用 SQLite `ATTACH DATABASE`，强制使用服务端受控 `security.duckdbCliPath` 并开启 DuckDB `enable_external_access = false`；同时新增非管理员连接私网/回环/链路本地地址的 SSRF 防护拦截（可通过 `security.allowPrivateNetworkForNonAdmin` 配置）。
  - **单次防重放验证码与可信反代限流**：图形验证码升级为基于主密钥派生的单次消费防重放令牌，限流器仅信任 `trustedProxies` 白名单反代透传的 `X-Forwarded-For` / `X-Real-IP`，并新增 2FA 验证挑战失败次数上限保护。
  - **TLS 证书严格校验、连接池并发安全与内存优化**：补齐各数据库驱动标准 TLS CA 校验选项，修复 SQL Server 连接池下 `USE [db]` 跨连接串库问题，`pool.Manager` 引入引用计数并发租约机制防止空闲回收误关执行中查询，限制 DuckDB / MongoDB 大结果集内存峰值，内置启动期 SQLite 元数据库事务自动迁移（`<DataDir>/.backups/` 快照备份），并将 SQLite 库对象加载提速为单次目录查询秒开。
- **手机端触控工作台与表格交互体验升级**：
  - 手机与触屏设备表格采用单滚动容器原生 GPU CSS `position: sticky` 固定表头（`.dbharbor-touch-sticky-grid`）彻底消除拖动闪烁，并新增**手指拖拽调整列宽**与**双击列分隔条自动撑开列宽**。
  - 左侧库表树与数据表格全面补齐触屏可见 `[⋮]` 更多操作按钮与长按上下文菜单，新增手机 SQL 终端快捷符号栏、长文本单元格点击预览展开、`34px–40px` 大触控按钮与高危删除/清空防误触隔离、左侧菜单 `keep-alive` 常驻内存缓存以及全局浮层视口防溢出夹紧。
- **跨平台服务安装器与一键在线二进制升级 (`install-service.sh` / `install-service.ps1`)**：
  - 支持 `upgrade`（兼容 `update` 别名）/ `-Action Upgrade`（兼容 `-Action Update`）从 GitHub Releases 在线下载更新、**强制 `SHA256SUMS.txt` 校验（失败即中止）**、替换前 `-version` 冒烟自检、`--mirror` / `-Mirror` 镜像加速及无头 `--admin-user` / `--admin-pass`（`>= 8` 位）初始化。
  - 修复带空格/引号的 `systemd` `ExecStart=` 与 macOS `launchd` `ProgramArguments` 自定义安装路径升级识别，完善 Windows `irm | iex` 路径兜底与 `%TEMP%` 自动清理，并通过优雅停机轮询（`Stop-DBHarborServiceAndWait`）+ `.old` 原子重命名彻底解决 Windows `.exe` 文件占用锁冲突。

## [v1.0.0] - 2026-11-07

### 首发版本特性
- **双形态分发 — 原生桌面窗体版与 Web 服务版 (`CGO_ENABLED=0`)**：
  - **桌面窗体版 (`dbharbor-desktop-*` 与 macOS `DBHarbor.app`)**：双击即开原生系统窗体（Windows 采用 `Win32 + Edge WebView2` 并内置 `-H=windowsgui` 无黑框启动、macOS 采用原生 `Cocoa NSWindow + WKWebView`、Linux 采用 `GTK3 + WebKit2GTK`），采用**无账户体系的纯本地模式**（免登录即开即用、原生深色窗口标题栏同步、原生系统本地数据库文件选择与另存为流式导出对话框、多窗口本地会话兼容、本地 AES-256-GCM 加密连接库）。
  - **Web 服务版 (`dbharbor-*`)**：零外部依赖单文件 Web 工作台，同时支持 TCP 与原生 `AF_UNIX` Unix Domain Socket 监听（`-unix-socket`、`-unix-socket-perm`、`-unix-socket-group`）、陈旧 Socket 自动清理、反代真实客户端 IP 识别、服务端本地数据库目录浏览与 `.db/.sqlite/.duckdb` 上传，并附带 Nginx / Caddy / Apache 生产级反向代理模板（`deploy/`）。
- **内置 14 种数据库纯 Go 驱动**：原生支持 ClickHouse（Native TCP / HTTP 双协议与分区治理）、DuckDB、MySQL、MariaDB、TiDB、OceanBase、PostgreSQL、人大金仓 KingbaseES、SQLite 3、SQL Server、Oracle（免 Instant Client）、达梦 DM8、Redis（DB0–15 键值浏览、6 种结构编辑、JSON 备份导入导出与 CLI）以及 MongoDB（JSON/表格双视图、BSON 增删改查、JSON/JSONL 文档导入导出、索引与 `RunCommand`）。
- **可视化表结构设计、Navicat 级铺满数据网格、函数/触发器创建与流式导入导出**：
  - 支持列新增/修改/重命名/删除、索引新建/修改/删除与外键维护、SQL 模板一键生成、表重命名/克隆、`CREATE VIEW`、可视化新建与编辑函数/存储过程/触发器（`PROCEDURE` / `FUNCTION` / `TRIGGER`）、数据库级用户与权限分配治理（`GRANT` / `REVOKE` / Redis ACL / MongoDB 角色）、SQLite 事务级无损重建表与实时 DDL 预览。
  - 全高铺满电子表格级数据浏览网格（水平滚动条贴底、点击下方空白行直接新增记录、多行批量改值、单行/多行整行 TSV 复制粘贴兼容记事本与 Excel）与按需裁剪的离线 Monaco 多标签 SQL 终端（`Ctrl/Cmd + Enter`、`EXPLAIN` 执行计划、SQL 格式化、`AbortController` 查询中断、结果一键另存文件、历史与代码片段）。
  - 专为手机小屏（`<= 640px`）重设计的响应式工作台排版：紧凑顶栏、单胶囊标签切换器、折叠式筛选面板、底部常驻批量提交栏、`85dvh` 底部抽屉式行编辑器及 Redis `键列表 | 键值详情 | CLI` 切换视图。
  - 支持整库 SQL Dump 备份、多表勾选/筛选条件/自定义查询/MongoDB 集合流式导出、文件分批事务导入与粘贴 SQL 脚本直接执行。
- **Web 多成员 RBAC 权限、安全加固与系统级服务治理**：
  - Web 服务版内置三级多成员 RBAC 权限体系（`Admin` / `Read-Write` / `Read-Only`）与共享/私有数据源授权隔离、IP/账号双维度防爆破限流（`HTTP 429`）、无状态 HMAC 矢量验证码、离线二维码 RFC 6238 TOTP 双因素认证（2FA）及 8 组一次性恢复码、在线会话吊销与安全审计日志。
  - 运行期数据目录默认遵循各操作系统标准应用数据规范（Windows 为 `%LOCALAPPDATA%\DBHarbor\data` 或服务态 `%ProgramData%\DBHarbor\data`，macOS 为 `~/Library/Application Support/DBHarbor/data`，Linux 为 `$XDG_DATA_HOME/dbharbor` 或 `/var/lib/dbharbor`），绝不污染启动目录；跨平台服务安装脚本（`install-service.sh`、`install-service.ps1`）支持启动角色与权限组配置。
- **全英文代码基线、多语言界面与 GitHub 在线更新提醒**：
  - 内置 **English (`en-US`)**、**简体中文 (`zh-CN`)**、**繁體中文 (`zh-TW`)**、**日本語 (`ja-JP`)** 实时切换与多语种文档。
  - 对接 GitHub `Willh92/DBHarbor` 在线检测更新（支持新版本主动通知提醒、自动提醒开关、忽略指定版本及自动匹配桌面版/服务版安装包）并同步更新日志，采用 **Apache License 2.0 (`Apache-2.0`)** 开源许可。
