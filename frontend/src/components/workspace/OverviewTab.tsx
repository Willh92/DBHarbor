import React, { Suspense, useCallback, useDeferredValue, useEffect, useMemo, useState } from 'react';
import {
  Button,
  Card,
  Col,
  Dropdown,
  Form,
  Input,
  Modal,
  Popconfirm,
  Radio,
  Row,
  Segmented,
  Select,
  Space,
  Statistic,
  Tag,
  Tooltip,
  Typography,
  message,
} from 'antd';
import {
  CheckCircleOutlined,
  ClearOutlined,
  CodeOutlined,
  CopyOutlined,
  DeleteOutlined,
  DownOutlined,
  DownloadOutlined,
  EditOutlined,
  MoreOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
  PlaySquareOutlined,
  PlusOutlined,
  ReloadOutlined,
  SearchOutlined,
  SyncOutlined,
  TableOutlined,
  ToolOutlined,
  UploadOutlined,
} from '@ant-design/icons';
import { api, isAuthPromptCancelledError } from '../../api/client';
import { useResponsive } from '../../hooks/useResponsive';
import { useI18n } from '../../i18n/useI18n';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import type { ObjectItem, ObjectSpec, WorkspaceTab } from '../../types';
import { formatBytes } from '../../utils/format';
import { ResizableTable, calcSmartColumnWidth } from '../common/ResizableTable';
import { ContextNavBar, type TransferModalMode, type TransferOpenOptions } from './ContextNavBar';
import type { SchemaObjectType } from './SchemaObjectModal';

const LazySchemaObjectModal = React.lazy(() => import('./SchemaObjectModal'));

interface Props {
  tab: WorkspaceTab;
  onOpenCreateTable: () => void;
  onOpenTransfer: (mode: TransferModalMode, tableName?: string, opts?: TransferOpenOptions) => void;
}

type CategoryFilterKey =
  | 'ALL'
  | 'TABLE'
  | 'VIEW'
  | 'MATERIALIZED_VIEW'
  | 'ROUTINE'
  | 'TRIGGER'
  | 'SEQUENCE'
  | 'EVENT'
  | 'PACKAGE'
  | 'TYPE'
  | 'SYNONYM'
  | 'EXTENSION'
  | 'DICTIONARY'
  | 'DB_LINK'
  | 'MACRO';

function getCategoryForType(tp: string): CategoryFilterKey {
  const up = (tp || 'TABLE').toUpperCase();
  if (up === 'FUNCTION' || up === 'PROCEDURE') return 'ROUTINE';
  if (up === 'PACKAGE_BODY') return 'PACKAGE';
  if (up === 'TABLE_MACRO') return 'MACRO';
  return (up as CategoryFilterKey) || 'TABLE';
}

export const OverviewTab: React.FC<Props> = React.memo(({ tab, onOpenCreateTable, onOpenTransfer }) => {
  const { openTab, drivers, bumpTreeRefresh, treeRefreshTick, isDesktop, role } = useWorkbenchStore();
  const { isMobile } = useResponsive();
  const { t } = useI18n();
  const canWrite = isDesktop || role !== 'readonly';
  const driverMeta = useMemo(() => drivers.find((d) => d.type === tab.dbType), [drivers, tab.dbType]);

  const [loading, setLoading] = useState(false);
  const [items, setItems] = useState<ObjectItem[]>([]);
  const [availableExtensions, setAvailableExtensions] = useState<ObjectItem[]>([]);
  const [filter, setFilter] = useState('');
  const [categoryFilter, setCategoryFilter] = useState<CategoryFilterKey>('ALL');
  const [selectedKeys, setSelectedKeys] = useState<React.Key[]>([]);
  const [batchRunning, setBatchRunning] = useState(false);

  // Unified SchemaObjectModal state
  const [schemaModal, setSchemaModal] = useState<{
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

  // Clone / Rename Table Modal
  const [cloneTarget, setCloneTarget] = useState<ObjectItem | null>(null);
  const [cloneForm] = Form.useForm();
  const [cloneSaving, setCloneSaving] = useState(false);

  // Edit Table Comment Modal
  const [commentTarget, setCommentTarget] = useState<ObjectItem | null>(null);
  const [commentText, setCommentText] = useState('');

  const loadOverview = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api.listObjects(tab.connectionId, tab.database, tab.schema || '');
      const allExts = res.extensions || [];
      setAvailableExtensions(allExts);
      const installedExts = allExts
        .filter((ext) => ext.status?.toUpperCase() !== 'AVAILABLE' && ext.extra?.installed !== 'false')
        .map((x) => ({ ...x, type: x.type || 'EXTENSION' }));

      const merged: ObjectItem[] = [
        ...(res.tables || []).map((x) => ({ ...x, type: x.type || 'TABLE' })),
        ...(res.views || []).map((x) => ({ ...x, type: x.type || 'VIEW' })),
        ...(res.materializedViews || []).map((x) => ({ ...x, type: x.type || 'MATERIALIZED_VIEW' })),
        ...(res.routines || []).map((x) => ({ ...x, type: x.type || 'FUNCTION' })),
        ...(res.triggers || []).map((x) => ({ ...x, type: x.type || 'TRIGGER' })),
        ...(res.sequences || []).map((x) => ({ ...x, type: x.type || 'SEQUENCE' })),
        ...(res.events || []).map((x) => ({ ...x, type: x.type || 'EVENT' })),
        ...(res.packages || []).map((x) => ({ ...x, type: x.type || 'PACKAGE' })),
        ...(res.types || []).map((x) => ({ ...x, type: x.type || 'TYPE' })),
        ...(res.synonyms || []).map((x) => ({ ...x, type: x.type || 'SYNONYM' })),
        ...installedExts,
        ...(res.dictionaries || []).map((x) => ({ ...x, type: x.type || 'DICTIONARY' })),
        ...(res.dbLinks || []).map((x) => ({ ...x, type: x.type || 'DB_LINK' })),
        ...(res.macros || []).map((x) => ({ ...x, type: x.type || 'MACRO' })),
      ];
      setItems(merged);
      setSelectedKeys([]);
    } catch (err: any) {
      if (!isAuthPromptCancelledError(err)) {
        message.error(err.message);
      }
    } finally {
      setLoading(false);
    }
  }, [tab.connectionId, tab.database, tab.schema]);

  useEffect(() => {
    setSelectedKeys([]);
  }, [tab.connectionId, tab.database, tab.schema]);

  useEffect(() => {
    void loadOverview();
  }, [loadOverview, treeRefreshTick]);

  const handleAction = useCallback(
    async (record: ObjectItem, action: string, spec?: ObjectSpec) => {
      try {
        await api.objectAction(tab.connectionId, {
          database: tab.database,
          schema: tab.schema,
          objectName: record.name,
          objectType: record.type,
          targetName: record.targetName || undefined,
          action,
          spec,
        });
        message.success(t('Executed {action} on {name}', { action, name: record.name }));
        bumpTreeRefresh();
        await loadOverview();
      } catch (err: any) {
        message.error(err.message);
      }
    },
    [tab.connectionId, tab.database, tab.schema, t, bumpTreeRefresh, loadOverview]
  );

  const handleSaveComment = async () => {
    if (!commentTarget) return;
    try {
      await api.objectAction(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        objectName: commentTarget.name,
        objectType: 'TABLE',
        action: 'COMMENT',
        comment: commentText,
      });
      message.success(t('Updated comment on {name}', { name: commentTarget.name }));
      setCommentTarget(null);
      await loadOverview();
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleCloneOrRename = async () => {
    if (!cloneTarget) return;
    try {
      const vals = await cloneForm.validateFields();
      setCloneSaving(true);
      await api.objectAction(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        objectName: cloneTarget.name,
        objectType: cloneTarget.type,
        action: vals.mode,
        targetName: vals.targetName.trim(),
      });
      message.success(t('Table operation completed -> {name}', { name: vals.targetName }));
      setCloneTarget(null);
      cloneForm.resetFields();
      bumpTreeRefresh();
      await loadOverview();
    } catch (err: any) {
      if (!err?.errorFields) message.error(err.message);
    } finally {
      setCloneSaving(false);
    }
  };

  const deferredFilter = useDeferredValue(filter);
  const { totalTables, totalViews, totalRows, totalSize, tableNames, categoryCounts } = useMemo(() => {
    let tbls = 0;
    let vws = 0;
    let rws = 0;
    let sz = 0;
    const tNames: string[] = [];
    const counts: Partial<Record<CategoryFilterKey, number>> = {};
    for (const i of items) {
      const cat = getCategoryForType(i.type);
      counts[cat] = (counts[cat] || 0) + 1;
      if (i.type === 'TABLE') {
        tbls++;
        tNames.push(i.name);
      } else if (i.type === 'VIEW' || i.type === 'MATERIALIZED_VIEW') {
        vws++;
      }
      rws += i.rows || 0;
      sz += (i.dataBytes || 0) + (i.indexBytes || 0);
    }
    return {
      totalTables: tbls,
      totalViews: vws,
      totalRows: rws,
      totalSize: sz,
      tableNames: tNames,
      categoryCounts: counts,
    };
  }, [items]);

  const categoryOptions = useMemo(() => {
    const defs: { key: CategoryFilterKey; label: string; always?: boolean }[] = [
      { key: 'TABLE', label: t('Tables'), always: true },
      { key: 'VIEW', label: t('Views') },
      { key: 'MATERIALIZED_VIEW', label: t('Materialized Views') },
      { key: 'ROUTINE', label: t('Routines') },
      { key: 'TRIGGER', label: t('Triggers') },
      { key: 'SEQUENCE', label: t('Sequences') },
      { key: 'EVENT', label: t('Events') },
      { key: 'PACKAGE', label: t('Packages') },
      { key: 'TYPE', label: t('Types') },
      { key: 'SYNONYM', label: t('Synonyms') },
      { key: 'EXTENSION', label: t('Extensions') },
      { key: 'DICTIONARY', label: t('Dictionaries') },
      { key: 'DB_LINK', label: t('DB Links') },
      { key: 'MACRO', label: t('Macros') },
    ];
    const opts: { value: CategoryFilterKey; label: string }[] = [
      { value: 'ALL', label: `${t('All')} (${items.length})` },
    ];
    for (const d of defs) {
      const cnt = categoryCounts[d.key] || 0;
      if (d.always || cnt > 0) {
        opts.push({ value: d.key, label: `${d.label} (${cnt})` });
      }
    }
    return opts;
  }, [items.length, categoryCounts, t]);

  const creatableMenuItems = useMemo(() => {
    const capTypes = (driverMeta?.capabilities?.objectTypes || ['VIEW']).filter((tp) => tp !== 'TABLE');
    const labels: Record<string, string> = {
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
      .filter((tp) => Boolean(labels[tp]))
      .map((tp) => ({
        key: tp,
        icon: <PlusOutlined />,
        label: labels[tp] || tp,
        onClick: () =>
          setSchemaModal({
            open: true,
            mode: 'create',
            initialType: tp,
            editObject: null,
          }),
      }));
  }, [driverMeta, t]);

  const filtered = useMemo(() => {
    const q = deferredFilter.trim().toLowerCase();
    return items.filter((i) => {
      if (categoryFilter !== 'ALL' && getCategoryForType(i.type) !== categoryFilter) {
        return false;
      }
      return q ? i.name.toLowerCase().includes(q) : true;
    });
  }, [items, deferredFilter, categoryFilter]);

  const makeRowKey = useCallback(
    (r: ObjectItem) => `${r.type}:${r.targetName || ''}:${r.name}`,
    []
  );

  const selectedObjects = useMemo(() => {
    if (selectedKeys.length === 0) return [];
    const keySet = new Set(selectedKeys.map(String));
    return items.filter((item) => keySet.has(makeRowKey(item)));
  }, [items, selectedKeys, makeRowKey]);

  const selectedTableNames = useMemo(
    () => selectedObjects.filter((o) => o.type === 'TABLE').map((o) => o.name),
    [selectedObjects]
  );

  const handleBatchMaintenance = async (action: 'OPTIMIZE' | 'TRUNCATE' | 'DROP') => {
    const targets = action === 'DROP' ? selectedObjects : selectedObjects.filter((o) => o.type === 'TABLE');
    if (targets.length === 0) return;
    setBatchRunning(true);
    let succeeded = 0;
    try {
      for (const obj of targets) {
        await api.objectAction(tab.connectionId, {
          database: tab.database,
          schema: tab.schema,
          objectName: obj.name,
          objectType: obj.type,
          targetName: obj.targetName || undefined,
          action,
        });
        succeeded++;
      }
      message.success(t('Batch {action} completed ({count} objects)', { action, count: succeeded }));
      bumpTreeRefresh();
      await loadOverview();
    } catch (err: any) {
      message.error(err.message);
      if (succeeded > 0) {
        bumpTreeRefresh();
        await loadOverview();
      }
    } finally {
      setBatchRunning(false);
    }
  };

  const openObjectEditor = useCallback((r: ObjectItem) => {
    setSchemaModal({
      open: true,
      mode: 'edit',
      initialType: r.type,
      editObject: r,
    });
  }, []);

  const overviewColumns = useMemo(
    () => [
      {
        title: t('Object Name'),
        dataIndex: 'name',
        width: calcSmartColumnWidth(t('Object Name'), { hasSorter: true, minBodyWidth: 210 }),
        sorter: (a: ObjectItem, b: ObjectItem) => a.name.localeCompare(b.name),
        render: (name: string, r: ObjectItem) => {
          const isDataObj = r.type === 'TABLE' || r.type === 'VIEW' || r.type === 'MATERIALIZED_VIEW';
          const tagColor =
            r.type === 'TABLE'
              ? 'blue'
              : r.type === 'VIEW'
                ? 'purple'
                : r.type === 'MATERIALIZED_VIEW'
                  ? 'cyan'
                  : r.type === 'TRIGGER'
                    ? 'orange'
                    : r.type === 'SEQUENCE'
                      ? 'green'
                      : r.type === 'EVENT'
                        ? 'magenta'
                        : 'geekblue';
          return (
            <div style={{ display: 'inline-flex', alignItems: 'center', gap: 6, whiteSpace: 'nowrap' }}>
              <Typography.Link
                strong
                onClick={() => {
                  if (isDataObj) {
                    openTab({
                      key: `data:${tab.connectionId}:${tab.database}:${tab.schema || ''}:${name}`,
                      title: name,
                      kind: 'table_data',
                      connectionId: tab.connectionId,
                      connName: tab.connName,
                      dbType: tab.dbType,
                      database: tab.database,
                      schema: tab.schema,
                      table: name,
                    });
                  } else {
                    openObjectEditor(r);
                  }
                }}
              >
                {name}
              </Typography.Link>
              <Tag color={tagColor} style={{ fontSize: 10, margin: 0 }}>
                {r.type}
              </Tag>
              {r.targetName && (
                <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                  ({r.targetName})
                </Typography.Text>
              )}
            </div>
          );
        },
      },
      {
        title: t('Status / Engine'),
        dataIndex: 'status',
        width: calcSmartColumnWidth(t('Status / Engine'), { minBodyWidth: 115 }),
        render: (_: string, r: ObjectItem) => {
          const st = (r.status || '').trim();
          const up = st.toUpperCase();
          const statusColor =
            up === 'ENABLED' || up === 'VALID' || up === 'POPULATED' || up === 'LOADED'
              ? 'success'
              : up === 'DISABLED' || up === 'INVALID'
                ? 'warning'
                : 'default';
          if (st && r.engine) {
            return (
              <Space size={4}>
                <Tag color={statusColor} style={{ margin: 0 }}>{st}</Tag>
                <Tag style={{ margin: 0 }}>{r.engine}</Tag>
              </Space>
            );
          }
          if (st) return <Tag color={statusColor} style={{ margin: 0 }}>{st}</Tag>;
          if (r.engine) return <Tag style={{ margin: 0 }}>{r.engine}</Tag>;
          return '-';
        },
      },
      {
        title: t('Rows'),
        dataIndex: 'rows',
        width: calcSmartColumnWidth(t('Rows'), { hasSorter: true, minBodyWidth: 80 }),
        align: 'right' as const,
        sorter: (a: ObjectItem, b: ObjectItem) => (a.rows || 0) - (b.rows || 0),
        render: (v: number, r: ObjectItem) =>
          r.type === 'TABLE' || r.type === 'VIEW' || r.type === 'MATERIALIZED_VIEW'
            ? v?.toLocaleString?.() ?? 0
            : '-',
      },
      {
        title: t('Size'),
        dataIndex: 'dataBytes',
        width: calcSmartColumnWidth(t('Size'), { hasSorter: true, minBodyWidth: 90 }),
        align: 'right' as const,
        sorter: (a: ObjectItem, b: ObjectItem) => (a.dataBytes || 0) - (b.dataBytes || 0),
        render: (_: number, r: ObjectItem) =>
          r.type === 'TABLE' || r.type === 'MATERIALIZED_VIEW'
            ? formatBytes((r.dataBytes || 0) + (r.indexBytes || 0))
            : '-',
      },
      {
        title: t('Collation / Comment'),
        dataIndex: 'comment',
        width: calcSmartColumnWidth(t('Collation / Comment'), { minBodyWidth: 145 }),
        ellipsis: true,
        render: (_: string, r: ObjectItem) => r.comment || r.collation || '-',
      },
      {
        title: t('Quick Actions'),
        key: 'actions',
        width: isMobile ? 210 : 310,
        render: (_: any, r: ObjectItem) => {
          const isDataObj = r.type === 'TABLE' || r.type === 'VIEW' || r.type === 'MATERIALIZED_VIEW';
          const btnStyle = isMobile ? { minHeight: 32, minWidth: 32 } : undefined;
          const writeTableItems: any[] =
            canWrite && r.type === 'TABLE'
              ? [
                  {
                    key: 'clone',
                    icon: <CopyOutlined />,
                    label: t('Clone or rename table'),
                    onClick: () => {
                      setCloneTarget(r);
                      cloneForm.setFieldsValue({
                        mode: 'COPY_DATA',
                        targetName: `${r.name}_copy`,
                      });
                    },
                  },
                  {
                    key: 'analyze',
                    icon: <ToolOutlined />,
                    label: t('Analyze Table'),
                    onClick: () => void handleAction(r, 'ANALYZE'),
                  },
                  {
                    key: 'optimize',
                    icon: <ToolOutlined />,
                    label: t('Optimize / Vacuum Table'),
                    onClick: () => void handleAction(r, 'OPTIMIZE'),
                  },
                  {
                    key: 'comment',
                    icon: <EditOutlined />,
                    label: t('Edit Table Comment'),
                    onClick: () => {
                      setCommentTarget(r);
                      setCommentText(r.comment || '');
                    },
                  },
                ]
              : [];

          const lifecycleMenuItems: any[] = [];
          if (canWrite) {
            if (r.type === 'MATERIALIZED_VIEW') {
              lifecycleMenuItems.push({
                key: 'refresh-mv',
                icon: <SyncOutlined />,
                label: t('Refresh View'),
                onClick: () => void handleAction(r, 'REFRESH'),
              });
            }
            if (r.type === 'EVENT') {
              const isDisabled = r.status?.toUpperCase() === 'DISABLED';
              lifecycleMenuItems.push({
                key: 'toggle-event',
                icon: isDisabled ? <PlayCircleOutlined /> : <PauseCircleOutlined />,
                label: isDisabled ? t('Enable') : t('Disable'),
                onClick: () => void handleAction(r, isDisabled ? 'ENABLE' : 'DISABLE'),
              });
            }
            if (r.type === 'DICTIONARY') {
              lifecycleMenuItems.push({
                key: 'reload-dict',
                icon: <ReloadOutlined />,
                label: t('Reload Dictionary'),
                onClick: () => void handleAction(r, 'RELOAD'),
              });
            }
            if (r.type === 'PACKAGE' || r.type === 'PACKAGE_BODY') {
              lifecycleMenuItems.push({
                key: 'compile-pkg',
                icon: <CheckCircleOutlined />,
                label: t('Compile'),
                onClick: () => void handleAction(r, 'COMPILE'),
              });
            }
            if (r.type === 'SEQUENCE') {
              lifecycleMenuItems.push({
                key: 'restart-seq',
                icon: <ReloadOutlined />,
                label: t('Restart Sequence'),
                onClick: () => void handleAction(r, 'RESTART', { restartWith: 1 }),
              });
            }
          }

          const mobileTopDataItems: any[] =
            isMobile && isDataObj
              ? [
                  {
                    key: 'sql',
                    icon: <CodeOutlined />,
                    label: t('Open SQL Console for this table'),
                    onClick: () =>
                      openTab({
                        key: `sql:${tab.connectionId}:${tab.database}:${r.name}`,
                        title: `SQL - ${r.name}`,
                        kind: 'sql_console',
                        connectionId: tab.connectionId,
                        connName: tab.connName,
                        dbType: tab.dbType,
                        database: tab.database,
                        schema: tab.schema,
                        table: r.name,
                        initialSQL: `SELECT * FROM ${r.name} LIMIT 100;`,
                      }),
                  },
                  ...(r.type === 'TABLE'
                    ? [
                        {
                          key: 'export',
                          icon: <DownloadOutlined />,
                          label: t('Export table'),
                          onClick: () => onOpenTransfer('export', r.name),
                        },
                      ]
                    : []),
                ]
              : [];

          const mobileDangerItems: any[] =
            isMobile && canWrite
              ? [
                  ...(r.type === 'TABLE'
                    ? [
                        {
                          key: 'truncate',
                          danger: true,
                          icon: <ClearOutlined />,
                          label: t('Truncate table'),
                          onClick: () => {
                            Modal.confirm({
                              title: t('Truncate all rows in table {name}?', { name: r.name }),
                              okType: 'danger',
                              onOk: () => handleAction(r, 'TRUNCATE'),
                            });
                          },
                        },
                      ]
                    : []),
                  {
                    key: 'drop',
                    danger: true,
                    icon: <DeleteOutlined />,
                    label: t('Drop object'),
                    onClick: () => {
                      Modal.confirm({
                        title: t('Danger: Permanently drop {name}?', { name: r.name }),
                        okType: 'danger',
                        onOk: () => handleAction(r, 'DROP'),
                      });
                    },
                  },
                ]
              : [];

          const moreMenuItems: any[] = [
            ...lifecycleMenuItems,
            ...(lifecycleMenuItems.length > 0 && (mobileTopDataItems.length > 0 || writeTableItems.length > 0)
              ? [{ type: 'divider' as const }]
              : []),
            ...mobileTopDataItems,
            ...(mobileTopDataItems.length > 0 && writeTableItems.length > 0 ? [{ type: 'divider' as const }] : []),
            ...writeTableItems,
            ...(mobileDangerItems.length > 0 &&
            (lifecycleMenuItems.length > 0 || mobileTopDataItems.length > 0 || writeTableItems.length > 0)
              ? [{ type: 'divider' as const }]
              : []),
            ...mobileDangerItems,
          ];

          return (
            <Space size={isMobile ? 4 : 2} style={{ whiteSpace: 'nowrap' }}>
              {isDataObj ? (
                <>
                  <Button
                    size="small"
                    type="link"
                    style={btnStyle}
                    icon={<TableOutlined />}
                    onClick={() =>
                      openTab({
                        key: `data:${tab.connectionId}:${tab.database}:${tab.schema || ''}:${r.name}`,
                        title: r.name,
                        kind: 'table_data',
                        connectionId: tab.connectionId,
                        connName: tab.connName,
                        dbType: tab.dbType,
                        database: tab.database,
                        schema: tab.schema,
                        table: r.name,
                      })
                    }
                  >
                    {t('Browse')}
                  </Button>
                  {r.type === 'TABLE' ? (
                    <Button
                      size="small"
                      type="link"
                      style={btnStyle}
                      icon={<EditOutlined />}
                      onClick={() =>
                        openTab({
                          key: `struct:${tab.connectionId}:${tab.database}:${tab.schema || ''}:${r.name}`,
                          title: `${r.name} [${t('Structure')}]`,
                          kind: 'table_structure',
                          connectionId: tab.connectionId,
                          connName: tab.connName,
                          dbType: tab.dbType,
                          database: tab.database,
                          schema: tab.schema,
                          table: r.name,
                        })
                      }
                    >
                      {t('Structure')}
                    </Button>
                  ) : (
                    <Button
                      size="small"
                      type="link"
                      style={btnStyle}
                      icon={<CodeOutlined />}
                      onClick={() => openObjectEditor(r)}
                    >
                      {canWrite ? t('Edit / DDL') : t('Inspect DDL')}
                    </Button>
                  )}
                  {!isMobile && canWrite && r.type === 'MATERIALIZED_VIEW' && (
                    <Tooltip title={t('Refresh View')}>
                      <Button
                        size="small"
                        type="text"
                        icon={<SyncOutlined />}
                        onClick={() => void handleAction(r, 'REFRESH')}
                      />
                    </Tooltip>
                  )}
                  {!isMobile && (
                    <Tooltip title={t('Open SQL Console for this table')}>
                      <Button
                        size="small"
                        type="text"
                        icon={<CodeOutlined />}
                        onClick={() =>
                          openTab({
                            key: `sql:${tab.connectionId}:${tab.database}:${r.name}`,
                            title: `SQL - ${r.name}`,
                            kind: 'sql_console',
                            connectionId: tab.connectionId,
                            connName: tab.connName,
                            dbType: tab.dbType,
                            database: tab.database,
                            schema: tab.schema,
                            table: r.name,
                            initialSQL: `SELECT * FROM ${r.name} LIMIT 100;`,
                          })
                        }
                      />
                    </Tooltip>
                  )}
                  {!isMobile && r.type === 'TABLE' && (
                    <Tooltip title={t('Export table')}>
                      <Button
                        size="small"
                        type="text"
                        icon={<DownloadOutlined />}
                        onClick={() => onOpenTransfer('export', r.name)}
                      />
                    </Tooltip>
                  )}
                </>
              ) : (
                <>
                  <Button
                    size="small"
                    type="link"
                    style={btnStyle}
                    icon={<EditOutlined />}
                    onClick={() => openObjectEditor(r)}
                  >
                    {canWrite ? t('Edit / DDL') : t('Inspect DDL')}
                  </Button>
                  {!isMobile && canWrite && r.type === 'EVENT' && (
                    <Button
                      size="small"
                      type="link"
                      icon={
                        r.status?.toUpperCase() === 'DISABLED' ? <PlayCircleOutlined /> : <PauseCircleOutlined />
                      }
                      onClick={() =>
                        void handleAction(r, r.status?.toUpperCase() === 'DISABLED' ? 'ENABLE' : 'DISABLE')
                      }
                    >
                      {r.status?.toUpperCase() === 'DISABLED' ? t('Enable') : t('Disable')}
                    </Button>
                  )}
                  {!isMobile && canWrite && r.type === 'DICTIONARY' && (
                    <Button
                      size="small"
                      type="link"
                      icon={<ReloadOutlined />}
                      onClick={() => void handleAction(r, 'RELOAD')}
                    >
                      {t('Reload')}
                    </Button>
                  )}
                  {!isMobile && canWrite && (r.type === 'PACKAGE' || r.type === 'PACKAGE_BODY') && (
                    <Button
                      size="small"
                      type="link"
                      icon={<CheckCircleOutlined />}
                      onClick={() => void handleAction(r, 'COMPILE')}
                    >
                      {t('Compile')}
                    </Button>
                  )}
                  {!isMobile && canWrite && r.type === 'SEQUENCE' && (
                    <Button
                      size="small"
                      type="link"
                      icon={<ReloadOutlined />}
                      onClick={() => void handleAction(r, 'RESTART', { restartWith: 1 })}
                    >
                      {t('Restart')}
                    </Button>
                  )}
                </>
              )}

              {!isMobile && canWrite && r.type === 'TABLE' && (
                <Tooltip title={t('Clone or rename table')}>
                  <Button
                    size="small"
                    type="text"
                    icon={<CopyOutlined />}
                    onClick={() => {
                      setCloneTarget(r);
                      cloneForm.setFieldsValue({
                        mode: 'COPY_DATA',
                        targetName: `${r.name}_copy`,
                      });
                    }}
                  />
                </Tooltip>
              )}
              {moreMenuItems.length > 0 && (
                <Dropdown trigger={['click']} placement="bottomRight" menu={{ items: moreMenuItems }}>
                  <Button size="small" type="text" style={btnStyle} icon={<MoreOutlined />} />
                </Dropdown>
              )}
              {!isMobile && canWrite && r.type === 'TABLE' && (
                <Popconfirm
                  title={t('Truncate all rows in table {name}?', { name: r.name })}
                  onConfirm={() => void handleAction(r, 'TRUNCATE')}
                >
                  <Tooltip title={t('Truncate table')}>
                    <Button size="small" type="text" danger icon={<ClearOutlined />} />
                  </Tooltip>
                </Popconfirm>
              )}
              {!isMobile && canWrite && (
                <Popconfirm
                  title={t('Danger: Permanently drop {name}?', { name: r.name })}
                  onConfirm={() => void handleAction(r, 'DROP')}
                >
                  <Tooltip title={t('Drop object')}>
                    <Button size="small" type="text" danger icon={<DeleteOutlined />} />
                  </Tooltip>
                </Popconfirm>
              )}
            </Space>
          );
        },
      },
    ],
    [
      t,
      canWrite,
      isMobile,
      tab.connectionId,
      tab.database,
      tab.schema,
      tab.connName,
      tab.dbType,
      openTab,
      onOpenTransfer,
      cloneForm,
      handleAction,
      openObjectEditor,
    ]
  );

  return (
    <div style={{ padding: isMobile ? '6px 8px' : '12px 16px', height: '100%', minWidth: 0, overflowY: 'auto', overflowX: 'hidden' }}>
      <ContextNavBar tab={tab} activeSection="overview" onOpenTransfer={onOpenTransfer} />

      <Row gutter={[isMobile ? 8 : 12, isMobile ? 6 : 10]} className="dbharbor-kpi-row" style={{ marginBottom: isMobile ? 8 : 12 }}>
        <Col xs={12} md={6}>
          <Card size="small">
            <Statistic
              title={totalViews > 0 ? `${t('Tables')} / ${t('Views')}` : t('Tables')}
              value={totalTables}
              suffix={totalViews > 0 ? `/ ${totalViews}` : ''}
            />
          </Card>
        </Col>
        <Col xs={12} md={6}>
          <Card size="small">
            <Statistic title={t('Total Rows')} value={totalRows} />
          </Card>
        </Col>
        <Col xs={12} md={6}>
          <Card size="small">
            <Statistic title={t('Estimated Size')} value={formatBytes(totalSize)} />
          </Card>
        </Col>
        <Col xs={12} md={6}>
          <Card size="small">
            <div title={`${tab.database}${tab.schema ? '.' + tab.schema : ''}`} style={{ minWidth: 0, overflow: 'hidden' }}>
              <Statistic
                title={t('Current Context')}
                value={`${tab.database}${tab.schema ? '.' + tab.schema : ''}`}
                valueStyle={{
                  fontSize: isMobile ? 14 : 16,
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                  whiteSpace: 'nowrap',
                }}
              />
            </div>
          </Card>
        </Col>
      </Row>

      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          marginBottom: 8,
          flexWrap: 'wrap',
          gap: 6,
        }}
      >
        <Space wrap size={6}>
          {canWrite && (
            <>
              <Button
                size={isMobile ? 'small' : 'middle'}
                style={isMobile ? { height: 34, paddingInline: 12 } : undefined}
                type="primary"
                icon={<PlusOutlined />}
                onClick={onOpenCreateTable}
              >
                {t('Create Table')}
              </Button>
              {creatableMenuItems.length > 0 && (
                <Dropdown trigger={['click']} menu={{ items: creatableMenuItems }}>
                  <Button
                    size={isMobile ? 'small' : 'middle'}
                    style={isMobile ? { height: 34, paddingInline: 10 } : undefined}
                    icon={<PlusOutlined />}
                  >
                    {t('New Object')} <DownOutlined style={{ fontSize: 10 }} />
                  </Button>
                </Dropdown>
              )}
            </>
          )}
          {!isMobile && (
            <>
              <Button
                icon={<CodeOutlined />}
                onClick={() =>
                  openTab({
                    key: `sql:${tab.connectionId}:${Date.now()}`,
                    title: `SQL - ${tab.database}`,
                    kind: 'sql_console',
                    connectionId: tab.connectionId,
                    connName: tab.connName,
                    dbType: tab.dbType,
                    database: tab.database,
                    schema: tab.schema,
                  })
                }
              >
                {t('SQL Console')}
              </Button>
              <Button icon={<DownloadOutlined />} onClick={() => onOpenTransfer('export')}>
                {t('Export DB (SQL Dump)')}
              </Button>
              {canWrite && (
                <>
                  <Button icon={<UploadOutlined />} onClick={() => onOpenTransfer('import')}>
                    {t('Import File')}
                  </Button>
                  <Button icon={<PlaySquareOutlined />} onClick={() => onOpenTransfer('sql_exec')}>
                    {t('Run SQL Script')}
                  </Button>
                </>
              )}
            </>
          )}
          <Button
            size={isMobile ? 'small' : 'middle'}
            style={isMobile ? { width: 34, height: 34, padding: 0 } : undefined}
            icon={<ReloadOutlined />}
            onClick={() => void loadOverview()}
            aria-label={t('Refresh')}
          >
            {isMobile ? '' : t('Refresh')}
          </Button>
        </Space>

        <div
          style={{
            display: 'flex',
            flexWrap: 'wrap',
            gap: 6,
            maxWidth: '100%',
            width: isMobile ? '100%' : undefined,
            alignItems: 'center',
          }}
        >
          {isMobile || categoryOptions.length > 5 ? (
            <Select
              size={isMobile ? 'middle' : 'small'}
              value={categoryFilter}
              onChange={(val) => setCategoryFilter(val)}
              options={categoryOptions}
              style={{ minWidth: isMobile ? 140 : 185, flex: isMobile ? 1 : undefined }}
            />
          ) : (
            <div style={{ maxWidth: '100%', overflowX: 'auto' }}>
              <Segmented
                size="small"
                value={categoryFilter}
                onChange={(val) => setCategoryFilter(val as CategoryFilterKey)}
                options={categoryOptions}
              />
            </div>
          )}
          <Input
            size={isMobile ? 'middle' : 'small'}
            allowClear
            prefix={<SearchOutlined />}
            placeholder={t('Filter objects...')}
            style={{ width: isMobile ? '100%' : 200, maxWidth: '100%', height: isMobile ? 34 : undefined }}
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          />
        </div>
      </div>

      {/* Multi-Object Batch Action Bar */}
      {selectedObjects.length > 0 && (
        <div
          style={{
            marginBottom: 8,
            padding: '6px 10px',
            borderRadius: 8,
            background: 'rgba(22, 119, 255, 0.08)',
            border: '1px solid rgba(22, 119, 255, 0.22)',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            flexWrap: 'wrap',
            gap: 6,
          }}
        >
          <Typography.Text strong style={{ fontSize: 12 }}>
            {t('Selected {count} object(s)', { count: selectedObjects.length })}
          </Typography.Text>
          <Space wrap size={6}>
            {selectedTableNames.length > 0 && (
              <Button
                size="small"
                type="primary"
                icon={<DownloadOutlined />}
                onClick={() => onOpenTransfer('export', undefined, { tables: selectedTableNames })}
              >
                {t('Export Selected ({count})', { count: selectedTableNames.length })}
              </Button>
            )}
            {canWrite && selectedTableNames.length > 0 && (
              <>
                <Button
                  size="small"
                  icon={<ToolOutlined />}
                  loading={batchRunning}
                  onClick={() => void handleBatchMaintenance('OPTIMIZE')}
                >
                  {t('Optimize Selected')}
                </Button>
                <Popconfirm
                  title={t('Truncate {count} selected table(s)?', { count: selectedTableNames.length })}
                  onConfirm={() => void handleBatchMaintenance('TRUNCATE')}
                >
                  <Button size="small" danger icon={<ClearOutlined />} loading={batchRunning}>
                    {t('Truncate Selected')}
                  </Button>
                </Popconfirm>
              </>
            )}
            {canWrite && (
              <Popconfirm
                title={t('Danger: Drop {count} selected object(s) permanently?', { count: selectedObjects.length })}
                onConfirm={() => void handleBatchMaintenance('DROP')}
              >
                <Button size="small" danger icon={<DeleteOutlined />} loading={batchRunning}>
                  {t('Drop Selected')}
                </Button>
              </Popconfirm>
            )}
            <Button size="small" type="text" onClick={() => setSelectedKeys([])}>
              {t('Clear Selection')}
            </Button>
          </Space>
        </div>
      )}

      <ResizableTable<ObjectItem>
        rowKey={makeRowKey}
        size="small"
        loading={loading}
        wrapClassName={isMobile ? 'dbharbor-mobile-table' : undefined}
        dataSource={filtered}
        scroll={{ x: 'max-content' }}
        rowSelection={{
          selectedRowKeys: selectedKeys,
          onChange: setSelectedKeys,
          columnWidth: isMobile ? 38 : 40,
        }}
        pagination={{ pageSize: 50, simple: isMobile, showSizeChanger: !isMobile }}
        columns={overviewColumns}
      />

      {/* Unified Lazy SchemaObjectModal */}
      {schemaModal.open && (
        <Suspense fallback={null}>
          <LazySchemaObjectModal
            open={schemaModal.open}
            mode={schemaModal.mode}
            initialType={schemaModal.initialType}
            connId={tab.connectionId}
            dbType={tab.dbType}
            database={tab.database}
            schema={tab.schema}
            tables={tableNames}
            availableExtensions={availableExtensions}
            capabilities={driverMeta?.capabilities}
            canWrite={canWrite}
            isDesktop={isDesktop}
            userRole={role}
            editObject={schemaModal.editObject}
            onClose={() => setSchemaModal((prev) => ({ ...prev, open: false }))}
            onSuccess={() => {
              bumpTreeRefresh();
              void loadOverview();
            }}
          />
        </Suspense>
      )}

      {/* Edit Table Comment Modal */}
      <Modal
        open={Boolean(commentTarget)}
        title={`${t('Edit Table Comment')} - ${commentTarget?.name || ''}`}
        width="min(520px, calc(100vw - 16px))"
        onCancel={() => setCommentTarget(null)}
        onOk={() => void handleSaveComment()}
        okText={t('Save')}
      >
        <Input.TextArea
          rows={3}
          value={commentText}
          onChange={(e) => setCommentText(e.target.value)}
          placeholder={t('Enter table description / comment')}
        />
      </Modal>

      {/* Copy / Rename Table Modal */}
      <Modal
        open={Boolean(cloneTarget)}
        title={`${t('Clone or rename table')} - ${cloneTarget?.name || ''}`}
        width="min(560px, calc(100vw - 16px))"
        onCancel={() => setCloneTarget(null)}
        onOk={() => void handleCloneOrRename()}
        confirmLoading={cloneSaving}
        okText={t('Execute Now')}
      >
        <Form form={cloneForm} layout="vertical">
          <Form.Item name="mode" label={t('Operation')} rules={[{ required: true }]}>
            <Radio.Group
              className="dbharbor-pill-radios"
              style={{
                display: 'flex',
                flexWrap: 'wrap',
                gap: 6,
                flexDirection: isMobile ? 'column' : 'row',
              }}
            >
              <Radio.Button value="COPY_DATA">{t('Copy Structure & Data')}</Radio.Button>
              <Radio.Button value="COPY_STRUCTURE">{t('Copy Structure Only')}</Radio.Button>
              <Radio.Button value="RENAME">{t('Rename Table')}</Radio.Button>
            </Radio.Group>
          </Form.Item>
          <Form.Item
            name="targetName"
            label={t('Target Table Name')}
            rules={[{ required: true, message: t('Please enter target table name') }]}
          >
            <Input placeholder="e.g. orders_backup_2026" />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
});

export default OverviewTab;
