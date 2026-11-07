import React, { Suspense, lazy, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Alert,
  Badge,
  Button,
  Card,
  Col,
  Dropdown,
  Input,
  Modal,
  Row,
  Space,
  Spin,
  Tabs,
  Tag,
  Tooltip,
  Typography,
  message,
  notification,
} from 'antd';
import {
  ArrowLeftOutlined,
  ArrowRightOutlined,
  CloseCircleOutlined,
  CloseOutlined,
  CodeOutlined,
  ColumnWidthOutlined,
  DashboardOutlined,
  DisconnectOutlined,
  DownOutlined,
  EditOutlined,
  ExclamationCircleOutlined,
  GithubOutlined,
  GlobalOutlined,
  KeyOutlined,
  LogoutOutlined,
  MenuFoldOutlined,
  MenuUnfoldOutlined,
  MoonOutlined,
  MoreOutlined,
  PlusOutlined,
  RocketOutlined,
  SafetyCertificateOutlined,
  SunOutlined,
  TeamOutlined,
  UserOutlined,
} from '@ant-design/icons';
import {
  api,
  getAutoUpdateRemindEnabled,
  getSkippedUpdateVersion,
  setAutoUpdateRemindEnabled,
  setSkippedUpdateVersion,
} from '../api/client';
import { BrandLogo } from '../components/common/BrandLogo';
import { LanguageSwitcher } from '../components/common/LanguageSwitcher';
import { ConnectionSidebar } from '../components/connection/ConnectionSidebar';
import type { TransferModalMode, TransferOpenOptions } from '../components/workspace/ContextNavBar';
import { TableDataTab } from '../components/workspace/TableDataTab';
import { useResponsive } from '../hooks/useResponsive';
import { LOCALE_OPTIONS } from '../i18n';
import { useI18n } from '../i18n/useI18n';
import { useWorkbenchStore } from '../store/useWorkbenchStore';
import type { BuildVersionInfo, SavedConnection, SystemStats, UpdateCheckResult, WorkspaceTab } from '../types';
import { DB_SHORT_LABELS } from '../utils/format';

// Lazy-load Monaco-heavy tabs, OverviewTab & on-demand modals to keep initial workbench JS & React fiber footprint minimal
const OverviewTab = lazy(() => import('../components/workspace/OverviewTab'));
const TableStructureTab = lazy(() =>
  import('../components/workspace/TableStructureTab').then((m) => ({ default: m.TableStructureTab }))
);
const SqlConsoleTab = lazy(() =>
  import('../components/workspace/SqlConsoleTab').then((m) => ({ default: m.SqlConsoleTab }))
);
const RedisBrowserTab = lazy(() =>
  import('../components/workspace/RedisBrowserTab').then((m) => ({ default: m.RedisBrowserTab }))
);
const ConnectionModal = lazy(() =>
  import('../components/connection/ConnectionModal').then((m) => ({ default: m.ConnectionModal }))
);
const CreateTableModal = lazy(() =>
  import('../components/workspace/CreateTableModal').then((m) => ({ default: m.CreateTableModal }))
);
const ImportExportModal = lazy(() =>
  import('../components/workspace/ImportExportModal').then((m) => ({ default: m.ImportExportModal }))
);
const SecurityModal = lazy(() =>
  import('../components/security/SecurityModal').then((m) => ({ default: m.SecurityModal }))
);
const VersionUpdateModal = lazy(() =>
  import('../components/common/VersionUpdateModal').then((m) => ({ default: m.VersionUpdateModal }))
);
const MongoCollectionTab = lazy(() =>
  import('../components/workspace/MongoCollectionTab').then((m) => ({ default: m.MongoCollectionTab }))
);
const ServerMonitorTab = lazy(() =>
  import('../components/workspace/ServerMonitorTab').then((m) => ({ default: m.ServerMonitorTab }))
);
const DbUsersTab = lazy(() =>
  import('../components/workspace/DbUsersTab').then((m) => ({ default: m.DbUsersTab }))
);
const UserManagementModal = lazy(() =>
  import('../components/security/UserManagementModal').then((m) => ({ default: m.UserManagementModal }))
);

const MAX_MOUNTED_TABS_DESKTOP = 5;
const MAX_MOUNTED_TABS_MOBILE = 2;

export const WorkbenchLayout: React.FC = () => {
  const {
    isDesktop,
    username,
    displayName,
    role,
    canCreateConn,
    totpEnabled,
    passwordChanged,
    darkMode,
    toggleDarkMode,
    logout,
    loadBootstrap,
    reloadConnections,
    bumpTreeRefresh,
    drivers,
    connections,
    tabs,
    activeTabKey,
    setActiveTab,
    closeTab,
    closeOtherTabs,
    closeLeftTabs,
    closeRightTabs,
    closeAllTabs,
    closeConnectionTabs,
    reorderTabs,
    openTab,
    updateTab,
  } = useWorkbenchStore();
  const { locale, setLocale, t } = useI18n();
  const { isMobile, isTablet } = useResponsive();
  const maxMountedTabs = isMobile ? MAX_MOUNTED_TABS_MOBILE : MAX_MOUNTED_TABS_DESKTOP;

  const effectiveCanCreateConn = isDesktop || canCreateConn || role === 'admin';
  const isAdmin = isDesktop || role === 'admin';

  const [isNarrowViewport, setIsNarrowViewport] = useState<boolean>(
    () => typeof window !== 'undefined' && window.innerWidth < 760
  );
  const [sidebarCollapsed, setSidebarCollapsed] = useState<boolean>(
    () => typeof window !== 'undefined' && window.innerWidth < 760
  );
  const [sidebarWidth, setSidebarWidth] = useState<number>(() => {
    const saved = Number(localStorage.getItem('dbharbor_sidebar_width'));
    return saved >= 210 && saved <= 480 ? saved : 272;
  });
  const asideRef = useRef<HTMLElement | null>(null);
  const sidebarSplitterRef = useRef<HTMLDivElement | null>(null);
  const isNarrowRef = useRef(isNarrowViewport);
  isNarrowRef.current = isNarrowViewport;

  const [draggingTabKey, setDraggingTabKey] = useState<string>('');
  const [dragOverTabKey, setDragOverTabKey] = useState<string>('');
  const [connModalOpen, setConnModalOpen] = useState(false);
  const [editingConn, setEditingConn] = useState<SavedConnection | null>(null);

  // LRU Keep-Alive mounted tab cap to bound inactive Monaco & Table DOM memory
  const [lruTabKeys, setLruTabKeys] = useState<string[]>(() => (activeTabKey ? [activeTabKey] : []));

  // Tab rename modal
  const [renamingTab, setRenamingTab] = useState<WorkspaceTab | null>(null);
  const [renameTitle, setRenameTitle] = useState('');

  // Security & 2FA Center Modal + Admin User Management Modal
  const [securityModalOpen, setSecurityModalOpen] = useState(false);
  const [securityInitialTab, setSecurityInitialTab] = useState<string>('2fa');
  const [securityHintDismissed, setSecurityHintDismissed] = useState(false);
  const [userMgmtOpen, setUserMgmtOpen] = useState(false);
  const [mobileMoreOpen, setMobileMoreOpen] = useState(false);
  const [mobileMoreView, setMobileMoreView] = useState<'root' | 'lang'>('root');
  const keepMobileMoreOpenRef = useRef(false);

  // Version & GitHub Update Check state
  const [versionModalOpen, setVersionModalOpen] = useState(false);
  const [buildInfo, setBuildInfo] = useState<BuildVersionInfo | null>(null);
  const [updateCheck, setUpdateCheck] = useState<UpdateCheckResult | null>(null);
  const [sysStats, setSysStats] = useState<SystemStats | null>(null);

  // Create table modal state
  const [createTblCtx, setCreateTblCtx] = useState<{
    open: boolean;
    connectionId: string;
    dbType: string;
    database: string;
    schema?: string;
  }>({ open: false, connectionId: '', dbType: 'mysql', database: '' });

  // Import/Export/SQL-Exec modal state
  const [transferCtx, setTransferCtx] = useState<{
    open: boolean;
    mode: TransferModalMode;
    connectionId: string;
    database: string;
    schema?: string;
    table?: string;
    tables?: string[];
    customSQL?: string;
  }>({ open: false, mode: 'export', connectionId: '', database: '' });

  useEffect(() => {
    loadBootstrap().catch((err) => message.error(err.message));

    // Fetch runtime version, lightweight memory stats, and non-blocking GitHub update check
    api.getVersion().then(setBuildInfo).catch(() => {});
    api.getSystemStats().then(setSysStats).catch(() => {});
    api
      .checkUpdate(false)
      .then((upd) => {
        setUpdateCheck(upd);
        if (
          upd.hasUpdate &&
          upd.latestVersion &&
          getAutoUpdateRemindEnabled() &&
          getSkippedUpdateVersion() !== upd.latestVersion
        ) {
          const notifKey = `dbharbor-upd-${upd.latestVersion}`;
          notification.info({
            key: notifKey,
            placement: 'bottomRight',
            duration: 10,
            message: t('New version {ver} is available for upgrade!', { ver: upd.latestVersion }),
            description: (
              <Space size={6} wrap style={{ marginTop: 6 }}>
                <Button
                  size="small"
                  type="primary"
                  onClick={() => {
                    notification.destroy(notifKey);
                    setVersionModalOpen(true);
                  }}
                >
                  {t('View Changelog & Download')}
                </Button>
                <Button
                  size="small"
                  onClick={() => {
                    setSkippedUpdateVersion(upd.latestVersion);
                    notification.destroy(notifKey);
                    message.info(t('Skipped reminder for {ver}', { ver: upd.latestVersion }));
                  }}
                >
                  {t('Skip This Version')}
                </Button>
                <Button
                  size="small"
                  type="text"
                  onClick={() => {
                    setAutoUpdateRemindEnabled(false);
                    notification.destroy(notifKey);
                    message.info(t('Automatic update reminder disabled'));
                  }}
                >
                  {t('Turn Off Reminders')}
                </Button>
              </Space>
            ),
          });
        }
      })
      .catch(() => {});

    const statsTimer = setInterval(() => {
      if (!document.hidden) {
        api.getSystemStats().then(setSysStats).catch(() => {});
      }
    }, 30000);
    return () => clearInterval(statsTimer);
  }, []);

  // Responsive viewport watcher: auto-collapse sidebar when window shrinks below 760px
  useEffect(() => {
    let wasNarrow = window.innerWidth < 760;
    const onResize = () => {
      const narrow = window.innerWidth < 760;
      setIsNarrowViewport(narrow);
      if (narrow && !wasNarrow) {
        setSidebarCollapsed(true);
      } else if (!narrow && wasNarrow) {
        setSidebarCollapsed(false);
      }
      wasNarrow = narrow;
    };
    window.addEventListener('resize', onResize, { passive: true });
    return () => window.removeEventListener('resize', onResize);
  }, []);

  // Track most-recently-visited tabs (capped at maxMountedTabs) so older background tabs unmount heavy DOM
  useEffect(() => {
    setLruTabKeys((prev) => {
      const validSet = new Set(tabs.map((t) => t.key));
      const next = activeTabKey && validSet.has(activeTabKey)
        ? [activeTabKey, ...prev.filter((k) => k !== activeTabKey && validSet.has(k))]
        : prev.filter((k) => validSet.has(k));
      return next.slice(0, maxMountedTabs);
    });
  }, [activeTabKey, tabs, maxMountedTabs]);

  const openNewSqlConsole = (refTab?: WorkspaceTab) => {
    const activeObj = refTab || tabs.find((x) => x.key === activeTabKey) || tabs[0];
    const conn =
      (activeObj ? connections.find((c) => c.id === activeObj.connectionId) : undefined) ||
      connections.find((c) => {
        const d = drivers.find((dr) => dr.type === c.type);
        return d ? d.category === 'relational' || d.category === 'olap' : c.type !== 'redis' && c.type !== 'mongodb';
      });
    if (!conn) {
      if (effectiveCanCreateConn) {
        setEditingConn(null);
        setConnModalOpen(true);
      }
      return;
    }
    const drv = drivers.find((d) => d.type === conn.type);
    if (drv && drv.category !== 'relational' && drv.category !== 'olap') {
      message.info(t('SQL Console is available for relational and OLAP engines'));
      return;
    }
    const dbName =
      (activeObj?.connectionId === conn.id ? activeObj.database : '') ||
      conn.database ||
      (conn.type === 'sqlite' || conn.type === 'duckdb' ? 'main' : '');
    const count = tabs.filter((x) => x.kind === 'sql_console' && x.connectionId === conn.id).length + 1;
    openTab({
      key: `sql:${conn.id}:${Date.now()}`,
      title: `SQL #${count} - ${dbName || conn.name}`,
      kind: 'sql_console',
      connectionId: conn.id,
      connName: conn.name,
      dbType: conn.type,
      database: dbName,
    });
  };

  // Global workbench keyboard shortcuts: Ctrl/Cmd+B toggles sidebar, Alt+W closes active tab, Alt+Shift+N opens New Connection, Alt+T opens New SQL Console, Alt+1..9 switches tabs
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.defaultPrevented) return;
      const active = document.activeElement as HTMLElement | null;
      const inEditorOrInput = Boolean(
        active &&
          (active.tagName === 'INPUT' ||
            active.tagName === 'TEXTAREA' ||
            active.isContentEditable ||
            active.closest?.('.monaco-editor'))
      );

      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'b') {
        e.preventDefault();
        setSidebarCollapsed((prev) => !prev);
      } else if (e.altKey && !e.shiftKey && e.key.toLowerCase() === 'w') {
        if (activeTabKey) {
          e.preventDefault();
          closeTab(activeTabKey);
        }
      } else if (e.altKey && e.shiftKey && e.key.toLowerCase() === 'n' && !inEditorOrInput) {
        if (effectiveCanCreateConn) {
          e.preventDefault();
          setEditingConn(null);
          setConnModalOpen(true);
        }
      } else if (e.altKey && !e.shiftKey && e.key.toLowerCase() === 't' && !inEditorOrInput) {
        e.preventDefault();
        openNewSqlConsole();
      } else if (e.altKey && !e.ctrlKey && !e.metaKey && e.key >= '1' && e.key <= '9') {
        if (tabs.length > 0) {
          e.preventDefault();
          const num = Number(e.key);
          const target = num === 9 ? tabs[tabs.length - 1] : tabs[num - 1];
          if (target) {
            setActiveTab(target.key);
          }
        }
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [activeTabKey, closeTab, tabs, setActiveTab, effectiveCanCreateConn, connections, drivers]);

  const openTransferForTab = useCallback(
    (tab: WorkspaceTab, mode: TransferModalMode, tableName?: string, opts?: TransferOpenOptions) => {
      setTransferCtx({
        open: true,
        mode,
        connectionId: tab.connectionId,
        database: tab.database,
        schema: tab.schema,
        table: tableName,
        tables: opts?.tables,
        customSQL: opts?.customSQL,
      });
    },
    []
  );

  const handleSidebarNewConnection = useCallback(() => {
    setEditingConn(null);
    setConnModalOpen(true);
  }, []);

  const handleSidebarEditConnection = useCallback((conn: SavedConnection) => {
    setEditingConn(conn);
    setConnModalOpen(true);
  }, []);

  const handleSidebarOpenCreateTable = useCallback((conn: SavedConnection, db: string, schema?: string) => {
    setCreateTblCtx({
      open: true,
      connectionId: conn.id,
      dbType: conn.type,
      database: db,
      schema,
    });
  }, []);

  const handleSidebarOpenTransfer = useCallback(
    (conn: SavedConnection, db: string, schema: string | undefined, mode: TransferModalMode, tableName?: string) => {
      setTransferCtx({
        open: true,
        mode,
        connectionId: conn.id,
        database: db,
        schema,
        table: tableName,
      });
    },
    []
  );

  const handleSidebarItemOpened = useCallback(() => {
    if (isNarrowRef.current) {
      setSidebarCollapsed(true);
    }
  }, []);

  const renderTabContent = useCallback(
    (tab: WorkspaceTab) => {
      const fallback = (
        <div style={{ padding: 48, textAlign: 'center' }}>
          <Spin tip={t('Loading module...')} />
        </div>
      );
      switch (tab.kind) {
        case 'overview':
          return (
            <Suspense fallback={fallback}>
              <OverviewTab
                tab={tab}
                onOpenCreateTable={() =>
                  setCreateTblCtx({
                    open: true,
                    connectionId: tab.connectionId,
                    dbType: tab.dbType,
                    database: tab.database,
                    schema: tab.schema,
                  })
                }
                onOpenTransfer={(mode, tableName, opts) => openTransferForTab(tab, mode, tableName, opts)}
              />
            </Suspense>
          );
        case 'table_data':
          return (
            <TableDataTab
              tab={tab}
              onOpenTransfer={(mode, tableName, opts) => openTransferForTab(tab, mode, tableName, opts)}
            />
          );
        case 'table_structure':
          return (
            <Suspense fallback={fallback}>
              <TableStructureTab
                tab={tab}
                onOpenTransfer={(mode, tableName, opts) => openTransferForTab(tab, mode, tableName, opts)}
              />
            </Suspense>
          );
        case 'sql_console':
          return (
            <Suspense fallback={fallback}>
              <SqlConsoleTab
                tab={tab}
                onOpenExportSQL={(database, sql) =>
                  setTransferCtx({
                    open: true,
                    mode: 'export',
                    connectionId: tab.connectionId,
                    database,
                    customSQL: sql,
                  })
                }
              />
            </Suspense>
          );
        case 'redis_browser':
          return (
            <Suspense fallback={fallback}>
              <RedisBrowserTab tab={tab} />
            </Suspense>
          );
        case 'mongo_collection':
          return (
            <Suspense fallback={fallback}>
              <MongoCollectionTab tab={tab} />
            </Suspense>
          );
        case 'monitor':
          return (
            <Suspense fallback={fallback}>
              <ServerMonitorTab tab={tab} />
            </Suspense>
          );
        case 'db_users':
          return (
            <Suspense fallback={fallback}>
              <DbUsersTab tab={tab} />
            </Suspense>
          );
        default:
          return null;
      }
    },
    [openTransferForTab, t]
  );

  const tabItems = useMemo(
    () =>
      tabs.map((tabItem) => {
        const badge = DB_SHORT_LABELS[tabItem.dbType];
        const isKeepAlive = tabItem.key === activeTabKey || lruTabKeys.includes(tabItem.key);
        return {
          key: tabItem.key,
          label: (
            <Space size={5}>
              {badge && (
                <span
                  style={{
                    fontSize: 10,
                    fontWeight: 700,
                    padding: '0 4px',
                    borderRadius: 4,
                    background: badge.color,
                    color: '#fff',
                  }}
                >
                  {badge.code}
                </span>
              )}
              <span>{tabItem.title}</span>
            </Space>
          ),
          children: isKeepAlive ? renderTabContent(tabItem) : null,
        };
      }),
    [tabs, activeTabKey, lruTabKeys, renderTabContent]
  );

  const displayVersion = updateCheck?.currentVersion || buildInfo?.displayVersion || 'v1.0.1';
  const hasNewVersion = Boolean(updateCheck?.hasUpdate);

  const handleSidebarSplitterPointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return;
    e.preventDefault();
    const pointerId = e.pointerId;
    const splitterEl = e.currentTarget;
    try {
      splitterEl.setPointerCapture(pointerId);
    } catch {
      // ignore if synthetic event without active pointer
    }

    const startX = e.clientX;
    const startW = asideRef.current ? asideRef.current.getBoundingClientRect().width : sidebarWidth;
    (window as any).__DBHARBOR_LAYOUT_DRAGGING__ = true;
    splitterEl.classList.add('is-dragging');
    document.body.classList.add('dbharbor-is-dragging');
    document.body.style.cursor = 'col-resize';

    let latestW = Math.round(startW);
    let rafId = 0;

    const applyWidth = () => {
      rafId = 0;
      if (asideRef.current) {
        asideRef.current.style.width = `${latestW}px`;
      }
    };

    const onMove = (ev: PointerEvent) => {
      latestW = Math.max(200, Math.min(480, Math.round(startW + (ev.clientX - startX))));
      if (!rafId) {
        rafId = window.requestAnimationFrame(applyWidth);
      }
    };

    const onUp = () => {
      if (rafId) {
        window.cancelAnimationFrame(rafId);
        rafId = 0;
      }
      if (asideRef.current) {
        asideRef.current.style.width = `${latestW}px`;
      }
      try {
        if (splitterEl.hasPointerCapture(pointerId)) {
          splitterEl.releasePointerCapture(pointerId);
        }
      } catch {
        // ignore
      }
      (window as any).__DBHARBOR_LAYOUT_DRAGGING__ = false;
      splitterEl.classList.remove('is-dragging');
      document.body.classList.remove('dbharbor-is-dragging');
      document.body.style.cursor = '';
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
      window.removeEventListener('pointercancel', onUp);
      setSidebarWidth(latestW);
      localStorage.setItem('dbharbor_sidebar_width', String(latestW));
      window.requestAnimationFrame(() => {
        window.dispatchEvent(new Event('dbharbor-layout-settled'));
      });
    };

    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
    window.addEventListener('pointercancel', onUp);
  };

  const buildTabContextMenuItems = (tabObj: WorkspaceTab, tabIdx: number) => {
    const drv = drivers.find((d) => d.type === tabObj.dbType);
    const isSqlEngine = drv ? drv.category === 'relational' || drv.category === 'olap' : tabObj.dbType !== 'redis' && tabObj.dbType !== 'mongodb';
    return [
      ...(isSqlEngine
        ? [
            {
              key: 'new-sql',
              icon: <CodeOutlined />,
              label: t('New SQL Console (Alt+T)'),
              onClick: () => openNewSqlConsole(tabObj),
            },
          ]
        : []),
      {
        key: 'rename',
        icon: <EditOutlined />,
        label: t('Rename Tab'),
        onClick: () => {
          setRenamingTab(tabObj);
          setRenameTitle(tabObj.title);
        },
      },
      { type: 'divider' as const },
      {
        key: 'close',
        icon: <CloseOutlined />,
        label: t('Close Tab'),
        onClick: () => closeTab(tabObj.key),
      },
      {
        key: 'close-others',
        icon: <ColumnWidthOutlined />,
        label: t('Close Others'),
        disabled: tabs.length <= 1,
        onClick: () => closeOtherTabs(tabObj.key),
      },
      {
        key: 'close-left',
        icon: <ArrowLeftOutlined />,
        label: t('Close Tabs to the Left'),
        disabled: tabIdx <= 0,
        onClick: () => closeLeftTabs(tabObj.key),
      },
      {
        key: 'close-right',
        icon: <ArrowRightOutlined />,
        label: t('Close Tabs to the Right'),
        disabled: tabIdx < 0 || tabIdx >= tabs.length - 1,
        onClick: () => closeRightTabs(tabObj.key),
      },
      { type: 'divider' as const },
      {
        key: 'close-conn',
        icon: <DisconnectOutlined />,
        label: t('Close Connection Tabs'),
        onClick: () => closeConnectionTabs(tabObj.connectionId),
      },
      {
        key: 'close-all',
        danger: true,
        icon: <CloseCircleOutlined />,
        label: t('Close All Tabs'),
        onClick: () => closeAllTabs(),
      },
    ];
  };

  const activeTabIdx = tabs.findIndex((x) => x.key === activeTabKey);
  const activeTabObj = activeTabIdx >= 0 ? tabs[activeTabIdx] : tabs[0];

  return (
    <div style={{ height: '100dvh', display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
      {/* Top Global Bar */}
      <header
        className="workbench-top-header"
        style={{
          height: isMobile ? 48 : 54,
          padding: isMobile ? '0 10px' : '0 16px',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: 8,
          minWidth: 0,
          overflow: 'hidden',
          borderBottom: '1px solid rgba(128,128,128,0.18)',
          background: darkMode ? '#141414' : '#ffffff',
          flexShrink: 0,
        }}
      >
        <Space size={8} style={{ minWidth: 0, overflow: 'hidden' }}>
          <Button
            type="text"
            size="middle"
            style={isMobile ? { width: 36, height: 36 } : { width: 32, height: 32 }}
            icon={sidebarCollapsed ? <MenuUnfoldOutlined style={isMobile ? { fontSize: 17 } : undefined} /> : <MenuFoldOutlined style={isMobile ? { fontSize: 17 } : undefined} />}
            onClick={() => setSidebarCollapsed(!sidebarCollapsed)}
          />
          <div style={{ display: 'flex', alignItems: 'center', gap: isMobile ? 7 : 8, minWidth: 0, overflow: 'hidden', whiteSpace: 'nowrap' }}>
            <span style={{ display: 'inline-flex', flexShrink: 0 }}>
              <BrandLogo size={isMobile ? 24 : 26} />
            </span>
            <Typography.Text strong style={{ fontSize: 16, flexShrink: 0 }}>
              DBHarbor
            </Typography.Text>
            {!isMobile && (
              <>
                <Tooltip
                  title={
                    hasNewVersion
                      ? t('New GitHub release {ver} available! Click to view changelog', {
                          ver: updateCheck?.latestVersion || '',
                        })
                      : t('Version Info, Update Check & Changelog')
                  }
                >
                  <Badge dot={hasNewVersion} offset={[-2, 2]} className="workbench-version-badge" style={{ flexShrink: 0 }}>
                    <Tag
                      color={hasNewVersion ? 'volcano' : 'geekblue'}
                      style={{ margin: 0, cursor: 'pointer', fontWeight: 600 }}
                      onClick={() => setVersionModalOpen(true)}
                    >
                      {displayVersion}
                      {hasNewVersion ? ` -> ${updateCheck?.latestVersion}` : ''}
                    </Tag>
                  </Badge>
                </Tooltip>
                <Tag color="cyan" className="workbench-header-tag-extra" style={{ margin: 0 }}>
                  {t('{count} Engines', { count: drivers.length || 14 })}
                </Tag>
                {isDesktop && (
                  <Tag color="purple" className="workbench-header-tag-extra" style={{ margin: 0 }}>
                    {t('Local Desktop')}
                  </Tag>
                )}
                {!isDesktop && role === 'readonly' && (
                  <Tag color="default" className="workbench-header-tag-extra" style={{ margin: 0 }}>
                    {t('Read-Only Mode')}
                  </Tag>
                )}
              </>
            )}
          </div>
        </Space>

        {isMobile ? (
          <Space size={8} style={{ flexShrink: 0 }}>
            {effectiveCanCreateConn && (
              <Button
                type="primary"
                style={{ width: 36, height: 36, padding: 0 }}
                icon={<PlusOutlined style={{ fontSize: 16 }} />}
                title={t('New Connection')}
                onClick={() => {
                  setEditingConn(null);
                  setConnModalOpen(true);
                }}
              />
            )}
            <Badge
              dot={hasNewVersion || (!isDesktop && (!totpEnabled || !passwordChanged))}
              color={!isDesktop && !passwordChanged ? 'red' : hasNewVersion ? 'volcano' : 'gold'}
              offset={[-3, 3]}
            >
              <Dropdown
                trigger={['click']}
                placement="bottomRight"
                open={mobileMoreOpen}
                onOpenChange={(nextOpen) => {
                  if (!nextOpen && keepMobileMoreOpenRef.current) {
                    keepMobileMoreOpenRef.current = false;
                    return;
                  }
                  setMobileMoreOpen(nextOpen);
                  if (!nextOpen) {
                    setMobileMoreView('root');
                  }
                }}
                menu={{
                  items:
                    mobileMoreView === 'lang'
                      ? [
                          {
                            key: 'lang_back',
                            icon: <ArrowLeftOutlined />,
                            label: <Typography.Text strong>{t('Language')}</Typography.Text>,
                            onClick: () => {
                              keepMobileMoreOpenRef.current = true;
                              setMobileMoreView('root');
                            },
                          },
                          { type: 'divider' as const },
                          ...LOCALE_OPTIONS.map((opt) => ({
                            key: `lang_${opt.value}`,
                            label: (
                              <div
                                style={{
                                  display: 'flex',
                                  alignItems: 'center',
                                  justifyContent: 'space-between',
                                  gap: 16,
                                  minWidth: 210,
                                }}
                              >
                                <span>{opt.label}</span>
                                {locale === opt.value ? (
                                  <span style={{ color: '#1677ff', fontWeight: 700, flexShrink: 0 }}>✓</span>
                                ) : null}
                              </div>
                            ),
                            onClick: () => {
                              setLocale(opt.value);
                              setMobileMoreOpen(false);
                              setMobileMoreView('root');
                            },
                          })),
                        ]
                      : [
                          {
                            key: 'theme',
                            icon: darkMode ? <SunOutlined /> : <MoonOutlined />,
                            label: darkMode ? t('Switch to Light Mode') : t('Switch to Dark Mode'),
                            onClick: toggleDarkMode,
                          },
                          {
                            key: 'lang',
                            icon: <GlobalOutlined />,
                            label: (
                              <div
                                style={{
                                  display: 'flex',
                                  alignItems: 'center',
                                  justifyContent: 'space-between',
                                  gap: 16,
                                  width: '100%',
                                }}
                              >
                                <span>{t('Language')}</span>
                                <Typography.Text type="secondary" style={{ fontSize: 12, flexShrink: 0 }}>
                                  {(LOCALE_OPTIONS.find((o) => o.value === locale) || LOCALE_OPTIONS[0]).short} ›
                                </Typography.Text>
                              </div>
                            ),
                            onClick: () => {
                              keepMobileMoreOpenRef.current = true;
                              setMobileMoreView('lang');
                            },
                          },
                          {
                            key: 'version',
                            icon: <RocketOutlined />,
                            label: hasNewVersion
                              ? t('Update Available ({ver}) & Changelog', { ver: updateCheck?.latestVersion || '' })
                              : t('Check Updates & Changelog ({ver})', { ver: displayVersion }),
                            onClick: () => setVersionModalOpen(true),
                          },
                          ...(!isDesktop
                            ? [
                                { type: 'divider' as const },
                                ...(role === 'admin'
                                  ? [
                                      {
                                        key: 'users',
                                        icon: <TeamOutlined />,
                                        label: t('Team & User Management'),
                                        onClick: () => setUserMgmtOpen(true),
                                      },
                                      {
                                        key: 'branding',
                                        icon: <SafetyCertificateOutlined />,
                                        label: t('Login Page Appearance'),
                                        onClick: () => {
                                          setSecurityInitialTab('branding');
                                          setSecurityModalOpen(true);
                                        },
                                      },
                                    ]
                                  : []),
                                {
                                  key: '2fa',
                                  icon: <SafetyCertificateOutlined />,
                                  label: totpEnabled ? t('Account & 2FA (Enabled)') : t('Account & Enable 2FA'),
                                  onClick: () => {
                                    setSecurityInitialTab('2fa');
                                    setSecurityModalOpen(true);
                                  },
                                },
                                {
                                  key: 'pwd',
                                  icon: <KeyOutlined />,
                                  label: t('Change Password'),
                                  onClick: () => {
                                    setSecurityInitialTab('password');
                                    setSecurityModalOpen(true);
                                  },
                                },
                                { type: 'divider' as const },
                                {
                                  key: 'logout',
                                  danger: true,
                                  icon: <LogoutOutlined />,
                                  label: `${t('Sign Out')} (${displayName || username})`,
                                  onClick: () => logout(true),
                                },
                              ]
                            : []),
                        ],
                }}
              >
                <Button style={{ width: 36, height: 36, padding: 0 }} icon={<MoreOutlined style={{ fontSize: 17 }} />} />
              </Dropdown>
            </Badge>
          </Space>
        ) : (
          <Space size={isTablet ? 6 : 8} style={{ flexShrink: 0 }}>
            {effectiveCanCreateConn && (
              <Button
                type="primary"
                size="middle"
                icon={<PlusOutlined />}
                title={isTablet ? t('New Connection') : undefined}
                onClick={() => {
                  setEditingConn(null);
                  setConnModalOpen(true);
                }}
              >
                {!isTablet && <span className="workbench-btn-label">{t('New Connection')}</span>}
              </Button>
            )}
            {!isDesktop && role === 'admin' && (
              <Tooltip title={t('Team & RBAC User Governance')}>
                <Button size="middle" icon={<TeamOutlined />} onClick={() => setUserMgmtOpen(true)}>
                  {!isTablet && <span className="workbench-btn-label">{t('Users')}</span>}
                </Button>
              </Tooltip>
            )}
            {!isDesktop && (
              <Tooltip title={t('Security Hardening, 2FA & Active Sessions')}>
                <Badge dot={!totpEnabled || !passwordChanged} color={!passwordChanged ? 'red' : 'gold'}>
                  <Button
                    size="middle"
                    icon={<SafetyCertificateOutlined style={{ color: totpEnabled ? '#52c41a' : undefined }} />}
                    onClick={() => {
                      setSecurityInitialTab('2fa');
                      setSecurityModalOpen(true);
                    }}
                  >
                    {!isTablet && <span className="workbench-btn-label">{t('Security / 2FA')}</span>}
                  </Button>
                </Badge>
              </Tooltip>
            )}
            <LanguageSwitcher size="middle" />
            <Button
              type="text"
              size="middle"
              style={{ width: 32, height: 32, padding: 0 }}
              icon={darkMode ? <SunOutlined /> : <MoonOutlined />}
              onClick={toggleDarkMode}
              title={t('Toggle Light / Dark Theme')}
            />
            {!isDesktop && (
              <Dropdown
                trigger={['click']}
                placement="bottomRight"
                menu={{
                  items: [
                    ...(role === 'admin'
                      ? [
                          {
                            key: 'users',
                            icon: <TeamOutlined />,
                            label: t('Team & User Management'),
                            onClick: () => setUserMgmtOpen(true),
                          },
                          {
                            key: 'branding',
                            icon: <SafetyCertificateOutlined />,
                            label: t('Login Page Appearance'),
                            onClick: () => {
                              setSecurityInitialTab('branding');
                              setSecurityModalOpen(true);
                            },
                          },
                          { type: 'divider' as const },
                        ]
                      : []),
                    {
                      key: '2fa',
                      icon: <SafetyCertificateOutlined />,
                      label: totpEnabled ? t('Account & 2FA (Enabled)') : t('Account & Enable 2FA'),
                      onClick: () => {
                        setSecurityInitialTab('2fa');
                        setSecurityModalOpen(true);
                      },
                    },
                    {
                      key: 'pwd',
                      icon: <KeyOutlined />,
                      label: t('Change Password'),
                      onClick: () => {
                        setSecurityInitialTab('password');
                        setSecurityModalOpen(true);
                      },
                    },
                    {
                      key: 'version',
                      icon: <RocketOutlined />,
                      label: hasNewVersion
                        ? t('Update Available ({ver}) & Changelog', { ver: updateCheck?.latestVersion || '' })
                        : t('Check Updates & Changelog ({ver})', { ver: displayVersion }),
                      onClick: () => setVersionModalOpen(true),
                    },
                    { type: 'divider' },
                    {
                      key: 'logout',
                      danger: true,
                      icon: <LogoutOutlined />,
                      label: t('Sign Out'),
                      onClick: () => logout(true),
                    },
                  ],
                }}
              >
                <Button
                  size="middle"
                  icon={<UserOutlined />}
                  title={isTablet ? displayName || username : undefined}
                >
                  {!isTablet && <span className="workbench-btn-label">{displayName || username}</span>}
                  {!isTablet && (
                    <Tag
                      color={role === 'admin' ? 'gold' : role === 'readwrite' ? 'blue' : 'default'}
                      className="workbench-header-tag-extra"
                      style={{ margin: '0 0 0 6px', fontSize: 11, lineHeight: '18px', padding: '0 5px' }}
                    >
                      {role === 'admin' ? t('Admin') : role === 'readwrite' ? 'RW' : 'RO'}
                    </Tag>
                  )}
                </Button>
              </Dropdown>
            )}
          </Space>
        )}
      </header>

      {/* Subtle Security Hardening Prompt when default password is still unchanged (Web Server mode only) */}
      {!isDesktop && !passwordChanged && !securityHintDismissed && (
        isMobile ? (
          <div
            style={{
              height: 30,
              padding: '0 10px',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'space-between',
              gap: 8,
              background: darkMode ? 'rgba(250, 173, 20, 0.14)' : '#fffbe6',
              borderBottom: '1px solid rgba(250, 173, 20, 0.35)',
              fontSize: 12,
              flexShrink: 0,
            }}
          >
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0, overflow: 'hidden' }}>
              <ExclamationCircleOutlined style={{ color: '#faad14', flexShrink: 0 }} />
              <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {t('Default password active — please harden account')}
              </span>
            </div>
            <Space size={8} style={{ flexShrink: 0 }}>
              <a
                style={{ fontWeight: 600, whiteSpace: 'nowrap' }}
                onClick={() => {
                  setSecurityInitialTab('password');
                  setSecurityModalOpen(true);
                }}
              >
                {t('Harden')}
              </a>
              <CloseOutlined
                style={{ fontSize: 11, opacity: 0.65, cursor: 'pointer' }}
                onClick={() => setSecurityHintDismissed(true)}
              />
            </Space>
          </div>
        ) : (
          <Alert
            banner
            type="warning"
            closable
            onClose={() => setSecurityHintDismissed(true)}
            message={
              <Space size={[10, 6]} wrap>
                <span>
                  {t(
                    'Security Notice: You are still using the default initial password. Please change your password and enable TOTP 2FA.'
                  )}
                </span>
                <Button
                  size="small"
                  type="primary"
                  ghost
                  onClick={() => {
                    setSecurityInitialTab('password');
                    setSecurityModalOpen(true);
                  }}
                >
                  {t('Harden Account Security')}
                </Button>
              </Space>
            }
          />
        )
      )}

      {/* Main Split Body — Keep-Alive ConnectionSidebar prevents re-mounting & re-fetching metadata on toggle */}
      <div style={{ flex: 1, minHeight: 0, minWidth: 0, display: 'flex', overflow: 'hidden', position: 'relative' }}>
        {isNarrowViewport && (
          <div
            onClick={() => setSidebarCollapsed(true)}
            style={{
              position: 'absolute',
              inset: 0,
              background: 'rgba(0, 0, 0, 0.38)',
              zIndex: 50,
              opacity: sidebarCollapsed ? 0 : 1,
              pointerEvents: sidebarCollapsed ? 'none' : 'auto',
              transition: 'opacity 0.2s ease',
            }}
          />
        )}
        <aside
          ref={asideRef}
          aria-hidden={sidebarCollapsed}
          style={
            isNarrowViewport
              ? {
                  position: 'absolute',
                  top: 0,
                  bottom: 0,
                  left: 0,
                  width: Math.min(296, Math.max(232, window.innerWidth - 44)),
                  zIndex: 55,
                  overflow: 'hidden',
                  background: darkMode ? '#111318' : '#fafafa',
                  boxShadow: sidebarCollapsed ? 'none' : '4px 0 22px rgba(0,0,0,0.32)',
                  borderRight: '1px solid rgba(128,128,128,0.22)',
                  transform: sidebarCollapsed ? 'translateX(-104%)' : 'translateX(0)',
                  pointerEvents: sidebarCollapsed ? 'none' : 'auto',
                  transition: 'transform 0.22s cubic-bezier(0.22, 1, 0.36, 1)',
                }
              : {
                  display: sidebarCollapsed ? 'none' : 'block',
                  width: sidebarWidth,
                  minWidth: 200,
                  maxWidth: 'min(480px, 42vw)',
                  flexShrink: 0,
                  overflow: 'hidden',
                  background: darkMode ? '#111318' : '#fafafa',
                }
          }
        >
          <ConnectionSidebar
            onNewConnection={handleSidebarNewConnection}
            onEditConnection={handleSidebarEditConnection}
            onOpenCreateTable={handleSidebarOpenCreateTable}
            onOpenTransfer={handleSidebarOpenTransfer}
            onItemOpened={handleSidebarItemOpened}
          />
        </aside>
        {!isNarrowViewport && !sidebarCollapsed && (
          <div
            ref={sidebarSplitterRef}
            className="dbharbor-sidebar-splitter"
            onPointerDown={handleSidebarSplitterPointerDown}
            onDoubleClick={() => {
              setSidebarWidth(272);
              if (asideRef.current) {
                asideRef.current.style.width = '272px';
              }
              localStorage.setItem('dbharbor_sidebar_width', '272');
              window.requestAnimationFrame(() => {
                window.dispatchEvent(new Event('dbharbor-layout-settled'));
              });
            }}
          />
        )}

        <main style={{ flex: 1, minWidth: 0, minHeight: 0, overflow: 'hidden', display: 'flex', flexDirection: 'column' }}>
          {tabs.length === 0 ? (
            <div style={{ flex: 1, overflowY: 'auto', padding: isMobile ? 14 : 32 }}>
              <div style={{ maxWidth: 920, margin: '0 auto' }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: isMobile ? 10 : 14, marginBottom: 8 }}>
                  <BrandLogo size={isMobile ? 36 : 48} glow />
                  <div style={{ minWidth: 0 }}>
                    <Typography.Title level={isMobile ? 5 : 3} style={{ margin: 0 }}>
                      {t('Welcome to DBHarbor {ver} Multi-Database Workbench', { ver: displayVersion })}
                    </Typography.Title>
                    <Typography.Text type="secondary" style={{ fontSize: isMobile ? 12 : 14 }}>
                      {t(
                        'Combines phpMyAdmin-style visual schema & table management with modern multi-engine IDE power: visual column alter, custom SQL, full-DB dumps, and 2FA security.'
                      )}
                    </Typography.Text>
                  </div>
                </div>

                <Row gutter={[16, 16]} style={{ marginTop: 20 }}>
                  <Col xs={24} md={8}>
                    <Card
                      hoverable={effectiveCanCreateConn}
                      onClick={() => {
                        if (!effectiveCanCreateConn) {
                          message.info(t('Contact an administrator to add or assign connections'));
                          return;
                        }
                        setEditingConn(null);
                        setConnModalOpen(true);
                      }}
                    >
                      <Space direction="vertical" size={6}>
                        <PlusOutlined style={{ fontSize: 24, color: '#1677ff' }} />
                        <Typography.Text strong>{t('1. Connect Any Database')}</Typography.Text>
                        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                          {t(
                            'Supports 14 Relational, OLAP, Key-Value & Document engines with AES-256-GCM encrypted credentials.'
                          )}
                        </Typography.Text>
                      </Space>
                    </Card>
                  </Col>
                  <Col xs={24} md={8}>
                    <Card
                      hoverable
                      onClick={() => {
                        const first = connections[0];
                        if (first) {
                          openTab({
                            key: `sql:${first.id}:${Date.now()}`,
                            title: `SQL - ${first.name}`,
                            kind: 'sql_console',
                            connectionId: first.id,
                            connName: first.name,
                            dbType: first.type,
                            database: first.database || 'main',
                          });
                        } else if (effectiveCanCreateConn) {
                          setConnModalOpen(true);
                        }
                      }}
                    >
                      <Space direction="vertical" size={6}>
                        <CodeOutlined style={{ fontSize: 24, color: '#13c2c2' }} />
                        <Typography.Text strong>{t('2. Multi-Tab Smart SQL Console')}</Typography.Text>
                        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                          {t(
                            'Embedded offline Monaco Editor with schema-aware autocomplete, EXPLAIN plans, multi-statement execution & 6 export formats.'
                          )}
                        </Typography.Text>
                      </Space>
                    </Card>
                  </Col>
                  <Col xs={24} md={8}>
                    <Card
                      hoverable
                      onClick={() => {
                        const first = connections[0];
                        if (first) {
                          openTab({
                            key: `monitor:${first.id}`,
                            title: `${first.name} (${t('Monitor')})`,
                            kind: 'monitor',
                            connectionId: first.id,
                            connName: first.name,
                            dbType: first.type,
                            database: first.database || '',
                          });
                        } else {
                          setConnModalOpen(true);
                        }
                      }}
                    >
                      <Space direction="vertical" size={6}>
                        <DashboardOutlined style={{ fontSize: 24, color: '#faad14' }} />
                        <Typography.Text strong>{t('3. Live Server Monitor & Session Kill')}</Typography.Text>
                        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                          {t(
                            'Inspect active queries / slow SQL with one-click kill, storage rankings, and ClickHouse partition governance.'
                          )}
                        </Typography.Text>
                      </Space>
                    </Card>
                  </Col>
                </Row>

                <Card size="small" title={t('All 14 Built-in Database Drivers')} style={{ marginTop: 24 }}>
                  <Space size={[8, 8]} wrap>
                    {drivers.map((d) => {
                      const badge = DB_SHORT_LABELS[d.type] || { code: 'DB', color: d.badgeColor };
                      return (
                        <Tag key={d.type} color={badge.color} style={{ padding: '3px 10px', fontSize: 12 }}>
                          <strong>{badge.code}</strong> · {d.name}
                        </Tag>
                      );
                    })}
                  </Space>
                </Card>
              </div>
            </div>
          ) : isMobile && activeTabObj ? (
            <div style={{ flex: 1, minHeight: 0, minWidth: 0, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
              {/* Mobile Single-Pill Tab Switcher Bar (touch-sized, anti-mis-tap) */}
              <div
                style={{
                  height: 42,
                  padding: '0 8px',
                  display: 'flex',
                  alignItems: 'center',
                  gap: 6,
                  borderBottom: '1px solid rgba(128,128,128,0.16)',
                  background: darkMode ? '#14161c' : '#fafafa',
                  flexShrink: 0,
                }}
              >
                <Dropdown
                  trigger={['click']}
                  placement="bottomLeft"
                  menu={{
                    selectedKeys: [activeTabObj.key],
                    items: tabs.map((tb, idx) => {
                      const b = DB_SHORT_LABELS[tb.dbType];
                      return {
                        key: tb.key,
                        label: (
                          <div
                            style={{
                              display: 'flex',
                              alignItems: 'center',
                              justifyContent: 'space-between',
                              gap: 10,
                              width: 'min(76vw, 310px)',
                            }}
                          >
                            <Space size={6} style={{ minWidth: 0, flex: 1, overflow: 'hidden' }}>
                              {b && (
                                <span
                                  style={{
                                    fontSize: 10,
                                    fontWeight: 700,
                                    padding: '1px 5px',
                                    borderRadius: 4,
                                    background: b.color,
                                    color: '#fff',
                                    flexShrink: 0,
                                  }}
                                >
                                  {b.code}
                                </span>
                              )}
                              <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                                {tb.title}
                              </span>
                            </Space>
                            <Space size={4} style={{ flexShrink: 0 }}>
                              <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                                #{idx + 1}
                              </Typography.Text>
                              <span
                                role="button"
                                title={t('Close Tab')}
                                onClick={(e) => {
                                  e.stopPropagation();
                                  closeTab(tb.key);
                                }}
                                style={{
                                  width: 28,
                                  height: 28,
                                  borderRadius: 6,
                                  display: 'inline-flex',
                                  alignItems: 'center',
                                  justifyContent: 'center',
                                  opacity: 0.72,
                                }}
                              >
                                <CloseOutlined style={{ fontSize: 12 }} />
                              </span>
                            </Space>
                          </div>
                        ),
                        onClick: () => setActiveTab(tb.key),
                      };
                    }),
                  }}
                >
                  <Button
                    style={{
                      flex: 1,
                      height: 34,
                      minWidth: 0,
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'space-between',
                      padding: '0 10px',
                      overflow: 'hidden',
                    }}
                  >
                    <Space size={6} style={{ minWidth: 0, overflow: 'hidden' }}>
                      {DB_SHORT_LABELS[activeTabObj.dbType] && (
                        <span
                          style={{
                            fontSize: 10,
                            fontWeight: 700,
                            padding: '1px 5px',
                            borderRadius: 4,
                            background: DB_SHORT_LABELS[activeTabObj.dbType].color,
                            color: '#fff',
                            flexShrink: 0,
                          }}
                        >
                          {DB_SHORT_LABELS[activeTabObj.dbType].code}
                        </span>
                      )}
                      <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', fontWeight: 500 }}>
                        {activeTabObj.title}
                      </span>
                    </Space>
                    <Space size={4} style={{ flexShrink: 0, marginLeft: 6 }}>
                      <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                        {(activeTabIdx >= 0 ? activeTabIdx : 0) + 1}/{tabs.length}
                      </Typography.Text>
                      <DownOutlined style={{ fontSize: 10, opacity: 0.65 }} />
                    </Space>
                  </Button>
                </Dropdown>
                {activeTabObj.dbType !== 'redis' && activeTabObj.dbType !== 'mongodb' && (
                  <Button
                    style={{ width: 34, height: 34, padding: 0, flexShrink: 0 }}
                    icon={<CodeOutlined />}
                    title={t('New SQL Console')}
                    onClick={() =>
                      openTab({
                        key: `sql:${activeTabObj.connectionId}:${Date.now()}`,
                        title: `SQL - ${activeTabObj.database || activeTabObj.connName}`,
                        kind: 'sql_console',
                        connectionId: activeTabObj.connectionId,
                        connName: activeTabObj.connName,
                        dbType: activeTabObj.dbType,
                        database: activeTabObj.database,
                        schema: activeTabObj.schema,
                      })
                    }
                  />
                )}
                <Dropdown
                  trigger={['click']}
                  placement="bottomRight"
                  menu={{ items: buildTabContextMenuItems(activeTabObj, activeTabIdx >= 0 ? activeTabIdx : 0) }}
                >
                  <Button
                    style={{ width: 34, height: 34, padding: 0, flexShrink: 0 }}
                    icon={<MoreOutlined style={{ fontSize: 16 }} />}
                    title={t('Tab Actions')}
                  />
                </Dropdown>
              </div>

              {/* Keep-Alive Tab Panes on Mobile */}
              <div style={{ flex: 1, minHeight: 0, minWidth: 0, overflow: 'hidden', position: 'relative' }}>
                {tabs.map((tb) => {
                  const isActive = tb.key === activeTabObj.key;
                  const isKeepAlive = isActive || lruTabKeys.includes(tb.key);
                  if (!isKeepAlive) return null;
                  const isSelfFlexPane =
                    tb.kind === 'table_data' ||
                    tb.kind === 'sql_console' ||
                    tb.kind === 'redis_browser' ||
                    tb.kind === 'mongo_collection';
                  return (
                    <div
                      key={tb.key}
                      style={{
                        display: isActive ? 'flex' : 'none',
                        flexDirection: 'column',
                        height: '100%',
                        width: '100%',
                        padding: isSelfFlexPane ? 0 : 8,
                        overflowY: isSelfFlexPane ? 'hidden' : 'auto',
                        overflowX: 'hidden',
                      }}
                    >
                      {renderTabContent(tb)}
                    </div>
                  );
                })}
              </div>
            </div>
          ) : (
            <Tabs
              type="editable-card"
              hideAdd
              size="small"
              activeKey={activeTabKey}
              onChange={setActiveTab}
              onEdit={(targetKey, action) => {
                if (action === 'remove' && typeof targetKey === 'string') {
                  closeTab(targetKey);
                }
              }}
              style={{ height: '100%' }}
              className="workbench-main-tabs"
              tabBarExtraContent={{
                right: (() => {
                  const activeIdx = tabs.findIndex((x) => x.key === activeTabKey);
                  const activeObj = activeIdx >= 0 ? tabs[activeIdx] : tabs[0];
                  if (!activeObj) return null;
                  return (
                    <Dropdown
                      trigger={['click']}
                      placement="bottomRight"
                      menu={{ items: buildTabContextMenuItems(activeObj, activeIdx >= 0 ? activeIdx : 0) }}
                    >
                      <Tooltip title={t('Tab Actions')}>
                        <Button type="text" size="small" icon={<MoreOutlined />} style={{ marginLeft: 4 }} />
                      </Tooltip>
                    </Dropdown>
                  );
                })(),
              }}
              renderTabBar={(tabBarProps, DefaultTabBar) => (
                <DefaultTabBar {...tabBarProps}>
                  {(node) => {
                    const tabKey = String(node.key || '');
                    const tabIdx = tabs.findIndex((x) => x.key === tabKey);
                    const tabObj = tabIdx >= 0 ? tabs[tabIdx] : undefined;
                    if (!tabObj) return node;
                    const isDropTarget = Boolean(dragOverTabKey === tabKey && draggingTabKey && draggingTabKey !== tabKey);
                    return (
                      <Dropdown
                        key={tabKey}
                        trigger={['contextMenu']}
                        menu={{ items: buildTabContextMenuItems(tabObj, tabIdx) }}
                      >
                        <div
                          draggable
                          onAuxClick={(e) => {
                            if (e.button === 1) {
                              e.preventDefault();
                              e.stopPropagation();
                              closeTab(tabKey);
                            }
                          }}
                          onDragStart={(e) => {
                            setDraggingTabKey(tabKey);
                            e.dataTransfer.effectAllowed = 'move';
                            try {
                              e.dataTransfer.setData('text/plain', tabKey);
                            } catch {
                              // ignore browser drag data restrictions
                            }
                          }}
                          onDragOver={(e) => {
                            e.preventDefault();
                            e.dataTransfer.dropEffect = 'move';
                            if (dragOverTabKey !== tabKey) {
                              setDragOverTabKey(tabKey);
                            }
                          }}
                          onDragLeave={() => {
                            if (dragOverTabKey === tabKey) {
                              setDragOverTabKey('');
                            }
                          }}
                          onDrop={(e) => {
                            e.preventDefault();
                            const srcKey = draggingTabKey || e.dataTransfer.getData('text/plain');
                            if (srcKey && srcKey !== tabKey) {
                              reorderTabs(srcKey, tabKey);
                            }
                            setDraggingTabKey('');
                            setDragOverTabKey('');
                          }}
                          onDragEnd={() => {
                            setDraggingTabKey('');
                            setDragOverTabKey('');
                          }}
                          style={{
                            display: 'inline-flex',
                            opacity: draggingTabKey === tabKey ? 0.45 : 1,
                            boxShadow: isDropTarget ? 'inset 2px 0 0 #1677ff' : undefined,
                            borderRadius: 6,
                          }}
                        >
                          {node}
                        </div>
                      </Dropdown>
                    );
                  }}
                </DefaultTabBar>
              )}
              items={tabItems}
            />
          )}
        </main>
      </div>

      {/* Compact Runtime & Version Status Footer */}
      <footer
        className="workbench-bottom-footer"
        style={{
          height: isMobile ? 24 : 26,
          padding: isMobile ? '0 10px' : '0 14px',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: 12,
          overflow: 'hidden',
          whiteSpace: 'nowrap',
          borderTop: '1px solid rgba(128,128,128,0.16)',
          background: darkMode ? '#111318' : '#f7f9fc',
          fontSize: 11,
          flexShrink: 0,
        }}
      >
        <Space size={14} style={{ minWidth: 0, overflow: 'hidden' }}>
          <Typography.Text type="secondary" style={{ fontSize: 11 }}>
            {t('Connections: {count}', { count: connections.length })}
          </Typography.Text>
          {!isMobile && sysStats && (
            <Space size={6} className="workbench-footer-extra">
              <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                {t('Backend RAM (Alloc): {mb} MB · Active Pools: {active}/{max}', {
                  mb: sysStats.allocMB.toFixed(1),
                  active: sysStats.activePools,
                  max: sysStats.maxPools,
                })}
              </Typography.Text>
              {isAdmin && sysStats.activePools > 0 && (
                <Tooltip title={t('Close idle database connection pools to free memory')}>
                  <a
                    style={{ fontSize: 11 }}
                    onClick={async () => {
                      try {
                        const res = await api.releaseIdlePools();
                        message.success(res.message);
                        const fresh = await api.getSystemStats();
                        setSysStats(fresh);
                      } catch (err: any) {
                        message.error(err.message);
                      }
                    }}
                  >
                    [{t('Release Pools')}]
                  </a>
                </Tooltip>
              )}
            </Space>
          )}
          {!isMobile && (
            <Typography.Text type="secondary" className="workbench-footer-mode" style={{ fontSize: 11 }}>
              {isDesktop
                ? t('Mode: Pure Local Vault')
                : totpEnabled
                  ? t('2FA: Enabled')
                  : t('2FA: Disabled')}
            </Typography.Text>
          )}
        </Space>

        <Space size={12} style={{ flexShrink: 0 }}>
          {!isMobile && (
            <Typography.Link
              href="https://github.com/Willh92/DBHarbor"
              target="_blank"
              rel="noreferrer"
              style={{ fontSize: 11 }}
            >
              <GithubOutlined />
              <span className="workbench-footer-repo-text"> Willh92/DBHarbor</span>
            </Typography.Link>
          )}
          <a onClick={() => setVersionModalOpen(true)} style={{ fontSize: 11 }}>
            {displayVersion}
            {isMobile
              ? hasNewVersion
                ? ` -> ${updateCheck?.latestVersion}`
                : ''
              : ` ${hasNewVersion ? t('(Upgrade to {ver})', { ver: updateCheck?.latestVersion || '' }) : t('(Updates / Changelog)')}`}
          </a>
        </Space>
      </footer>

      {/* Rename Tab Modal */}
      <Modal
        open={Boolean(renamingTab)}
        title={t('Rename Tab')}
        width="min(420px, calc(100vw - 24px))"
        onCancel={() => setRenamingTab(null)}
        onOk={() => {
          if (renamingTab && renameTitle.trim()) {
            updateTab(renamingTab.key, { title: renameTitle.trim() });
          }
          setRenamingTab(null);
        }}
        okText={t('Save')}
        destroyOnClose
      >
        <Input
          autoFocus
          value={renameTitle}
          maxLength={80}
          placeholder={t('Enter custom tab title')}
          onChange={(e) => setRenameTitle(e.target.value)}
          onPressEnter={() => {
            if (renamingTab && renameTitle.trim()) {
              updateTab(renamingTab.key, { title: renameTitle.trim() });
            }
            setRenamingTab(null);
          }}
        />
      </Modal>

      {/* Lazy on-demand Modals — zero idle React fibers when closed */}
      <Suspense fallback={null}>
        {connModalOpen && (
          <ConnectionModal
            open={connModalOpen}
            editing={editingConn}
            onClose={() => setConnModalOpen(false)}
            onSaved={reloadConnections}
          />
        )}

        {createTblCtx.open && (
          <CreateTableModal
            open={createTblCtx.open}
            connectionId={createTblCtx.connectionId}
            dbType={createTblCtx.dbType}
            database={createTblCtx.database}
            schema={createTblCtx.schema}
            onClose={() => setCreateTblCtx((s) => ({ ...s, open: false }))}
            onCreated={(tblName) => {
              bumpTreeRefresh();
              const conn = connections.find((c) => c.id === createTblCtx.connectionId);
              if (conn) {
                openTab({
                  key: `struct:${conn.id}:${createTblCtx.database}:${createTblCtx.schema || ''}:${tblName}`,
                  title: `${tblName} [${t('Structure')}]`,
                  kind: 'table_structure',
                  connectionId: conn.id,
                  connName: conn.name,
                  dbType: conn.type,
                  database: createTblCtx.database,
                  schema: createTblCtx.schema,
                  table: tblName,
                });
              }
            }}
          />
        )}

        {transferCtx.open && (
          <ImportExportModal
            open={transferCtx.open}
            initialMode={transferCtx.mode}
            connectionId={transferCtx.connectionId}
            database={transferCtx.database}
            schema={transferCtx.schema}
            table={transferCtx.table}
            initialTables={transferCtx.tables}
            customSQL={transferCtx.customSQL}
            onClose={() => setTransferCtx((s) => ({ ...s, open: false }))}
            onImported={bumpTreeRefresh}
          />
        )}

        {!isDesktop && securityModalOpen && (
          <SecurityModal
            open={securityModalOpen}
            initialTab={securityInitialTab}
            onClose={() => setSecurityModalOpen(false)}
          />
        )}

        {!isDesktop && role === 'admin' && userMgmtOpen && (
          <UserManagementModal
            open={userMgmtOpen}
            onClose={() => setUserMgmtOpen(false)}
          />
        )}

        {versionModalOpen && (
          <VersionUpdateModal
            open={versionModalOpen}
            initialBuildInfo={buildInfo}
            initialCheckResult={updateCheck}
            onClose={() => setVersionModalOpen(false)}
            onUpdateChecked={setUpdateCheck}
          />
        )}
      </Suspense>
    </div>
  );
};
