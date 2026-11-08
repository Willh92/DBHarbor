import React, { Suspense, useCallback, useDeferredValue, useEffect, useMemo, useRef, useState } from 'react';
import {
  Alert,
  Badge,
  Button,
  Checkbox,
  Col,
  Dropdown,
  Empty,
  Form,
  Input,
  InputNumber,
  Modal,
  Row,
  Segmented,
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
  AppstoreOutlined,
  BlockOutlined,
  BookOutlined,
  ClearOutlined,
  ClockCircleOutlined,
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
  FieldTimeOutlined,
  FolderOpenOutlined,
  FunctionOutlined,
  KeyOutlined,
  LinkOutlined,
  LockOutlined,
  MoreOutlined,
  NodeCollapseOutlined,
  NodeExpandOutlined,
  NumberOutlined,
  PlusOutlined,
  ReloadOutlined,
  RightOutlined,
  SaveOutlined,
  SearchOutlined,
  ShareAltOutlined,
  SyncOutlined,
  TableOutlined,
  TagsOutlined,
  TeamOutlined,
  ThunderboltOutlined,
  UploadOutlined,
} from '@ant-design/icons';
import { api, isAuthPromptCancelledError } from '../../api/client';
import { useResponsive } from '../../hooks/useResponsive';
import { useI18n } from '../../i18n/useI18n';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import type {
  CollectionSummary,
  DatabaseSummary,
  ExportedConnectionProfile,
  ObjectItem,
  SavedConnection,
  SchemaObjects,
} from '../../types';
import { DB_SHORT_LABELS, formatBytes } from '../../utils/format';
import { hasEphemeralFileHandle, removeEphemeralFileHandle } from '../../utils/ephemeralFileHandles';
import { writeTextToClipboard } from '../../utils/tableClipboard';
import { LazyCodeEditor } from '../common/LazyCodeEditor';
import type { TransferModalMode } from '../workspace/ContextNavBar';
import type { SchemaObjectType } from '../workspace/SchemaObjectModal';

const LazySchemaObjectModal = React.lazy(() => import('../workspace/SchemaObjectModal'));

type SidebarSectionKey =
  | 'tables'
  | 'views'
  | 'materializedViews'
  | 'routines'
  | 'triggers'
  | 'sequences'
  | 'events'
  | 'packages'
  | 'types'
  | 'synonyms'
  | 'extensions'
  | 'dictionaries'
  | 'dbLinks'
  | 'macros';

export type FlatSidebarRow =
  | {
      kind: 'section_header';
      key: string;
      sectionKey: SidebarSectionKey;
      label: string;
      count: number;
      expanded: boolean;
      createType?: string;
    }
  | {
      kind: 'object_row';
      key: string;
      sectionKey: SidebarSectionKey;
      objectType: string;
      item: ObjectItem;
    };

const EMPTY_SCHEMA_OBJECTS: SchemaObjects = {
  database: '',
  schema: '',
  tables: [],
  views: [],
  routines: [],
};

interface Props {
  onNewConnection: () => void;
  onOpenLocalDB?: () => void;
  onSaveBackEphemeral?: (conn: SavedConnection) => void;
  onDownloadEphemeral?: (conn: SavedConnection) => void;
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
  onOpenLocalDB,
  onSaveBackEphemeral,
  onDownloadEphemeral,
  onEditConnection,
  onOpenCreateTable,
  onOpenTransfer,
  onItemOpened,
}) => {
  const {
    connections,
    drivers,
    openTab,
    closeTab,
    reloadConnections,
    closeConnectionTabs,
    treeRefreshTick,
    bumpTreeRefresh,
    requestedConnId,
    requestSelectConnection,
    isDesktop,
    darkMode,
    role,
    canCreateConn,
  } = useWorkbenchStore();
  const { isMobile } = useResponsive();
  const { t } = useI18n();
  const itemRowHeight = isMobile ? 40 : ITEM_ROW_HEIGHT;

  const canWrite = isDesktop || role !== 'readonly';
  const effectiveCanCreateConn = isDesktop || role === 'admin' || canCreateConn;

  const [connSearch, setConnSearch] = useState('');
  const [activeConnId, setActiveConnId] = useState<string>('');
  const [databases, setDatabases] = useState<DatabaseSummary[]>([]);
  const [selectedDB, setSelectedDB] = useState<string>('');
  const [schemas, setSchemas] = useState<string[]>([]);
  const [selectedSchema, setSelectedSchema] = useState<string>('');
  const [schemaObjects, setSchemaObjects] = useState<SchemaObjects>(EMPTY_SCHEMA_OBJECTS);
  const [collections, setCollections] = useState<CollectionSummary[]>([]);
  const [loadingMeta, setLoadingMeta] = useState(false);

  // Scoped context key isolates search & section expansion across connections, databases, and schemas
  const contextScopeKey = `${activeConnId}::${selectedDB}::${selectedSchema}`;
  const [searchByContext, setSearchByContext] = useState<Record<string, string>>({});
  const search = searchByContext[contextScopeKey] || '';
  const [expandedByContext, setExpandedByContext] = useState<Record<string, Record<string, boolean>>>({});
  const [searchExpandedOverrides, setSearchExpandedOverrides] = useState<Record<string, Record<string, boolean>>>({});
  const setSearch = useCallback(
    (val: string) => {
      setSearchByContext((prev) => {
        if (!val && !prev[contextScopeKey]) return prev;
        const next = { ...prev };
        if (!val) {
          delete next[contextScopeKey];
        } else {
          next[contextScopeKey] = val;
        }
        return next;
      });
      if (!val.trim()) {
        const prefix = `${contextScopeKey}::`;
        setSearchExpandedOverrides((prev) => {
          const keys = Object.keys(prev).filter((k) => k.startsWith(prefix));
          if (keys.length === 0) return prev;
          const next = { ...prev };
          for (const k of keys) {
            delete next[k];
          }
          return next;
        });
      }
    },
    [contextScopeKey]
  );
  const deferredSearch = useDeferredValue(search);

  // Top connection switcher stays expanded by default; all DB object categories default to collapsed (false) per context
  const [connectionsCollapsed, setConnectionsCollapsed] = useState(false);

  // Database & Schema lifecycle modals
  const [newDBModalOpen, setNewDBModalOpen] = useState(false);
  const [newDBName, setNewDBName] = useState('');
  const [dropDBModalOpen, setDropDBModalOpen] = useState(false);
  const [dropDBConfirmInput, setDropDBConfirmInput] = useState('');
  const [newSchemaModalOpen, setNewSchemaModalOpen] = useState(false);
  const [newSchemaName, setNewSchemaName] = useState('');
  const [dropSchemaCascade, setDropSchemaCascade] = useState(false);
  const [dropSchemaModalOpen, setDropSchemaModalOpen] = useState(false);

  // Unified SchemaObjectModal state (lazy-loaded)
  const [schemaObjModal, setSchemaObjModal] = useState<{
    open: boolean;
    mode: 'create' | 'edit';
    initialType: SchemaObjectType | string;
    editObject: ObjectItem | null;
  }>({
    open: false,
    mode: 'create',
    initialType: 'VIEW',
    editObject: null,
  });

  // MongoDB Create Collection / View Modal state
  const [mongoCreateOpen, setMongoCreateOpen] = useState(false);
  const [mongoCreateSaving, setMongoCreateSaving] = useState(false);
  const [mongoCreateKind, setMongoCreateKind] = useState<'standard' | 'capped' | 'timeseries' | 'view'>('standard');
  const [mongoCreateName, setMongoCreateName] = useState('');
  const [mongoMaxBytes, setMongoMaxBytes] = useState<number | null>(10485760);
  const [mongoMaxDocs, setMongoMaxDocs] = useState<number | null>(null);
  const [mongoTimeField, setMongoTimeField] = useState('timestamp');
  const [mongoMetaField, setMongoMetaField] = useState('');
  const [mongoGranularity, setMongoGranularity] = useState('seconds');
  const [mongoExpireSec, setMongoExpireSec] = useState<number | null>(null);
  const [mongoViewOn, setMongoViewOn] = useState('');
  const [mongoPipelineJson, setMongoPipelineJson] = useState('[\n  { "$match": {} }\n]');
  const [mongoValidatorJson, setMongoValidatorJson] = useState('');

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
        schemaObjects: SchemaObjects;
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
    if (!requestedConnId || drivers.length === 0) return;
    const matched = connections.find((c) => c.id === requestedConnId);
    if (matched) {
      requestSelectConnection('');
      handleSelectConnection(matched, true);
    }
  }, [requestedConnId, connections, drivers.length]);

  const prevConnIdsRef = useRef<Set<string>>(new Set());
  useEffect(() => {
    const currentIds = new Set(connections.map((c) => c.id));
    const removedIds: string[] = [];
    prevConnIdsRef.current.forEach((id) => {
      if (!currentIds.has(id)) {
        removedIds.push(id);
      }
    });
    prevConnIdsRef.current = currentIds;

    if (removedIds.length > 0) {
      for (const removedId of removedIds) {
        invalidateConnCache(removedId);
        void removeEphemeralFileHandle(removedId);
      }
      const hasRemovedPrefix = (key: string) => removedIds.some((id) => key.startsWith(`${id}::`));
      const pruneMap = <T,>(prev: Record<string, T>): Record<string, T> => {
        const keys = Object.keys(prev).filter(hasRemovedPrefix);
        if (keys.length === 0) return prev;
        const next = { ...prev };
        for (const k of keys) {
          delete next[k];
        }
        return next;
      };
      setExpandedByContext(pruneMap);
      setSearchByContext(pruneMap);
      setSearchExpandedOverrides(pruneMap);
    }

    if (connections.length === 0) {
      setActiveConnId('');
      setDatabases([]);
      setSelectedDB('');
      setSchemas([]);
      setSelectedSchema('');
      setSchemaObjects(EMPTY_SCHEMA_OBJECTS);
      setCollections([]);
    }
  }, [connections]);

  useEffect(() => {
    if (drivers.length === 0) return;
    if (!activeConnId && connections.length > 0) {
      const unlockedFirst = connections.find((c) => c.rememberPassword !== false || c.unlocked);
      if (unlockedFirst) {
        handleSelectConnection(unlockedFirst);
      }
    } else if (activeConnId && !connections.some((c) => c.id === activeConnId)) {
      const unlockedFirst = connections.find((c) => c.rememberPassword !== false || c.unlocked);
      if (unlockedFirst) {
        handleSelectConnection(unlockedFirst);
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

  // Close context-bound modals whenever active connection, database, or schema changes
  useEffect(() => {
    setNewDBModalOpen(false);
    setNewSchemaModalOpen(false);
    setDropDBModalOpen(false);
    setDropDBConfirmInput('');
    setDropSchemaModalOpen(false);
    setMongoCreateOpen(false);
    setSchemaObjModal((prev) => (prev.open ? { ...prev, open: false } : prev));
  }, [activeConnId]);

  useEffect(() => {
    setDropDBModalOpen(false);
    setDropDBConfirmInput('');
    setDropSchemaModalOpen(false);
    setMongoCreateOpen(false);
    setSchemaObjModal((prev) => (prev.open ? { ...prev, open: false } : prev));
  }, [selectedDB, selectedSchema]);

  const loadDatabaseChildren = async (
    conn: SavedConnection,
    dbName: string,
    preferredSchema: string,
    parentSeq?: number,
    forceReload = false
  ): Promise<string | null> => {
    const drv = drivers.find((d) => d.type === conn.type);
    if (!drv || drv.category === 'keyvalue') return null;

    const cacheKey = `${conn.id}::${dbName}::${drv.supportsSchemas ? preferredSchema || '__default__' : ''}`;
    if (!forceReload) {
      const cached = dbChildrenCacheRef.current.get(cacheKey);
      if (cached) {
        setSchemas(cached.schemas);
        setSelectedSchema(cached.selectedSchema);
        setSchemaObjects(cached.schemaObjects);
        setCollections(cached.collections);
        const existingConnEntry = connDatabasesCacheRef.current.get(conn.id);
        if (existingConnEntry) {
          existingConnEntry.lastDB = dbName;
          existingConnEntry.lastSchema = cached.selectedSchema;
        }
        return cached.selectedSchema;
      }
    }

    const seq = parentSeq ?? ++reqSeqRef.current;
    setLoadingMeta(true);
    setSchemaObjects(EMPTY_SCHEMA_OBJECTS);
    setCollections([]);
    if (!drv.supportsSchemas) {
      setSchemas([]);
      setSelectedSchema('');
    }
    try {
      if (drv.category === 'document') {
        const colls = await api.docListCollections(conn.id, dbName);
        if (seq !== reqSeqRef.current) return null;
        setCollections(colls);
        dbChildrenCacheRef.current.set(cacheKey, {
          schemas: [],
          selectedSchema: '',
          schemaObjects: EMPTY_SCHEMA_OBJECTS,
          collections: colls,
        });
        return '';
      }

      let schemaToUse = preferredSchema;
      let scList: string[] = [];
      if (drv.supportsSchemas) {
        scList = await api.listSchemas(conn.id, dbName);
        if (seq !== reqSeqRef.current) return null;
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
      if (seq !== reqSeqRef.current) return null;
      const normalizedObjs: SchemaObjects = {
        ...objs,
        tables: objs.tables || [],
        views: objs.views || [],
        routines: objs.routines || [],
      };
      setSchemaObjects(normalizedObjs);

      const snapshot = {
        schemas: scList,
        selectedSchema: schemaToUse,
        schemaObjects: normalizedObjs,
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
      return schemaToUse;
    } catch (err: any) {
      if (seq === reqSeqRef.current && !isAuthPromptCancelledError(err)) {
        message.error(err.message);
      }
      return null;
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
          setSchemas([]);
          setSelectedSchema('');
          setSchemaObjects(EMPTY_SCHEMA_OBJECTS);
          setCollections([]);
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
        } else {
          setSchemas([]);
          setSelectedSchema('');
          setSchemaObjects(EMPTY_SCHEMA_OBJECTS);
          setCollections([]);
        }
        return;
      }
    }

    const seq = ++reqSeqRef.current;
    setActiveConnId(conn.id);
    setLoadingMeta(true);
    setDatabases([]);
    setSelectedDB('');
    setSchemas([]);
    setSelectedSchema('');
    setSchemaObjects(EMPTY_SCHEMA_OBJECTS);
    setCollections([]);

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
        if (isAuthPromptCancelledError(err)) {
          setActiveConnId('');
        } else {
          message.error(`${conn.name}: ${err.message}`);
        }
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
      const existingConnEntry = connDatabasesCacheRef.current.get(activeConn.id);
      if (existingConnEntry) {
        existingConnEntry.lastDB = dbName;
      }
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
    const resolvedSchema = await loadDatabaseChildren(activeConn, dbName, '');
    if (resolvedSchema !== null && (activeDriver?.category === 'relational' || activeDriver?.category === 'olap')) {
      openTab({
        key: `overview:${activeConn.id}:${dbName}:${resolvedSchema}`,
        title: `${dbName} (${t('Overview')})`,
        kind: 'overview',
        connectionId: activeConn.id,
        connName: activeConn.name,
        dbType: activeConn.type,
        database: dbName,
        schema: resolvedSchema,
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
      if (!isAuthPromptCancelledError(err)) {
        message.error(`${conn.name}: ${err.message}`);
      }
    }
  };

  const handleDisconnectPool = async (conn: SavedConnection) => {
    try {
      await api.disconnectConnection(conn.id);
      invalidateConnCache(conn.id);
      if (conn.rememberPassword === false && activeConnId === conn.id) {
        setActiveConnId('');
        setDatabases([]);
        setSelectedDB('');
        setSchemas([]);
        setSelectedSchema('');
        setSchemaObjects(EMPTY_SCHEMA_OBJECTS);
        setCollections([]);
      }
      await reloadConnections();
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
    void removeEphemeralFileHandle(conn.id);
    await api.deleteConnection(conn.id);
    closeConnectionTabs(conn.id);
    if (activeConnId === conn.id) {
      setActiveConnId('');
      setDatabases([]);
      setSchemaObjects(EMPTY_SCHEMA_OBJECTS);
      setCollections([]);
    }
    await reloadConnections();
    message.success(conn.ephemeral ? t('Temporary database closed and cleaned up') : t('Connection deleted'));
  };

  const confirmDeleteConn = (conn: SavedConnection) => {
    Modal.confirm({
      title: conn.ephemeral ? t('Close temporary database and clean up server copy?') : t('Delete this connection?'),
      content: conn.ephemeral
        ? `${conn.name} — ${t('Make sure you have saved or downloaded your latest changes before closing.')}`
        : conn.name,
      okText: conn.ephemeral ? t('Close & Clean Up') : t('Delete Connection'),
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

  const openCreateObjectModal = (type: string) => {
    if (!activeConn || !selectedDB) return;
    if (type === 'TABLE') {
      onOpenCreateTable(activeConn, selectedDB, selectedSchema);
      return;
    }
    setSchemaObjModal({
      open: true,
      mode: 'create',
      initialType: type,
      editObject: null,
    });
  };

  const openEditObjectModal = (item: ObjectItem, fallbackType: string) => {
    setSchemaObjModal({
      open: true,
      mode: 'edit',
      initialType: item.type || fallbackType,
      editObject: { ...item, type: item.type || fallbackType },
    });
  };

  const handleObjectMaintenance = (
    objName: string,
    objType: string,
    action: string,
    targetName?: string
  ) => {
    if (!activeConn || !selectedDB || !canWrite) return;
    const connRef = activeConn;
    const dbRef = selectedDB;
    const scRef = selectedSchema;

    const executeAction = async () => {
      try {
        await api.objectAction(connRef.id, {
          database: dbRef,
          schema: scRef,
          objectName: objName,
          objectType: objType,
          targetName: targetName || undefined,
          action,
        });
        message.success(t('Executed {action} on {name}', { action, name: objName }));
        invalidateConnCache(connRef.id);
        bumpTreeRefresh();
      } catch (err: any) {
        message.error(err.message);
      }
    };

    if (action === 'DROP' || action === 'TRUNCATE') {
      Modal.confirm({
        title:
          action === 'TRUNCATE'
            ? t('Truncate all rows in table {name}?', { name: objName })
            : t('Danger: Permanently drop {name}?', { name: objName }),
        okButtonProps: { danger: true },
        onOk: executeAction,
      });
    } else {
      void executeAction();
    }
  };

  const handleOpenMongoCreateModal = (initialKind: 'standard' | 'capped' | 'timeseries' | 'view' = 'standard') => {
    setMongoCreateKind(initialKind);
    setMongoCreateName('');
    setMongoMaxBytes(10485760);
    setMongoMaxDocs(null);
    setMongoTimeField('timestamp');
    setMongoMetaField('');
    setMongoGranularity('seconds');
    setMongoExpireSec(null);
    const firstNonView = collections.find((c) => c.type !== 'view')?.name || '';
    setMongoViewOn(firstNonView);
    setMongoPipelineJson('[\n  { "$match": {} }\n]');
    setMongoValidatorJson('');
    setMongoCreateOpen(true);
  };

  const handleCreateMongoCollection = async () => {
    if (!activeConn || !selectedDB) return;
    const name = mongoCreateName.trim();
    if (!name) {
      message.warning(t('Please enter a collection or view name'));
      return;
    }
    if (mongoCreateKind === 'view' && !mongoViewOn.trim()) {
      message.warning(t('Please select a source collection for the view'));
      return;
    }
    if (mongoCreateKind === 'timeseries' && !mongoTimeField.trim()) {
      message.warning(t('Please enter the timeField for the time-series collection'));
      return;
    }

    setMongoCreateSaving(true);
    try {
      await api.docCreateCollection(activeConn.id, {
        database: selectedDB,
        collection: name,
        type:
          mongoCreateKind === 'view'
            ? 'view'
            : mongoCreateKind === 'timeseries'
              ? 'timeseries'
              : 'collection',
        capped: mongoCreateKind === 'capped' ? true : undefined,
        maxBytes: mongoCreateKind === 'capped' && mongoMaxBytes ? mongoMaxBytes : undefined,
        maxDocs: mongoCreateKind === 'capped' && mongoMaxDocs ? mongoMaxDocs : undefined,
        viewOn: mongoCreateKind === 'view' ? mongoViewOn.trim() : undefined,
        pipelineJson: mongoCreateKind === 'view' ? mongoPipelineJson.trim() : undefined,
        timeField: mongoCreateKind === 'timeseries' ? mongoTimeField.trim() : undefined,
        metaField: mongoCreateKind === 'timeseries' && mongoMetaField.trim() ? mongoMetaField.trim() : undefined,
        granularity: mongoCreateKind === 'timeseries' ? mongoGranularity : undefined,
        expireAfterSeconds:
          mongoCreateKind === 'timeseries' && mongoExpireSec !== null ? mongoExpireSec : undefined,
        validatorJson:
          mongoCreateKind === 'standard' && mongoValidatorJson.trim() ? mongoValidatorJson.trim() : undefined,
      });
      message.success(t('Created {name}', { name }));
      setMongoCreateOpen(false);
      invalidateConnCache(activeConn.id);
      bumpTreeRefresh();
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setMongoCreateSaving(false);
    }
  };

  const handleDropMongoCollection = (coll: CollectionSummary) => {
    if (!activeConn || !selectedDB || !canWrite) return;
    const connRef = activeConn;
    const dbRef = selectedDB;
    Modal.confirm({
      title: t('Danger: Permanently drop {name}?', { name: coll.name }),
      okButtonProps: { danger: true },
      onOk: async () => {
        try {
          await api.docDropCollection(connRef.id, dbRef, coll.name);
          closeTab(`mongo:${connRef.id}:${dbRef}:${coll.name}`);
          message.success(t('Dropped {name}', { name: coll.name }));
          invalidateConnCache(connRef.id);
          bumpTreeRefresh();
        } catch (err: any) {
          message.error(err.message);
        }
      },
    });
  };

  const buildConnMenuItems = (conn: SavedConnection) => {
    const canEditThisConn = isDesktop || conn.editable !== false;
    const isEph = Boolean(conn.ephemeral);
    const hasLocalHandle = isEph && hasEphemeralFileHandle(conn.id);
    const items: any[] = [
      ...(isEph
        ? [
            ...(hasLocalHandle && onSaveBackEphemeral
              ? [
                  {
                    key: 'eph-save-back',
                    icon: <SaveOutlined />,
                    label: t('Save Back to Local File'),
                    onClick: () => onSaveBackEphemeral(conn),
                  },
                ]
              : []),
            ...(onDownloadEphemeral
              ? [
                  {
                    key: 'eph-download',
                    icon: <DownloadOutlined />,
                    label: t('Download DB File'),
                    onClick: () => onDownloadEphemeral(conn),
                  },
                ]
              : []),
            { type: 'divider' as const },
          ]
        : []),
      {
        key: 'ping',
        icon: <ApiOutlined />,
        label: t('Test Connection'),
        onClick: () => handlePingConn(conn),
      },
      ...(canEditThisConn && !isEph
        ? [
            {
              key: 'edit',
              icon: <EditOutlined />,
              label: t('Edit Connection'),
              onClick: () => onEditConnection(conn),
            },
          ]
        : []),
      ...(effectiveCanCreateConn && canEditThisConn && !isEph
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
          label: isEph ? t('Close Temp DB & Clean Up') : t('Delete Connection'),
          onClick: () => confirmDeleteConn(conn),
        }
      );
    }
    return items;
  };

  const buildObjectMenuItems = (item: ObjectItem, fallbackType: string) => {
    if (!activeConn) return [];
    const conn = activeConn;
    const objType = (item.type || fallbackType || 'TABLE').toUpperCase();
    const isBrowsable = objType === 'TABLE' || objType === 'VIEW' || objType === 'MATERIALIZED_VIEW';

    const items: any[] = [];
    if (isBrowsable) {
      items.push({
        key: 'browse',
        icon: <TableOutlined />,
        label: t('Browse Data'),
        onClick: () => {
          openTab({
            key: `data:${conn.id}:${selectedDB}:${selectedSchema}:${item.name}`,
            title:
              objType === 'VIEW'
                ? `${item.name} (${t('View')})`
                : objType === 'MATERIALIZED_VIEW'
                  ? `${item.name} (${t('MatView')})`
                  : item.name,
            kind: 'table_data',
            connectionId: conn.id,
            connName: conn.name,
            dbType: conn.type,
            database: selectedDB,
            schema: selectedSchema,
            table: item.name,
          });
          onItemOpened?.();
        },
      });
    }

    if (objType === 'TABLE') {
      items.push({
        key: 'structure',
        icon: <EditOutlined />,
        label: t('Table Structure'),
        onClick: () => {
          openTab({
            key: `struct:${conn.id}:${selectedDB}:${selectedSchema}:${item.name}`,
            title: `${item.name} [${t('Structure')}]`,
            kind: 'table_structure',
            connectionId: conn.id,
            connName: conn.name,
            dbType: conn.type,
            database: selectedDB,
            schema: selectedSchema,
            table: item.name,
          });
          onItemOpened?.();
        },
      });
    } else {
      items.push({
        key: 'ddl',
        icon: <EditOutlined />,
        label: canWrite ? t('Edit / DDL') : t('Inspect DDL'),
        onClick: () => openEditObjectModal(item, objType),
      });
    }

    if (canWrite && objType === 'MATERIALIZED_VIEW') {
      items.push({
        key: 'refresh-mv',
        icon: <SyncOutlined />,
        label: t('Refresh View'),
        onClick: () => handleObjectMaintenance(item.name, 'MATERIALIZED_VIEW', 'REFRESH'),
      });
    }
    if (canWrite && objType === 'DICTIONARY') {
      items.push({
        key: 'reload-dict',
        icon: <ReloadOutlined />,
        label: t('Reload Dictionary'),
        onClick: () => handleObjectMaintenance(item.name, 'DICTIONARY', 'RELOAD'),
      });
    }
    if (canWrite && (objType === 'PACKAGE' || objType === 'PACKAGE_BODY')) {
      items.push({
        key: 'compile-pkg',
        icon: <ThunderboltOutlined />,
        label: t('Compile'),
        onClick: () => handleObjectMaintenance(item.name, objType, 'COMPILE'),
      });
    }
    if (canWrite && objType === 'EVENT') {
      items.push(
        {
          key: 'enable-ev',
          icon: <ThunderboltOutlined />,
          label: t('Enable'),
          onClick: () => handleObjectMaintenance(item.name, 'EVENT', 'ENABLE'),
        },
        {
          key: 'disable-ev',
          icon: <DisconnectOutlined />,
          label: t('Disable'),
          onClick: () => handleObjectMaintenance(item.name, 'EVENT', 'DISABLE'),
        }
      );
    }

    if (isBrowsable) {
      items.push({
        key: 'sql',
        icon: <CodeOutlined />,
        label: t('Custom SQL'),
        onClick: () => {
          openTab({
            key: `sql:${conn.id}:${selectedDB}:${item.name}`,
            title: `SQL - ${item.name}`,
            kind: 'sql_console',
            connectionId: conn.id,
            connName: conn.name,
            dbType: conn.type,
            database: selectedDB,
            schema: selectedSchema,
            table: item.name,
            initialSQL: `SELECT * FROM ${item.name} LIMIT 100;`,
          });
          onItemOpened?.();
        },
      });
    }

    if (objType === 'TABLE' && onOpenTransfer) {
      items.push({
        key: 'export',
        icon: <DownloadOutlined />,
        label: t('Export Table'),
        onClick: () => onOpenTransfer(conn, selectedDB, selectedSchema, 'export', item.name),
      });
    }

    items.push({
      key: 'copy-name',
      icon: <CopyOutlined />,
      label: t('Copy Name'),
      onClick: () => {
        writeTextToClipboard(item.name);
        message.success(t('Copied {name}', { name: item.name }));
      },
    });

    if (canWrite) {
      items.push({ type: 'divider' as const });
      if (objType === 'TABLE') {
        items.push({
          key: 'truncate',
          danger: true,
          icon: <ClearOutlined />,
          label: t('Truncate table'),
          onClick: () => handleObjectMaintenance(item.name, 'TABLE', 'TRUNCATE'),
        });
      }
      items.push({
        key: 'drop',
        danger: true,
        icon: <DeleteOutlined />,
        label: t('Drop object'),
        onClick: () => handleObjectMaintenance(item.name, objType, 'DROP', item.targetName),
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
  const filteredColls = useMemo(
    () => (qLower ? collections.filter((c) => c.name.toLowerCase().includes(qLower)) : collections),
    [collections, qLower]
  );

  const creatableObjectMenuItems = useMemo(() => {
    const capTypes = activeDriver?.capabilities?.objectTypes || ['TABLE', 'VIEW'];
    const labelMap: Record<string, string> = {
      TABLE: t('New Table'),
      VIEW: t('New View'),
      MATERIALIZED_VIEW: t('New Materialized View'),
      SEQUENCE: t('New Sequence'),
      FUNCTION: t('New Function'),
      PROCEDURE: t('New Procedure'),
      TRIGGER: t('New Trigger'),
      EVENT: t('New Scheduled Event'),
      TYPE: t('New Custom Type'),
      EXTENSION: t('Install Extension'),
      PACKAGE: t('New Package'),
      SYNONYM: t('New Synonym'),
      DB_LINK: t('New Database Link'),
      DICTIONARY: t('New Dictionary'),
      MACRO: t('New Macro'),
    };
    return capTypes
      .filter((tp) => Boolean(labelMap[tp]))
      .map((tp) => ({
        key: tp,
        icon: tp === 'TABLE' ? <TableOutlined /> : <PlusOutlined />,
        label: labelMap[tp] || tp,
        onClick: () => openCreateObjectModal(tp),
      }));
  }, [activeDriver, t, selectedDB, selectedSchema, activeConn]);

  const searchScopeKey = `${contextScopeKey}::${qLower}`;

  const isSectionExpanded = useCallback(
    (group: string, matchCount = 0): boolean => {
      if (qLower) {
        const ov = searchExpandedOverrides[searchScopeKey]?.[group];
        return ov !== undefined ? ov : matchCount > 0;
      }
      return Boolean(expandedByContext[contextScopeKey]?.[group]);
    },
    [qLower, searchExpandedOverrides, searchScopeKey, expandedByContext, contextScopeKey]
  );

  const toggleObjectSection = useCallback(
    (group: string, matchCount = 0) => {
      const nextOpen = !isSectionExpanded(group, matchCount);
      if (qLower) {
        setSearchExpandedOverrides((prev) => ({
          ...prev,
          [searchScopeKey]: {
            ...(prev[searchScopeKey] || {}),
            [group]: nextOpen,
          },
        }));
        return;
      }
      setExpandedByContext((prev) => ({
        ...prev,
        [contextScopeKey]: {
          ...(prev[contextScopeKey] || {}),
          [group]: nextOpen,
        },
      }));
    },
    [isSectionExpanded, qLower, searchScopeKey, contextScopeKey]
  );

  // Single memoized flat virtualized array across all 14 active object categories
  const flatRows = useMemo<FlatSidebarRow[]>(() => {
    if (!activeConn || activeDriver?.category === 'keyvalue' || activeDriver?.category === 'document') {
      return [];
    }
    const capTypes = activeDriver?.capabilities?.objectTypes || ['TABLE', 'VIEW'];
    const supportsTables = capTypes.includes('TABLE') || (schemaObjects.tables?.length || 0) > 0;
    const supportsViews = capTypes.includes('VIEW') || (schemaObjects.views?.length || 0) > 0;

    const installedExtensions = (schemaObjects.extensions || []).filter(
      (ext) => ext.status?.toUpperCase() !== 'AVAILABLE' && ext.extra?.installed !== 'false'
    );

    const sections: {
      sectionKey: SidebarSectionKey;
      rawItems: ObjectItem[];
      alwaysShow: boolean;
      defaultType: string;
      createType?: string;
      formatLabel: (count: number) => string;
    }[] = [
      {
        sectionKey: 'tables',
        rawItems: schemaObjects.tables || [],
        alwaysShow: supportsTables,
        defaultType: 'TABLE',
        createType: 'TABLE',
        formatLabel: (count) => t('Tables ({count})', { count }),
      },
      {
        sectionKey: 'views',
        rawItems: schemaObjects.views || [],
        alwaysShow: supportsViews && ((schemaObjects.views?.length || 0) > 0 || Boolean(selectedDB && canWrite)),
        defaultType: 'VIEW',
        createType: 'VIEW',
        formatLabel: (count) => t('Views ({count})', { count }),
      },
      {
        sectionKey: 'materializedViews',
        rawItems: schemaObjects.materializedViews || [],
        alwaysShow: false,
        defaultType: 'MATERIALIZED_VIEW',
        createType: 'MATERIALIZED_VIEW',
        formatLabel: (count) => t('Materialized Views ({count})', { count }),
      },
      {
        sectionKey: 'routines',
        rawItems: schemaObjects.routines || [],
        alwaysShow: false,
        defaultType: 'FUNCTION',
        createType: 'FUNCTION',
        formatLabel: (count) => t('Functions & Procedures ({count})', { count }),
      },
      {
        sectionKey: 'triggers',
        rawItems: schemaObjects.triggers || [],
        alwaysShow: false,
        defaultType: 'TRIGGER',
        createType: 'TRIGGER',
        formatLabel: (count) => t('Triggers ({count})', { count }),
      },
      {
        sectionKey: 'sequences',
        rawItems: schemaObjects.sequences || [],
        alwaysShow: false,
        defaultType: 'SEQUENCE',
        createType: 'SEQUENCE',
        formatLabel: (count) => t('Sequences ({count})', { count }),
      },
      {
        sectionKey: 'events',
        rawItems: schemaObjects.events || [],
        alwaysShow: false,
        defaultType: 'EVENT',
        createType: 'EVENT',
        formatLabel: (count) => t('Events ({count})', { count }),
      },
      {
        sectionKey: 'packages',
        rawItems: schemaObjects.packages || [],
        alwaysShow: false,
        defaultType: 'PACKAGE',
        createType: 'PACKAGE',
        formatLabel: (count) => t('Packages ({count})', { count }),
      },
      {
        sectionKey: 'types',
        rawItems: schemaObjects.types || [],
        alwaysShow: false,
        defaultType: 'TYPE',
        createType: 'TYPE',
        formatLabel: (count) => t('Types ({count})', { count }),
      },
      {
        sectionKey: 'synonyms',
        rawItems: schemaObjects.synonyms || [],
        alwaysShow: false,
        defaultType: 'SYNONYM',
        createType: 'SYNONYM',
        formatLabel: (count) => t('Synonyms ({count})', { count }),
      },
      {
        sectionKey: 'extensions',
        rawItems: installedExtensions,
        alwaysShow: false,
        defaultType: 'EXTENSION',
        createType: 'EXTENSION',
        formatLabel: (count) => t('Extensions ({count})', { count }),
      },
      {
        sectionKey: 'dictionaries',
        rawItems: schemaObjects.dictionaries || [],
        alwaysShow: false,
        defaultType: 'DICTIONARY',
        createType: 'DICTIONARY',
        formatLabel: (count) => t('Dictionaries ({count})', { count }),
      },
      {
        sectionKey: 'dbLinks',
        rawItems: schemaObjects.dbLinks || [],
        alwaysShow: false,
        defaultType: 'DB_LINK',
        createType: 'DB_LINK',
        formatLabel: (count) => t('DB Links ({count})', { count }),
      },
      {
        sectionKey: 'macros',
        rawItems: schemaObjects.macros || [],
        alwaysShow: false,
        defaultType: 'MACRO',
        createType: 'MACRO',
        formatLabel: (count) => t('Macros ({count})', { count }),
      },
    ];

    const out: FlatSidebarRow[] = [];
    for (const sec of sections) {
      if (!sec.alwaysShow && sec.rawItems.length === 0) continue;
      const filtered = qLower
        ? sec.rawItems.filter((it) => it.name.toLowerCase().includes(qLower))
        : sec.rawItems;
      if (qLower && filtered.length === 0 && sec.sectionKey !== 'tables') continue;

      const sectionExpanded = isSectionExpanded(sec.sectionKey, filtered.length);
      out.push({
        kind: 'section_header',
        key: `hdr:${contextScopeKey}:${sec.sectionKey}`,
        sectionKey: sec.sectionKey,
        label: sec.formatLabel(filtered.length),
        count: filtered.length,
        expanded: sectionExpanded,
        createType: sec.createType,
      });

      if (sectionExpanded) {
        for (const it of filtered) {
          const objType = (it.type || sec.defaultType).toUpperCase();
          out.push({
            kind: 'object_row',
            key: `obj:${contextScopeKey}:${sec.sectionKey}:${objType}:${it.targetName || ''}:${it.name}`,
            sectionKey: sec.sectionKey,
            objectType: objType,
            item: it,
          });
        }
      }
    }
    return out;
  }, [activeConn, activeDriver, schemaObjects, selectedDB, canWrite, qLower, contextScopeKey, isSectionExpanded, t]);

  const visibleSectionEntries = useMemo<{ key: string; matchCount: number }[]>(() => {
    if (!activeConn || activeDriver?.category === 'keyvalue') return [];
    if (activeDriver?.category === 'document') {
      return selectedDB ? [{ key: 'collections', matchCount: filteredColls.length }] : [];
    }
    return flatRows
      .filter((r): r is Extract<FlatSidebarRow, { kind: 'section_header' }> => r.kind === 'section_header')
      .map((r) => ({ key: r.sectionKey, matchCount: r.count }));
  }, [activeConn, activeDriver, selectedDB, filteredColls.length, flatRows]);

  const allSectionsExpanded = useMemo(
    () =>
      visibleSectionEntries.length > 0 &&
      visibleSectionEntries.every((sec) => isSectionExpanded(sec.key, sec.matchCount)),
    [visibleSectionEntries, isSectionExpanded]
  );

  const handleToggleExpandAll = useCallback(() => {
    if (visibleSectionEntries.length === 0) return;
    const nextExpanded = !allSectionsExpanded;
    if (qLower) {
      const patch: Record<string, boolean> = {};
      for (const sec of visibleSectionEntries) {
        patch[sec.key] = nextExpanded;
      }
      setSearchExpandedOverrides((prev) => ({
        ...prev,
        [searchScopeKey]: {
          ...(prev[searchScopeKey] || {}),
          ...patch,
        },
      }));
    } else {
      setExpandedByContext((prev) => {
        if (!nextExpanded) {
          if (!prev[contextScopeKey]) return prev;
          const next = { ...prev };
          delete next[contextScopeKey];
          return next;
        }
        const patch: Record<string, boolean> = { ...(prev[contextScopeKey] || {}) };
        for (const sec of visibleSectionEntries) {
          patch[sec.key] = true;
        }
        return { ...prev, [contextScopeKey]: patch };
      });
    }
    if (!nextExpanded) {
      if (treeScrollRef.current) {
        treeScrollRef.current.scrollTop = 0;
      }
      setScrollTop(0);
    }
  }, [visibleSectionEntries, allSectionsExpanded, qLower, searchScopeKey, contextScopeKey]);

  const useVirtualFlat = flatRows.length > VIRTUAL_THRESHOLD;
  const flatStartIdx = useVirtualFlat
    ? Math.max(0, Math.floor(scrollTop / itemRowHeight) - OVERSCAN_ROWS)
    : 0;
  const flatEndIdx = useVirtualFlat
    ? Math.min(flatRows.length, Math.ceil((scrollTop + viewportH) / itemRowHeight) + OVERSCAN_ROWS)
    : flatRows.length;
  const visibleFlatRows = useVirtualFlat ? flatRows.slice(flatStartIdx, flatEndIdx) : flatRows;
  const flatTopPad = useVirtualFlat ? flatStartIdx * itemRowHeight : 0;
  const flatBottomPad = useVirtualFlat ? (flatRows.length - flatEndIdx) * itemRowHeight : 0;

  const collectionsExpanded = isSectionExpanded('collections', filteredColls.length);
  const useVirtualColls = collectionsExpanded && filteredColls.length > VIRTUAL_THRESHOLD;
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
    group: string,
    label: string,
    matchCount: number,
    extra?: React.ReactNode
  ) => {
    const isClosed = !isSectionExpanded(group, matchCount);
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
        onClick={() => toggleObjectSection(group, matchCount)}
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
            marginBottom: connectionsCollapsed ? 0 : 8,
          }}
        >
          <div
            onClick={() => setConnectionsCollapsed((prev) => !prev)}
            style={{ display: 'inline-flex', alignItems: 'center', gap: 6, cursor: 'pointer', userSelect: 'none' }}
          >
            {connectionsCollapsed ? (
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
                  ...(!isDesktop && effectiveCanCreateConn && canWrite && onOpenLocalDB
                    ? [
                        {
                          key: 'open-ephemeral-db',
                          icon: <FolderOpenOutlined />,
                          label: t('Open Local SQLite / DuckDB'),
                          onClick: onOpenLocalDB,
                        },
                        { type: 'divider' as const },
                      ]
                    : []),
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

        {!connectionsCollapsed && (
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
                        {conn.ephemeral && (
                          <Tag
                            color="orange"
                            style={{ margin: 0, fontSize: 10, lineHeight: '16px', paddingInline: 4, flexShrink: 0 }}
                          >
                            {t('Temp')}
                          </Tag>
                        )}
                        {conn.rememberPassword === false && !conn.unlocked && (
                          <Tooltip title={t('Password not remembered — session credentials required')}>
                            <KeyOutlined style={{ fontSize: 11, color: '#faad14', flexShrink: 0 }} />
                          </Tooltip>
                        )}
                        {!isDesktop && !conn.ephemeral && conn.visibility === 'private' && (
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
                {canWrite && selectedDB && creatableObjectMenuItems.length > 0 && (
                  <Dropdown
                    trigger={['click']}
                    placement="bottomLeft"
                    menu={{ items: creatableObjectMenuItems }}
                  >
                    <Button size={isMobile ? 'middle' : 'small'} icon={<PlusOutlined />}>
                      {t('New Object')} <DownOutlined style={{ fontSize: 10 }} />
                    </Button>
                  </Dropdown>
                )}
              </>
            )}
            {activeDriver?.category === 'document' && canWrite && selectedDB && (
              <Button
                size={isMobile ? 'middle' : 'small'}
                type="primary"
                ghost
                icon={<PlusOutlined />}
                onClick={() => handleOpenMongoCreateModal('standard')}
              >
                {t('New Collection / View')}
              </Button>
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
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginTop: 8 }}>
              <Input
                size={isMobile ? 'middle' : 'small'}
                allowClear
                prefix={<SearchOutlined />}
                placeholder={t('Filter tables / views / collections...')}
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                style={{ flex: 1, minWidth: 0 }}
              />
              {visibleSectionEntries.length > 0 && (
                <Tooltip title={allSectionsExpanded ? t('Collapse All') : t('Expand All')}>
                  <Button
                    size={isMobile ? 'middle' : 'small'}
                    type={allSectionsExpanded ? 'primary' : 'default'}
                    ghost={allSectionsExpanded}
                    icon={allSectionsExpanded ? <NodeCollapseOutlined /> : <NodeExpandOutlined />}
                    onClick={handleToggleExpandAll}
                    aria-label={allSectionsExpanded ? t('Collapse All') : t('Expand All')}
                    data-testid="sidebar-toggle-expand-all"
                    style={{ flexShrink: 0, paddingInline: isMobile ? 8 : 7, fontSize: 12 }}
                  >
                    {allSectionsExpanded ? t('Collapse All') : t('Expand All')}
                  </Button>
                </Tooltip>
              )}
            </div>
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
              {renderSectionHeader(
                'collections',
                t('Collections ({count})', { count: filteredColls.length }),
                filteredColls.length,
                selectedDB && canWrite ? (
                  <Tooltip title={t('New Collection / View')}>
                    <Button
                      type="link"
                      size="small"
                      icon={<PlusOutlined />}
                      style={isMobile ? { padding: '0 8px', height: 30 } : { padding: '0 4px', height: 22 }}
                      onClick={() => handleOpenMongoCreateModal('standard')}
                    >
                      {t('New')}
                    </Button>
                  </Tooltip>
                ) : undefined
              )}
              {collectionsExpanded && (
                <div style={{ paddingTop: collTopPad, paddingBottom: collBottomPad }}>
                  {visibleCollSlice.map((coll) => {
                    const isView = coll.type === 'view';
                    const isTimeSeries = coll.type === 'timeseries' || Boolean(coll.timeField);
                    const collMenuItems: any[] = [
                      {
                        key: 'open',
                        icon: isView ? <EyeOutlined /> : <DatabaseOutlined />,
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
                      ...(canWrite
                        ? [
                            { type: 'divider' as const },
                            {
                              key: 'drop',
                              danger: true,
                              icon: <DeleteOutlined />,
                              label: isView ? t('Drop View') : t('Drop Collection'),
                              onClick: () => handleDropMongoCollection(coll),
                            },
                          ]
                        : []),
                    ];
                    return (
                      <Dropdown
                        key={`${contextScopeKey}:${coll.name}`}
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
                            {isView ? (
                              <EyeOutlined style={{ color: '#722ed1', flexShrink: 0 }} />
                            ) : isTimeSeries ? (
                              <FieldTimeOutlined style={{ color: '#08979c', flexShrink: 0 }} />
                            ) : (
                              <DatabaseOutlined style={{ color: '#389e0d', flexShrink: 0 }} />
                            )}
                            <Typography.Text ellipsis style={{ flex: 1, minWidth: 0, fontSize: 13 }} title={coll.name}>
                              {coll.name}
                            </Typography.Text>
                          </div>
                          <Space size={4} style={{ flexShrink: 0 }} onClick={(e) => e.stopPropagation()}>
                            {isView ? (
                              <Tag color="purple" style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '16px' }}>
                                {t('View')}
                              </Tag>
                            ) : isTimeSeries ? (
                              <Tag color="cyan" style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '16px' }}>
                                TS
                              </Tag>
                            ) : coll.capped ? (
                              <Tag color="gold" style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '16px' }}>
                                {t('Capped')}
                              </Tag>
                            ) : null}
                            {!isView && <Tag style={{ margin: 0, fontSize: 11, flexShrink: 0 }}>{coll.count}</Tag>}
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
            <div style={{ paddingTop: flatTopPad, paddingBottom: flatBottomPad }}>
              {visibleFlatRows.map((row) => {
                if (row.kind === 'section_header') {
                  const isClosed = !row.expanded;
                  return (
                    <div
                      key={row.key}
                      data-section-key={row.sectionKey}
                      data-section-expanded={row.expanded ? 'true' : 'false'}
                      style={{
                        height: itemRowHeight,
                        display: 'flex',
                        justifyContent: 'space-between',
                        alignItems: 'center',
                        padding: '0 4px',
                        borderRadius: 6,
                        cursor: 'pointer',
                        userSelect: 'none',
                      }}
                      className="sidebar-item-row"
                      onClick={() => toggleObjectSection(row.sectionKey, row.count)}
                    >
                      <Space size={6} style={{ minWidth: 0, overflow: 'hidden' }}>
                        {isClosed ? (
                          <RightOutlined style={{ fontSize: 10, opacity: 0.65 }} />
                        ) : (
                          <DownOutlined style={{ fontSize: 10, opacity: 0.65 }} />
                        )}
                        <Typography.Text type="secondary" strong ellipsis style={{ fontSize: 12 }}>
                          {row.label}
                        </Typography.Text>
                      </Space>
                      {selectedDB && canWrite && row.createType && (
                        <div onClick={(e) => e.stopPropagation()}>
                          <Button
                            type="link"
                            size="small"
                            icon={<PlusOutlined />}
                            style={{ padding: '0 4px', height: 22 }}
                            onClick={() => openCreateObjectModal(row.createType!)}
                          />
                        </div>
                      )}
                    </div>
                  );
                }

                const { item, objectType } = row;
                const menuItems = buildObjectMenuItems(item, objectType);
                const isBrowsable =
                  objectType === 'TABLE' || objectType === 'VIEW' || objectType === 'MATERIALIZED_VIEW';

                const renderIcon = () => {
                  switch (objectType) {
                    case 'TABLE':
                      return <TableOutlined style={{ color: '#1677ff', flexShrink: 0 }} />;
                    case 'VIEW':
                      return <EyeOutlined style={{ color: '#722ed1', flexShrink: 0 }} />;
                    case 'MATERIALIZED_VIEW':
                      return <EyeOutlined style={{ color: '#13c2c2', flexShrink: 0 }} />;
                    case 'FUNCTION':
                    case 'PROCEDURE':
                      return <FunctionOutlined style={{ color: '#08979c', flexShrink: 0 }} />;
                    case 'TRIGGER':
                      return <ThunderboltOutlined style={{ color: '#d46b08', flexShrink: 0 }} />;
                    case 'SEQUENCE':
                      return <NumberOutlined style={{ color: '#389e0d', flexShrink: 0 }} />;
                    case 'EVENT':
                      return <ClockCircleOutlined style={{ color: '#eb2f96', flexShrink: 0 }} />;
                    case 'PACKAGE':
                    case 'PACKAGE_BODY':
                      return <AppstoreOutlined style={{ color: '#531dab', flexShrink: 0 }} />;
                    case 'TYPE':
                      return <TagsOutlined style={{ color: '#fa8c16', flexShrink: 0 }} />;
                    case 'SYNONYM':
                      return <ShareAltOutlined style={{ color: '#2f54eb', flexShrink: 0 }} />;
                    case 'EXTENSION':
                      return <BlockOutlined style={{ color: '#52c41a', flexShrink: 0 }} />;
                    case 'DICTIONARY':
                      return <BookOutlined style={{ color: '#0958d9', flexShrink: 0 }} />;
                    case 'DB_LINK':
                      return <LinkOutlined style={{ color: '#c41d7f', flexShrink: 0 }} />;
                    default:
                      return <CodeOutlined style={{ color: '#08979c', flexShrink: 0 }} />;
                  }
                };

                const handleRowClick = () => {
                  if (isBrowsable) {
                    openTab({
                      key: `data:${activeConn.id}:${selectedDB}:${selectedSchema}:${item.name}`,
                      title:
                        objectType === 'VIEW'
                          ? `${item.name} (${t('View')})`
                          : objectType === 'MATERIALIZED_VIEW'
                            ? `${item.name} (${t('MatView')})`
                            : item.name,
                      kind: 'table_data',
                      connectionId: activeConn.id,
                      connName: activeConn.name,
                      dbType: activeConn.type,
                      database: selectedDB,
                      schema: selectedSchema,
                      table: item.name,
                    });
                    onItemOpened?.();
                    return;
                  }
                  openEditObjectModal(item, objectType);
                };

                return (
                  <Dropdown key={row.key} trigger={['contextMenu']} menu={{ items: menuItems }}>
                    <div
                      onClick={handleRowClick}
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
                        {renderIcon()}
                        <Typography.Text ellipsis style={{ flex: 1, minWidth: 0, fontSize: 13 }} title={item.name}>
                          {item.name}
                        </Typography.Text>
                      </div>
                      <Space size={2} style={{ flexShrink: 0 }} onClick={(e) => e.stopPropagation()}>
                        {objectType === 'TABLE' && item.dataBytes > 0 && (
                          <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                            {formatBytes(item.dataBytes)}
                          </Typography.Text>
                        )}
                        {(objectType === 'FUNCTION' || objectType === 'PROCEDURE') && (
                          <Tag style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '16px' }}>
                            {objectType === 'PROCEDURE' ? 'PROC' : 'FUNC'}
                          </Tag>
                        )}
                        {objectType !== 'TABLE' && item.status && (
                          <Tag
                            color={
                              item.status.toUpperCase() === 'INVALID' || item.status.toUpperCase() === 'DISABLED'
                                ? 'orange'
                                : 'default'
                            }
                            style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '16px' }}
                          >
                            {item.status}
                          </Tag>
                        )}
                        {objectType === 'TRIGGER' && item.targetName && (
                          <Typography.Text
                            type="secondary"
                            ellipsis
                            style={{ fontSize: 10, maxWidth: 68 }}
                            title={item.targetName}
                          >
                            {item.targetName}
                          </Typography.Text>
                        )}
                        {objectType === 'TABLE' ? (
                          <Tooltip title={t('Table Structure')}>
                            <Button
                              type="text"
                              size="small"
                              style={isMobile ? { width: 30, height: 30, padding: 0 } : undefined}
                              icon={<EditOutlined style={{ fontSize: 12 }} />}
                              onClick={() => {
                                openTab({
                                  key: `struct:${activeConn.id}:${selectedDB}:${selectedSchema}:${item.name}`,
                                  title: `${item.name} [${t('Structure')}]`,
                                  kind: 'table_structure',
                                  connectionId: activeConn.id,
                                  connName: activeConn.name,
                                  dbType: activeConn.type,
                                  database: selectedDB,
                                  schema: selectedSchema,
                                  table: item.name,
                                });
                                onItemOpened?.();
                              }}
                            />
                          </Tooltip>
                        ) : (
                          <Tooltip title={canWrite ? t('Edit / DDL') : t('Inspect DDL')}>
                            <Button
                              type="text"
                              size="small"
                              style={isMobile ? { width: 30, height: 30, padding: 0 } : undefined}
                              icon={<CodeOutlined style={{ fontSize: 12 }} />}
                              onClick={() => openEditObjectModal(item, objectType)}
                            />
                          </Tooltip>
                        )}
                        {isMobile && (
                          <Dropdown trigger={['click']} placement="bottomRight" menu={{ items: menuItems }}>
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

      {/* MongoDB Create Collection / View Modal */}
      <Modal
        open={mongoCreateOpen}
        title={t('New Collection / View')}
        width="min(640px, calc(100vw - 16px))"
        onCancel={() => setMongoCreateOpen(false)}
        onOk={handleCreateMongoCollection}
        confirmLoading={mongoCreateSaving}
        okText={t('Create Now')}
      >
        <Form layout="vertical" size="small">
          <Form.Item label={t('Collection Mode')} style={{ marginBottom: 10 }}>
            {isMobile ? (
              <Select
                style={{ width: '100%' }}
                value={mongoCreateKind}
                onChange={(v) => setMongoCreateKind(v as 'standard' | 'capped' | 'timeseries' | 'view')}
                options={[
                  { value: 'standard', label: t('Standard') },
                  { value: 'capped', label: t('Capped') },
                  { value: 'timeseries', label: t('Time-Series') },
                  { value: 'view', label: t('MongoDB View') },
                ]}
              />
            ) : (
              <Segmented
                block
                value={mongoCreateKind}
                onChange={(v) => setMongoCreateKind(v as 'standard' | 'capped' | 'timeseries' | 'view')}
                options={[
                  { value: 'standard', label: t('Standard') },
                  { value: 'capped', label: t('Capped') },
                  { value: 'timeseries', label: t('Time-Series') },
                  { value: 'view', label: t('MongoDB View') },
                ]}
              />
            )}
          </Form.Item>
          <Form.Item
            label={mongoCreateKind === 'view' ? t('View Name') : t('Collection Name')}
            required
            style={{ marginBottom: 10 }}
          >
            <Input
              value={mongoCreateName}
              onChange={(e) => setMongoCreateName(e.target.value)}
              placeholder={mongoCreateKind === 'view' ? 'v_recent_events' : 'events'}
            />
          </Form.Item>

          {mongoCreateKind === 'capped' && (
            <Row gutter={[10, 8]}>
              <Col xs={24} sm={12}>
                <Form.Item label={t('Max Size (Bytes)')} required style={{ marginBottom: 10 }}>
                  <InputNumber
                    min={4096}
                    value={mongoMaxBytes}
                    onChange={setMongoMaxBytes}
                    style={{ width: '100%' }}
                  />
                </Form.Item>
              </Col>
              <Col xs={24} sm={12}>
                <Form.Item label={t('Max Documents (Optional)')} style={{ marginBottom: 10 }}>
                  <InputNumber min={1} value={mongoMaxDocs} onChange={setMongoMaxDocs} style={{ width: '100%' }} />
                </Form.Item>
              </Col>
            </Row>
          )}

          {mongoCreateKind === 'timeseries' && (
            <Row gutter={[10, 8]}>
              <Col xs={24} sm={12}>
                <Form.Item label={t('Time Field')} required style={{ marginBottom: 10 }}>
                  <Input
                    value={mongoTimeField}
                    onChange={(e) => setMongoTimeField(e.target.value)}
                    placeholder="timestamp"
                  />
                </Form.Item>
              </Col>
              <Col xs={24} sm={12}>
                <Form.Item label={t('Meta Field (Optional)')} style={{ marginBottom: 10 }}>
                  <Input
                    value={mongoMetaField}
                    onChange={(e) => setMongoMetaField(e.target.value)}
                    placeholder="metadata"
                  />
                </Form.Item>
              </Col>
              <Col xs={12}>
                <Form.Item label={t('Granularity')} style={{ marginBottom: 10 }}>
                  <Select
                    value={mongoGranularity}
                    onChange={setMongoGranularity}
                    options={[
                      { value: 'seconds', label: 'seconds' },
                      { value: 'minutes', label: 'minutes' },
                      { value: 'hours', label: 'hours' },
                    ]}
                  />
                </Form.Item>
              </Col>
              <Col xs={12}>
                <Form.Item label={t('TTL (expireAfterSeconds)')} style={{ marginBottom: 10 }}>
                  <InputNumber min={1} value={mongoExpireSec} onChange={setMongoExpireSec} style={{ width: '100%' }} />
                </Form.Item>
              </Col>
            </Row>
          )}

          {mongoCreateKind === 'view' && (
            <>
              <Form.Item label={t('Source Collection (viewOn)')} required style={{ marginBottom: 10 }}>
                <Select
                  showSearch
                  value={mongoViewOn || undefined}
                  onChange={setMongoViewOn}
                  placeholder={t('Select source collection')}
                  options={collections
                    .filter((c) => c.type !== 'view')
                    .map((c) => ({ label: c.name, value: c.name }))}
                />
              </Form.Item>
              <Form.Item label={t('Aggregation Pipeline (JSON Array)')} required style={{ marginBottom: 0 }}>
                <div style={{ border: '1px solid rgba(128,128,128,0.25)', borderRadius: 8, overflow: 'hidden' }}>
                  <LazyCodeEditor value={mongoPipelineJson} onChange={setMongoPipelineJson} language="json" height={180} darkMode={darkMode} />
                </div>
              </Form.Item>
            </>
          )}

          {mongoCreateKind === 'standard' && (
            <Form.Item label={t('JSON Schema Validator (Optional)')} style={{ marginBottom: 0 }}>
              <div style={{ border: '1px solid rgba(128,128,128,0.25)', borderRadius: 8, overflow: 'hidden' }}>
                <LazyCodeEditor value={mongoValidatorJson} onChange={setMongoValidatorJson} language="json" height={150} darkMode={darkMode} />
              </div>
            </Form.Item>
          )}
        </Form>
      </Modal>

      {activeConn && schemaObjModal.open && (
        <Suspense fallback={null}>
          <LazySchemaObjectModal
            open={schemaObjModal.open}
            mode={schemaObjModal.mode}
            initialType={schemaObjModal.initialType}
            connId={activeConn.id}
            dbType={activeConn.type}
            database={selectedDB}
            schema={selectedSchema}
            tables={(schemaObjects.tables || []).map((tbl) => tbl.name)}
            availableExtensions={schemaObjects.extensions || []}
            capabilities={activeDriver?.capabilities}
            canWrite={canWrite}
            isDesktop={isDesktop}
            userRole={role}
            editObject={schemaObjModal.editObject}
            onClose={() => setSchemaObjModal((prev) => ({ ...prev, open: false }))}
            onSuccess={() => {
              invalidateConnCache(activeConn.id);
              bumpTreeRefresh();
            }}
          />
        </Suspense>
      )}
    </div>
  );
});
