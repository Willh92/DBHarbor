import React, { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Button,
  Checkbox,
  Dropdown,
  Empty,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Segmented,
  Select,
  Space,
  Tag,
  Tooltip,
  Typography,
  Upload,
  message,
} from 'antd';
import {
  ClearOutlined,
  CodeOutlined,
  CopyOutlined,
  DeleteOutlined,
  DownloadOutlined,
  EditOutlined,
  MoreOutlined,
  PlusOutlined,
  ReloadOutlined,
  SaveOutlined,
  SearchOutlined,
  TeamOutlined,
  UploadOutlined,
} from '@ant-design/icons';
import { api } from '../../api/client';
import { useResponsive } from '../../hooks/useResponsive';
import { useI18n } from '../../i18n/useI18n';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import type { KVCollectionItem, KVEntryDetail, KVKeySummary, WorkspaceTab } from '../../types';
import { formatRawCellText } from '../../utils/tableClipboard';
import { ResizableTable, calcSmartColumnWidth } from '../common/ResizableTable';

const TYPE_COLORS: Record<string, string> = {
  string: 'blue',
  hash: 'purple',
  list: 'cyan',
  set: 'green',
  zset: 'orange',
  stream: 'magenta',
};

export const RedisBrowserTab: React.FC<{ tab: WorkspaceTab }> = ({ tab }) => {
  const { t } = useI18n();
  const { isMobile } = useResponsive();
  const isDesktop = useWorkbenchStore((s) => s.isDesktop);
  const role = useWorkbenchStore((s) => s.role);
  const openTab = useWorkbenchStore((s) => s.openTab);
  const canWrite = isDesktop || role !== 'readonly';

  const initialDB = Number((tab.database || 'db0').replace('db', '')) || 0;
  const [dbIndex, setDbIndex] = useState<number>(initialDB);
  const [mobilePane, setMobilePane] = useState<'keys' | 'value' | 'cli'>('keys');
  const [pattern, setPattern] = useState<string>('*');
  const [keys, setKeys] = useState<KVKeySummary[]>([]);
  const [nextCursor, setNextCursor] = useState<number>(0);
  const [dbCounts, setDbCounts] = useState<Record<string, number>>({});
  const [loading, setLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [checkedKeys, setCheckedKeys] = useState<React.Key[]>([]);

  const [selectedKey, setSelectedKey] = useState<string>('');
  const [detail, setDetail] = useState<KVEntryDetail | null>(null);
  const [editStringVal, setEditStringVal] = useState<string>('');
  const [ttlInput, setTtlInput] = useState<number>(-1);

  // CLI Console
  const [cmdInput, setCmdInput] = useState('INFO server');
  const [cmdOutput, setCmdOutput] = useState('');

  // New key modal
  const [newKeyOpen, setNewKeyOpen] = useState(false);
  const [newKeyForm] = Form.useForm();

  // Add / Edit item modal (hash/list/set/zset)
  const [itemModalOpen, setItemModalOpen] = useState(false);
  const [editingItem, setEditingItem] = useState<KVCollectionItem | null>(null);
  const [itemForm] = Form.useForm();

  // Rename / Duplicate key modal
  const [keyActionModal, setKeyActionModal] = useState<{ mode: 'rename' | 'duplicate'; open: boolean }>({
    mode: 'rename',
    open: false,
  });
  const [targetKeyName, setTargetKeyName] = useState('');
  const [overwriteTargetKey, setOverwriteTargetKey] = useState(false);

  // Flush DB confirmation modal
  const [flushModalOpen, setFlushModalOpen] = useState(false);
  const [flushConfirmText, setFlushConfirmText] = useState('');

  const loadKeys = async (targetDB = dbIndex, pat = pattern) => {
    setLoading(true);
    try {
      const res = await api.kvScan(tab.connectionId, targetDB, pat || '*', 0);
      setKeys(res.keys || []);
      setNextCursor(res.cursor || 0);
      setDbCounts(res.dbKeys || {});
      setCheckedKeys([]);
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setLoading(false);
    }
  };

  const handleLoadMore = async () => {
    if (!nextCursor) return;
    setLoadingMore(true);
    try {
      const res = await api.kvScan(tab.connectionId, dbIndex, pattern || '*', nextCursor);
      setNextCursor(res.cursor || 0);
      setDbCounts(res.dbKeys || {});
      setKeys((prev) => {
        const seen = new Set(prev.map((k) => k.key));
        const extra = (res.keys || []).filter((k) => !seen.has(k.key));
        return [...prev, ...extra];
      });
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setLoadingMore(false);
    }
  };

  useEffect(() => {
    loadKeys(dbIndex, pattern);
  }, [tab.connectionId, dbIndex]);

  const loadKeyDetail = useCallback(
    async (key: string) => {
      setSelectedKey(key);
      try {
        const d = await api.kvGetKey(tab.connectionId, dbIndex, key);
        setDetail(d);
        setTtlInput(d.ttl);
        if (d.type === 'string') {
          try {
            const parsed = JSON.parse(d.stringValue || '');
            setEditStringVal(JSON.stringify(parsed, null, 2));
          } catch {
            setEditStringVal(d.stringValue || '');
          }
        }
      } catch (err: any) {
        message.error(err.message);
      }
    },
    [tab.connectionId, dbIndex]
  );

  const handleSaveString = async () => {
    if (!detail || !canWrite) return;
    try {
      await api.kvSetKey(tab.connectionId, {
        db: dbIndex,
        key: detail.key,
        type: 'string',
        ttl: ttlInput,
        stringValue: editStringVal,
      });
      message.success(t('Key value saved'));
      loadKeyDetail(detail.key);
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleUpdateTTL = async () => {
    if (!detail || !canWrite) return;
    try {
      await api.kvSetTTL(tab.connectionId, dbIndex, detail.key, ttlInput);
      message.success(t('TTL updated'));
      loadKeyDetail(detail.key);
      loadKeys();
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleBatchDelete = async () => {
    if (!canWrite || checkedKeys.length === 0) return;
    try {
      const targets = checkedKeys.map(String);
      const res = await api.kvDeleteKeys(tab.connectionId, dbIndex, targets);
      message.success(t('Deleted {count} keys', { count: res.deleted }));
      if (detail && targets.includes(detail.key)) {
        setDetail(null);
        setSelectedKey('');
      }
      loadKeys();
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleRenameOrDuplicate = async () => {
    if (!detail || !targetKeyName.trim()) return;
    const dst = targetKeyName.trim();
    try {
      if (keyActionModal.mode === 'rename') {
        await api.kvRenameKey(tab.connectionId, dbIndex, detail.key, dst, !overwriteTargetKey);
        message.success(t('Key renamed to {key}', { key: dst }));
      } else {
        await api.kvDuplicateKey(tab.connectionId, dbIndex, detail.key, dst, overwriteTargetKey);
        message.success(t('Key duplicated to {key}', { key: dst }));
      }
      setKeyActionModal({ mode: 'rename', open: false });
      await loadKeys();
      await loadKeyDetail(dst);
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleFlushDB = async () => {
    if (flushConfirmText.trim().toUpperCase() !== `DB${dbIndex}`) return;
    try {
      await api.kvFlushDB(tab.connectionId, dbIndex);
      message.success(t('Flushed DB{db}', { db: dbIndex }));
      setFlushModalOpen(false);
      setFlushConfirmText('');
      setDetail(null);
      setSelectedKey('');
      loadKeys();
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleCreateKey = async () => {
    try {
      const vals = await newKeyForm.validateFields();
      const payload: any = {
        db: dbIndex,
        key: vals.key,
        type: vals.type,
        ttl: vals.ttl ?? -1,
      };
      if (vals.type === 'string') {
        payload.stringValue = vals.value || '';
      } else {
        payload.items = [{ field: vals.field || 'field1', value: vals.value || '', score: Number(vals.score || 0) }];
      }
      await api.kvSetKey(tab.connectionId, payload);
      message.success(t('Key {key} created', { key: vals.key }));
      setNewKeyOpen(false);
      newKeyForm.resetFields();
      await loadKeys();
      await loadKeyDetail(vals.key);
    } catch (err: any) {
      if (!err?.errorFields) message.error(err.message);
    }
  };

  const handleRunCmd = async () => {
    if (!cmdInput.trim()) return;
    try {
      const res = await api.kvCommand(tab.connectionId, dbIndex, cmdInput.trim());
      setCmdOutput(`> ${res.command}  (${res.elapsedMs}ms)\n${res.output}`);
    } catch (err: any) {
      setCmdOutput(`> ${cmdInput}\nERROR: ${err.message}`);
    }
  };

  const handleExportKeys = async (onlySelected: boolean) => {
    try {
      const data = await api.kvExportKeys(tab.connectionId, {
        db: dbIndex,
        pattern: pattern || '*',
        keys: onlySelected && checkedKeys.length > 0 ? checkedKeys.map(String) : undefined,
        maxKeys: 2000,
      });
      const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json;charset=utf-8' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `redis_db${dbIndex}_keys_${new Date().toISOString().slice(0, 10)}.json`;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      setTimeout(() => URL.revokeObjectURL(url), 1500);
      message.success(t('Exported {count} Redis keys', { count: data.entries?.length || 0 }));
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleImportKeysFile = async (file: File) => {
    try {
      const rawText = await file.text();
      const parsed = JSON.parse(rawText);
      const entries = Array.isArray(parsed) ? parsed : Array.isArray(parsed?.entries) ? parsed.entries : [];
      if (!entries.length) {
        message.warning(t('No valid Redis key entries found in JSON file'));
        return false;
      }
      const res = await api.kvImportKeys(tab.connectionId, {
        db: dbIndex,
        overwrite: true,
        entries,
      });
      message.success(t('Imported {count} Redis keys', { count: res.imported }));
      loadKeys();
    } catch (err: any) {
      message.error(err.message || t('Failed to import Redis keys'));
    }
    return false;
  };

  const dbOptions = useMemo(
    () =>
      Array.from({ length: 16 }, (_, i) => ({
        value: i,
        label: `DB${i} (${dbCounts[i] || 0})`,
      })),
    [dbCounts]
  );

  const keyColumns = useMemo(
    () => [
      {
        title: t('Type'),
        dataIndex: 'type',
        width: 72,
        render: (tp: string) => (
          <Tag color={TYPE_COLORS[tp] || 'default'} style={{ margin: 0, fontSize: 10 }}>
            {tp.toUpperCase()}
          </Tag>
        ),
      },
      {
        title: t('Key Name'),
        dataIndex: 'key',
        width: 160,
        ellipsis: true,
        render: (k: string) => <span data-raw-copy={k}>{k}</span>,
      },
      {
        title: 'TTL',
        dataIndex: 'ttl',
        width: 68,
        render: (v: number) => (v < 0 ? t('No Expiry') : `${v}s`),
      },
    ],
    [t]
  );

  const itemColumns = useMemo(() => {
    if (!detail) return [];
    const detailKey = detail.key;
    const detailType = detail.type;
    return [
      ...(detailType === 'hash'
        ? [{ title: 'Field', dataIndex: 'field', width: calcSmartColumnWidth('Field', { minBodyWidth: 150 }) }]
        : []),
      ...(detailType === 'list' ? [{ title: 'Index', dataIndex: 'index', width: 84 }] : []),
      ...(detailType === 'zset' ? [{ title: 'Score', dataIndex: 'score', width: 100 }] : []),
      ...(detailType === 'stream' ? [{ title: 'Entry ID', dataIndex: 'id', width: 160 }] : []),
      {
        title: 'Value',
        dataIndex: 'value',
        width: 240,
        ellipsis: true,
        render: (v: any) => <span data-raw-copy={formatRawCellText(v)}>{String(v ?? '')}</span>,
      },
      ...(detailType !== 'stream' && canWrite
        ? [
            {
              title: t('Actions'),
              width: 96,
              resizable: false,
              onHeaderCell: () => ({ 'data-copy-skip': 'true' } as any),
              render: (_: any, r: KVCollectionItem) => (
                <Space size={2} data-copy-skip="true">
                  <Button
                    size="small"
                    type="text"
                    icon={<EditOutlined />}
                    title={t('Edit')}
                    onClick={() => {
                      setEditingItem(r);
                      itemForm.setFieldsValue({
                        field: r.field,
                        value: r.value,
                        score: r.score ?? 0,
                      });
                      setItemModalOpen(true);
                    }}
                  />
                  <Button
                    size="small"
                    type="text"
                    danger
                    icon={<DeleteOutlined />}
                    title={t('Delete')}
                    onClick={async () => {
                      await api.kvMutateItem(tab.connectionId, {
                        db: dbIndex,
                        key: detailKey,
                        type: detailType,
                        action: 'DELETE',
                        field: r.field,
                        value: r.value,
                      });
                      loadKeyDetail(detailKey);
                    }}
                  />
                </Space>
              ),
            },
          ]
        : []),
    ];
  }, [detail?.key, detail?.type, tab.connectionId, dbIndex, loadKeyDetail, canWrite, itemForm, t]);

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', minWidth: 0, minHeight: 0, padding: isMobile ? 6 : 12, gap: 8, overflow: 'hidden' }}>
      {isMobile && (
        <Segmented
          block
          size="small"
          value={mobilePane}
          onChange={(v) => setMobilePane(v as 'keys' | 'value' | 'cli')}
          options={[
            { label: `${t('Keys')} (${keys.length})`, value: 'keys' },
            { label: selectedKey ? `${t('Value')}: ${selectedKey.slice(0, 10)}` : t('Value'), value: 'value' },
            { label: 'CLI', value: 'cli' },
          ]}
        />
      )}
      <div className="dbharbor-redis-split">
        {/* Left Key List */}
        <div
          className="dbharbor-redis-left"
          style={isMobile ? { display: mobilePane === 'keys' ? 'flex' : 'none', flex: 1, maxHeight: 'none' } : undefined}
        >
          <div style={{ display: 'flex', gap: 6, marginBottom: 8, flexWrap: 'wrap', flexShrink: 0 }}>
            <Select
              size="small"
              style={{ width: 108 }}
              value={dbIndex}
              onChange={(v) => {
                setDbIndex(v);
                setDetail(null);
              }}
              options={dbOptions}
            />
            <Input
              size="small"
              style={{ flex: 1, minWidth: 100 }}
              prefix={<SearchOutlined />}
              placeholder={t('Key pattern, e.g. user:*')}
              value={pattern}
              onChange={(e) => setPattern(e.target.value)}
              onPressEnter={() => loadKeys(dbIndex, pattern)}
            />
            <Tooltip title={t('Refresh')}>
              <Button size="small" icon={<ReloadOutlined />} onClick={() => loadKeys()} />
            </Tooltip>
            {canWrite && (
              <Tooltip title={t('New Redis Key (DB{db})', { db: dbIndex })}>
                <Button size="small" type="primary" icon={<PlusOutlined />} onClick={() => setNewKeyOpen(true)} />
              </Tooltip>
            )}
            <Dropdown
              trigger={['click']}
              menu={{
                items: [
                  {
                    key: 'exp_all',
                    icon: <DownloadOutlined />,
                    label: t('Export Keys (.json)'),
                    onClick: () => handleExportKeys(false),
                  },
                  ...(checkedKeys.length > 0
                    ? [
                        {
                          key: 'exp_sel',
                          icon: <DownloadOutlined />,
                          label: `${t('Export Selected')} (${checkedKeys.length})`,
                          onClick: () => handleExportKeys(true),
                        },
                      ]
                    : []),
                  {
                    key: 'users',
                    icon: <TeamOutlined />,
                    label: t('Users & Grants'),
                    onClick: () =>
                      openTab({
                        key: `dbusers:${tab.connectionId}`,
                        title: `${tab.connName} (${t('Users & Grants')})`,
                        kind: 'db_users',
                        connectionId: tab.connectionId,
                        connName: tab.connName,
                        dbType: tab.dbType,
                        database: `db${dbIndex}`,
                      }),
                  },
                  ...(canWrite
                    ? [
                        { type: 'divider' as const },
                        {
                          key: 'flush',
                          danger: true,
                          icon: <ClearOutlined />,
                          label: t('Flush DB'),
                          onClick: () => {
                            setFlushConfirmText('');
                            setFlushModalOpen(true);
                          },
                        },
                      ]
                    : []),
                ],
              }}
            >
              <Button size="small" icon={<MoreOutlined />} />
            </Dropdown>
            {canWrite && (
              <Upload showUploadList={false} accept=".json" beforeUpload={handleImportKeysFile}>
                <Tooltip title={t('Import Keys (.json)')}>
                  <Button size="small" icon={<UploadOutlined />} />
                </Tooltip>
              </Upload>
            )}
          </div>

          {canWrite && checkedKeys.length > 0 && (
            <div style={{ marginBottom: 6, display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexShrink: 0 }}>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {t('Selected {count} keys', { count: checkedKeys.length })}
              </Typography.Text>
              <Popconfirm
                title={t('Delete {count} selected keys?', { count: checkedKeys.length })}
                onConfirm={handleBatchDelete}
              >
                <Button size="small" danger icon={<DeleteOutlined />}>
                  {t('Delete Selected ({count})', { count: checkedKeys.length })}
                </Button>
              </Popconfirm>
            </div>
          )}

          <div style={{ flex: 1, minHeight: 0, overflowY: 'auto' }}>
            <ResizableTable<KVKeySummary>
              rowKey="key"
              size="small"
              loading={loading}
              dataSource={keys}
              scroll={{ x: 280 }}
              rowSelection={
                canWrite
                  ? {
                      columnWidth: 34,
                      selectedRowKeys: checkedKeys,
                      onChange: setCheckedKeys,
                    }
                  : undefined
              }
              pagination={{ pageSize: 50, size: 'small', responsive: true }}
              onRow={(r) => ({
                onClick: () => {
                  loadKeyDetail(r.key);
                  if (isMobile) setMobilePane('value');
                },
                style: { cursor: 'pointer', background: r.key === selectedKey ? 'rgba(22,119,255,0.12)' : undefined },
              })}
              columns={keyColumns}
            />
            {nextCursor !== 0 && (
              <div style={{ textAlign: 'center', padding: '6px 0' }}>
                <Button size="small" loading={loadingMore} onClick={handleLoadMore}>
                  {t('Load More Keys')}
                </Button>
              </div>
            )}
          </div>
        </div>

        {/* Right Value Inspector & Editor */}
        <div
          className="dbharbor-redis-right"
          style={isMobile ? { display: mobilePane === 'keys' ? 'none' : 'flex' } : undefined}
        >
          {(!isMobile || mobilePane === 'value') && (!detail ? (
            <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', minHeight: 160 }}>
              <Empty description={t('Select a key on the left to inspect or edit')} />
            </div>
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 8 }}>
                <Space wrap>
                  <Tag color={TYPE_COLORS[detail.type] || 'blue'}>{detail.type.toUpperCase()}</Tag>
                  <Typography.Text strong copyable>
                    {detail.key}
                  </Typography.Text>
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    ({detail.length})
                  </Typography.Text>
                </Space>

                <Space wrap size={6}>
                  <InputNumber
                    size="small"
                    addonBefore="TTL(s)"
                    disabled={!canWrite}
                    value={ttlInput}
                    onChange={(v) => setTtlInput(v ?? -1)}
                    style={{ width: 145 }}
                  />
                  {canWrite && (
                    <>
                      <Button size="small" onClick={handleUpdateTTL}>
                        {t('Update TTL')}
                      </Button>
                      <Button
                        size="small"
                        icon={<EditOutlined />}
                        onClick={() => {
                          setTargetKeyName(detail.key);
                          setOverwriteTargetKey(false);
                          setKeyActionModal({ mode: 'rename', open: true });
                        }}
                      >
                        {t('Rename')}
                      </Button>
                      <Button
                        size="small"
                        icon={<CopyOutlined />}
                        onClick={() => {
                          setTargetKeyName(`${detail.key}:copy`);
                          setOverwriteTargetKey(false);
                          setKeyActionModal({ mode: 'duplicate', open: true });
                        }}
                      >
                        {t('Duplicate')}
                      </Button>
                      <Popconfirm
                        title={t('Delete key {key}?', { key: detail.key })}
                        onConfirm={async () => {
                          await api.kvDeleteKeys(tab.connectionId, dbIndex, [detail.key]);
                          message.success(t('Key deleted'));
                          setDetail(null);
                          loadKeys();
                        }}
                      >
                        <Button size="small" danger icon={<DeleteOutlined />}>
                          {t('Delete Key')}
                        </Button>
                      </Popconfirm>
                    </>
                  )}
                </Space>
              </div>

              {detail.type === 'string' ? (
                <div>
                  <Input.TextArea
                    rows={12}
                    readOnly={!canWrite}
                    value={editStringVal}
                    onChange={(e) => setEditStringVal(e.target.value)}
                    style={{ fontFamily: 'monospace', fontSize: 13 }}
                  />
                  {canWrite && (
                    <Button type="primary" icon={<SaveOutlined />} style={{ marginTop: 8 }} onClick={handleSaveString}>
                      {t('Save String')}
                    </Button>
                  )}
                </div>
              ) : (
                <div>
                  {detail.type !== 'stream' && canWrite && (
                    <Button
                      size="small"
                      type="primary"
                      icon={<PlusOutlined />}
                      style={{ marginBottom: 8 }}
                      onClick={() => {
                        setEditingItem(null);
                        itemForm.resetFields();
                        setItemModalOpen(true);
                      }}
                    >
                      {t('Add Item')}
                    </Button>
                  )}
                  <ResizableTable<KVCollectionItem>
                    size="small"
                    bordered
                    rowKey={(r, idx) => r.field || r.id || `${idx}_${r.value}`}
                    dataSource={detail.items}
                    scroll={{ x: 480 }}
                    pagination={{ pageSize: 30, size: 'small', responsive: true }}
                    columns={itemColumns}
                  />
                </div>
              )}
            </div>
          ))}

          {/* Embedded Redis Command Console */}
          {(!isMobile || mobilePane === 'cli') && (
            <div style={{ marginTop: isMobile ? 0 : 'auto', paddingTop: isMobile ? 0 : 12, borderTop: isMobile ? 'none' : '1px solid rgba(128,128,128,0.18)' }}>
              <Space.Compact style={{ width: '100%' }}>
                <Input
                  prefix={<CodeOutlined />}
                  placeholder={t('Enter Redis command, e.g. GET key / HGETALL user:1 / SLOWLOG GET 10')}
                  value={cmdInput}
                  onChange={(e) => setCmdInput(e.target.value)}
                  onPressEnter={handleRunCmd}
                />
                <Button type="primary" onClick={handleRunCmd}>
                  {t('Run Command')}
                </Button>
              </Space.Compact>
              {cmdOutput && (
                <pre
                  style={{
                    marginTop: 6,
                    maxHeight: isMobile ? 'calc(100dvh - 210px)' : 130,
                    overflow: 'auto',
                    padding: 8,
                    borderRadius: 6,
                    background: 'rgba(0,0,0,0.78)',
                    color: '#95de64',
                    fontSize: 12,
                  }}
                >
                  {cmdOutput}
                </pre>
              )}
            </div>
          )}
        </div>
      </div>

      <Modal
        open={newKeyOpen}
        title={t('New Redis Key (DB{db})', { db: dbIndex })}
        onCancel={() => setNewKeyOpen(false)}
        onOk={handleCreateKey}
        destroyOnClose
      >
        <Form form={newKeyForm} layout="vertical" initialValues={{ type: 'string', ttl: -1 }}>
          <Form.Item name="key" label={t('Key Name')} rules={[{ required: true }]}>
            <Input placeholder="e.g. app:config:feature_flag" />
          </Form.Item>
          <Form.Item name="type" label={t('Data Structure Type')} rules={[{ required: true }]}>
            <Select
              options={[
                { label: 'String', value: 'string' },
                { label: 'Hash', value: 'hash' },
                { label: 'List', value: 'list' },
                { label: 'Set', value: 'set' },
                { label: 'Sorted Set (ZSet)', value: 'zset' },
              ]}
            />
          </Form.Item>
          <Form.Item noStyle shouldUpdate>
            {({ getFieldValue }) =>
              getFieldValue('type') === 'hash' ? (
                <Form.Item name="field" label={t('Initial Hash Field')}>
                  <Input placeholder="field1" />
                </Form.Item>
              ) : getFieldValue('type') === 'zset' ? (
                <Form.Item name="score" label={t('Initial Score')}>
                  <InputNumber style={{ width: '100%' }} />
                </Form.Item>
              ) : null
            }
          </Form.Item>
          <Form.Item name="value" label={t('Initial Value')} rules={[{ required: true }]}>
            <Input.TextArea rows={3} />
          </Form.Item>
          <Form.Item name="ttl" label={t('TTL in Seconds (-1 for no expiry)')}>
            <InputNumber style={{ width: '100%' }} />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        open={itemModalOpen}
        title={editingItem ? t('Edit Item in {key}', { key: detail?.key || '' }) : t('Add Item to {key}', { key: detail?.key || '' })}
        onCancel={() => {
          setItemModalOpen(false);
          setEditingItem(null);
        }}
        destroyOnClose
        onOk={async () => {
          const vals = await itemForm.validateFields();
          await api.kvMutateItem(tab.connectionId, {
            db: dbIndex,
            key: detail?.key,
            type: detail?.type,
            action: editingItem ? 'UPDATE' : 'ADD',
            field: vals.field ?? editingItem?.field,
            index: editingItem?.index,
            oldValue: editingItem?.value,
            value: vals.value,
            score: Number(vals.score || 0),
          });
          setItemModalOpen(false);
          setEditingItem(null);
          itemForm.resetFields();
          if (detail) loadKeyDetail(detail.key);
        }}
      >
        <Form form={itemForm} layout="vertical">
          {detail?.type === 'hash' && (
            <Form.Item name="field" label="Field" rules={[{ required: true }]}>
              <Input disabled={Boolean(editingItem)} />
            </Form.Item>
          )}
          {detail?.type === 'zset' && (
            <Form.Item name="score" label="Score" initialValue={0}>
              <InputNumber style={{ width: '100%' }} />
            </Form.Item>
          )}
          <Form.Item name="value" label="Value" rules={[{ required: true }]}>
            <Input.TextArea rows={3} />
          </Form.Item>
        </Form>
      </Modal>

      {/* Rename / Duplicate Key Modal */}
      <Modal
        open={keyActionModal.open}
        title={keyActionModal.mode === 'rename' ? t('Rename Redis Key') : t('Duplicate Redis Key')}
        onCancel={() => setKeyActionModal((s) => ({ ...s, open: false }))}
        onOk={handleRenameOrDuplicate}
        destroyOnClose
      >
        <Space direction="vertical" style={{ width: '100%' }} size={10}>
          <Typography.Text type="secondary">
            {t('Source Key')}: <code>{detail?.key}</code>
          </Typography.Text>
          <Input
            autoFocus
            value={targetKeyName}
            onChange={(e) => setTargetKeyName(e.target.value)}
            onPressEnter={handleRenameOrDuplicate}
            placeholder={t('Enter target key name')}
          />
          <Checkbox checked={overwriteTargetKey} onChange={(e) => setOverwriteTargetKey(e.target.checked)}>
            {t('Overwrite if target key already exists')}
          </Checkbox>
        </Space>
      </Modal>

      {/* Flush DB Confirmation Modal */}
      <Modal
        open={flushModalOpen}
        title={t('Flush Redis Database (DB{db})', { db: dbIndex })}
        okText={t('Flush DB')}
        okButtonProps={{
          danger: true,
          disabled: flushConfirmText.trim().toUpperCase() !== `DB${dbIndex}`,
        }}
        onCancel={() => setFlushModalOpen(false)}
        onOk={handleFlushDB}
        destroyOnClose
      >
        <Space direction="vertical" style={{ width: '100%' }} size={10}>
          <Typography.Text type="danger">
            {t('Warning: FLUSHDB will permanently delete ALL keys inside DB{db}. Type DB{db} below to confirm:', {
              db: dbIndex,
            })}
          </Typography.Text>
          <Input
            autoFocus
            value={flushConfirmText}
            onChange={(e) => setFlushConfirmText(e.target.value)}
            placeholder={`DB${dbIndex}`}
          />
        </Space>
      </Modal>
    </div>
  );
};
