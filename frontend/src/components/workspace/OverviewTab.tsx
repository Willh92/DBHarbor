import React, { useCallback, useDeferredValue, useEffect, useMemo, useState } from 'react';
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
  Space,
  Statistic,
  Tag,
  Tooltip,
  Typography,
  message,
} from 'antd';
import {
  ClearOutlined,
  CodeOutlined,
  CopyOutlined,
  DeleteOutlined,
  DownloadOutlined,
  EditOutlined,
  EyeOutlined,
  FunctionOutlined,
  MoreOutlined,
  PlaySquareOutlined,
  PlusOutlined,
  ReloadOutlined,
  SearchOutlined,
  TableOutlined,
  ThunderboltOutlined,
  ToolOutlined,
  UploadOutlined,
} from '@ant-design/icons';
import { api } from '../../api/client';
import { useResponsive } from '../../hooks/useResponsive';
import { useI18n } from '../../i18n/useI18n';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import type { ObjectItem, WorkspaceTab } from '../../types';
import { formatBytes } from '../../utils/format';
import { ResizableTable, calcSmartColumnWidth } from '../common/ResizableTable';
import { ContextNavBar, type TransferModalMode, type TransferOpenOptions } from './ContextNavBar';
import {
  ROUTINE_CAPABLE_ENGINES,
  RoutineTriggerModal,
  TRIGGER_CAPABLE_ENGINES,
} from './RoutineTriggerModal';

interface Props {
  tab: WorkspaceTab;
  onOpenCreateTable: () => void;
  onOpenTransfer: (mode: TransferModalMode, tableName?: string, opts?: TransferOpenOptions) => void;
}

export const OverviewTab: React.FC<Props> = React.memo(({ tab, onOpenCreateTable, onOpenTransfer }) => {
  const { openTab, bumpTreeRefresh, treeRefreshTick, isDesktop, role } = useWorkbenchStore();
  const { isMobile } = useResponsive();
  const { t } = useI18n();
  const canWrite = isDesktop || role !== 'readonly';
  const supportsRoutines = ROUTINE_CAPABLE_ENGINES.has(tab.dbType);
  const supportsTriggers = TRIGGER_CAPABLE_ENGINES.has(tab.dbType);

  const [loading, setLoading] = useState(false);
  const [items, setItems] = useState<ObjectItem[]>([]);
  const [filter, setFilter] = useState('');
  const [categoryFilter, setCategoryFilter] = useState<'ALL' | 'TABLE' | 'VIEW' | 'ROUTINE' | 'TRIGGER'>('ALL');
  const [selectedKeys, setSelectedKeys] = useState<React.Key[]>([]);
  const [batchRunning, setBatchRunning] = useState(false);

  // Create / Edit View Modal
  const [viewModalOpen, setViewModalOpen] = useState(false);
  const [viewForm] = Form.useForm();
  const [viewSaving, setViewSaving] = useState(false);

  // Create / Edit Routine or Trigger Modal
  const [rtModal, setRtModal] = useState<{
    open: boolean;
    mode: 'routine' | 'trigger';
    editObject: { name: string; objectType: 'PROCEDURE' | 'FUNCTION' | 'TRIGGER' } | null;
  }>({ open: false, mode: 'routine', editObject: null });

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
      const merged = [
        ...(res.tables || []),
        ...(res.views || []),
        ...(res.routines || []),
        ...(res.triggers || []),
      ];
      setItems(merged);
      setSelectedKeys([]);
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setLoading(false);
    }
  }, [tab.connectionId, tab.database, tab.schema]);

  useEffect(() => {
    loadOverview();
  }, [loadOverview, treeRefreshTick]);

  const handleAction = useCallback(
    async (record: ObjectItem, action: 'TRUNCATE' | 'DROP' | 'OPTIMIZE' | 'ANALYZE') => {
      try {
        await api.objectAction(tab.connectionId, {
          database: tab.database,
          schema: tab.schema,
          objectName: record.name,
          objectType: record.type,
          action,
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

  const openEditView = async (vw: ObjectItem) => {
    viewForm.setFieldsValue({ viewName: vw.name, viewSQL: '' });
    setViewModalOpen(true);
    try {
      const res = await api.getObjectDDL(tab.connectionId, tab.database, tab.schema || '', 'VIEW', vw.name);
      viewForm.setFieldsValue({ viewName: vw.name, viewSQL: res.ddl });
    } catch (err: any) {
      message.warning(err.message);
    }
  };

  const handleCreateView = async () => {
    try {
      const vals = await viewForm.validateFields();
      setViewSaving(true);
      await api.objectAction(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        objectName: vals.viewName.trim(),
        objectType: 'VIEW',
        action: 'CREATE_VIEW',
        viewSql: vals.viewSQL.trim(),
      });
      message.success(t('View {name} saved successfully', { name: vals.viewName }));
      setViewModalOpen(false);
      viewForm.resetFields();
      bumpTreeRefresh();
      await loadOverview();
    } catch (err: any) {
      if (!err?.errorFields) message.error(err.message);
    } finally {
      setViewSaving(false);
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
  const {
    totalTables,
    totalViews,
    totalRoutines,
    totalTriggers,
    totalRows,
    totalSize,
    tableNames,
  } = useMemo(() => {
    let tbls = 0;
    let vws = 0;
    let rts = 0;
    let trgs = 0;
    let rws = 0;
    let sz = 0;
    const tNames: string[] = [];
    for (const i of items) {
      if (i.type === 'TABLE') {
        tbls++;
        tNames.push(i.name);
      } else if (i.type === 'VIEW') {
        vws++;
      } else if (i.type === 'PROCEDURE' || i.type === 'FUNCTION') {
        rts++;
      } else if (i.type === 'TRIGGER') {
        trgs++;
      }
      rws += i.rows || 0;
      sz += (i.dataBytes || 0) + (i.indexBytes || 0);
    }
    return {
      totalTables: tbls,
      totalViews: vws,
      totalRoutines: rts,
      totalTriggers: trgs,
      totalRows: rws,
      totalSize: sz,
      tableNames: tNames,
    };
  }, [items]);

  const filtered = useMemo(() => {
    const q = deferredFilter.trim().toLowerCase();
    return items.filter((i) => {
      if (categoryFilter === 'TABLE' && i.type !== 'TABLE') return false;
      if (categoryFilter === 'VIEW' && i.type !== 'VIEW') return false;
      if (categoryFilter === 'ROUTINE' && i.type !== 'PROCEDURE' && i.type !== 'FUNCTION') return false;
      if (categoryFilter === 'TRIGGER' && i.type !== 'TRIGGER') return false;
      return q ? i.name.toLowerCase().includes(q) : true;
    });
  }, [items, deferredFilter, categoryFilter]);

  const selectedObjects = useMemo(() => {
    if (selectedKeys.length === 0) return [];
    const keySet = new Set(selectedKeys.map(String));
    return items.filter((item) => keySet.has(`${item.type}:${item.name}`));
  }, [items, selectedKeys]);

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

  const openRoutineOrTrigger = useCallback((r: ObjectItem) => {
    if (r.type === 'TRIGGER') {
      setRtModal({ open: true, mode: 'trigger', editObject: { name: r.name, objectType: 'TRIGGER' } });
    } else if (r.type === 'PROCEDURE' || r.type === 'FUNCTION') {
      setRtModal({ open: true, mode: 'routine', editObject: { name: r.name, objectType: r.type } });
    }
  }, []);

  const overviewColumns = useMemo(
    () => [
      {
        title: t('Object Name'),
        dataIndex: 'name',
        width: calcSmartColumnWidth(t('Object Name'), { hasSorter: true, minBodyWidth: 200 }),
        sorter: (a: ObjectItem, b: ObjectItem) => a.name.localeCompare(b.name),
        render: (name: string, r: ObjectItem) => {
          const isDataObj = r.type === 'TABLE' || r.type === 'VIEW';
          const tagColor =
            r.type === 'TABLE'
              ? 'blue'
              : r.type === 'VIEW'
                ? 'purple'
                : r.type === 'TRIGGER'
                  ? 'orange'
                  : 'cyan';
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
                    openRoutineOrTrigger(r);
                  }
                }}
              >
                {name}
              </Typography.Link>
              <Tag color={tagColor} style={{ fontSize: 10, margin: 0 }}>
                {r.type}
              </Tag>
            </div>
          );
        },
      },
      {
        title: t('Engine'),
        dataIndex: 'engine',
        width: calcSmartColumnWidth(t('Engine'), { minBodyWidth: 105 }),
        render: (v?: string) => (v ? <Tag style={{ margin: 0 }}>{v}</Tag> : '-'),
      },
      {
        title: t('Rows'),
        dataIndex: 'rows',
        width: calcSmartColumnWidth(t('Rows'), { hasSorter: true, minBodyWidth: 85 }),
        align: 'right' as const,
        sorter: (a: ObjectItem, b: ObjectItem) => (a.rows || 0) - (b.rows || 0),
        render: (v: number, r: ObjectItem) =>
          r.type === 'TABLE' || r.type === 'VIEW' ? v?.toLocaleString?.() ?? 0 : '-',
      },
      {
        title: t('Size'),
        dataIndex: 'dataBytes',
        width: calcSmartColumnWidth(t('Size'), { hasSorter: true, minBodyWidth: 95 }),
        align: 'right' as const,
        sorter: (a: ObjectItem, b: ObjectItem) => (a.dataBytes || 0) - (b.dataBytes || 0),
        render: (_: number, r: ObjectItem) =>
          r.type === 'TABLE' ? formatBytes((r.dataBytes || 0) + (r.indexBytes || 0)) : '-',
      },
      {
        title: t('Collation / Comment'),
        dataIndex: 'comment',
        width: calcSmartColumnWidth(t('Collation / Comment'), { minBodyWidth: 150 }),
        ellipsis: true,
        render: (_: string, r: ObjectItem) => r.comment || r.collation || '-',
      },
      {
        title: t('Quick Actions'),
        key: 'actions',
        width: isMobile ? 210 : 290,
        render: (_: any, r: ObjectItem) => {
          const isDataObj = r.type === 'TABLE' || r.type === 'VIEW';
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
                    onClick: () => handleAction(r, 'ANALYZE'),
                  },
                  {
                    key: 'optimize',
                    icon: <ToolOutlined />,
                    label: t('Optimize / Vacuum Table'),
                    onClick: () => handleAction(r, 'OPTIMIZE'),
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
                  {
                    key: 'export',
                    icon: <DownloadOutlined />,
                    label: t('Export table'),
                    onClick: () => onOpenTransfer('export', r.name),
                  },
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
            ...mobileTopDataItems,
            ...(mobileTopDataItems.length > 0 && writeTableItems.length > 0 ? [{ type: 'divider' as const }] : []),
            ...writeTableItems,
            ...(mobileDangerItems.length > 0 && (mobileTopDataItems.length > 0 || writeTableItems.length > 0)
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
                    <Button size="small" type="link" style={btnStyle} icon={<CodeOutlined />} onClick={() => openEditView(r)}>
                      {canWrite ? t('Edit View SQL') : t('Inspect DDL')}
                    </Button>
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
                  {!isMobile && (
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
                <Button
                  size="small"
                  type="link"
                  style={btnStyle}
                  icon={<EditOutlined />}
                  onClick={() => openRoutineOrTrigger(r)}
                >
                  {canWrite ? t('Edit SQL') : t('Inspect DDL')}
                </Button>
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
                  onConfirm={() => handleAction(r, 'TRUNCATE')}
                >
                  <Tooltip title={t('Truncate table')}>
                    <Button size="small" type="text" danger icon={<ClearOutlined />} />
                  </Tooltip>
                </Popconfirm>
              )}
              {!isMobile && canWrite && (
                <Popconfirm
                  title={t('Danger: Permanently drop {name}?', { name: r.name })}
                  onConfirm={() => handleAction(r, 'DROP')}
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
      openRoutineOrTrigger,
    ]
  );

  return (
    <div style={{ padding: isMobile ? '6px 8px' : '12px 16px', height: '100%', minWidth: 0, overflowY: 'auto' }}>
      <ContextNavBar tab={tab} activeSection="overview" onOpenTransfer={onOpenTransfer} />

      <Row gutter={[isMobile ? 8 : 12, isMobile ? 6 : 10]} className="dbharbor-kpi-row" style={{ marginBottom: isMobile ? 8 : 12 }}>
        <Col xs={12} md={6}>
          <Card size="small">
            <Statistic title={t('Tables')} value={totalTables} suffix={totalViews > 0 ? `/ ${totalViews}` : ''} />
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
            <Statistic
              title={t('Current Context')}
              value={`${tab.database}${tab.schema ? '.' + tab.schema : ''}`}
              valueStyle={{ fontSize: isMobile ? 14 : 16 }}
            />
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
              <Dropdown
                trigger={['click']}
                menu={{
                  items: [
                    {
                      key: 'new-view',
                      icon: <EyeOutlined />,
                      label: t('Create View'),
                      onClick: () => {
                        viewForm.resetFields();
                        setViewModalOpen(true);
                      },
                    },
                    ...(supportsRoutines
                      ? [
                          {
                            key: 'new-routine',
                            icon: <FunctionOutlined />,
                            label: t('New Routine'),
                            onClick: () => setRtModal({ open: true, mode: 'routine', editObject: null }),
                          },
                        ]
                      : []),
                    ...(supportsTriggers
                      ? [
                          {
                            key: 'new-trigger',
                            icon: <ThunderboltOutlined />,
                            label: t('New Trigger'),
                            onClick: () => setRtModal({ open: true, mode: 'trigger', editObject: null }),
                          },
                        ]
                      : []),
                  ],
                }}
              >
                <Button
                  size={isMobile ? 'small' : 'middle'}
                  style={isMobile ? { height: 34, paddingInline: 10 } : undefined}
                  icon={<PlusOutlined />}
                >
                  {t('New Object')}
                </Button>
              </Dropdown>
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
            onClick={loadOverview}
            aria-label={t('Refresh')}
          >
            {isMobile ? '' : t('Refresh')}
          </Button>
        </Space>

        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, maxWidth: '100%', width: isMobile ? '100%' : undefined, alignItems: 'center' }}>
          <div style={{ maxWidth: '100%', overflowX: 'auto' }}>
            <Segmented
              size={isMobile ? 'middle' : 'small'}
              value={categoryFilter}
              onChange={(val) => setCategoryFilter(val as any)}
              options={[
                { value: 'ALL', label: `${t('All')} (${items.length})` },
                { value: 'TABLE', label: `${t('Tables')} (${totalTables})` },
                ...(totalViews > 0 || canWrite ? [{ value: 'VIEW', label: `${t('Views')} (${totalViews})` }] : []),
                ...(totalRoutines > 0 || (canWrite && supportsRoutines)
                  ? [{ value: 'ROUTINE', label: `${t('Routines')} (${totalRoutines})` }]
                  : []),
                ...(totalTriggers > 0 || (canWrite && supportsTriggers)
                  ? [{ value: 'TRIGGER', label: `${t('Triggers')} (${totalTriggers})` }]
                  : []),
              ]}
            />
          </div>
          <Input
            size="middle"
            allowClear
            prefix={<SearchOutlined />}
            placeholder={t('Filter objects...')}
            style={{ width: isMobile ? '100%' : 210, maxWidth: '100%', height: isMobile ? 34 : undefined }}
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
                  onClick={() => handleBatchMaintenance('OPTIMIZE')}
                >
                  {t('Optimize Selected')}
                </Button>
                <Popconfirm
                  title={t('Truncate {count} selected table(s)?', { count: selectedTableNames.length })}
                  onConfirm={() => handleBatchMaintenance('TRUNCATE')}
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
                onConfirm={() => handleBatchMaintenance('DROP')}
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
        rowKey={(r) => `${r.type}:${r.name}`}
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

      {/* Create / Edit View Modal */}
      <Modal
        open={viewModalOpen}
        title={t('Create Database View (CREATE VIEW)')}
        width="min(680px, 95vw)"
        onCancel={() => setViewModalOpen(false)}
        onOk={canWrite ? handleCreateView : () => setViewModalOpen(false)}
        confirmLoading={viewSaving}
        okText={canWrite ? t('Save View') : t('Close')}
      >
        <Form form={viewForm} layout="vertical">
          <Form.Item
            name="viewName"
            label={t('View Name')}
            rules={[{ required: true, message: t('Please enter view name') }]}
          >
            <Input readOnly={!canWrite} placeholder="e.g. v_active_orders" />
          </Form.Item>
          <Form.Item
            name="viewSQL"
            label={t('SELECT Definition SQL')}
            rules={[{ required: true, message: t('Please enter SELECT definition SQL') }]}
          >
            <Input.TextArea
              rows={7}
              readOnly={!canWrite}
              style={{ fontFamily: 'monospace', fontSize: 12 }}
              placeholder="SELECT id, customer_id, total_amount FROM orders WHERE status = 'PAID'"
            />
          </Form.Item>
        </Form>
      </Modal>

      {/* Create / Edit Routine or Trigger Modal */}
      <RoutineTriggerModal
        open={rtModal.open}
        mode={rtModal.mode}
        connectionId={tab.connectionId}
        connName={tab.connName}
        dbType={tab.dbType}
        database={tab.database}
        schema={tab.schema}
        availableTables={tableNames}
        editObject={rtModal.editObject}
        canWrite={canWrite}
        onClose={() => setRtModal((prev) => ({ ...prev, open: false }))}
        onSaved={() => {
          bumpTreeRefresh();
          loadOverview();
        }}
      />

      {/* Edit Table Comment Modal */}
      <Modal
        open={Boolean(commentTarget)}
        title={`${t('Edit Table Comment')} - ${commentTarget?.name || ''}`}
        onCancel={() => setCommentTarget(null)}
        onOk={handleSaveComment}
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
        onCancel={() => setCloneTarget(null)}
        onOk={handleCloneOrRename}
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
