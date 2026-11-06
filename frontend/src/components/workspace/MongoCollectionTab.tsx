import React, { useEffect, useMemo, useState } from 'react';
import {
  Button,
  Checkbox,
  Dropdown,
  Form,
  Input,
  Modal,
  Pagination,
  Popconfirm,
  Segmented,
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
  PlayCircleOutlined,
  PlusOutlined,
  ReloadOutlined,
  TeamOutlined,
  UploadOutlined,
} from '@ant-design/icons';
import { api } from '../../api/client';
import { useResponsive } from '../../hooks/useResponsive';
import { useI18n } from '../../i18n/useI18n';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import type { DocIndexInfo, WorkspaceTab } from '../../types';
import { formatRawCellText, writeTextToClipboard } from '../../utils/tableClipboard';
import { LazyCodeEditor } from '../common/LazyCodeEditor';
import { ResizableTable, calcSmartColumnWidth } from '../common/ResizableTable';

export const MongoCollectionTab: React.FC<{ tab: WorkspaceTab }> = ({ tab }) => {
  const darkMode = useWorkbenchStore((s) => s.darkMode);
  const isDesktop = useWorkbenchStore((s) => s.isDesktop);
  const role = useWorkbenchStore((s) => s.role);
  const updateTab = useWorkbenchStore((s) => s.updateTab);
  const bumpTreeRefresh = useWorkbenchStore((s) => s.bumpTreeRefresh);
  const openTab = useWorkbenchStore((s) => s.openTab);
  const canWrite = isDesktop || role !== 'readonly';
  const { isMobile } = useResponsive();
  const { t } = useI18n();

  const [viewMode, setViewMode] = useState<'json' | 'table'>('json');
  const [filterJson, setFilterJson] = useState('{}');
  const [sortJson, setSortJson] = useState('{"_id": -1}');
  const [docs, setDocs] = useState<Record<string, any>[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(30);
  const [loading, setLoading] = useState(false);
  const [selectedIds, setSelectedIds] = useState<React.Key[]>([]);

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
  const [idxForm] = Form.useForm();
  const [cmdText, setCmdText] = useState(`{\n  "count": "${tab.table || 'users'}"\n}`);
  const [cmdResult, setCmdResult] = useState('');

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
      setDocs(res.documents || []);
      setTotal(res.total || 0);
      setSelectedIds([]);
    } catch (err: any) {
      message.error(err.message);
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
      // ignore index fetch error on restricted users
    }
  };

  useEffect(() => {
    loadDocs(1, pageSize);
    loadIndexes();
  }, [tab.connectionId, tab.database, tab.table]);

  const handleSaveDoc = async () => {
    if (!tab.table || !canWrite) return;
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
    if (!tab.table || !canWrite || selectedIds.length === 0) return;
    try {
      await api.docDelete(tab.connectionId, tab.database, tab.table, selectedIds.map(String));
      message.success(t('Deleted {count} documents', { count: selectedIds.length }));
      loadDocs();
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleRenameCollection = async () => {
    const next = newCollName.trim();
    if (!tab.table || !next || next === tab.table) return;
    try {
      await api.docRenameCollection(tab.connectionId, tab.database, tab.table, next);
      message.success(t('Collection renamed to {name}', { name: next }));
      setRenameOpen(false);
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
      message.error(err.message);
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
        width: canWrite ? 116 : 56,
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
              {canWrite && (
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
  }, [docs, canWrite, t]);

  return (
    <div style={{ padding: isMobile ? 6 : 12, height: '100%', minWidth: 0, minHeight: 0, overflowY: 'auto' }}>
      <Tabs
        size="small"
        tabBarExtraContent={
          <Space size={6} wrap>
            {canWrite && (
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
            {canWrite && (
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
                  key: `dbusers:${tab.connectionId}`,
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
          </Space>
        }
        items={[
          {
            key: 'docs',
            label: t('Documents ({count})', { count: total }),
            children: (
              <div>
                <div style={{ display: 'flex', gap: 8, marginBottom: 10, flexWrap: 'wrap', alignItems: 'center' }}>
                  <Input
                    addonBefore="Filter"
                    style={{ width: 300, maxWidth: '100%', fontFamily: 'monospace' }}
                    size="small"
                    value={filterJson}
                    onChange={(e) => setFilterJson(e.target.value)}
                    onPressEnter={() => loadDocs(1, pageSize)}
                  />
                  <Input
                    addonBefore="Sort"
                    style={{ width: 190, maxWidth: '100%', fontFamily: 'monospace' }}
                    size="small"
                    value={sortJson}
                    onChange={(e) => setSortJson(e.target.value)}
                    onPressEnter={() => loadDocs(1, pageSize)}
                  />
                  <Button size="small" type="primary" onClick={() => loadDocs(1, pageSize)}>
                    {t('Query')}
                  </Button>
                  {canWrite && (
                    <Button
                      size="small"
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
                  {canWrite && selectedIds.length > 0 && (
                    <Popconfirm
                      title={t('Delete {count} selected documents?', { count: selectedIds.length })}
                      onConfirm={handleBatchDelete}
                    >
                      <Button size="small" danger icon={<DeleteOutlined />}>
                        {t('Delete Selected ({count})', { count: selectedIds.length })}
                      </Button>
                    </Popconfirm>
                  )}
                  <Button size="small" icon={<ReloadOutlined />} onClick={() => loadDocs()} />
                  <Segmented
                    size="small"
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
                              {canWrite && (
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
                                      await api.docDelete(tab.connectionId, tab.database, tab.table || '', [idStr]);
                                      message.success(t('Document deleted'));
                                      loadDocs();
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
                    rowKey={(r) => String(r._id)}
                    rowSelection={
                      canWrite
                        ? {
                            columnWidth: 40,
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
                    onClick={() => setIdxModalOpen(true)}
                  >
                    {t('Create Index')}
                  </Button>
                )}
                <ResizableTable<DocIndexInfo>
                  rowKey="name"
                  size="small"
                  pagination={false}
                  scroll={{ x: 620 }}
                  dataSource={indexes}
                  columns={[
                    { title: t('Index Name'), dataIndex: 'name', width: 180 },
                    { title: t('Index Keys'), dataIndex: 'keys', width: 250, render: (v) => <code>{JSON.stringify(v)}</code> },
                    { title: t('Unique'), dataIndex: 'unique', width: 100, render: (u) => (u ? <Tag color="green">UNIQUE</Tag> : '-') },
                    ...(canWrite
                      ? [
                          {
                            title: t('Actions'),
                            width: 90,
                            render: (_: any, r: DocIndexInfo) =>
                              r.name !== '_id_' ? (
                                <Popconfirm
                                  title={t('Drop index {name}?', { name: r.name })}
                                  onConfirm={async () => {
                                    await api.docDropIndex(tab.connectionId, tab.database, tab.table || '', r.name);
                                    loadIndexes();
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
        width="min(680px, calc(100vw - 24px))"
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
        width="min(640px, calc(100vw - 24px))"
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
        onCancel={() => setRenameOpen(false)}
        onOk={handleRenameCollection}
        destroyOnClose
      >
        <Input
          autoFocus
          value={newCollName}
          onChange={(e) => setNewCollName(e.target.value)}
          onPressEnter={handleRenameCollection}
          placeholder={t('Enter new collection name')}
        />
      </Modal>

      <Modal
        open={idxModalOpen}
        title={t('Create MongoDB Index')}
        onCancel={() => setIdxModalOpen(false)}
        onOk={async () => {
          const vals = await idxForm.validateFields();
          await api.docCreateIndex(tab.connectionId, tab.database, tab.table || '', vals.keysJson, Boolean(vals.unique));
          setIdxModalOpen(false);
          loadIndexes();
        }}
      >
        <Form form={idxForm} layout="vertical" initialValues={{ keysJson: '{"createdAt": -1}', unique: false }}>
          <Form.Item name="keysJson" label={t('Index Keys JSON (1 asc, -1 desc)')} rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="unique" valuePropName="checked">
            <Checkbox>{t('Unique Index (Unique)')}</Checkbox>
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
};
