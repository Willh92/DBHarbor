# 更新日志 (Changelog)

**语言 / Language**: [English](./CHANGELOG.md) | **简体中文** | [繁體中文](./CHANGELOG.zh-TW.md) | [日本語](./CHANGELOG.ja.md)

DBHarbor 的所有重要版本变更均记录于此文件，并与 GitHub 仓库 [Willh92/DBHarbor](https://github.com/Willh92/DBHarbor.git) 保持同步。

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
