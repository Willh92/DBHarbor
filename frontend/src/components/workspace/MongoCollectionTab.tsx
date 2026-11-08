import React, { useEffect, useMemo, useRef, useState } from 'react';
import {
  Alert,
  Button,
  Checkbox,
  Col,
  Dropdown,
  Form,
  Input,
  InputNumber,
  Modal,
  Pagination,
  Popconfirm,
  Row,
  Segmented,
  Select,
  Space,
  Tabs,
  Tag,
  Typography,
  Upload,
  message,
} from 'antd';
import {
  CopyOutlined,
  DeleteOutlined,
  DownloadOutlined,
  EditOutlined,
  EyeOutlined,
  FieldTimeOutlined,
  MoreOutlined,
  PlayCircleOutlined,
  PlusOutlined,
  ReloadOutlined,
  SaveOutlined,
  TeamOutlined,
  UploadOutlined,
} from '@ant-design/icons';
import { api, isAuthPromptCancelledError } from '../../api/client';
import { useResponsive } from '../../hooks/useResponsive';
import { useI18n } from '../../i18n/useI18n';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import type { CollectionSummary, DocIndexInfo, WorkspaceTab } from '../../types';
import { formatRawCellText, writeTextToClipboard } from '../../utils/tableClipboard';
import { LazyCodeEditor } from '../common/LazyCodeEditor';
import { ResizableTable, calcSmartColumnWidth } from '../common/ResizableTable';

export const MongoCollectionTab: React.FC<{ tab: WorkspaceTab }> = ({ tab }) => {
  const darkMode = useWorkbenchStore((s) => s.darkMode);
  const isDesktop = useWorkbenchStore((s) => s.isDesktop);
  const role = useWorkbenchStore((s) => s.role);
  const updateTab = useWorkbenchStore((s) => s.updateTab);
  const closeTab = useWorkbenchStore((s) => s.closeTab);
  const bumpTreeRefresh = useWorkbenchStore((s) => s.bumpTreeRefresh);
  const openTab = useWorkbenchStore((s) => s.openTab);
  const connUnlocked = useWorkbenchStore((s) => s.connections.find((c) => c.id === tab.connectionId)?.unlocked);
  const treeRefreshTick = useWorkbenchStore((s) => s.treeRefreshTick);
  const canWrite = isDesktop || role !== 'readonly';
  const { isMobile } = useResponsive();
  const { t } = useI18n();
  const needsUnlockReloadRef = useRef(false);

  const [collMeta, setCollMeta] = useState<CollectionSummary | null>(null);
  const isView = collMeta?.type === 'view';
  const isTimeSeries = collMeta?.type === 'timeseries' || Boolean(collMeta?.timeField);
  const canMutateDocs = canWrite && !isView;

  const [viewMode, setViewMode] = useState<'json' | 'table'>('json');
  const [filterJson, setFilterJson] = useState('{}');
  const [sortJson, setSortJson] = useState('{"_id": -1}');
  const [docs, setDocs] = useState<Record<string, any>[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(30);
  const [loading, setLoading] = useState(false);
  const [selectedIds, setSelectedIds] = useState<React.Key[]>([]);

  // View Pipeline Editor
  const [editViewOn, setEditViewOn] = useState('');
  const [editPipelineJson, setEditPipelineJson] = useState('[\n  { "$match": {} }\n]');
  const [savingOptions, setSavingOptions] = useState(false);

  // Validation Schema Editor
  const [editValidatorJson, setEditValidatorJson] = useState('{}');
  const [editValidationLevel, setEditValidationLevel] = useState<string>('strict');
  const [editValidationAction, setEditValidationAction] = useState<string>('error');

  // Document Editor Modal
  const [docModalOpen, setDocModalOpen] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [docJsonText, setDocJsonText] = useState('{\n  "name": "example"\n}');

  // Import Documents Modal
  const [importOpen, setImportOpen] = useState(false);
  const [importing, setImporting] = useState(false);
  const [importText, setImportText] = useState('[\n  { "name": "item_1" }\n]');
  const [importContinueOnError, setImportContinueOnError] = useState(true);

  // Rename Collection Modal
  const [renameOpen, setRenameOpen] = useState(false);
  const [newCollName, setNewCollName] = useState('');

  // Indexes & Command Runner
  const [indexes, setIndexes] = useState<DocIndexInfo[]>([]);
  const [idxModalOpen, setIdxModalOpen] = useState(false);
  const [idxSubmitting, setIdxSubmitting] = useState(false);
  const [idxForm] = Form.useForm();
  const [cmdText, setCmdText] = useState(`{\n  "count": "${tab.table || 'users'}"\n}`);
  const [cmdResult, setCmdResult] = useState('');

  const loadCollectionMeta = async () => {
    if (!tab.table) return;
    try {
      const meta = await api.docGetCollectionOptions(tab.connectionId, tab.database, tab.table);
      setCollMeta(meta);
      if (meta?.type === 'view') {
        setEditViewOn(meta.viewOn || '');
        setEditPipelineJson(meta.pipelineJson || '[]');
      } else {
        setEditValidatorJson(meta?.validatorJson || '{}');
        setEditValidationLevel(meta?.validationLevel || 'strict');
        setEditValidationAction(meta?.validationAction || 'error');
      }
    } catch {
      // fallback if server restricts listCollections options
    }
  };

  const loadDocs = async (p = page, ps = pageSize) => {
    if (!tab.table) return;
    setLoading(true);
    try {
      const res = await api.docFind(tab.connectionId, {
        database: tab.database,
        collection: tab.table,
        filterJson,
        sortJson,
        projectionJson: '{}',
        page: p,
        pageSize: ps,
      });
      needsUnlockReloadRef.current = false;
      setDocs(res.documents || []);
      setTotal(res.total || 0);
      setSelectedIds([]);
    } catch (err: any) {
      if (isAuthPromptCancelledError(err)) {
        needsUnlockReloadRef.current = true;
      } else {
        message.error(err.message);
      }
    } finally {
      setLoading(false);
    }
  };

  const loadIndexes = async () => {
    if (!tab.table) return;
    try {
      const list = await api.docListIndexes(tab.connectionId, tab.database, tab.table);
      setIndexes(list || []);
    } catch {
      // ignore index fetch error on views or restricted users
    }
  };

  useEffect(() => {
    setPage(1);
    setCmdResult('');
    loadCollectionMeta();
    loadDocs(1, pageSize);
    loadIndexes();
  }, [tab.connectionId, tab.database, tab.table]);

  useEffect(() => {
    if (needsUnlockReloadRef.current && connUnlocked) {
      needsUnlockReloadRef.current = false;
      loadCollectionMeta();
      loadDocs(page, pageSize);
      loadIndexes();
    }
  }, [connUnlocked, treeRefreshTick]);

  const handleSaveDoc = async () => {
    if (!tab.table || !canMutateDocs) return;
    try {
      if (editingId) {
        await api.docUpdate(tab.connectionId, tab.database, tab.table, editingId, docJsonText);
        message.success(t('Document updated'));
      } else {
        await api.docInsert(tab.connectionId, tab.database, tab.table, docJsonText);
        message.success(t('Document inserted'));
      }
      setDocModalOpen(false);
      loadDocs();
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleCloneDocument = (doc: Record<string, any>) => {
    const cloned = { ...doc };
    delete cloned._id;
    setEditingId(null);
    setDocJsonText(JSON.stringify(cloned, null, 2));
    setDocModalOpen(true);
  };

  const handleBatchDelete = async () => {
    if (!tab.table || !canMutateDocs || selectedIds.length === 0) return;
    try {
      await api.docDelete(tab.connectionId, tab.database, tab.table, selectedIds.map(String));
      message.success(t('Deleted {count} documents', { count: selectedIds.length }));
      loadDocs();
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleRenameCollection = async () => {
    const prevName = tab.table;
    const next = newCollName.trim();
    if (!prevName || !next || next === prevName) return;
    try {
      await api.docRenameCollection(tab.connectionId, tab.database, prevName, next);
      message.success(t('Collection renamed to {name}', { name: next }));
      setRenameOpen(false);
      setCmdResult('');
      setCmdText((prev) => {
        const prevDefault = `{\n  "count": "${prevName}"\n}`;
        return prev.trim() === prevDefault.trim() ? `{\n  "count": "${next}"\n}` : prev;
      });
      bumpTreeRefresh();
      updateTab(tab.key, {
        key: `mongo:${tab.connectionId}:${tab.database}:${next}`,
        title: next,
        table: next,
      });
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleDropCollectionOrView = () => {
    if (!tab.table || !canWrite) return;
    Modal.confirm({
      title: isView ? t('Drop View') : t('Drop Collection'),
      content: isView
        ? t('Permanently drop MongoDB view {name}?', { name: tab.table })
        : t('Permanently drop MongoDB collection {name} and all its documents?', { name: tab.table }),
      okButtonProps: { danger: true },
      okText: t('Drop'),
      onOk: async () => {
        try {
          await api.docDropCollection(tab.connectionId, tab.database, tab.table || '');
          message.success(isView ? t('View dropped') : t('Collection dropped'));
          bumpTreeRefresh();
          closeTab(tab.key);
        } catch (err: any) {
          message.error(err.message);
        }
      },
    });
  };

  const handleSaveViewPipeline = async () => {
    if (!tab.table || !canWrite) return;
    setSavingOptions(true);
    try {
      await api.docUpdateCollectionOptions(tab.connectionId, {
        database: tab.database,
        collection: tab.table,
        viewOn: editViewOn.trim(),
        pipelineJson: editPipelineJson,
      });
      message.success(t('View pipeline updated'));
      await loadCollectionMeta();
      await loadDocs(1, pageSize);
    } catch (err: any) {
      if (!isAuthPromptCancelledError(err)) {
        message.error(err.message);
      }
    } finally {
      setSavingOptions(false);
    }
  };

  const handleSaveValidation = async () => {
    if (!tab.table || !canWrite) return;
    setSavingOptions(true);
    try {
      await api.docUpdateCollectionOptions(tab.connectionId, {
        database: tab.database,
        collection: tab.table,
        validatorJson: editValidatorJson,
        validationLevel: editValidationLevel,
        validationAction: editValidationAction,
      });
      message.success(t('Collection validation rules updated'));
      await loadCollectionMeta();
    } catch (err: any) {
      if (!isAuthPromptCancelledError(err)) {
        message.error(err.message);
      }
    } finally {
      setSavingOptions(false);
    }
  };

  const handleExport = async (format: 'json' | 'jsonl' | 'csv' | 'csv_bom') => {
    if (!tab.table) return;
    try {
      const res = await api.triggerDocExportDownload(tab.connectionId, {
        database: tab.database,
        collection: tab.table,
        filterJson,
        sortJson,
        format,
      });
      if (!res.canceled) {
        message.success(res.savedPath ? `${t('Export saved')}: ${res.savedPath}` : t('Export started'));
      }
    } catch (err: any) {
      if (!isAuthPromptCancelledError(err)) {
        message.error(err.message);
      }
    }
  };

  const docTableColumns = useMemo(() => {
    const keySet = new Set<string>(['_id']);
    docs.forEach((d) => {
      Object.keys(d).forEach((k) => keySet.add(k));
    });
    const baseCols = Array.from(keySet).map((k) => ({
      title: <span data-col-name={k}>{k}</span>,
      dataIndex: k,
      width: calcSmartColumnWidth(k, { minBodyWidth: 150 }),
      ellipsis: true,
      render: (v: any) => {
        const text = v === null || v === undefined ? '' : typeof v === 'object' ? JSON.stringify(v) : String(v);
        return <span data-raw-copy={formatRawCellText(text)}>{text}</span>;
      },
    }));
    return [
      ...baseCols,
      {
        title: t('Actions'),
        key: '__actions',
        width: canMutateDocs ? 116 : 56,
        resizable: false,
        onHeaderCell: () => ({ 'data-copy-skip': 'true' } as any),
        render: (_: any, doc: Record<string, any>) => {
          const idStr = String(doc._id ?? '');
          return (
            <Space size={2} data-copy-skip="true">
              <Button
                size="small"
                type="text"
                icon={<CopyOutlined />}
                title={t('Copy JSON')}
                onClick={() => {
                  writeTextToClipboard(JSON.stringify(doc, null, 2));
                  message.success(t('Copied to clipboard'));
                }}
              />
              {canMutateDocs && (
                <>
                  <Button
                    size="small"
                    type="text"
                    icon={<EditOutlined />}
                    title={t('Edit')}
                    onClick={() => {
                      setEditingId(idStr);
                      setDocJsonText(JSON.stringify(doc, null, 2));
                      setDocModalOpen(true);
                    }}
                  />
                  <Button
                    size="small"
                    type="text"
                    icon={<PlusOutlined />}
                    title={t('Clone Document')}
                    onClick={() => handleCloneDocument(doc)}
                  />
                </>
              )}
            </Space>
          );
        },
      },
    ];
  }, [docs, canMutateDocs, t]);

  return (
    <div style={{ padding: isMobile ? 6 : 12, height: '100%', minWidth: 0, minHeight: 0, overflowY: 'auto' }}>
      {(isView || isTimeSeries || collMeta?.capped) && (
        <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', alignItems: 'center', marginBottom: 6 }}>
          {isView && (
            <>
              <Tag color="purple" icon={<EyeOutlined />}>
                {t('View (Read-Only)')}
              </Tag>
              {collMeta?.viewOn && (
                <Tag>
                  {t('Source')}: <code>{collMeta.viewOn}</code>
                </Tag>
              )}
            </>
          )}
          {isTimeSeries && (
            <Tag color="cyan" icon={<FieldTimeOutlined />}>
              {t('Time-Series')}: <code>{collMeta?.timeField}</code>
            </Tag>
          )}
          {collMeta?.capped && <Tag color="orange">{t('Capped')}</Tag>}
        </div>
      )}

      <Tabs
        size="small"
        tabBarExtraContent={
          isMobile ? (
            <Dropdown
              trigger={['click']}
              placement="bottomRight"
              menu={{
                items: [
                  ...(canMutateDocs
                    ? [
                        {
                          key: 'import',
                          icon: <UploadOutlined />,
                          label: t('Import Documents'),
                          onClick: () => setImportOpen(true),
                        },
                        {
                          key: 'rename',
                          icon: <EditOutlined />,
                          label: t('Rename Collection'),
                          onClick: () => {
                            setNewCollName(tab.table || '');
                            setRenameOpen(true);
                          },
                        },
                      ]
                    : []),
                  {
                    key: 'users',
                    icon: <TeamOutlined />,
                    label: t('Users & Grants'),
                    onClick: () =>
                      openTab({
                        key: `db_users:${tab.connectionId}`,
                        title: `${tab.connName} (${t('Users & Grants')})`,
                        kind: 'db_users',
                        connectionId: tab.connectionId,
                        connName: tab.connName,
                        dbType: tab.dbType,
                        database: tab.database,
                      }),
                  },
                  { type: 'divider' as const },
                  {
                    type: 'group' as const,
                    label: t('Export Collection'),
                    children: [
                      { key: 'exp_json', icon: <DownloadOutlined />, label: 'JSON Array (.json)', onClick: () => handleExport('json') },
                      { key: 'exp_jsonl', icon: <DownloadOutlined />, label: 'JSON Lines / NDJSON (.jsonl)', onClick: () => handleExport('jsonl') },
                      { key: 'exp_csv', icon: <DownloadOutlined />, label: 'CSV (.csv)', onClick: () => handleExport('csv') },
                      { key: 'exp_csv_bom', icon: <DownloadOutlined />, label: 'CSV Excel UTF-8 BOM (.csv)', onClick: () => handleExport('csv_bom') },
                    ],
                  },
                  ...(canWrite
                    ? [
                        { type: 'divider' as const },
                        {
                          key: 'drop',
                          danger: true,
                          icon: <DeleteOutlined />,
                          label: isView ? t('Drop View') : t('Drop Collection'),
                          onClick: handleDropCollectionOrView,
                        },
                      ]
                    : []),
                ],
              }}
            >
              <Button
                size="small"
                style={{ width: 32, height: 32, padding: 0, marginLeft: 4 }}
                icon={<MoreOutlined />}
                title={t('Actions')}
              />
            </Dropdown>
          ) : (
            <Space size={6} wrap>
              {canMutateDocs && (
                <Button size="small" icon={<UploadOutlined />} onClick={() => setImportOpen(true)}>
                  {t('Import Documents')}
                </Button>
              )}
              <Dropdown
                menu={{
                  items: [
                    { key: 'json', label: 'JSON Array (.json)', onClick: () => handleExport('json') },
                    { key: 'jsonl', label: 'JSON Lines / NDJSON (.jsonl)', onClick: () => handleExport('jsonl') },
                    { key: 'csv', label: 'CSV (.csv)', onClick: () => handleExport('csv') },
                    { key: 'csv_bom', label: 'CSV Excel UTF-8 BOM (.csv)', onClick: () => handleExport('csv_bom') },
                  ],
                }}
              >
                <Button size="small" icon={<DownloadOutlined />}>
                  {t('Export Collection')}
                </Button>
              </Dropdown>
              {canMutateDocs && (
                <Button
                  size="small"
                  icon={<EditOutlined />}
                  onClick={() => {
                    setNewCollName(tab.table || '');
                    setRenameOpen(true);
                  }}
                >
                  {t('Rename Collection')}
                </Button>
              )}
              <Button
                size="small"
                icon={<TeamOutlined />}
                onClick={() =>
                  openTab({
                    key: `db_users:${tab.connectionId}`,
                    title: `${tab.connName} (${t('Users & Grants')})`,
                    kind: 'db_users',
                    connectionId: tab.connectionId,
                    connName: tab.connName,
                    dbType: tab.dbType,
                    database: tab.database,
                  })
                }
              >
                {t('Users & Grants')}
              </Button>
              {canWrite && (
                <Button size="small" danger icon={<DeleteOutlined />} onClick={handleDropCollectionOrView}>
                  {isView ? t('Drop View') : t('Drop Collection')}
                </Button>
              )}
            </Space>
          )
        }
        items={[
          {
            key: 'docs',
            label: t('Documents ({count})', { count: total }),
            children: (
              <div>
                <div style={{ display: 'flex', gap: 8, marginBottom: 10, flexWrap: 'wrap', alignItems: 'center' }}>
                  <Input
                    addonBefore={t('Filter')}
                    style={{ width: isMobile ? '100%' : 300, maxWidth: '100%', fontFamily: 'monospace' }}
                    size={isMobile ? 'middle' : 'small'}
                    value={filterJson}
                    onChange={(e) => setFilterJson(e.target.value)}
                    onPressEnter={() => loadDocs(1, pageSize)}
                  />
                  <Input
                    addonBefore={t('Sort')}
                    style={{ width: isMobile ? '100%' : 190, maxWidth: '100%', fontFamily: 'monospace' }}
                    size={isMobile ? 'middle' : 'small'}
                    value={sortJson}
                    onChange={(e) => setSortJson(e.target.value)}
                    onPressEnter={() => loadDocs(1, pageSize)}
                  />
                  <Button
                    size="small"
                    style={isMobile ? { height: 34, paddingInline: 12 } : undefined}
                    type="primary"
                    onClick={() => loadDocs(1, pageSize)}
                  >
                    {t('Query')}
                  </Button>
                  {canMutateDocs && (
                    <Button
                      size="small"
                      style={isMobile ? { height: 34, paddingInline: 12 } : undefined}
                      icon={<PlusOutlined />}
                      onClick={() => {
                        setEditingId(null);
                        setDocJsonText('{\n  \n}');
                        setDocModalOpen(true);
                      }}
                    >
                      {t('Insert Document')}
                    </Button>
                  )}
                  {canMutateDocs && selectedIds.length > 0 && (
                    <Popconfirm
                      title={t('Delete {count} selected documents?', { count: selectedIds.length })}
                      onConfirm={handleBatchDelete}
                    >
                      <Button
                        size="small"
                        style={isMobile ? { height: 34, paddingInline: 10 } : undefined}
                        danger
                        icon={<DeleteOutlined />}
                      >
                        {t('Delete Selected ({count})', { count: selectedIds.length })}
                      </Button>
                    </Popconfirm>
                  )}
                  <Button
                    size="small"
                    style={isMobile ? { width: 34, height: 34, padding: 0 } : undefined}
                    icon={<ReloadOutlined />}
                    onClick={() => {
                      loadCollectionMeta();
                      loadDocs();
                    }}
                  />
                  <Segmented
                    size={isMobile ? 'middle' : 'small'}
                    value={viewMode}
                    onChange={(v) => setViewMode(v as 'json' | 'table')}
                    options={[
                      { label: t('JSON View'), value: 'json' },
                      { label: t('Table View'), value: 'table' },
                    ]}
                  />
                </div>

                {viewMode === 'json' ? (
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                    {docs.map((doc, i) => {
                      const idStr = String(doc._id ?? i);
                      return (
                        <div
                          key={idStr}
                          style={{
                            padding: '8px 12px',
                            borderRadius: 8,
                            border: '1px solid rgba(128,128,128,0.22)',
                          }}
                        >
                          <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 4, flexWrap: 'wrap', gap: 6 }}>
                            <Tag color="green">_id: {idStr}</Tag>
                            <Space size={4}>
                              <Button
                                size="small"
                                type="link"
                                icon={<CopyOutlined />}
                                onClick={() => {
                                  writeTextToClipboard(JSON.stringify(doc, null, 2));
                                  message.success(t('Copied to clipboard'));
                                }}
                              >
                                {t('Copy JSON')}
                              </Button>
                              {canMutateDocs && (
                                <>
                                  <Button
                                    size="small"
                                    type="link"
                                    icon={<PlusOutlined />}
                                    onClick={() => handleCloneDocument(doc)}
                                  >
                                    {t('Clone Document')}
                                  </Button>
                                  <Button
                                    size="small"
                                    type="link"
                                    icon={<EditOutlined />}
                                    onClick={() => {
                                      setEditingId(idStr);
                                      setDocJsonText(JSON.stringify(doc, null, 2));
                                      setDocModalOpen(true);
                                    }}
                                  >
                                    {t('Edit')}
                                  </Button>
                                  <Popconfirm
                                    title={t('Delete this document?')}
                                    onConfirm={async () => {
                                      try {
                                        await api.docDelete(tab.connectionId, tab.database, tab.table || '', [idStr]);
                                        message.success(t('Document deleted'));
                                        loadDocs();
                                      } catch (err: any) {
                                        message.error(err.message);
                                      }
                                    }}
                                  >
                                    <Button size="small" type="text" danger icon={<DeleteOutlined />} />
                                  </Popconfirm>
                                </>
                              )}
                            </Space>
                          </div>
                          <pre style={{ margin: 0, fontSize: 12, maxHeight: 200, overflow: 'auto', fontFamily: 'monospace' }}>
                            {JSON.stringify(doc, null, 2)}
                          </pre>
                        </div>
                      );
                    })}
                  </div>
                ) : (
                  <ResizableTable
                    size="small"
                    bordered
                    loading={loading}
                    rowKey={(r, idx) => String(r._id ?? idx)}
                    rowSelection={
                      canMutateDocs
                        ? {
                            columnWidth: isMobile ? 38 : 40,
                            selectedRowKeys: selectedIds,
                            onChange: setSelectedIds,
                          }
                        : undefined
                    }
                    pagination={false}
                    scroll={{ x: 'max-content' }}
                    dataSource={docs}
                    columns={docTableColumns}
                  />
                )}

                <div style={{ marginTop: 12, textAlign: 'right' }}>
                  <Pagination
                    size="small"
                    simple={isMobile}
                    current={page}
                    pageSize={pageSize}
                    total={total}
                    onChange={(p, ps) => {
                      setPage(p);
                      setPageSize(ps);
                      loadDocs(p, ps);
                    }}
                  />
                </div>
              </div>
            ),
          },
          ...(isView
            ? [
                {
                  key: 'view_pipeline',
                  label: t('View Pipeline'),
                  children: (
                    <Space direction="vertical" style={{ width: '100%' }} size={10}>
                      <Alert
                        type="info"
                        showIcon
                        message={t('MongoDB Read-Only Aggregation View')}
                        description={t('Modify the source collection or aggregation pipeline JSON below and save via collMod.')}
                      />
                      <Row gutter={12}>
                        <Col xs={24} sm={12}>
                          <Typography.Text strong>{t('Source Collection (viewOn)')}</Typography.Text>
                          <Input
                            style={{ marginTop: 4 }}
                            disabled={!canWrite}
                            value={editViewOn}
                            onChange={(e) => setEditViewOn(e.target.value)}
                            placeholder="e.g. orders"
                          />
                        </Col>
                      </Row>
                      <div>
                        <Typography.Text strong>{t('Aggregation Pipeline (JSON Array)')}</Typography.Text>
                        <div style={{ height: 260, marginTop: 6, border: '1px solid rgba(128,128,128,0.22)', borderRadius: 6, overflow: 'hidden' }}>
                          <LazyCodeEditor
                            language="json"
                            darkMode={darkMode}
                            readOnly={!canWrite}
                            value={editPipelineJson}
                            onChange={(v) => setEditPipelineJson(v)}
                          />
                        </div>
                      </div>
                      {canWrite && (
                        <div>
                          <Button type="primary" icon={<SaveOutlined />} loading={savingOptions} onClick={handleSaveViewPipeline}>
                            {t('Save View Pipeline')}
                          </Button>
                        </div>
                      )}
                    </Space>
                  ),
                },
              ]
            : [
                {
                  key: 'indexes',
                  label: t('Indexes ({count})', { count: indexes.length }),
                  children: (
                    <div>
                      {canWrite && (
                        <Button
                          size="small"
                          type="primary"
                          icon={<PlusOutlined />}
                          style={{ marginBottom: 8 }}
                          onClick={() => {
                            idxForm.resetFields();
                            setIdxModalOpen(true);
                          }}
                        >
                          {t('Create Index')}
                        </Button>
                      )}
                      <ResizableTable<DocIndexInfo>
                        rowKey="name"
                        size="small"
                        pagination={false}
                        scroll={{ x: 700 }}
                        dataSource={indexes}
                        columns={[
                          { title: t('Index Name'), dataIndex: 'name', width: 180 },
                          {
                            title: t('Index Keys'),
                            dataIndex: 'keys',
                            width: 240,
                            render: (v) => <code>{JSON.stringify(v)}</code>,
                          },
                          {
                            title: t('Properties'),
                            width: 240,
                            render: (_: any, r: DocIndexInfo) => (
                              <Space size={4} wrap>
                                {r.unique && <Tag color="green">UNIQUE</Tag>}
                                {r.sparse && <Tag color="blue">SPARSE</Tag>}
                                {typeof r.expireAfterSeconds === 'number' && (
                                  <Tag color="orange">TTL: {r.expireAfterSeconds}s</Tag>
                                )}
                                {r.partialFilterJson && (
                                  <Tag color="purple" title={r.partialFilterJson}>
                                    {t('Partial')}: {r.partialFilterJson}
                                  </Tag>
                                )}
                                {!r.unique && !r.sparse && typeof r.expireAfterSeconds !== 'number' && !r.partialFilterJson && '-'}
                              </Space>
                            ),
                          },
                          ...(canWrite
                            ? [
                                {
                                  title: t('Actions'),
                                  width: 84,
                                  render: (_: any, r: DocIndexInfo) =>
                                    r.name !== '_id_' ? (
                                      <Popconfirm
                                        title={t('Drop index {name}?', { name: r.name })}
                                        onConfirm={async () => {
                                          try {
                                            await api.docDropIndex(tab.connectionId, tab.database, tab.table || '', r.name);
                                            loadIndexes();
                                          } catch (err: any) {
                                            message.error(err.message);
                                          }
                                        }}
                                      >
                                        <Button size="small" danger type="text" icon={<DeleteOutlined />} />
                                      </Popconfirm>
                                    ) : null,
                                },
                              ]
                            : []),
                        ]}
                      />
                    </div>
                  ),
                },
                {
                  key: 'validation',
                  label: t('Validation (JSON Schema)'),
                  children: (
                    <Space direction="vertical" style={{ width: '100%' }} size={10}>
                      <Row gutter={12}>
                        <Col xs={24} sm={12}>
                          <Typography.Text strong>{t('Validation Level')}</Typography.Text>
                          <Select
                            style={{ width: '100%', marginTop: 4 }}
                            disabled={!canWrite}
                            value={editValidationLevel}
                            onChange={setEditValidationLevel}
                            options={[
                              { label: 'off', value: 'off' },
                              { label: 'strict', value: 'strict' },
                              { label: 'moderate', value: 'moderate' },
                            ]}
                          />
                        </Col>
                        <Col xs={24} sm={12}>
                          <Typography.Text strong>{t('Validation Action')}</Typography.Text>
                          <Select
                            style={{ width: '100%', marginTop: 4 }}
                            disabled={!canWrite}
                            value={editValidationAction}
                            onChange={setEditValidationAction}
                            options={[
                              { label: 'error', value: 'error' },
                              { label: 'warn', value: 'warn' },
                            ]}
                          />
                        </Col>
                      </Row>
                      <div>
                        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 6 }}>
                          <Typography.Text strong>{t('Validator Expression (JSON / $jsonSchema)')}</Typography.Text>
                          {canWrite && (
                            <Button
                              size="small"
                              type="link"
                              onClick={() =>
                                setEditValidatorJson(
                                  JSON.stringify(
                                    {
                                      $jsonSchema: {
                                        bsonType: 'object',
                                        required: ['name'],
                                        properties: {
                                          name: { bsonType: 'string' },
                                        },
                                      },
                                    },
                                    null,
                                    2
                                  )
                                )
                              }
                            >
                              {t('Insert $jsonSchema Template')}
                            </Button>
                          )}
                        </div>
                        <div style={{ height: 240, marginTop: 6, border: '1px solid rgba(128,128,128,0.22)', borderRadius: 6, overflow: 'hidden' }}>
                          <LazyCodeEditor
                            language="json"
                            darkMode={darkMode}
                            readOnly={!canWrite}
                            value={editValidatorJson}
                            onChange={(v) => setEditValidatorJson(v)}
                          />
                        </div>
                      </div>
                      {canWrite && (
                        <div>
                          <Button type="primary" icon={<SaveOutlined />} loading={savingOptions} onClick={handleSaveValidation}>
                            {t('Save Validation Rules')}
                          </Button>
                        </div>
                      )}
                    </Space>
                  ),
                },
              ]),
          {
            key: 'command',
            label: t('RunCommand Console'),
            children: (
              <div>
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  {t('Enter MongoDB Database Command JSON (supports aggregate, find, collStats, explain, etc.):')}
                </Typography.Text>
                <div style={{ height: 180, margin: '8px 0', border: '1px solid rgba(128,128,128,0.2)', borderRadius: 6, overflow: 'hidden' }}>
                  <LazyCodeEditor
                    language="json"
                    darkMode={darkMode}
                    value={cmdText}
                    onChange={(v) => setCmdText(v)}
                  />
                </div>
                <Button
                  type="primary"
                  icon={<PlayCircleOutlined />}
                  onClick={async () => {
                    try {
                      const res = await api.docRunCommand(tab.connectionId, tab.database, cmdText);
                      setCmdResult(JSON.stringify(res.result, null, 2));
                    } catch (err: any) {
                      message.error(err.message);
                    }
                  }}
                >
                  {t('Run Command')}
                </Button>
                {cmdResult && (
                  <pre style={{ marginTop: 10, padding: 10, borderRadius: 8, background: 'rgba(128,128,128,0.1)', maxHeight: 280, overflow: 'auto', fontSize: 12 }}>
                    {cmdResult}
                  </pre>
                )}
              </div>
            ),
          },
        ]}
      />

      <Modal
        open={docModalOpen}
        destroyOnClose
        title={editingId ? t('Edit Document (_id: {id})', { id: editingId }) : t('Insert BSON/JSON Document')}
        width="min(680px, calc(100vw - 16px))"
        onCancel={() => setDocModalOpen(false)}
        onOk={handleSaveDoc}
      >
        {docModalOpen && (
          <div style={{ height: 'min(340px, calc(100vh - 210px))', minHeight: 160, border: '1px solid rgba(128,128,128,0.22)', borderRadius: 6, overflow: 'hidden' }}>
            <LazyCodeEditor
              language="json"
              darkMode={darkMode}
              value={docJsonText}
              onChange={(v) => setDocJsonText(v)}
            />
          </div>
        )}
      </Modal>

      <Modal
        open={importOpen}
        destroyOnClose
        title={t('Import JSON / JSONL Documents')}
        width="min(640px, calc(100vw - 16px))"
        onCancel={() => setImportOpen(false)}
        confirmLoading={importing}
        okText={t('Import Text')}
        onOk={async () => {
          if (!tab.table || !importText.trim()) return;
          setImporting(true);
          try {
            const res = await api.docImportText(tab.connectionId, {
              database: tab.database,
              collection: tab.table,
              content: importText,
              continueOnError: importContinueOnError,
            });
            message.success(t('Imported {count} documents', { count: res.importedRows }));
            setImportOpen(false);
            loadDocs(1, pageSize);
          } catch (err: any) {
            message.error(err.message);
          } finally {
            setImporting(false);
          }
        }}
      >
        <Space direction="vertical" style={{ width: '100%' }} size={10}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 8 }}>
            <Upload
              showUploadList={false}
              accept=".json,.jsonl,.ndjson"
              beforeUpload={async (file) => {
                if (!tab.table) return false;
                setImporting(true);
                try {
                  const res = await api.docImportFile(tab.connectionId, {
                    database: tab.database,
                    collection: tab.table,
                    file,
                    continueOnError: importContinueOnError,
                  });
                  message.success(t('Imported {count} documents', { count: res.importedRows }));
                  setImportOpen(false);
                  loadDocs(1, pageSize);
                } catch (err: any) {
                  message.error(err.message);
                } finally {
                  setImporting(false);
                }
                return false;
              }}
            >
              <Button type="primary" ghost icon={<UploadOutlined />} loading={importing}>
                {t('Select .json / .jsonl File')}
              </Button>
            </Upload>
            <Checkbox checked={importContinueOnError} onChange={(e) => setImportContinueOnError(e.target.checked)}>
              {t('Continue on error')}
            </Checkbox>
          </div>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {t('Or paste a JSON array / JSON Lines (NDJSON) payload below:')}
          </Typography.Text>
          <div style={{ height: 220, border: '1px solid rgba(128,128,128,0.22)', borderRadius: 6, overflow: 'hidden' }}>
            <LazyCodeEditor
              language="json"
              darkMode={darkMode}
              value={importText}
              onChange={(v) => setImportText(v)}
            />
          </div>
        </Space>
      </Modal>

      <Modal
        open={renameOpen}
        title={t('Rename Collection')}
        width="min(460px, calc(100vw - 16px))"
        onCancel={() => setRenameOpen(false)}
        onOk={handleRenameCollection}
        destroyOnClose
      >
        <Input
          autoFocus={!isMobile}
          value={newCollName}
          onChange={(e) => setNewCollName(e.target.value)}
          onPressEnter={handleRenameCollection}
          placeholder={t('Enter new collection name')}
        />
      </Modal>

      <Modal
        open={idxModalOpen}
        title={t('Create MongoDB Index')}
        width="min(560px, calc(100vw - 16px))"
        confirmLoading={idxSubmitting}
        onCancel={() => setIdxModalOpen(false)}
        destroyOnClose
        onOk={async () => {
          try {
            const vals = await idxForm.validateFields();
            setIdxSubmitting(true);
            await api.docCreateIndex(tab.connectionId, {
              database: tab.database,
              collection: tab.table || '',
              name: vals.name?.trim() || undefined,
              keysJson: vals.keysJson,
              unique: Boolean(vals.unique),
              sparse: Boolean(vals.sparse),
              expireAfterSeconds:
                typeof vals.expireAfterSeconds === 'number' ? vals.expireAfterSeconds : undefined,
              partialFilterJson: vals.partialFilterJson?.trim() || undefined,
            });
            message.success(t('Index created'));
            setIdxModalOpen(false);
            loadIndexes();
          } catch (err: any) {
            if (!err?.errorFields) message.error(err.message);
          } finally {
            setIdxSubmitting(false);
          }
        }}
      >
        <Form
          form={idxForm}
          layout="vertical"
          initialValues={{ keysJson: '{"createdAt": -1}', unique: false, sparse: false }}
        >
          <Form.Item name="name" label={t('Index Name (Optional)')}>
            <Input placeholder="e.g. idx_created_at" />
          </Form.Item>
          <Form.Item name="keysJson" label={t('Index Keys JSON (1 asc, -1 desc)')} rules={[{ required: true }]}>
            <Input style={{ fontFamily: 'monospace' }} />
          </Form.Item>
          <Space size={16} wrap style={{ marginBottom: 8 }}>
            <Form.Item name="unique" valuePropName="checked" noStyle>
              <Checkbox>{t('Unique Index (Unique)')}</Checkbox>
            </Form.Item>
            <Form.Item name="sparse" valuePropName="checked" noStyle>
              <Checkbox>{t('Sparse Index')}</Checkbox>
            </Form.Item>
          </Space>
          <Form.Item name="expireAfterSeconds" label={t('TTL Expire After Seconds (Optional)')}>
            <InputNumber min={0} style={{ width: '100%' }} placeholder="e.g. 3600" />
          </Form.Item>
          <Form.Item name="partialFilterJson" label={t('Partial Filter Expression JSON (Optional)')}>
            <Input style={{ fontFamily: 'monospace' }} placeholder='e.g. {"status": "active"}' />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
};
