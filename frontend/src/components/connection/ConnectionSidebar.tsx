import React, { useDeferredValue, useEffect, useMemo, useRef, useState } from 'react';
import {
  Alert,
  Badge,
  Button,
  Checkbox,
  Dropdown,
  Empty,
  Input,
  Modal,
  Popconfirm,
  Select,
  Space,
  Spin,
  Tag,
  Tooltip,
  Typography,
  message,
} from 'antd';
import {
  ApiOutlined,
  ClearOutlined,
  CodeOutlined,
  CopyOutlined,
  DashboardOutlined,
  DatabaseOutlined,
  DeleteOutlined,
  DisconnectOutlined,
  DownOutlined,
  DownloadOutlined,
  EditOutlined,
  EyeOutlined,
  FolderOpenOutlined,
  FunctionOutlined,
  KeyOutlined,
  LockOutlined,
  MoreOutlined,
  PlusOutlined,
  ReloadOutlined,
  RightOutlined,
  SearchOutlined,
  TableOutlined,
  TeamOutlined,
  ThunderboltOutlined,
  UploadOutlined,
} from '@ant-design/icons';
import { api } from '../../api/client';
import { useResponsive } from '../../hooks/useResponsive';
import { useI18n } from '../../i18n/useI18n';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import type {
  CollectionSummary,
  DatabaseSummary,
  ExportedConnectionProfile,
  ObjectItem,
  SavedConnection,
} from '../../types';
import { DB_SHORT_LABELS, formatBytes } from '../../utils/format';
import { writeTextToClipboard } from '../../utils/tableClipboard';
import type { TransferModalMode } from '../workspace/ContextNavBar';
import {
  ROUTINE_CAPABLE_ENGINES,
  RoutineTriggerModal,
  TRIGGER_CAPABLE_ENGINES,
} from '../workspace/RoutineTriggerModal';

interface Props {
  onNewConnection: () => void;
  onEditConnection: (conn: SavedConnection) => void;
  onOpenCreateTable: (conn: SavedConnection, database: string, schema: string) => void;
  onOpenTransfer?: (
    conn: SavedConnection,
    database: string,
    schema: string,
    mode: TransferModalMode,
    tableName?: string
  ) => void;
  onItemOpened?: () => void;
}

const VIRTUAL_THRESHOLD = 60;
const ITEM_ROW_HEIGHT = 30;
const OVERSCAN_ROWS = 12;

export const ConnectionSidebar: React.FC<Props> = React.memo(({
  onNewConnection,
  onEditConnection,
  onOpenCreateTable,
  onOpenTransfer,
  onItemOpened,
}) => {
  const {
    connections,
    drivers,
    openTab,
    reloadConnections,
    closeConnectionTabs,
    treeRefreshTick,
    bumpTreeRefresh,
    isDesktop,
    role,
    canCreateConn,
  } = useWorkbenchStore();
  const { isMobile } = useResponsive();
  const { t } = useI18n();
  const itemRowHeight = isMobile ? 40 : ITEM_ROW_HEIGHT;

  const canWrite = isDesktop || role !== 'readonly';
  const effectiveCanCreateConn = isDesktop || role === 'admin' || canCreateConn;

  const [connSearch, setConnSearch] = useState('');
  const [search, setSearch] = useState('');
  const deferredSearch = useDeferredValue(search);
  const [activeConnId, setActiveConnId] = useState<string>('');
  const [databases, setDatabases] = useState<DatabaseSummary[]>([]);
  const [selectedDB, setSelectedDB] = useState<string>('');
  const [schemas, setSchemas] = useState<string[]>([]);
  const [selectedSchema, setSelectedSchema] = useState<string>('');
  const [tables, setTables] = useState<ObjectItem[]>([]);
  const [views, setViews] = useState<ObjectItem[]>([]);
  const [routines, setRoutines] = useState<ObjectItem[]>([]);
  const [triggers, setTriggers] = useState<ObjectItem[]>([]);
  const [collections, setCollections] = useState<CollectionSummary[]>([]);
  const [loadingMeta, setLoadingMeta] = useState(false);

  // Collapsible section states
  const [collapsed, setCollapsed] = useState<{
    connections: boolean;
    tables: boolean;
    views: boolean;
    routines: boolean;
    triggers: boolean;
    collections: boolean;
  }>({
    connections: false,
    tables: false,
    views: false,
    routines: false,
    triggers: false,
    collections: false,
  });

  const toggleGroup = (group: keyof typeof collapsed) => {
    setCollapsed((prev) => ({ ...prev, [group]: !prev[group] }));
  };

  // Database & Schema lifecycle modals
  const [newDBModalOpen, setNewDBModalOpen] = useState(false);
  const [newDBName, setNewDBName] = useState('');
  const [dropDBModalOpen, setDropDBModalOpen] = useState(false);
  const [dropDBConfirmInput, setDropDBConfirmInput] = useState('');
  const [newSchemaModalOpen, setNewSchemaModalOpen] = useState(false);
  const [newSchemaName, setNewSchemaName] = useState('');
  const [dropSchemaCascade, setDropSchemaCascade] = useState(false);
  const [dropSchemaModalOpen, setDropSchemaModalOpen] = useState(false);

  // DDL Inspector & View Editor Modal
  const [ddlModal, setDdlModal] = useState<{
    open: boolean;
    loading: boolean;
    saving: boolean;
    isNewView: boolean;
    objectType: string;
    objectName: string;
    sql: string;
  }>({
    open: false,
    loading: false,
    saving: false,
    isNewView: false,
    objectType: 'VIEW',
    objectName: '',
    sql: '',
  });

  // Routine & Trigger visual creator / DDL editor modal
  const [routineTriggerModal, setRoutineTriggerModal] = useState<{
    open: boolean;
    mode: 'routine' | 'trigger';
    editObject: { name: string; objectType: 'PROCEDURE' | 'FUNCTION' | 'TRIGGER' } | null;
  }>({
    open: false,
    mode: 'routine',
    editObject: null,
  });

  const importInputRef = useRef<HTMLInputElement | null>(null);

  // Sequence counter prevents stale responses from overwriting rapid connection/DB switches
  const reqSeqRef = useRef(0);
  const prevRefreshTickRef = useRef(treeRefreshTick);

  // In-memory metadata cache so toggling connections/databases/sidebar never triggers redundant reloads
  const connDatabasesCacheRef = useRef<Map<string, { databases: DatabaseSummary[]; lastDB: string; lastSchema: string }>>(
    new Map()
  );
  const dbChildrenCacheRef = useRef<
    Map<
      string,
      {
        schemas: string[];
        selectedSchema: string;
        tables: ObjectItem[];
        views: ObjectItem[];
        routines: ObjectItem[];
        triggers: ObjectItem[];
        collections: CollectionSummary[];
      }
    >
  >(new Map());

  const invalidateConnCache = (connId?: string) => {
    if (!connId) {
      connDatabasesCacheRef.current.clear();
      dbChildrenCacheRef.current.clear();
      return;
    }
    connDatabasesCacheRef.current.delete(connId);
    for (const k of Array.from(dbChildrenCacheRef.current.keys())) {
      if (k.startsWith(`${connId}::`)) {
        dbChildrenCacheRef.current.delete(k);
      }
    }
  };

  // Virtual scroll state for large schemas (hundreds to thousands of tables)
  const treeScrollRef = useRef<HTMLDivElement | null>(null);
  const scrollRafRef = useRef(0);
  const [scrollTop, setScrollTop] = useState(0);
  const [viewportH, setViewportH] = useState(420);

  const activeConn = connections.find((c) => c.id === activeConnId);
  const activeDriver = drivers.find((d) => d.type === activeConn?.type);
  const supportsDbUsers = Boolean(activeConn && !['sqlite', 'duckdb'].includes(activeConn.type));

  useEffect(() => {
    const el = treeScrollRef.current;
    if (!el) return;
    const measure = () => {
      const h = el.clientHeight || 420;
      setViewportH((prev) => (prev === h ? prev : h));
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => {
      ro.disconnect();
      if (scrollRafRef.current) {
        window.cancelAnimationFrame(scrollRafRef.current);
      }
    };
  }, []);

  useEffect(() => {
    if (drivers.length === 0) return;
    if (!activeConnId && connections.length > 0) {
      handleSelectConnection(connections[0]);
    } else if (activeConnId && !connections.some((c) => c.id === activeConnId)) {
      if (connections.length > 0) {
        handleSelectConnection(connections[0]);
      } else {
        setActiveConnId('');
      }
    }
  }, [connections, drivers.length]);

  useEffect(() => {
    if (prevRefreshTickRef.current === treeRefreshTick) return;
    prevRefreshTickRef.current = treeRefreshTick;
    if (activeConn && selectedDB) {
      invalidateConnCache(activeConn.id);
      loadDatabaseChildren(activeConn, selectedDB, selectedSchema, undefined, true);
    }
  }, [treeRefreshTick]);

  // Reset scroll offset when filter or database changes
  useEffect(() => {
    if (treeScrollRef.current) {
      treeScrollRef.current.scrollTop = 0;
    }
    setScrollTop(0);
  }, [activeConnId, selectedDB, selectedSchema, deferredSearch]);

  const loadDatabaseChildren = async (
    conn: SavedConnection,
    dbName: string,
    preferredSchema: string,
    parentSeq?: number,
    forceReload = false
  ) => {
    const drv = drivers.find((d) => d.type === conn.type);
    if (!drv || drv.category === 'keyvalue') return;

    const cacheKey = `${conn.id}::${dbName}::${drv.supportsSchemas ? preferredSchema || '__default__' : ''}`;
    if (!forceReload) {
      const cached = dbChildrenCacheRef.current.get(cacheKey);
      if (cached) {
        setSchemas(cached.schemas);
        setSelectedSchema(cached.selectedSchema);
        setTables(cached.tables);
        setViews(cached.views);
        setRoutines(cached.routines);
        setTriggers(cached.triggers);
        setCollections(cached.collections);
        const existingConnEntry = connDatabasesCacheRef.current.get(conn.id);
        if (existingConnEntry) {
          existingConnEntry.lastDB = dbName;
          existingConnEntry.lastSchema = cached.selectedSchema;
        }
        return;
      }
    }

    const seq = parentSeq ?? ++reqSeqRef.current;
    setLoadingMeta(true);
    try {
      if (drv.category === 'document') {
        const colls = await api.docListCollections(conn.id, dbName);
        if (seq !== reqSeqRef.current) return;
        setCollections(colls);
        dbChildrenCacheRef.current.set(cacheKey, {
          schemas: [],
          selectedSchema: '',
          tables: [],
          views: [],
          routines: [],
          triggers: [],
          collections: colls,
        });
        return;
      }

      let schemaToUse = preferredSchema;
      let scList: string[] = [];
      if (drv.supportsSchemas) {
        scList = await api.listSchemas(conn.id, dbName);
        if (seq !== reqSeqRef.current) return;
        setSchemas(scList);
        if (!schemaToUse || !scList.includes(schemaToUse)) {
          schemaToUse = scList[0] || '';
        }
        setSelectedSchema(schemaToUse);
      } else {
        setSchemas([]);
        setSelectedSchema('');
        schemaToUse = '';
      }

      const objs = await api.listObjects(conn.id, dbName, schemaToUse);
      if (seq !== reqSeqRef.current) return;
      const nextTables = objs.tables || [];
      const nextViews = objs.views || [];
      const nextRoutines = objs.routines || [];
      const nextTriggers = objs.triggers || [];
      setTables(nextTables);
      setViews(nextViews);
      setRoutines(nextRoutines);
      setTriggers(nextTriggers);

      const snapshot = {
        schemas: scList,
        selectedSchema: schemaToUse,
        tables: nextTables,
        views: nextViews,
        routines: nextRoutines,
        triggers: nextTriggers,
        collections: [],
      };
      dbChildrenCacheRef.current.set(cacheKey, snapshot);
      if (drv.supportsSchemas && schemaToUse) {
        dbChildrenCacheRef.current.set(`${conn.id}::${dbName}::${schemaToUse}`, snapshot);
      }
      const existingConnEntry = connDatabasesCacheRef.current.get(conn.id);
      if (existingConnEntry) {
        existingConnEntry.lastDB = dbName;
        existingConnEntry.lastSchema = schemaToUse;
      }
    } catch (err: any) {
      if (seq === reqSeqRef.current) {
        message.error(err.message);
      }
    } finally {
      if (seq === reqSeqRef.current) {
        setLoadingMeta(false);
      }
    }
  };

  const handleSelectConnection = async (conn: SavedConnection, forceReload = false) => {
    const drv = drivers.find((d) => d.type === conn.type);

    // Avoid redundant reload when clicking an already-active & loaded connection row
    if (!forceReload && conn.id === activeConnId && databases.length > 0) {
      if (drv?.category === 'keyvalue') {
        openTab({
          key: `redis:${conn.id}`,
          title: `${conn.name} [Redis]`,
          kind: 'redis_browser',
          connectionId: conn.id,
          connName: conn.name,
          dbType: conn.type,
          database: selectedDB || (conn.database ? `db${conn.database}` : 'db0'),
        });
        onItemOpened?.();
      }
      return;
    }

    if (forceReload) {
      invalidateConnCache(conn.id);
    } else {
      const cachedConn = connDatabasesCacheRef.current.get(conn.id);
      if (cachedConn && cachedConn.databases.length > 0) {
        setActiveConnId(conn.id);
        setDatabases(cachedConn.databases);
        setSelectedDB(cachedConn.lastDB);
        if (drv?.category === 'keyvalue') {
          openTab({
            key: `redis:${conn.id}`,
            title: `${conn.name} [Redis]`,
            kind: 'redis_browser',
            connectionId: conn.id,
            connName: conn.name,
            dbType: conn.type,
            database: cachedConn.lastDB || 'db0',
          });
          onItemOpened?.();
          return;
        }
        if (cachedConn.lastDB) {
          await loadDatabaseChildren(conn, cachedConn.lastDB, cachedConn.lastSchema, undefined, false);
        }
        return;
      }
    }

    const seq = ++reqSeqRef.current;
    setActiveConnId(conn.id);
    setLoadingMeta(true);
    setDatabases([]);
    setTables([]);
    setViews([]);
    setRoutines([]);
    setTriggers([]);
    setCollections([]);
    setSchemas([]);

    try {
      if (drv?.category === 'keyvalue') {
        const dbs = await api.listDatabases(conn.id);
        if (seq !== reqSeqRef.current) return;
        setDatabases(dbs);
        const initDB = conn.database ? `db${conn.database}` : 'db0';
        setSelectedDB(initDB);
        connDatabasesCacheRef.current.set(conn.id, { databases: dbs, lastDB: initDB, lastSchema: '' });
        openTab({
          key: `redis:${conn.id}`,
          title: `${conn.name} [Redis]`,
          kind: 'redis_browser',
          connectionId: conn.id,
          connName: conn.name,
          dbType: conn.type,
          database: initDB,
        });
        onItemOpened?.();
        return;
      }

      const dbs = await api.listDatabases(conn.id);
      if (seq !== reqSeqRef.current) return;
      setDatabases(dbs);
      const targetDB =
        (conn.database && dbs.some((d) => d.name === conn.database) ? conn.database : dbs[0]?.name) || '';
      setSelectedDB(targetDB);
      connDatabasesCacheRef.current.set(conn.id, { databases: dbs, lastDB: targetDB, lastSchema: '' });
      if (targetDB) {
        await loadDatabaseChildren(conn, targetDB, '', seq, forceReload);
      }
    } catch (err: any) {
      if (seq === reqSeqRef.current) {
        message.error(`${conn.name}: ${err.message}`);
      }
    } finally {
      if (seq === reqSeqRef.current) {
        setLoadingMeta(false);
      }
    }
  };

  const handleDBChange = async (dbName: string) => {
    setSelectedDB(dbName);
    if (!activeConn) return;
    if (activeDriver?.category === 'keyvalue') {
      openTab({
        key: `redis:${activeConn.id}`,
        title: `${activeConn.name} [Redis]`,
        kind: 'redis_browser',
        connectionId: activeConn.id,
        connName: activeConn.name,
        dbType: activeConn.type,
        database: dbName,
      });
      onItemOpened?.();
      return;
    }
    await loadDatabaseChildren(activeConn, dbName, '');
    if (activeDriver?.category === 'relational' || activeDriver?.category === 'olap') {
      openTab({
        key: `overview:${activeConn.id}:${dbName}`,
        title: `${dbName} (${t('Overview')})`,
        kind: 'overview',
        connectionId: activeConn.id,
        connName: activeConn.name,
        dbType: activeConn.type,
        database: dbName,
        schema: selectedSchema,
      });
    }
  };

  const handleSchemaChange = async (sc: string) => {
    setSelectedSchema(sc);
    if (activeConn && selectedDB) {
      await loadDatabaseChildren(activeConn, selectedDB, sc);
    }
  };

  const handlePingConn = async (conn: SavedConnection) => {
    try {
      const info = await api.pingConnection(conn.id);
      message.success(`${conn.name}: ${info.version || 'OK'} (${info.latencyMs} ms)`);
    } catch (err: any) {
      message.error(`${conn.name}: ${err.message}`);
    }
  };

  const handleDisconnectPool = async (conn: SavedConnection) => {
    try {
      await api.disconnectConnection(conn.id);
      message.success(t('Connection pool released'));
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleDuplicateConn = async (conn: SavedConnection) => {
    try {
      await api.duplicateConnection(conn.id);
      await reloadConnections();
      message.success(t('Connection duplicated'));
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleExportConnections = async () => {
    try {
      const bundle = await api.exportConnections();
      const blob = new Blob([JSON.stringify(bundle, null, 2)], { type: 'application/json;charset=utf-8' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `dbharbor-connections-${new Date().toISOString().slice(0, 10)}.json`;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      URL.revokeObjectURL(url);
      message.success(t('Exported {count} connection profile(s)', { count: bundle.connections.length }));
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleImportConnectionsFile = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = '';
    if (!file) return;
    try {
      const raw = await file.text();
      const parsed = JSON.parse(raw);
      const list: ExportedConnectionProfile[] = Array.isArray(parsed)
        ? parsed
        : Array.isArray(parsed?.connections)
          ? parsed.connections
          : [];
      if (list.length === 0) {
        message.warning(t('No valid connection profiles found in JSON file'));
        return;
      }
      const res = await api.importConnections(list);
      await reloadConnections();
      message.success(t('Imported {count} connection profile(s)', { count: res.imported }));
    } catch (err: any) {
      message.error(err.message || t('Failed to import connections JSON'));
    }
  };

  const handleDeleteConn = async (conn: SavedConnection) => {
    invalidateConnCache(conn.id);
    await api.deleteConnection(conn.id);
    closeConnectionTabs(conn.id);
    if (activeConnId === conn.id) {
      setActiveConnId('');
      setDatabases([]);
      setTables([]);
      setViews([]);
      setRoutines([]);
      setTriggers([]);
      setCollections([]);
    }
    await reloadConnections();
    message.success(t('Connection deleted'));
  };

  const confirmDeleteConn = (conn: SavedConnection) => {
    Modal.confirm({
      title: t('Delete this connection?'),
      content: conn.name,
      okText: t('Delete Connection'),
      okButtonProps: { danger: true },
      onOk: () => handleDeleteConn(conn),
    });
  };

  const handleCreateDB = async () => {
    if (!activeConn || !newDBName.trim()) return;
    try {
      const created = newDBName.trim();
      await api.createDatabase(activeConn.id, created);
      message.success(t('Database {name} created successfully', { name: created }));
      setNewDBModalOpen(false);
      setNewDBName('');
      await handleSelectConnection(activeConn, true);
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleDropDB = async () => {
    if (!activeConn || !selectedDB) return;
    if (dropDBConfirmInput.trim() !== selectedDB) {
      message.warning(t('Please enter the exact database name to confirm'));
      return;
    }
    try {
      const target = selectedDB;
      await api.dropDatabase(activeConn.id, target);
      message.success(t('Database {name} dropped', { name: target }));
      setDropDBModalOpen(false);
      setDropDBConfirmInput('');
      await handleSelectConnection(activeConn, true);
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleCreateSchema = async () => {
    if (!activeConn || !selectedDB || !newSchemaName.trim()) return;
    try {
      const sc = newSchemaName.trim();
      await api.createSchema(activeConn.id, selectedDB, sc);
      message.success(t('Schema {name} created', { name: sc }));
      setNewSchemaModalOpen(false);
      setNewSchemaName('');
      invalidateConnCache(activeConn.id);
      await loadDatabaseChildren(activeConn, selectedDB, sc, undefined, true);
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleDropSchema = async () => {
    if (!activeConn || !selectedDB || !selectedSchema) return;
    try {
      const sc = selectedSchema;
      await api.dropSchema(activeConn.id, selectedDB, sc, dropSchemaCascade);
      message.success(t('Schema {name} dropped', { name: sc }));
      setDropSchemaModalOpen(false);
      setDropSchemaCascade(false);
      invalidateConnCache(activeConn.id);
      await loadDatabaseChildren(activeConn, selectedDB, '', undefined, true);
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const openDbUsersTab = (conn: SavedConnection) => {
    openTab({
      key: `db_users:${conn.id}`,
      title: `${conn.name} (${t('Users & Grants')})`,
      kind: 'db_users',
      connectionId: conn.id,
      connName: conn.name,
      dbType: conn.type,
      database: conn.id === activeConnId ? selectedDB : conn.database || '',
      schema: conn.id === activeConnId ? selectedSchema : '',
    });
    onItemOpened?.();
  };

  const openObjectDDLInspector = async (objectType: string, objectName: string) => {
    if (!activeConn) return;
    setDdlModal({
      open: true,
      loading: true,
      saving: false,
      isNewView: false,
      objectType,
      objectName,
      sql: '',
    });
    try {
      const res = await api.getObjectDDL(activeConn.id, selectedDB, selectedSchema, objectType, objectName);
      setDdlModal((prev) => ({ ...prev, loading: false, sql: res.ddl || '' }));
    } catch (err: any) {
      setDdlModal((prev) => ({
        ...prev,
        loading: false,
        sql: `-- ${err.message || 'DDL unavailable'}`,
      }));
    }
  };

  const openNewViewModal = () => {
    setDdlModal({
      open: true,
      loading: false,
      saving: false,
      isNewView: true,
      objectType: 'VIEW',
      objectName: '',
      sql: 'SELECT 1 AS id;',
    });
  };

  const handleSaveViewDDL = async () => {
    if (!activeConn || !selectedDB) return;
    const vName = ddlModal.objectName.trim();
    const vSql = ddlModal.sql.trim();
    if (!vName || !vSql) {
      message.warning(t('View name and SQL definition are required'));
      return;
    }
    setDdlModal((prev) => ({ ...prev, saving: true }));
    try {
      await api.objectAction(activeConn.id, {
        database: selectedDB,
        schema: selectedSchema,
        objectName: vName,
        objectType: 'VIEW',
        action: 'CREATE_VIEW',
        viewSql: vSql,
      });
      message.success(t('View {name} saved successfully', { name: vName }));
      setDdlModal((prev) => ({ ...prev, open: false, saving: false }));
      bumpTreeRefresh();
    } catch (err: any) {
      setDdlModal((prev) => ({ ...prev, saving: false }));
      message.error(err.message);
    }
  };

  const handleObjectMaintenance = (
    objName: string,
    objType: 'TABLE' | 'VIEW' | 'TRIGGER' | 'PROCEDURE' | 'FUNCTION',
    action: 'TRUNCATE' | 'DROP'
  ) => {
    if (!activeConn || !selectedDB || !canWrite) return;
    const connRef = activeConn;
    const dbRef = selectedDB;
    const scRef = selectedSchema;
    Modal.confirm({
      title:
        action === 'TRUNCATE'
          ? t('Truncate all rows in table {name}?', { name: objName })
          : t('Danger: Permanently drop {name}?', { name: objName }),
      okButtonProps: { danger: true },
      onOk: async () => {
        try {
          await api.objectAction(connRef.id, {
            database: dbRef,
            schema: scRef,
            objectName: objName,
            objectType: objType,
            action,
          });
          message.success(t('Executed {action} on {name}', { action, name: objName }));
          bumpTreeRefresh();
        } catch (err: any) {
          message.error(err.message);
        }
      },
    });
  };

  const buildConnMenuItems = (conn: SavedConnection) => {
    const canEditThisConn = isDesktop || conn.editable !== false;
    const items: any[] = [
      {
        key: 'ping',
        icon: <ApiOutlined />,
        label: t('Test Connection'),
        onClick: () => handlePingConn(conn),
      },
      ...(canEditThisConn
        ? [
            {
              key: 'edit',
              icon: <EditOutlined />,
              label: t('Edit Connection'),
              onClick: () => onEditConnection(conn),
            },
          ]
        : []),
      ...(effectiveCanCreateConn && canEditThisConn
        ? [
            {
              key: 'duplicate',
              icon: <CopyOutlined />,
              label: t('Duplicate Connection'),
              onClick: () => handleDuplicateConn(conn),
            },
          ]
        : []),
      ...(!['sqlite', 'duckdb'].includes(conn.type)
        ? [
            {
              key: 'db-users',
              icon: <TeamOutlined />,
              label: t('Users & Grants'),
              onClick: () => openDbUsersTab(conn),
            },
          ]
        : []),
      {
        key: 'monitor',
        icon: <DashboardOutlined />,
        label: t('Live Monitor'),
        onClick: () => {
          openTab({
            key: `monitor:${conn.id}`,
            title: `${conn.name} (${t('Monitor')})`,
            kind: 'monitor',
            connectionId: conn.id,
            connName: conn.name,
            dbType: conn.type,
            database: conn.id === activeConnId ? selectedDB : conn.database || '',
          });
          onItemOpened?.();
        },
      },
      {
        key: 'disconnect',
        icon: <DisconnectOutlined />,
        label: t('Disconnect Pool'),
        onClick: () => handleDisconnectPool(conn),
      },
    ];
    if (canEditThisConn) {
      items.push(
        { type: 'divider' as const },
        {
          key: 'delete',
          danger: true,
          icon: <DeleteOutlined />,
          label: t('Delete Connection'),
          onClick: () => confirmDeleteConn(conn),
        }
      );
    }
    return items;
  };

  const buildObjectMenuItems = (name: string, kind: 'TABLE' | 'VIEW') => {
    if (!activeConn) return [];
    const conn = activeConn;
    const items: any[] = [
      {
        key: 'browse',
        icon: <TableOutlined />,
        label: t('Browse Data'),
        onClick: () => {
          openTab({
            key: `data:${conn.id}:${selectedDB}:${selectedSchema}:${name}`,
            title: kind === 'VIEW' ? `${name} (${t('View')})` : name,
            kind: 'table_data',
            connectionId: conn.id,
            connName: conn.name,
            dbType: conn.type,
            database: selectedDB,
            schema: selectedSchema,
            table: name,
          });
          onItemOpened?.();
        },
      },
      ...(kind === 'TABLE'
        ? [
            {
              key: 'structure',
              icon: <EditOutlined />,
              label: t('Table Structure'),
              onClick: () => {
                openTab({
                  key: `struct:${conn.id}:${selectedDB}:${selectedSchema}:${name}`,
                  title: `${name} [${t('Structure')}]`,
                  kind: 'table_structure',
                  connectionId: conn.id,
                  connName: conn.name,
                  dbType: conn.type,
                  database: selectedDB,
                  schema: selectedSchema,
                  table: name,
                });
                onItemOpened?.();
              },
            },
          ]
        : [
            {
              key: 'view-ddl',
              icon: <EditOutlined />,
              label: canWrite ? t('Edit View SQL') : t('Inspect DDL'),
              onClick: () => openObjectDDLInspector('VIEW', name),
            },
          ]),
      {
        key: 'sql',
        icon: <CodeOutlined />,
        label: t('Custom SQL'),
        onClick: () => {
          openTab({
            key: `sql:${conn.id}:${selectedDB}:${name}`,
            title: `SQL - ${name}`,
            kind: 'sql_console',
            connectionId: conn.id,
            connName: conn.name,
            dbType: conn.type,
            database: selectedDB,
            schema: selectedSchema,
            table: name,
            initialSQL: `SELECT * FROM ${name} LIMIT 100;`,
          });
          onItemOpened?.();
        },
      },
      ...(onOpenTransfer
        ? [
            {
              key: 'export',
              icon: <DownloadOutlined />,
              label: t('Export Table'),
              onClick: () => onOpenTransfer(conn, selectedDB, selectedSchema, 'export', name),
            },
          ]
        : []),
      {
        key: 'copy-name',
        icon: <CopyOutlined />,
        label: t('Copy Name'),
        onClick: () => {
          writeTextToClipboard(name);
          message.success(t('Copied {name}', { name }));
        },
      },
    ];

    if (canWrite) {
      items.push({ type: 'divider' as const });
      if (kind === 'TABLE') {
        items.push({
          key: 'truncate',
          danger: true,
          icon: <ClearOutlined />,
          label: t('Truncate table'),
          onClick: () => handleObjectMaintenance(name, 'TABLE', 'TRUNCATE'),
        });
      }
      items.push({
        key: 'drop',
        danger: true,
        icon: <DeleteOutlined />,
        label: t('Drop object'),
        onClick: () => handleObjectMaintenance(name, kind, 'DROP'),
      });
    }
    return items;
  };

  const connQLower = connSearch.trim().toLowerCase();
  const filteredConnections = useMemo(
    () =>
      connQLower
        ? connections.filter(
            (c) => c.name.toLowerCase().includes(connQLower) || c.type.toLowerCase().includes(connQLower)
          )
        : connections,
    [connections, connQLower]
  );

  const qLower = deferredSearch.trim().toLowerCase();
  const filteredTables = useMemo(
    () => (qLower ? tables.filter((tbl) => tbl.name.toLowerCase().includes(qLower)) : tables),
    [tables, qLower]
  );
  const filteredViews = useMemo(
    () => (qLower ? views.filter((v) => v.name.toLowerCase().includes(qLower)) : views),
    [views, qLower]
  );
  const filteredRoutines = useMemo(
    () => (qLower ? routines.filter((r) => r.name.toLowerCase().includes(qLower)) : routines),
    [routines, qLower]
  );
  const filteredTriggers = useMemo(
    () => (qLower ? triggers.filter((tr) => tr.name.toLowerCase().includes(qLower)) : triggers),
    [triggers, qLower]
  );
  const filteredColls = useMemo(
    () => (qLower ? collections.filter((c) => c.name.toLowerCase().includes(qLower)) : collections),
    [collections, qLower]
  );

  // Compute virtual window slice for large table / view / collection lists (> 60 items)
  const useVirtualTables = !collapsed.tables && filteredTables.length > VIRTUAL_THRESHOLD;
  const tableStartIdx = useVirtualTables
    ? Math.max(0, Math.floor(scrollTop / itemRowHeight) - OVERSCAN_ROWS)
    : 0;
  const tableEndIdx = useVirtualTables
    ? Math.min(
        filteredTables.length,
        Math.ceil((scrollTop + viewportH) / itemRowHeight) + OVERSCAN_ROWS
      )
    : filteredTables.length;
  const visibleTableSlice = useVirtualTables
    ? filteredTables.slice(tableStartIdx, tableEndIdx)
    : filteredTables;
  const tableTopPad = useVirtualTables ? tableStartIdx * itemRowHeight : 0;
  const tableBottomPad = useVirtualTables ? (filteredTables.length - tableEndIdx) * itemRowHeight : 0;

  const viewsOffsetPx = collapsed.tables ? 34 : filteredTables.length * itemRowHeight + 44;
  const relViewsScrollTop = Math.max(0, scrollTop - viewsOffsetPx);
  const useVirtualViews = !collapsed.views && filteredViews.length > VIRTUAL_THRESHOLD;
  const viewStartIdx = useVirtualViews
    ? Math.max(0, Math.floor(relViewsScrollTop / itemRowHeight) - OVERSCAN_ROWS)
    : 0;
  const viewEndIdx = useVirtualViews
    ? Math.min(
        filteredViews.length,
        Math.ceil((relViewsScrollTop + viewportH) / itemRowHeight) + OVERSCAN_ROWS
      )
    : filteredViews.length;
  const visibleViewSlice = useVirtualViews ? filteredViews.slice(viewStartIdx, viewEndIdx) : filteredViews;
  const viewTopPad = useVirtualViews ? viewStartIdx * itemRowHeight : 0;
  const viewBottomPad = useVirtualViews ? (filteredViews.length - viewEndIdx) * itemRowHeight : 0;

  const useVirtualColls = !collapsed.collections && filteredColls.length > VIRTUAL_THRESHOLD;
  const collStartIdx = useVirtualColls
    ? Math.max(0, Math.floor(scrollTop / itemRowHeight) - OVERSCAN_ROWS)
    : 0;
  const collEndIdx = useVirtualColls
    ? Math.min(
        filteredColls.length,
        Math.ceil((scrollTop + viewportH) / itemRowHeight) + OVERSCAN_ROWS
      )
    : filteredColls.length;
  const visibleCollSlice = useVirtualColls ? filteredColls.slice(collStartIdx, collEndIdx) : filteredColls;
  const collTopPad = useVirtualColls ? collStartIdx * itemRowHeight : 0;
  const collBottomPad = useVirtualColls ? (filteredColls.length - collEndIdx) * itemRowHeight : 0;

  const renderSectionHeader = (
    group: keyof typeof collapsed,
    label: string,
    extra?: React.ReactNode
  ) => {
    const isClosed = collapsed[group];
    return (
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          minHeight: isMobile ? 36 : 26,
          marginBottom: isClosed ? 2 : 4,
          padding: isMobile ? '4px 6px' : '2px 4px',
          borderRadius: 6,
          cursor: 'pointer',
          userSelect: 'none',
        }}
        className="sidebar-item-row"
        onClick={() => toggleGroup(group)}
      >
        <Space size={6} style={{ minWidth: 0 }}>
          {isClosed ? (
            <RightOutlined style={{ fontSize: 10, opacity: 0.65 }} />
          ) : (
            <DownOutlined style={{ fontSize: 10, opacity: 0.65 }} />
          )}
          <Typography.Text type="secondary" strong style={{ fontSize: 12 }}>
            {label}
          </Typography.Text>
        </Space>
        {extra && <div onClick={(e) => e.stopPropagation()}>{extra}</div>}
      </div>
    );
  };

  return (
    <div style={{ height: '100%', display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
      <input
        ref={importInputRef}
        type="file"
        accept=".json,application/json"
        style={{ display: 'none' }}
        onChange={handleImportConnectionsFile}
      />

      {/* Connection Switcher Header */}
      <div style={{ padding: '10px 12px', borderBottom: '1px solid rgba(128,128,128,0.15)', flexShrink: 0 }}>
        <div
          style={{
            display: 'flex',
            justifyContent: 'space-between',
            alignItems: 'center',
            marginBottom: collapsed.connections ? 0 : 8,
          }}
        >
          <div
            onClick={() => toggleGroup('connections')}
            style={{ display: 'inline-flex', alignItems: 'center', gap: 6, cursor: 'pointer', userSelect: 'none' }}
          >
            {collapsed.connections ? (
              <RightOutlined style={{ fontSize: 10, opacity: 0.65 }} />
            ) : (
              <DownOutlined style={{ fontSize: 10, opacity: 0.65 }} />
            )}
            <Typography.Text strong style={{ fontSize: 12, textTransform: 'uppercase', letterSpacing: 0.5 }}>
              {t('Connections ({count})', { count: connections.length })}
            </Typography.Text>
          </div>
          <Space size={4}>
            <Dropdown
              trigger={['click']}
              placement="bottomRight"
              menu={{
                items: [
                  {
                    key: 'export-conns',
                    icon: <DownloadOutlined />,
                    label: t('Export Connections (.json)'),
                    disabled: connections.length === 0,
                    onClick: handleExportConnections,
                  },
                  ...(effectiveCanCreateConn
                    ? [
                        {
                          key: 'import-conns',
                          icon: <UploadOutlined />,
                          label: t('Import Connections (.json)'),
                          onClick: () => importInputRef.current?.click(),
                        },
                      ]
                    : []),
                ],
              }}
            >
              <Button size="small" type="text" icon={<MoreOutlined />} />
            </Dropdown>
            {effectiveCanCreateConn && (
              <Button type="primary" size="small" icon={<PlusOutlined />} onClick={onNewConnection}>
                {t('New')}
              </Button>
            )}
          </Space>
        </div>

        {!collapsed.connections && (
          <>
            {connections.length > 4 && (
              <Input
                size="small"
                allowClear
                prefix={<SearchOutlined />}
                placeholder={t('Filter connections...')}
                value={connSearch}
                onChange={(e) => setConnSearch(e.target.value)}
                style={{ marginBottom: 6 }}
              />
            )}

            <div style={{ maxHeight: isMobile ? 184 : 148, overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: isMobile ? 6 : 4 }}>
              {filteredConnections.map((conn) => {
                const isSelected = conn.id === activeConnId;
                const badge = DB_SHORT_LABELS[conn.type] || { code: 'DB', color: conn.color };
                const connMenu = { items: buildConnMenuItems(conn) };
                return (
                  <Dropdown key={conn.id} trigger={['contextMenu']} menu={connMenu}>
                    <div
                      onClick={() => handleSelectConnection(conn, false)}
                      style={{
                        minHeight: isMobile ? 42 : 34,
                        padding: isMobile ? '6px 10px' : '5px 8px',
                        borderRadius: 8,
                        cursor: 'pointer',
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'space-between',
                        background: isSelected ? 'rgba(22,119,255,0.12)' : 'transparent',
                        border: isSelected ? `1px solid ${conn.color || '#1677ff'}` : '1px solid transparent',
                      }}
                    >
                      <div
                        style={{
                          display: 'flex',
                          alignItems: 'center',
                          gap: 8,
                          flex: 1,
                          minWidth: 0,
                          overflow: 'hidden',
                        }}
                      >
                        <span
                          style={{
                            width: 22,
                            height: 22,
                            borderRadius: 5,
                            background: conn.color || badge.color,
                            color: '#fff',
                            fontSize: 11,
                            fontWeight: 700,
                            display: 'inline-flex',
                            alignItems: 'center',
                            justifyContent: 'center',
                            flexShrink: 0,
                          }}
                        >
                          {badge.code}
                        </span>
                        <Typography.Text
                          ellipsis
                          strong={isSelected}
                          style={{ flex: 1, minWidth: 0, fontSize: 13 }}
                          title={conn.name}
                        >
                          {conn.name}
                        </Typography.Text>
                        {!isDesktop && conn.visibility === 'private' && (
                          <Tooltip title={t('Personal Private Connection')}>
                            <LockOutlined style={{ fontSize: 11, opacity: 0.55, flexShrink: 0 }} />
                          </Tooltip>
                        )}
                      </div>

                      <div style={{ flexShrink: 0 }} onClick={(e) => e.stopPropagation()}>
                        <Dropdown menu={connMenu} trigger={['click']} placement="bottomRight">
                          <Button
                            type="text"
                            size={isMobile ? 'middle' : 'small'}
                            style={isMobile ? { width: 34, height: 34 } : undefined}
                            icon={<MoreOutlined />}
                          />
                        </Dropdown>
                      </div>
                    </div>
                  </Dropdown>
                );
              })}
            </div>
          </>
        )}
      </div>

      {/* Active Connection Database & Quick Actions */}
      {activeConn && (
        <div style={{ padding: '8px 12px', borderBottom: '1px solid rgba(128,128,128,0.15)', flexShrink: 0 }}>
          <Space.Compact
            size={isMobile ? 'middle' : 'small'}
            style={{ width: '100%', marginBottom: activeDriver?.supportsSchemas ? 6 : 8 }}
          >
            <Select
              style={{ flex: 1, minWidth: 0 }}
              size={isMobile ? 'middle' : 'small'}
              placeholder={t('Select Database')}
              value={selectedDB || undefined}
              onChange={handleDBChange}
              showSearch
              options={databases.map((d) => ({
                value: d.name,
                label: `${d.name}${d.tablesCount ? ` (${d.tablesCount})` : ''}`,
              }))}
            />
            <Button
              size={isMobile ? 'middle' : 'small'}
              icon={<ReloadOutlined />}
              title={t('Refresh Metadata')}
              onClick={() => handleSelectConnection(activeConn, true)}
            />
            {canWrite &&
              activeDriver?.category !== 'keyvalue' &&
              activeConn.type !== 'sqlite' &&
              activeConn.type !== 'duckdb' && (
                <Dropdown
                  trigger={['click']}
                  placement="bottomRight"
                  menu={{
                    items: [
                      {
                        key: 'create-db',
                        icon: <PlusOutlined />,
                        label: t('Create Database'),
                        onClick: () => setNewDBModalOpen(true),
                      },
                      ...(selectedDB
                        ? [
                            { type: 'divider' as const },
                            {
                              key: 'drop-db',
                              danger: true,
                              icon: <DeleteOutlined />,
                              label: `${t('Drop Database')} (${selectedDB})`,
                              onClick: () => {
                                setDropDBConfirmInput('');
                                setDropDBModalOpen(true);
                              },
                            },
                          ]
                        : []),
                    ],
                  }}
                >
                  <Button size={isMobile ? 'middle' : 'small'} icon={<MoreOutlined />} title={t('Database Actions')} />
                </Dropdown>
              )}
          </Space.Compact>

          {activeDriver?.supportsSchemas && selectedDB && (
            <Space.Compact size={isMobile ? 'middle' : 'small'} style={{ width: '100%', marginBottom: 8 }}>
              <Select
                style={{ flex: 1, minWidth: 0 }}
                size={isMobile ? 'middle' : 'small'}
                value={selectedSchema || undefined}
                onChange={handleSchemaChange}
                options={schemas.map((s) => ({ value: s, label: `Schema: ${s}` }))}
              />
              {canWrite && (
                <Dropdown
                  trigger={['click']}
                  placement="bottomRight"
                  menu={{
                    items: [
                      {
                        key: 'create-schema',
                        icon: <PlusOutlined />,
                        label: t('Create Schema'),
                        onClick: () => setNewSchemaModalOpen(true),
                      },
                      ...(selectedSchema && selectedSchema !== 'public' && selectedSchema !== 'dbo'
                        ? [
                            { type: 'divider' as const },
                            {
                              key: 'drop-schema',
                              danger: true,
                              icon: <DeleteOutlined />,
                              label: `${t('Drop Schema')} (${selectedSchema})`,
                              onClick: () => setDropSchemaModalOpen(true),
                            },
                          ]
                        : []),
                    ],
                  }}
                >
                  <Button size={isMobile ? 'middle' : 'small'} icon={<MoreOutlined />} title={t('Schema Actions')} />
                </Dropdown>
              )}
            </Space.Compact>
          )}

          <Space size={isMobile ? 6 : 4} wrap style={{ width: '100%' }}>
            {(activeDriver?.category === 'relational' || activeDriver?.category === 'olap') && (
              <>
                <Button
                  size={isMobile ? 'middle' : 'small'}
                  icon={<FolderOpenOutlined />}
                  disabled={!selectedDB}
                  onClick={() => {
                    openTab({
                      key: `overview:${activeConn.id}:${selectedDB}:${selectedSchema}`,
                      title: `${selectedDB} (${t('Overview')})`,
                      kind: 'overview',
                      connectionId: activeConn.id,
                      connName: activeConn.name,
                      dbType: activeConn.type,
                      database: selectedDB,
                      schema: selectedSchema,
                    });
                    onItemOpened?.();
                  }}
                >
                  {t('Overview')}
                </Button>
                <Button
                  size={isMobile ? 'middle' : 'small'}
                  type="primary"
                  ghost
                  icon={<CodeOutlined />}
                  onClick={() => {
                    openTab({
                      key: `sql:${activeConn.id}:${Date.now()}`,
                      title: `SQL - ${selectedDB || activeConn.name}`,
                      kind: 'sql_console',
                      connectionId: activeConn.id,
                      connName: activeConn.name,
                      dbType: activeConn.type,
                      database: selectedDB,
                      schema: selectedSchema,
                    });
                    onItemOpened?.();
                  }}
                >
                  {t('SQL Console')}
                </Button>
              </>
            )}
            {activeDriver?.category === 'keyvalue' && (
              <Button
                size={isMobile ? 'middle' : 'small'}
                type="primary"
                ghost
                icon={<KeyOutlined />}
                onClick={() => {
                  openTab({
                    key: `redis:${activeConn.id}`,
                    title: `${activeConn.name} [Redis]`,
                    kind: 'redis_browser',
                    connectionId: activeConn.id,
                    connName: activeConn.name,
                    dbType: activeConn.type,
                    database: selectedDB || 'db0',
                  });
                  onItemOpened?.();
                }}
              >
                {t('KV Browser')}
              </Button>
            )}
            {supportsDbUsers && (
              <Button size={isMobile ? 'middle' : 'small'} icon={<TeamOutlined />} onClick={() => openDbUsersTab(activeConn)}>
                {t('Users & Grants')}
              </Button>
            )}
            <Button
              size={isMobile ? 'middle' : 'small'}
              icon={<DashboardOutlined />}
              onClick={() => {
                openTab({
                  key: `monitor:${activeConn.id}`,
                  title: `${activeConn.name} (${t('Monitor')})`,
                  kind: 'monitor',
                  connectionId: activeConn.id,
                  connName: activeConn.name,
                  dbType: activeConn.type,
                  database: selectedDB,
                });
                onItemOpened?.();
              }}
            >
              {t('Monitor')}
            </Button>
          </Space>

          {activeDriver?.category !== 'keyvalue' && (
            <Input
              size={isMobile ? 'middle' : 'small'}
              allowClear
              prefix={<SearchOutlined />}
              placeholder={t('Filter tables / views / collections...')}
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              style={{ marginTop: 8 }}
            />
          )}
        </div>
      )}

      {/* Object Tree List (Collapsible + Virtualized for large schemas) */}
      <div
        ref={treeScrollRef}
        onScroll={(e) => {
          const nextTop = e.currentTarget.scrollTop;
          if (!scrollRafRef.current) {
            scrollRafRef.current = window.requestAnimationFrame(() => {
              scrollRafRef.current = 0;
              setScrollTop(nextTop);
            });
          }
        }}
        style={{ flex: 1, minHeight: 0, overflowY: 'auto', padding: '8px 10px' }}
      >
        <Spin spinning={loadingMeta}>
          {!activeConn ? (
            <Empty description={t('Select or create a database connection')} image={Empty.PRESENTED_IMAGE_SIMPLE} />
          ) : activeDriver?.category === 'keyvalue' ? (
            <div style={{ padding: 8 }}>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {t('Redis Logical Databases (click to switch):')}
              </Typography.Text>
              <div style={{ marginTop: 8, display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 6 }}>
                {databases.map((d) => (
                  <Button
                    key={d.name}
                    size="small"
                    type={selectedDB === d.name ? 'primary' : 'default'}
                    onClick={() => handleDBChange(d.name)}
                    style={{ justifyContent: 'space-between', display: 'flex' }}
                  >
                    <span>{d.name.toUpperCase()}</span>
                    <Badge count={d.tablesCount || 0} showZero color="#8c8c8c" overflowCount={999999} />
                  </Button>
                ))}
              </div>
            </div>
          ) : activeDriver?.category === 'document' ? (
            <div>
              {renderSectionHeader('collections', t('Collections ({count})', { count: filteredColls.length }))}
              {!collapsed.collections && (
                <div style={{ paddingTop: collTopPad, paddingBottom: collBottomPad }}>
                  {visibleCollSlice.map((coll) => {
                    const collMenuItems = [
                      {
                        key: 'open',
                        icon: <DatabaseOutlined />,
                        label: t('Browse Data'),
                        onClick: () => {
                          openTab({
                            key: `mongo:${activeConn.id}:${selectedDB}:${coll.name}`,
                            title: coll.name,
                            kind: 'mongo_collection',
                            connectionId: activeConn.id,
                            connName: activeConn.name,
                            dbType: activeConn.type,
                            database: selectedDB,
                            table: coll.name,
                          });
                          onItemOpened?.();
                        },
                      },
                      {
                        key: 'copy',
                        icon: <CopyOutlined />,
                        label: t('Copy Name'),
                        onClick: () => {
                          writeTextToClipboard(coll.name);
                          message.success(t('Copied {name}', { name: coll.name }));
                        },
                      },
                    ];
                    return (
                      <Dropdown
                        key={coll.name}
                        trigger={['contextMenu']}
                        menu={{ items: collMenuItems }}
                      >
                        <div
                          onClick={() => {
                            openTab({
                              key: `mongo:${activeConn.id}:${selectedDB}:${coll.name}`,
                              title: `${coll.name}`,
                              kind: 'mongo_collection',
                              connectionId: activeConn.id,
                              connName: activeConn.name,
                              dbType: activeConn.type,
                              database: selectedDB,
                              table: coll.name,
                            });
                            onItemOpened?.();
                          }}
                          style={{
                            height: itemRowHeight,
                            padding: '0 8px',
                            borderRadius: 6,
                            cursor: 'pointer',
                            display: 'flex',
                            justifyContent: 'space-between',
                            alignItems: 'center',
                          }}
                          className="sidebar-item-row"
                        >
                          <div
                            style={{
                              display: 'flex',
                              alignItems: 'center',
                              gap: 6,
                              flex: 1,
                              minWidth: 0,
                              overflow: 'hidden',
                            }}
                          >
                            <DatabaseOutlined style={{ color: '#389e0d', flexShrink: 0 }} />
                            <Typography.Text ellipsis style={{ flex: 1, minWidth: 0, fontSize: 13 }} title={coll.name}>
                              {coll.name}
                            </Typography.Text>
                          </div>
                          <Space size={4} style={{ flexShrink: 0 }} onClick={(e) => e.stopPropagation()}>
                            <Tag style={{ margin: 0, fontSize: 11, flexShrink: 0 }}>{coll.count}</Tag>
                            {isMobile && (
                              <Dropdown trigger={['click']} placement="bottomRight" menu={{ items: collMenuItems }}>
                                <Button
                                  type="text"
                                  size="small"
                                  style={{ width: 30, height: 30, padding: 0 }}
                                  icon={<MoreOutlined />}
                                  onClick={(e) => e.stopPropagation()}
                                />
                              </Dropdown>
                            )}
                          </Space>
                        </div>
                      </Dropdown>
                    );
                  })}
                </div>
              )}
            </div>
          ) : (
            <>
              {/* 1. Tables Section */}
              {renderSectionHeader(
                'tables',
                t('Tables ({count})', { count: filteredTables.length }),
                selectedDB && canWrite ? (
                  <Tooltip title={t('Visual Create Table')}>
                    <Button
                      type="link"
                      size="small"
                      icon={<PlusOutlined />}
                      style={isMobile ? { padding: '0 8px', height: 30 } : { padding: '0 4px', height: 22 }}
                      onClick={() => onOpenCreateTable(activeConn, selectedDB, selectedSchema)}
                    >
                      {t('New Table')}
                    </Button>
                  </Tooltip>
                ) : undefined
              )}

              {!collapsed.tables && (
                <div style={{ paddingTop: tableTopPad, paddingBottom: tableBottomPad }}>
                  {visibleTableSlice.map((tbl) => {
                    const tableMenuItems = buildObjectMenuItems(tbl.name, 'TABLE');
                    return (
                      <Dropdown
                        key={tbl.name}
                        trigger={['contextMenu']}
                        menu={{ items: tableMenuItems }}
                      >
                        <div
                          onClick={() => {
                            openTab({
                              key: `data:${activeConn.id}:${selectedDB}:${selectedSchema}:${tbl.name}`,
                              title: tbl.name,
                              kind: 'table_data',
                              connectionId: activeConn.id,
                              connName: activeConn.name,
                              dbType: activeConn.type,
                              database: selectedDB,
                              schema: selectedSchema,
                              table: tbl.name,
                            });
                            onItemOpened?.();
                          }}
                          style={{
                            height: itemRowHeight,
                            padding: '0 8px',
                            borderRadius: 6,
                            cursor: 'pointer',
                            display: 'flex',
                            justifyContent: 'space-between',
                            alignItems: 'center',
                          }}
                          className="sidebar-item-row"
                        >
                          <div
                            style={{
                              display: 'flex',
                              alignItems: 'center',
                              gap: 6,
                              flex: 1,
                              minWidth: 0,
                              overflow: 'hidden',
                            }}
                          >
                            <TableOutlined style={{ color: '#1677ff', flexShrink: 0 }} />
                            <Typography.Text ellipsis style={{ flex: 1, minWidth: 0, fontSize: 13 }} title={tbl.name}>
                              {tbl.name}
                            </Typography.Text>
                          </div>
                          <Space size={2} style={{ flexShrink: 0 }} onClick={(e) => e.stopPropagation()}>
                            {tbl.dataBytes > 0 && (
                              <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                                {formatBytes(tbl.dataBytes)}
                              </Typography.Text>
                            )}
                            <Tooltip title={t('Table Structure')}>
                              <Button
                                type="text"
                                size="small"
                                style={isMobile ? { width: 30, height: 30, padding: 0 } : undefined}
                                icon={<EditOutlined style={{ fontSize: 12 }} />}
                                onClick={() => {
                                  openTab({
                                    key: `struct:${activeConn.id}:${selectedDB}:${selectedSchema}:${tbl.name}`,
                                    title: `${tbl.name} [${t('Structure')}]`,
                                    kind: 'table_structure',
                                    connectionId: activeConn.id,
                                    connName: activeConn.name,
                                    dbType: activeConn.type,
                                    database: selectedDB,
                                    schema: selectedSchema,
                                    table: tbl.name,
                                  });
                                  onItemOpened?.();
                                }}
                              />
                            </Tooltip>
                            {isMobile && (
                              <Dropdown trigger={['click']} placement="bottomRight" menu={{ items: tableMenuItems }}>
                                <Button
                                  type="text"
                                  size="small"
                                  style={{ width: 30, height: 30, padding: 0 }}
                                  icon={<MoreOutlined />}
                                  onClick={(e) => e.stopPropagation()}
                                />
                              </Dropdown>
                            )}
                          </Space>
                        </div>
                      </Dropdown>
                    );
                  })}
                </div>
              )}

              {/* 2. Views Section */}
              {(views.length > 0 || (selectedDB && canWrite)) && (
                <div style={{ marginTop: 8 }}>
                  {renderSectionHeader(
                    'views',
                    t('Views ({count})', { count: filteredViews.length }),
                    selectedDB && canWrite ? (
                      <Tooltip title={t('Create View')}>
                        <Button
                          type="link"
                          size="small"
                          icon={<PlusOutlined />}
                          style={isMobile ? { padding: '0 8px', height: 30 } : { padding: '0 4px', height: 22 }}
                          onClick={openNewViewModal}
                        >
                          {t('New View')}
                        </Button>
                      </Tooltip>
                    ) : undefined
                  )}
                  {!collapsed.views && filteredViews.length > 0 && (
                    <div style={{ paddingTop: viewTopPad, paddingBottom: viewBottomPad }}>
                      {visibleViewSlice.map((vw) => {
                        const viewMenuItems = buildObjectMenuItems(vw.name, 'VIEW');
                        return (
                          <Dropdown
                            key={vw.name}
                            trigger={['contextMenu']}
                            menu={{ items: viewMenuItems }}
                          >
                            <div
                              onClick={() => {
                                openTab({
                                  key: `data:${activeConn.id}:${selectedDB}:${selectedSchema}:${vw.name}`,
                                  title: `${vw.name} (${t('View')})`,
                                  kind: 'table_data',
                                  connectionId: activeConn.id,
                                  connName: activeConn.name,
                                  dbType: activeConn.type,
                                  database: selectedDB,
                                  schema: selectedSchema,
                                  table: vw.name,
                                });
                                onItemOpened?.();
                              }}
                              style={{
                                height: itemRowHeight,
                                padding: '0 8px',
                                borderRadius: 6,
                                cursor: 'pointer',
                                display: 'flex',
                                justifyContent: 'space-between',
                                alignItems: 'center',
                                gap: 6,
                              }}
                              className="sidebar-item-row"
                            >
                              <div
                                style={{
                                  display: 'flex',
                                  alignItems: 'center',
                                  gap: 6,
                                  flex: 1,
                                  minWidth: 0,
                                  overflow: 'hidden',
                                }}
                              >
                                <EyeOutlined style={{ color: '#722ed1', flexShrink: 0 }} />
                                <Typography.Text ellipsis style={{ fontSize: 13, flex: 1, minWidth: 0 }} title={vw.name}>
                                  {vw.name}
                                </Typography.Text>
                              </div>
                              <Space size={2} style={{ flexShrink: 0 }} onClick={(e) => e.stopPropagation()}>
                                <Tooltip title={canWrite ? t('Edit View SQL') : t('Inspect DDL')}>
                                  <Button
                                    type="text"
                                    size="small"
                                    style={isMobile ? { width: 30, height: 30, padding: 0 } : undefined}
                                    icon={<CodeOutlined style={{ fontSize: 12 }} />}
                                    onClick={(e) => {
                                      e.stopPropagation();
                                      openObjectDDLInspector('VIEW', vw.name);
                                    }}
                                  />
                                </Tooltip>
                                {isMobile && (
                                  <Dropdown trigger={['click']} placement="bottomRight" menu={{ items: viewMenuItems }}>
                                    <Button
                                      type="text"
                                      size="small"
                                      style={{ width: 30, height: 30, padding: 0 }}
                                      icon={<MoreOutlined />}
                                      onClick={(e) => e.stopPropagation()}
                                    />
                                  </Dropdown>
                                )}
                              </Space>
                            </div>
                          </Dropdown>
                        );
                      })}
                    </div>
                  )}
                </div>
              )}

              {/* 3. Functions & Procedures Section */}
              {(routines.length > 0 ||
                (selectedDB && canWrite && ROUTINE_CAPABLE_ENGINES.has(activeConn.type))) && (
                <div style={{ marginTop: 8 }}>
                  {renderSectionHeader(
                    'routines',
                    t('Routines ({count})', { count: filteredRoutines.length }),
                    selectedDB && canWrite && ROUTINE_CAPABLE_ENGINES.has(activeConn.type) ? (
                      <Tooltip title={t('Create Routine (Procedure / Function)')}>
                        <Button
                          type="link"
                          size="small"
                          icon={<PlusOutlined />}
                          style={isMobile ? { padding: '0 8px', height: 30 } : { padding: '0 4px', height: 22 }}
                          onClick={() =>
                            setRoutineTriggerModal({
                              open: true,
                              mode: 'routine',
                              editObject: null,
                            })
                          }
                        >
                          {t('New Routine')}
                        </Button>
                      </Tooltip>
                    ) : undefined
                  )}
                  {!collapsed.routines &&
                    filteredRoutines.map((rt) => {
                      const rtType = (rt.type || 'FUNCTION').toUpperCase() as 'FUNCTION' | 'PROCEDURE';
                      const openEditor = () =>
                        setRoutineTriggerModal({
                          open: true,
                          mode: 'routine',
                          editObject: { name: rt.name, objectType: rtType },
                        });
                      const routineMenuItems = [
                        {
                          key: 'ddl',
                          icon: <EditOutlined />,
                          label: canWrite ? t('Edit Routine SQL') : t('Inspect DDL'),
                          onClick: openEditor,
                        },
                        {
                          key: 'copy',
                          icon: <CopyOutlined />,
                          label: t('Copy Name'),
                          onClick: () => {
                            writeTextToClipboard(rt.name);
                            message.success(t('Copied {name}', { name: rt.name }));
                          },
                        },
                        ...(canWrite
                          ? [
                              { type: 'divider' as const },
                              {
                                key: 'drop',
                                danger: true,
                                icon: <DeleteOutlined />,
                                label: t('Drop object'),
                                onClick: () => handleObjectMaintenance(rt.name, rtType, 'DROP'),
                              },
                            ]
                          : []),
                      ];
                      return (
                        <Dropdown
                          key={`${rtType}:${rt.name}`}
                          trigger={['contextMenu']}
                          menu={{ items: routineMenuItems }}
                        >
                          <div
                            onClick={openEditor}
                            style={{
                              height: itemRowHeight,
                              padding: '0 8px',
                              borderRadius: 6,
                              cursor: 'pointer',
                              display: 'flex',
                              justifyContent: 'space-between',
                              alignItems: 'center',
                              gap: 6,
                            }}
                            className="sidebar-item-row"
                          >
                            <div
                              style={{
                                display: 'flex',
                                alignItems: 'center',
                                gap: 6,
                                flex: 1,
                                minWidth: 0,
                                overflow: 'hidden',
                              }}
                            >
                              <FunctionOutlined style={{ color: '#08979c', flexShrink: 0 }} />
                              <Typography.Text ellipsis style={{ fontSize: 13, flex: 1, minWidth: 0 }} title={rt.name}>
                                {rt.name}
                              </Typography.Text>
                            </div>
                            <Space size={2} style={{ flexShrink: 0 }} onClick={(e) => e.stopPropagation()}>
                              <Tag style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '16px' }}>
                                {rtType === 'PROCEDURE' ? 'PROC' : 'FUNC'}
                              </Tag>
                              <Tooltip title={canWrite ? t('Edit Routine SQL') : t('Inspect DDL')}>
                                <Button
                                  type="text"
                                  size="small"
                                  style={isMobile ? { width: 30, height: 30, padding: 0 } : undefined}
                                  icon={<EditOutlined style={{ fontSize: 11 }} />}
                                  onClick={openEditor}
                                />
                              </Tooltip>
                              {isMobile && (
                                <Dropdown trigger={['click']} placement="bottomRight" menu={{ items: routineMenuItems }}>
                                  <Button
                                    type="text"
                                    size="small"
                                    style={{ width: 30, height: 30, padding: 0 }}
                                    icon={<MoreOutlined />}
                                    onClick={(e) => e.stopPropagation()}
                                  />
                                </Dropdown>
                              )}
                            </Space>
                          </div>
                        </Dropdown>
                      );
                    })}
                </div>
              )}

              {/* 4. Triggers Section */}
              {(triggers.length > 0 ||
                (selectedDB && canWrite && TRIGGER_CAPABLE_ENGINES.has(activeConn.type))) && (
                <div style={{ marginTop: 8 }}>
                  {renderSectionHeader(
                    'triggers',
                    t('Triggers ({count})', { count: filteredTriggers.length }),
                    selectedDB && canWrite && TRIGGER_CAPABLE_ENGINES.has(activeConn.type) ? (
                      <Tooltip title={t('Create Trigger')}>
                        <Button
                          type="link"
                          size="small"
                          icon={<PlusOutlined />}
                          style={isMobile ? { padding: '0 8px', height: 30 } : { padding: '0 4px', height: 22 }}
                          onClick={() =>
                            setRoutineTriggerModal({
                              open: true,
                              mode: 'trigger',
                              editObject: null,
                            })
                          }
                        >
                          {t('New Trigger')}
                        </Button>
                      </Tooltip>
                    ) : undefined
                  )}
                  {!collapsed.triggers &&
                    filteredTriggers.map((trg) => {
                      const openTriggerEditor = () =>
                        setRoutineTriggerModal({
                          open: true,
                          mode: 'trigger',
                          editObject: { name: trg.name, objectType: 'TRIGGER' },
                        });
                      const triggerMenuItems = [
                        {
                          key: 'ddl',
                          icon: <EditOutlined />,
                          label: canWrite ? t('Edit Trigger SQL') : t('Inspect DDL'),
                          onClick: openTriggerEditor,
                        },
                        {
                          key: 'copy',
                          icon: <CopyOutlined />,
                          label: t('Copy Name'),
                          onClick: () => {
                            writeTextToClipboard(trg.name);
                            message.success(t('Copied {name}', { name: trg.name }));
                          },
                        },
                        ...(canWrite
                          ? [
                              { type: 'divider' as const },
                              {
                                key: 'drop',
                                danger: true,
                                icon: <DeleteOutlined />,
                                label: t('Drop object'),
                                onClick: () => handleObjectMaintenance(trg.name, 'TRIGGER', 'DROP'),
                              },
                            ]
                          : []),
                      ];
                      return (
                        <Dropdown
                          key={trg.name}
                          trigger={['contextMenu']}
                          menu={{ items: triggerMenuItems }}
                        >
                          <div
                            onClick={openTriggerEditor}
                            style={{
                              height: itemRowHeight,
                              padding: '0 8px',
                              borderRadius: 6,
                              cursor: 'pointer',
                              display: 'flex',
                              justifyContent: 'space-between',
                              alignItems: 'center',
                              gap: 6,
                            }}
                            className="sidebar-item-row"
                          >
                            <div
                              style={{
                                display: 'flex',
                                alignItems: 'center',
                                gap: 6,
                                flex: 1,
                                minWidth: 0,
                                overflow: 'hidden',
                              }}
                            >
                              <ThunderboltOutlined style={{ color: '#d46b08', flexShrink: 0 }} />
                              <Typography.Text ellipsis style={{ fontSize: 13, flex: 1, minWidth: 0 }} title={trg.name}>
                                {trg.name}
                              </Typography.Text>
                            </div>
                            <Space size={2} style={{ flexShrink: 0 }} onClick={(e) => e.stopPropagation()}>
                              {trg.comment && (
                                <Typography.Text
                                  type="secondary"
                                  ellipsis
                                  style={{ fontSize: 10, maxWidth: 80 }}
                                  title={trg.comment}
                                >
                                  {trg.comment}
                                </Typography.Text>
                              )}
                              <Tooltip title={canWrite ? t('Edit Trigger SQL') : t('Inspect DDL')}>
                                <Button
                                  type="text"
                                  size="small"
                                  style={isMobile ? { width: 30, height: 30, padding: 0 } : undefined}
                                  icon={<EditOutlined style={{ fontSize: 11 }} />}
                                  onClick={openTriggerEditor}
                                />
                              </Tooltip>
                              {isMobile && (
                                <Dropdown trigger={['click']} placement="bottomRight" menu={{ items: triggerMenuItems }}>
                                  <Button
                                    type="text"
                                    size="small"
                                    style={{ width: 30, height: 30, padding: 0 }}
                                    icon={<MoreOutlined />}
                                    onClick={(e) => e.stopPropagation()}
                                  />
                                </Dropdown>
                              )}
                            </Space>
                          </div>
                        </Dropdown>
                      );
                    })}
                </div>
              )}
            </>
          )}
        </Spin>
      </div>

      {/* Create Database Modal */}
      <Modal
        open={newDBModalOpen}
        title={t('Create Database')}
        onCancel={() => setNewDBModalOpen(false)}
        onOk={handleCreateDB}
        okText={t('Create Now')}
      >
        <Input
          placeholder={t('Enter new database name, e.g. analytics_db')}
          value={newDBName}
          onChange={(e) => setNewDBName(e.target.value)}
          onPressEnter={handleCreateDB}
        />
      </Modal>

      {/* Drop Database Exact Confirmation Modal */}
      <Modal
        open={dropDBModalOpen}
        title={t('Drop Database')}
        onCancel={() => setDropDBModalOpen(false)}
        onOk={handleDropDB}
        okText={t('Drop Database')}
        okButtonProps={{ danger: true, disabled: dropDBConfirmInput.trim() !== selectedDB }}
      >
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 12 }}
          message={t('Danger: Dropping database {name} will permanently destroy all tables and data inside it.', {
            name: selectedDB,
          })}
        />
        <Typography.Text style={{ display: 'block', marginBottom: 6 }}>
          {t('Type {name} below to confirm:', { name: selectedDB })}
        </Typography.Text>
        <Input
          value={dropDBConfirmInput}
          onChange={(e) => setDropDBConfirmInput(e.target.value)}
          placeholder={selectedDB}
          onPressEnter={handleDropDB}
        />
      </Modal>

      {/* Create Schema Modal */}
      <Modal
        open={newSchemaModalOpen}
        title={t('Create Schema')}
        onCancel={() => setNewSchemaModalOpen(false)}
        onOk={handleCreateSchema}
        okText={t('Create Now')}
      >
        <Input
          placeholder={t('Enter new schema name, e.g. reporting')}
          value={newSchemaName}
          onChange={(e) => setNewSchemaName(e.target.value)}
          onPressEnter={handleCreateSchema}
        />
      </Modal>

      {/* Drop Schema Modal */}
      <Modal
        open={dropSchemaModalOpen}
        title={t('Drop Schema')}
        onCancel={() => setDropSchemaModalOpen(false)}
        onOk={handleDropSchema}
        okText={t('Drop Schema')}
        okButtonProps={{ danger: true }}
      >
        <Typography.Paragraph style={{ marginBottom: 10 }}>
          {t('Danger: Permanently drop {name}?', { name: selectedSchema })}
        </Typography.Paragraph>
        <Checkbox checked={dropSchemaCascade} onChange={(e) => setDropSchemaCascade(e.target.checked)}>
          {t('CASCADE (also drop contained objects)')}
        </Checkbox>
      </Modal>

      {/* Object DDL Inspector & View SQL Editor Modal */}
      <Modal
        open={ddlModal.open}
        title={
          ddlModal.isNewView
            ? t('Create View')
            : `${ddlModal.objectType}: ${ddlModal.objectName}`
        }
        width="min(780px, 95vw)"
        onCancel={() => setDdlModal((prev) => ({ ...prev, open: false }))}
        footer={
          <Space wrap>
            <Button
              icon={<CopyOutlined />}
              onClick={() => {
                writeTextToClipboard(ddlModal.sql);
                message.success(t('Copied to clipboard'));
              }}
            >
              {t('Copy SQL')}
            </Button>
            {activeConn && (
              <Button
                icon={<CodeOutlined />}
                onClick={() => {
                  openTab({
                    key: `sql:${activeConn.id}:${Date.now()}`,
                    title: `SQL - ${ddlModal.objectName || selectedDB}`,
                    kind: 'sql_console',
                    connectionId: activeConn.id,
                    connName: activeConn.name,
                    dbType: activeConn.type,
                    database: selectedDB,
                    schema: selectedSchema,
                    initialSQL: ddlModal.sql,
                  });
                  setDdlModal((prev) => ({ ...prev, open: false }));
                  onItemOpened?.();
                }}
              >
                {t('Open in SQL Console')}
              </Button>
            )}
            {!ddlModal.isNewView && ddlModal.objectType === 'VIEW' && canWrite && activeConn && selectedDB && (
              <Popconfirm
                title={t('Danger: Permanently drop {name}?', { name: ddlModal.objectName })}
                okButtonProps={{ danger: true }}
                onConfirm={async () => {
                  try {
                    await api.objectAction(activeConn.id, {
                      database: selectedDB,
                      schema: selectedSchema,
                      objectName: ddlModal.objectName,
                      objectType: 'VIEW',
                      action: 'DROP',
                    });
                    message.success(t('Executed {action} on {name}', { action: 'DROP', name: ddlModal.objectName }));
                    setDdlModal((prev) => ({ ...prev, open: false }));
                    invalidateConnCache(activeConn.id);
                    bumpTreeRefresh();
                  } catch (err: any) {
                    message.error(err.message);
                  }
                }}
              >
                <Button danger icon={<DeleteOutlined />}>
                  {t('Drop')}
                </Button>
              </Popconfirm>
            )}
            {ddlModal.objectType === 'VIEW' && canWrite && (
              <Button type="primary" loading={ddlModal.saving} onClick={handleSaveViewDDL}>
                {t('Save View')}
              </Button>
            )}
            <Button onClick={() => setDdlModal((prev) => ({ ...prev, open: false }))}>{t('Close')}</Button>
          </Space>
        }
      >
        <Spin spinning={ddlModal.loading}>
          {ddlModal.isNewView && (
            <Input
              style={{ marginBottom: 10 }}
              addonBefore={t('View Name')}
              placeholder="v_active_orders"
              value={ddlModal.objectName}
              onChange={(e) => setDdlModal((prev) => ({ ...prev, objectName: e.target.value }))}
            />
          )}
          <Input.TextArea
            rows={14}
            value={ddlModal.sql}
            readOnly={!(ddlModal.objectType === 'VIEW' && canWrite)}
            onChange={(e) => setDdlModal((prev) => ({ ...prev, sql: e.target.value }))}
            style={{ fontFamily: 'JetBrains Mono, Consolas, monospace', fontSize: 12.5 }}
          />
        </Spin>
      </Modal>

      {activeConn && (
        <RoutineTriggerModal
          open={routineTriggerModal.open}
          mode={routineTriggerModal.mode}
          connectionId={activeConn.id}
          connName={activeConn.name}
          dbType={activeConn.type}
          database={selectedDB}
          schema={selectedSchema}
          availableTables={tables.map((tbl) => tbl.name)}
          editObject={routineTriggerModal.editObject}
          canWrite={canWrite}
          onClose={() => setRoutineTriggerModal((prev) => ({ ...prev, open: false }))}
          onSaved={() => bumpTreeRefresh()}
        />
      )}
    </div>
  );
});
