import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
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
  Tabs,
  Tag,
  Tooltip,
  Typography,
  Upload,
  message,
} from 'antd';
import {
  ApartmentOutlined,
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
  ScissorOutlined,
  SearchOutlined,
  SendOutlined,
  TeamOutlined,
  ToolOutlined,
  UploadOutlined,
} from '@ant-design/icons';
import { api } from '../../api/client';
import { useResponsive } from '../../hooks/useResponsive';
import { useI18n } from '../../i18n/useI18n';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import type {
  KVCollectionItem,
  KVConfigItem,
  KVEntryDetail,
  KVKeySummary,
  KVSlowlogEntry,
  KVStreamConsumer,
  KVStreamGroup,
  KVStreamInfo,
  WorkspaceTab,
} from '../../types';
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

function formatMemBytes(bytes?: number): string {
  if (!bytes || bytes <= 0) return '';
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(2)} MB`;
}

export const RedisBrowserTab: React.FC<{ tab: WorkspaceTab }> = ({ tab }) => {
  const { t } = useI18n();
  const { isMobile } = useResponsive();
  const isDesktop = useWorkbenchStore((s) => s.isDesktop);
  const role = useWorkbenchStore((s) => s.role);
  const openTab = useWorkbenchStore((s) => s.openTab);
  const updateTab = useWorkbenchStore((s) => s.updateTab);
  const canWrite = isDesktop || role !== 'readonly';

  const initialDB = Number((tab.database || 'db0').replace('db', '')) || 0;
  const [dbIndex, setDbIndex] = useState<number>(initialDB);
  const scanSeqRef = useRef(0);
  const detailSeqRef = useRef(0);
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

  // Stream XADD & XTRIM modals
  const [streamAddOpen, setStreamAddOpen] = useState(false);
  const [streamEntryId, setStreamEntryId] = useState('*');
  const [streamPairs, setStreamPairs] = useState<{ k: string; v: string }[]>([{ k: 'event', v: '' }]);
  const [streamTrimOpen, setStreamTrimOpen] = useState(false);
  const [streamMaxLen, setStreamMaxLen] = useState<number>(1000);

  // Stream Consumer Groups modal
  const [groupsModalOpen, setGroupsModalOpen] = useState(false);
  const [streamInfo, setStreamInfo] = useState<KVStreamInfo | null>(null);
  const [streamInfoLoading, setStreamInfoLoading] = useState(false);
  const [newGroupName, setNewGroupName] = useState('');
  const [newGroupStartId, setNewGroupStartId] = useState('$');
  const [setIdTarget, setSetIdTarget] = useState<{ group: string; id: string } | null>(null);

  // Server Tools modal (Slowlog / Config / PubSub)
  const [toolsOpen, setToolsOpen] = useState(false);
  const [toolsTab, setToolsTab] = useState<'slowlog' | 'config' | 'pubsub'>('slowlog');
  const [slowlog, setSlowlog] = useState<KVSlowlogEntry[]>([]);
  const [slowlogLoading, setSlowlogLoading] = useState(false);
  const [configItems, setConfigItems] = useState<KVConfigItem[]>([]);
  const [configLoading, setConfigLoading] = useState(false);
  const [configFilter, setConfigFilter] = useState('');
  const [editingConfig, setEditingConfig] = useState<{ key: string; value: string } | null>(null);
  const [pubChannel, setPubChannel] = useState('events:default');
  const [pubMessage, setPubMessage] = useState('{\n  "type": "ping"\n}');
  const [publishing, setPublishing] = useState(false);

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
    const seq = ++scanSeqRef.current;
    setLoading(true);
    try {
      const res = await api.kvScan(tab.connectionId, targetDB, pat || '*', 0);
      if (seq !== scanSeqRef.current) return;
      setKeys(res.keys || []);
      setNextCursor(res.cursor || 0);
      setDbCounts(res.dbKeys || {});
      setCheckedKeys([]);
    } catch (err: any) {
      if (seq === scanSeqRef.current) {
        message.error(err.message);
      }
    } finally {
      if (seq === scanSeqRef.current) {
        setLoading(false);
      }
    }
  };

  const handleLoadMore = async () => {
    if (!nextCursor) return;
    const seq = scanSeqRef.current;
    setLoadingMore(true);
    try {
      const res = await api.kvScan(tab.connectionId, dbIndex, pattern || '*', nextCursor);
      if (seq !== scanSeqRef.current) return;
      setNextCursor(res.cursor || 0);
      setDbCounts(res.dbKeys || {});
      setKeys((prev) => {
        const seen = new Set(prev.map((k) => k.key));
        const extra = (res.keys || []).filter((k) => !seen.has(k.key));
        return [...prev, ...extra];
      });
    } catch (err: any) {
      if (seq === scanSeqRef.current) {
        message.error(err.message);
      }
    } finally {
      if (seq === scanSeqRef.current) {
        setLoadingMore(false);
      }
    }
  };

  // Sync external database switches from sidebar (e.g. clicking DB0 / DB1)
  useEffect(() => {
    if (!tab.database) return;
    const parsed = Number(tab.database.replace(/^db/i, ''));
    if (!Number.isNaN(parsed) && parsed >= 0 && parsed !== dbIndex) {
      detailSeqRef.current += 1;
      setDbIndex(parsed);
      setSelectedKey('');
      setDetail(null);
      setCheckedKeys([]);
      setStreamInfo(null);
      setEditingItem(null);
    }
  }, [tab.database]);

  useEffect(() => {
    loadKeys(dbIndex, pattern);
  }, [tab.connectionId, dbIndex]);

  const loadKeyDetail = useCallback(
    async (key: string) => {
      const seq = ++detailSeqRef.current;
      setSelectedKey(key);
      try {
        const d = await api.kvGetKey(tab.connectionId, dbIndex, key);
        if (seq !== detailSeqRef.current) return;
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
        if (seq === detailSeqRef.current) {
          message.error(err.message);
        }
      }
    },
    [tab.connectionId, dbIndex]
  );

  const loadStreamGroups = useCallback(
    async (keyName?: string) => {
      const k = keyName || detail?.key;
      if (!k) return;
      setStreamInfoLoading(true);
      try {
        const info = await api.kvStreamInfo(tab.connectionId, dbIndex, k);
        setStreamInfo(info);
      } catch (err: any) {
        message.error(err.message);
      } finally {
        setStreamInfoLoading(false);
      }
    },
    [tab.connectionId, dbIndex, detail?.key]
  );

  const loadSlowlog = useCallback(async () => {
    setSlowlogLoading(true);
    try {
      const res = await api.kvGetDiagnostics(tab.connectionId, { kind: 'slowlog', count: 100 });
      setSlowlog(res.slowlog || []);
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setSlowlogLoading(false);
    }
  }, [tab.connectionId]);

  const loadServerConfig = useCallback(async () => {
    setConfigLoading(true);
    try {
      const res = await api.kvGetDiagnostics(tab.connectionId, { kind: 'config', pattern: '*' });
      setConfigItems(res.config || []);
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setConfigLoading(false);
    }
  }, [tab.connectionId]);

  useEffect(() => {
    if (!toolsOpen) return;
    if (toolsTab === 'slowlog') {
      loadSlowlog();
    } else if (toolsTab === 'config' && configItems.length === 0) {
      loadServerConfig();
    }
  }, [toolsOpen, toolsTab]);

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
      } else if (vals.type === 'stream') {
        let fieldsMap: Record<string, string> = {};
        try {
          const parsed = JSON.parse(vals.value || '{}');
          if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
            throw new Error('not object');
          }
          Object.entries(parsed).forEach(([fk, fv]) => {
            if (fk.trim()) fieldsMap[fk.trim()] = fv === null || fv === undefined ? '' : String(fv);
          });
        } catch {
          message.error(t('Stream entry fields must be a valid JSON object'));
          return;
        }
        if (Object.keys(fieldsMap).length === 0) {
          fieldsMap = { event: 'init' };
        }
        const entryId = (vals.streamId || '*').trim() || '*';
        payload.id = entryId;
        payload.fields = fieldsMap;
        payload.items = [{ id: entryId, value: JSON.stringify(fieldsMap) }];
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

  const handleStreamAddEntry = async () => {
    if (!detail || detail.type !== 'stream') return;
    const fields: Record<string, string> = {};
    streamPairs.forEach((p) => {
      if (p.k.trim()) fields[p.k.trim()] = p.v;
    });
    if (Object.keys(fields).length === 0) {
      message.warning(t('Please enter at least one field name'));
      return;
    }
    try {
      await api.kvMutateItem(tab.connectionId, {
        db: dbIndex,
        key: detail.key,
        type: 'stream',
        action: 'ADD',
        id: streamEntryId.trim() || '*',
        fields,
      });
      message.success(t('Stream entry added'));
      setStreamAddOpen(false);
      loadKeyDetail(detail.key);
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleStreamTrim = async () => {
    if (!detail || detail.type !== 'stream') return;
    try {
      await api.kvMutateItem(tab.connectionId, {
        db: dbIndex,
        key: detail.key,
        type: 'stream',
        action: 'TRIM',
        maxLen: streamMaxLen,
      });
      message.success(t('Stream trimmed to MAXLEN {maxLen}', { maxLen: streamMaxLen }));
      setStreamTrimOpen(false);
      loadKeyDetail(detail.key);
    } catch (err: any) {
      message.error(err.message);
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

  const filteredConfigItems = useMemo(() => {
    const q = configFilter.trim().toLowerCase();
    if (!q) return configItems;
    return configItems.filter((c) => c.key.toLowerCase().includes(q) || c.value.toLowerCase().includes(q));
  }, [configItems, configFilter]);

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
        ? [{ title: t('Field'), dataIndex: 'field', width: calcSmartColumnWidth(t('Field'), { minBodyWidth: 150 }) }]
        : []),
      ...(detailType === 'list' ? [{ title: t('Index'), dataIndex: 'index', width: 84 }] : []),
      ...(detailType === 'zset' ? [{ title: t('Score'), dataIndex: 'score', width: 100 }] : []),
      ...(detailType === 'stream' ? [{ title: t('Entry ID'), dataIndex: 'id', width: 165 }] : []),
      {
        title: t('Value'),
        dataIndex: 'value',
        width: 240,
        ellipsis: true,
        render: (v: any) => <span data-raw-copy={formatRawCellText(v)}>{String(v ?? '')}</span>,
      },
      ...(canWrite
        ? [
            {
              title: t('Actions'),
              width: detailType === 'stream' ? 68 : 96,
              resizable: false,
              onHeaderCell: () => ({ 'data-copy-skip': 'true' } as any),
              render: (_: any, r: KVCollectionItem) => (
                <Space size={6} data-copy-skip="true">
                  {detailType !== 'stream' && (
                    <Button
                      size="small"
                      type="text"
                      style={isMobile ? { minWidth: 32, minHeight: 32 } : undefined}
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
                  )}
                  <Popconfirm
                    title={t('Delete')}
                    onConfirm={async () => {
                      try {
                        await api.kvMutateItem(tab.connectionId, {
                          db: dbIndex,
                          key: detailKey,
                          type: detailType,
                          action: 'DELETE',
                          id: r.id,
                          field: r.field,
                          value: r.value,
                        });
                        loadKeyDetail(detailKey);
                      } catch (err: any) {
                        message.error(err.message);
                      }
                    }}
                  >
                    <Button
                      size="small"
                      type="text"
                      danger
                      style={isMobile ? { minWidth: 32, minHeight: 32 } : undefined}
                      icon={<DeleteOutlined />}
                      title={t('Delete')}
                    />
                  </Popconfirm>
                </Space>
              ),
            },
          ]
        : []),
    ];
  }, [detail?.key, detail?.type, tab.connectionId, dbIndex, loadKeyDetail, canWrite, itemForm, isMobile, t]);

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', minWidth: 0, minHeight: 0, padding: isMobile ? 6 : 12, gap: 8, overflow: 'hidden' }}>
      {isMobile && (
        <Segmented
          block
          size="middle"
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
          <div style={{ display: 'flex', gap: 6, marginBottom: 8, flexWrap: 'wrap', flexShrink: 0, alignItems: 'center' }}>
            <Select
              size={isMobile ? 'middle' : 'small'}
              style={{ width: 108, height: isMobile ? 34 : undefined }}
              value={dbIndex}
              onChange={(v) => {
                detailSeqRef.current += 1;
                setDbIndex(v);
                setSelectedKey('');
                setDetail(null);
                setCheckedKeys([]);
                setStreamInfo(null);
                setEditingItem(null);
                updateTab(tab.key, { database: `db${v}` });
              }}
              options={dbOptions}
            />
            <Input
              size={isMobile ? 'middle' : 'small'}
              style={{ flex: 1, minWidth: 96, height: isMobile ? 34 : undefined }}
              prefix={<SearchOutlined />}
              placeholder={t('Key pattern, e.g. user:*')}
              value={pattern}
              onChange={(e) => setPattern(e.target.value)}
              onPressEnter={() => loadKeys(dbIndex, pattern)}
            />
            <Tooltip title={t('Refresh')}>
              <Button
                size="small"
                style={isMobile ? { width: 34, height: 34, padding: 0 } : undefined}
                icon={<ReloadOutlined />}
                onClick={() => loadKeys()}
              />
            </Tooltip>
            {canWrite && (
              <Tooltip title={t('New Redis Key (DB{db})', { db: dbIndex })}>
                <Button
                  size="small"
                  style={isMobile ? { width: 34, height: 34, padding: 0 } : undefined}
                  type="primary"
                  icon={<PlusOutlined />}
                  onClick={() => setNewKeyOpen(true)}
                />
              </Tooltip>
            )}
            <Tooltip title={t('Server Tools')}>
              <Button
                size="small"
                style={isMobile ? { width: 34, height: 34, padding: 0 } : undefined}
                icon={<ToolOutlined />}
                onClick={() => setToolsOpen(true)}
              />
            </Tooltip>
            <Dropdown
              trigger={['click']}
              placement="bottomRight"
              menu={{
                items: [
                  {
                    key: 'tools',
                    icon: <ToolOutlined />,
                    label: t('Server Tools (Slowlog / Config / PubSub)'),
                    onClick: () => setToolsOpen(true),
                  },
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
                        key: `db_users:${tab.connectionId}`,
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
              <Button
                size="small"
                style={isMobile ? { width: 34, height: 34, padding: 0 } : undefined}
                icon={<MoreOutlined />}
              />
            </Dropdown>
            {canWrite && (
              <Upload showUploadList={false} accept=".json" beforeUpload={handleImportKeysFile}>
                <Tooltip title={t('Import Keys (.json)')}>
                  <Button
                    size="small"
                    style={isMobile ? { width: 34, height: 34, padding: 0 } : undefined}
                    icon={<UploadOutlined />}
                  />
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
                      columnWidth: isMobile ? 38 : 40,
                      selectedRowKeys: checkedKeys,
                      onChange: setCheckedKeys,
                    }
                  : undefined
              }
              pagination={{ pageSize: 50, size: 'small', simple: isMobile, responsive: true }}
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
                <Space size={6} wrap>
                  <Tag color={TYPE_COLORS[detail.type] || 'blue'}>{detail.type.toUpperCase()}</Tag>
                  <Typography.Text strong copyable>
                    {detail.key}
                  </Typography.Text>
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    ({detail.length})
                  </Typography.Text>
                  {detail.memoryBytes !== undefined && detail.memoryBytes > 0 && (
                    <Tag color="geekblue" style={{ margin: 0 }}>
                      {t('Mem')}: {formatMemBytes(detail.memoryBytes)}
                    </Tag>
                  )}
                  {detail.encoding && (
                    <Tag style={{ margin: 0 }}>
                      {t('Encoding')}: {detail.encoding}
                    </Tag>
                  )}
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
                          try {
                            await api.kvDeleteKeys(tab.connectionId, dbIndex, [detail.key]);
                            message.success(t('Key deleted'));
                            setSelectedKey('');
                            setDetail(null);
                            loadKeys();
                          } catch (err: any) {
                            message.error(err.message);
                          }
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
                  <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginBottom: 8 }}>
                    {detail.type === 'stream' ? (
                      <>
                        {canWrite && (
                          <>
                            <Button
                              size="small"
                              type="primary"
                              icon={<PlusOutlined />}
                              onClick={() => {
                                setStreamEntryId('*');
                                setStreamPairs([{ k: 'event', v: '' }]);
                                setStreamAddOpen(true);
                              }}
                            >
                              {t('Add Entry')}
                            </Button>
                            <Button
                              size="small"
                              icon={<ScissorOutlined />}
                              onClick={() => setStreamTrimOpen(true)}
                            >
                              {t('Trim Stream')}
                            </Button>
                          </>
                        )}
                        <Button
                          size="small"
                          icon={<ApartmentOutlined />}
                          onClick={() => {
                            setGroupsModalOpen(true);
                            loadStreamGroups(detail.key);
                          }}
                        >
                          {t('Consumer Groups')}
                        </Button>
                      </>
                    ) : (
                      canWrite && (
                        <Button
                          size="small"
                          type="primary"
                          icon={<PlusOutlined />}
                          onClick={() => {
                            setEditingItem(null);
                            itemForm.resetFields();
                            setItemModalOpen(true);
                          }}
                        >
                          {t('Add Item')}
                        </Button>
                      )
                    )}
                  </div>

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

      {/* New Key Modal */}
      <Modal
        open={newKeyOpen}
        title={t('New Redis Key (DB{db})', { db: dbIndex })}
        width="min(520px, calc(100vw - 16px))"
        onCancel={() => setNewKeyOpen(false)}
        onOk={handleCreateKey}
        destroyOnClose
      >
        <Form
          form={newKeyForm}
          layout="vertical"
          initialValues={{ type: 'string', ttl: -1, streamId: '*' }}
        >
          <Form.Item name="key" label={t('Key Name')} rules={[{ required: true }]}>
            <Input placeholder="e.g. app:config:feature_flag" />
          </Form.Item>
          <Form.Item name="type" label={t('Data Structure Type')} rules={[{ required: true }]}>
            <Select
              onChange={(tp) => {
                if (tp === 'stream') {
                  newKeyForm.setFieldsValue({
                    value: '{\n  "event": "created",\n  "source": "dbharbor"\n}',
                  });
                }
              }}
              options={[
                { label: 'String', value: 'string' },
                { label: 'Hash', value: 'hash' },
                { label: 'List', value: 'list' },
                { label: 'Set', value: 'set' },
                { label: 'Sorted Set (ZSet)', value: 'zset' },
                { label: 'Stream', value: 'stream' },
              ]}
            />
          </Form.Item>
          <Form.Item noStyle shouldUpdate>
            {({ getFieldValue }) => {
              const tp = getFieldValue('type');
              if (tp === 'hash') {
                return (
                  <Form.Item name="field" label={t('Initial Hash Field')}>
                    <Input placeholder="field1" />
                  </Form.Item>
                );
              }
              if (tp === 'zset') {
                return (
                  <Form.Item name="score" label={t('Initial Score')}>
                    <InputNumber style={{ width: '100%' }} />
                  </Form.Item>
                );
              }
              if (tp === 'stream') {
                return (
                  <Form.Item name="streamId" label={t('Initial Entry ID (* for auto)')}>
                    <Input placeholder="*" />
                  </Form.Item>
                );
              }
              return null;
            }}
          </Form.Item>
          <Form.Item noStyle shouldUpdate>
            {({ getFieldValue }) => (
              <Form.Item
                name="value"
                label={
                  getFieldValue('type') === 'stream'
                    ? t('Stream Entry Fields (JSON Object)')
                    : t('Initial Value')
                }
                rules={[{ required: true }]}
              >
                <Input.TextArea rows={3} style={{ fontFamily: 'monospace' }} />
              </Form.Item>
            )}
          </Form.Item>
          <Form.Item name="ttl" label={t('TTL in Seconds (-1 for no expiry)')}>
            <InputNumber style={{ width: '100%' }} />
          </Form.Item>
        </Form>
      </Modal>

      {/* Hash / List / Set / ZSet Item Modal */}
      <Modal
        open={itemModalOpen}
        title={editingItem ? t('Edit Item in {key}', { key: detail?.key || '' }) : t('Add Item to {key}', { key: detail?.key || '' })}
        width="min(500px, calc(100vw - 16px))"
        onCancel={() => {
          setItemModalOpen(false);
          setEditingItem(null);
        }}
        destroyOnClose
        onOk={async () => {
          try {
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
          } catch (err: any) {
            if (!err?.errorFields) {
              message.error(err.message);
            }
          }
        }}
      >
        <Form form={itemForm} layout="vertical">
          {detail?.type === 'hash' && (
            <Form.Item name="field" label={t('Field')} rules={[{ required: true }]}>
              <Input disabled={Boolean(editingItem)} />
            </Form.Item>
          )}
          {detail?.type === 'zset' && (
            <Form.Item name="score" label={t('Score')} initialValue={0}>
              <InputNumber style={{ width: '100%' }} />
            </Form.Item>
          )}
          <Form.Item name="value" label={t('Value')} rules={[{ required: true }]}>
            <Input.TextArea rows={3} />
          </Form.Item>
        </Form>
      </Modal>

      {/* Stream Add Entry (XADD) Modal */}
      <Modal
        open={streamAddOpen}
        title={t('Add Stream Entry (XADD) — {key}', { key: detail?.key || '' })}
        width="min(560px, calc(100vw - 16px))"
        onCancel={() => setStreamAddOpen(false)}
        onOk={handleStreamAddEntry}
        destroyOnClose
      >
        <Space direction="vertical" style={{ width: '100%' }} size={10}>
          <div>
            <Typography.Text strong>{t('Entry ID (* for auto-generated)')}</Typography.Text>
            <Input
              style={{ marginTop: 4, fontFamily: 'monospace' }}
              value={streamEntryId}
              onChange={(e) => setStreamEntryId(e.target.value)}
              placeholder="*"
            />
          </div>
          <div>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 6 }}>
              <Typography.Text strong>{t('Field / Value Pairs')}</Typography.Text>
              <Button
                size="small"
                type="dashed"
                icon={<PlusOutlined />}
                onClick={() => setStreamPairs((prev) => [...prev, { k: '', v: '' }])}
              >
                {t('Add Field')}
              </Button>
            </div>
            {streamPairs.map((pair, idx) => (
              <div key={idx} style={{ display: 'flex', gap: 6, marginBottom: 6, alignItems: 'center' }}>
                <Input
                  size="small"
                  style={{ width: '40%', fontFamily: 'monospace' }}
                  placeholder={t('Field')}
                  value={pair.k}
                  onChange={(e) =>
                    setStreamPairs((prev) => prev.map((p, i) => (i === idx ? { ...p, k: e.target.value } : p)))
                  }
                />
                <Input
                  size="small"
                  style={{ flex: 1, fontFamily: 'monospace' }}
                  placeholder={t('Value')}
                  value={pair.v}
                  onChange={(e) =>
                    setStreamPairs((prev) => prev.map((p, i) => (i === idx ? { ...p, v: e.target.value } : p)))
                  }
                />
                <Button
                  size="small"
                  type="text"
                  danger
                  disabled={streamPairs.length <= 1}
                  icon={<DeleteOutlined />}
                  onClick={() => setStreamPairs((prev) => prev.filter((_, i) => i !== idx))}
                />
              </div>
            ))}
          </div>
        </Space>
      </Modal>

      {/* Stream Trim (XTRIM) Modal */}
      <Modal
        open={streamTrimOpen}
        title={t('Trim Stream (XTRIM) — {key}', { key: detail?.key || '' })}
        width="min(420px, calc(100vw - 16px))"
        onCancel={() => setStreamTrimOpen(false)}
        onOk={handleStreamTrim}
        destroyOnClose
      >
        <Space direction="vertical" style={{ width: '100%' }} size={8}>
          <Typography.Text type="secondary">
            {t('Retain at most MAXLEN latest entries in this Redis Stream:')}
          </Typography.Text>
          <InputNumber
            min={0}
            style={{ width: '100%' }}
            value={streamMaxLen}
            onChange={(v) => setStreamMaxLen(v ?? 0)}
          />
        </Space>
      </Modal>

      {/* Stream Consumer Groups Modal */}
      <Modal
        open={groupsModalOpen}
        title={t('Stream Consumer Groups — {key}', { key: detail?.key || '' })}
        width="min(740px, calc(100vw - 16px))"
        footer={<Button onClick={() => setGroupsModalOpen(false)}>{t('Close')}</Button>}
        onCancel={() => setGroupsModalOpen(false)}
        destroyOnClose
      >
        <Space direction="vertical" style={{ width: '100%' }} size={12}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 8 }}>
            <Space size={6} wrap>
              <Tag color="magenta">
                {t('Length')}: {streamInfo?.length ?? 0}
              </Tag>
              <Tag color="blue">
                {t('Groups')}: {streamInfo?.groupsCount ?? 0}
              </Tag>
              {streamInfo?.firstId && (
                <Tag>
                  {t('First ID')}: <code>{streamInfo.firstId}</code>
                </Tag>
              )}
              {streamInfo?.lastId && (
                <Tag>
                  {t('Last ID')}: <code>{streamInfo.lastId}</code>
                </Tag>
              )}
            </Space>
            <Button size="small" icon={<ReloadOutlined />} loading={streamInfoLoading} onClick={() => loadStreamGroups()}>
              {t('Refresh')}
            </Button>
          </div>

          {canWrite && detail && (
            <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', alignItems: 'center' }}>
              <Input
                size="small"
                style={{ width: isMobile ? '100%' : 200 }}
                placeholder={t('New Group Name')}
                value={newGroupName}
                onChange={(e) => setNewGroupName(e.target.value)}
              />
              <Select
                size="small"
                style={{ width: isMobile ? '100%' : 180 }}
                value={newGroupStartId}
                onChange={setNewGroupStartId}
                options={[
                  { label: `$ (${t('New messages only')})`, value: '$' },
                  { label: `0 (${t('From beginning')})`, value: '0' },
                ]}
              />
              <Button
                size="small"
                type="primary"
                icon={<PlusOutlined />}
                onClick={async () => {
                  if (!newGroupName.trim()) {
                    message.warning(t('Please enter a group name'));
                    return;
                  }
                  try {
                    await api.kvStreamGroup(tab.connectionId, {
                      db: dbIndex,
                      key: detail.key,
                      action: 'CREATE',
                      group: newGroupName.trim(),
                      startId: newGroupStartId,
                    });
                    message.success(t('Consumer group created'));
                    setNewGroupName('');
                    loadStreamGroups(detail.key);
                  } catch (err: any) {
                    message.error(err.message);
                  }
                }}
              >
                {t('Create Group')}
              </Button>
            </div>
          )}

          {setIdTarget && detail && (
            <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', alignItems: 'center', padding: 8, borderRadius: 6, border: '1px solid rgba(22,119,255,0.35)' }}>
              <Typography.Text strong>
                {t('Set Last ID for {group}:', { group: setIdTarget.group })}
              </Typography.Text>
              <Input
                size="small"
                style={{ width: 180, fontFamily: 'monospace' }}
                value={setIdTarget.id}
                onChange={(e) => setSetIdTarget({ ...setIdTarget, id: e.target.value })}
                placeholder="$ or 0-0"
              />
              <Button
                size="small"
                type="primary"
                onClick={async () => {
                  try {
                    await api.kvStreamGroup(tab.connectionId, {
                      db: dbIndex,
                      key: detail.key,
                      action: 'SETID',
                      group: setIdTarget.group,
                      startId: setIdTarget.id.trim() || '$',
                    });
                    message.success(t('Consumer group ID updated'));
                    setSetIdTarget(null);
                    loadStreamGroups(detail.key);
                  } catch (err: any) {
                    message.error(err.message);
                  }
                }}
              >
                {t('Apply')}
              </Button>
              <Button size="small" onClick={() => setSetIdTarget(null)}>
                {t('Cancel')}
              </Button>
            </div>
          )}

          <ResizableTable<KVStreamGroup>
            rowKey="name"
            size="small"
            loading={streamInfoLoading}
            pagination={false}
            scroll={{ x: 600 }}
            dataSource={streamInfo?.groups || []}
            expandable={{
              expandedRowRender: (grp) => (
                <div style={{ padding: '4px 8px' }}>
                  {!grp.consumerList || grp.consumerList.length === 0 ? (
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      {t('No active consumers in this group')}
                    </Typography.Text>
                  ) : (
                    <ResizableTable<KVStreamConsumer>
                      rowKey="name"
                      size="small"
                      pagination={false}
                      dataSource={grp.consumerList}
                      columns={[
                        { title: t('Consumer'), dataIndex: 'name', width: 180 },
                        { title: t('Pending'), dataIndex: 'pending', width: 100 },
                        { title: t('Idle (ms)'), dataIndex: 'idleMs', width: 120 },
                        ...(canWrite && detail
                          ? [
                              {
                                title: t('Actions'),
                                width: 80,
                                render: (_: any, c: KVStreamConsumer) => (
                                  <Popconfirm
                                    title={t('Remove consumer {name}?', { name: c.name })}
                                    onConfirm={async () => {
                                      try {
                                        await api.kvStreamGroup(tab.connectionId, {
                                          db: dbIndex,
                                          key: detail.key,
                                          action: 'DELCONSUMER',
                                          group: grp.name,
                                          consumer: c.name,
                                        });
                                        loadStreamGroups(detail.key);
                                      } catch (err: any) {
                                        message.error(err.message);
                                      }
                                    }}
                                  >
                                    <Button size="small" danger type="text" icon={<DeleteOutlined />} />
                                  </Popconfirm>
                                ),
                              },
                            ]
                          : []),
                      ]}
                    />
                  )}
                </div>
              ),
            }}
            columns={[
              { title: t('Group Name'), dataIndex: 'name', width: 150 },
              { title: t('Consumers'), dataIndex: 'consumers', width: 95 },
              { title: t('Pending'), dataIndex: 'pending', width: 90 },
              { title: t('Lag'), dataIndex: 'lag', width: 80, render: (v) => v ?? '-' },
              {
                title: t('Last Delivered ID'),
                dataIndex: 'lastDeliveredId',
                width: 165,
                render: (v) => <code>{v}</code>,
              },
              ...(canWrite && detail
                ? [
                    {
                      title: t('Actions'),
                      width: 130,
                      render: (_: any, grp: KVStreamGroup) => (
                        <Space size={4}>
                          <Button
                            size="small"
                            type="link"
                            onClick={() => setSetIdTarget({ group: grp.name, id: grp.lastDeliveredId || '$' })}
                          >
                            {t('Set ID')}
                          </Button>
                          <Popconfirm
                            title={t('Destroy consumer group {name}?', { name: grp.name })}
                            onConfirm={async () => {
                              try {
                                await api.kvStreamGroup(tab.connectionId, {
                                  db: dbIndex,
                                  key: detail.key,
                                  action: 'DESTROY',
                                  group: grp.name,
                                });
                                loadStreamGroups(detail.key);
                              } catch (err: any) {
                                message.error(err.message);
                              }
                            }}
                          >
                            <Button size="small" danger type="text" icon={<DeleteOutlined />} />
                          </Popconfirm>
                        </Space>
                      ),
                    },
                  ]
                : []),
            ]}
          />
        </Space>
      </Modal>

      {/* Server Tools Modal (Slowlog / Server Config / PubSub) */}
      <Modal
        open={toolsOpen}
        title={`${t('Redis Server Tools')} — ${tab.connName}`}
        width="min(760px, calc(100vw - 16px))"
        style={isMobile ? { top: 10 } : undefined}
        styles={{
          body: {
            maxHeight: isMobile ? 'calc(100dvh - 140px)' : 'calc(100dvh - 180px)',
            overflowY: 'auto',
          },
        }}
        footer={<Button onClick={() => setToolsOpen(false)}>{t('Close')}</Button>}
        onCancel={() => setToolsOpen(false)}
        destroyOnClose
      >
        <Tabs
          size="small"
          activeKey={toolsTab}
          onChange={(k) => setToolsTab(k as 'slowlog' | 'config' | 'pubsub')}
          items={[
            {
              key: 'slowlog',
              label: t('Slowlog'),
              children: (
                <div>
                  <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 8, flexWrap: 'wrap', gap: 8 }}>
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      {t('Latest slow queries recorded by Redis SLOWLOG')}
                    </Typography.Text>
                    <Space size={6}>
                      <Button size="small" icon={<ReloadOutlined />} loading={slowlogLoading} onClick={loadSlowlog}>
                        {t('Refresh')}
                      </Button>
                      {canWrite && (
                        <Popconfirm
                          title={t('Clear all Redis slowlog entries?')}
                          onConfirm={async () => {
                            try {
                              await api.kvDiagnosticsAction(tab.connectionId, { action: 'RESET_SLOWLOG' });
                              message.success(t('Slowlog cleared'));
                              loadSlowlog();
                            } catch (err: any) {
                              message.error(err.message);
                            }
                          }}
                        >
                          <Button size="small" danger icon={<ClearOutlined />}>
                            {t('Reset Slowlog')}
                          </Button>
                        </Popconfirm>
                      )}
                    </Space>
                  </div>
                  <ResizableTable<KVSlowlogEntry>
                    rowKey="id"
                    size="small"
                    loading={slowlogLoading}
                    scroll={{ x: 580 }}
                    pagination={{ pageSize: 15, size: 'small' }}
                    dataSource={slowlog}
                    columns={[
                      { title: 'ID', dataIndex: 'id', width: 70 },
                      { title: t('Timestamp'), dataIndex: 'timestamp', width: 165 },
                      {
                        title: t('Duration'),
                        dataIndex: 'durationUs',
                        width: 110,
                        render: (us: number) => (us >= 1000 ? `${(us / 1000).toFixed(2)} ms` : `${us} μs`),
                      },
                      {
                        title: t('Command'),
                        dataIndex: 'command',
                        width: 260,
                        ellipsis: true,
                        render: (cmd: string) => <code>{cmd}</code>,
                      },
                      {
                        title: t('Client'),
                        dataIndex: 'clientAddr',
                        width: 140,
                        ellipsis: true,
                        render: (addr: string, r) => (r.clientName ? `${addr || ''} (${r.clientName})` : addr || '-'),
                      },
                    ]}
                  />
                </div>
              ),
            },
            {
              key: 'config',
              label: t('Server Config'),
              children: (
                <div>
                  <div style={{ display: 'flex', gap: 8, marginBottom: 8, flexWrap: 'wrap', alignItems: 'center' }}>
                    <Input
                      size="small"
                      style={{ flex: 1, minWidth: 180 }}
                      prefix={<SearchOutlined />}
                      placeholder={t('Filter parameters, e.g. maxmemory / timeout / save')}
                      value={configFilter}
                      onChange={(e) => setConfigFilter(e.target.value)}
                    />
                    <Button size="small" icon={<ReloadOutlined />} loading={configLoading} onClick={loadServerConfig}>
                      {t('Refresh')}
                    </Button>
                  </div>

                  {editingConfig && canWrite && (
                    <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', alignItems: 'center', marginBottom: 8, padding: 8, borderRadius: 6, border: '1px solid rgba(22,119,255,0.35)' }}>
                      <code>{editingConfig.key}</code>
                      <Input
                        size="small"
                        style={{ flex: 1, minWidth: 160, fontFamily: 'monospace' }}
                        value={editingConfig.value}
                        onChange={(e) => setEditingConfig({ ...editingConfig, value: e.target.value })}
                      />
                      <Button
                        size="small"
                        type="primary"
                        onClick={async () => {
                          try {
                            await api.kvDiagnosticsAction(tab.connectionId, {
                              action: 'SET_CONFIG',
                              key: editingConfig.key,
                              value: editingConfig.value,
                            });
                            message.success(t('Parameter {key} updated', { key: editingConfig.key }));
                            setEditingConfig(null);
                            loadServerConfig();
                          } catch (err: any) {
                            message.error(err.message);
                          }
                        }}
                      >
                        {t('Save')}
                      </Button>
                      <Button size="small" onClick={() => setEditingConfig(null)}>
                        {t('Cancel')}
                      </Button>
                    </div>
                  )}

                  <ResizableTable<KVConfigItem>
                    rowKey="key"
                    size="small"
                    loading={configLoading}
                    scroll={{ x: 500 }}
                    pagination={{ pageSize: 15, size: 'small' }}
                    dataSource={filteredConfigItems}
                    columns={[
                      {
                        title: t('Parameter'),
                        dataIndex: 'key',
                        width: 220,
                        render: (k: string) => <code>{k}</code>,
                      },
                      {
                        title: t('Value'),
                        dataIndex: 'value',
                        width: 260,
                        ellipsis: true,
                        render: (v: string) => <span style={{ fontFamily: 'monospace' }}>{v || '(empty)'}</span>,
                      },
                      ...(canWrite
                        ? [
                            {
                              title: t('Actions'),
                              width: 72,
                              render: (_: any, item: KVConfigItem) => (
                                <Button
                                  size="small"
                                  type="text"
                                  icon={<EditOutlined />}
                                  onClick={() => setEditingConfig({ key: item.key, value: item.value })}
                                />
                              ),
                            },
                          ]
                        : []),
                    ]}
                  />
                </div>
              ),
            },
            {
              key: 'pubsub',
              label: t('Pub/Sub Publish'),
              children: (
                <Space direction="vertical" style={{ width: '100%' }} size={10}>
                  <div>
                    <Typography.Text strong>{t('Channel Name')}</Typography.Text>
                    <Input
                      style={{ marginTop: 4, fontFamily: 'monospace' }}
                      disabled={!canWrite}
                      value={pubChannel}
                      onChange={(e) => setPubChannel(e.target.value)}
                      placeholder="e.g. notifications:global"
                    />
                  </div>
                  <div>
                    <Typography.Text strong>{t('Message Payload')}</Typography.Text>
                    <Input.TextArea
                      rows={5}
                      style={{ marginTop: 4, fontFamily: 'monospace' }}
                      disabled={!canWrite}
                      value={pubMessage}
                      onChange={(e) => setPubMessage(e.target.value)}
                    />
                  </div>
                  {canWrite && (
                    <Button
                      type="primary"
                      icon={<SendOutlined />}
                      loading={publishing}
                      onClick={async () => {
                        if (!pubChannel.trim()) {
                          message.warning(t('Please enter a channel name'));
                          return;
                        }
                        setPublishing(true);
                        try {
                          const res = await api.kvDiagnosticsAction(tab.connectionId, {
                            action: 'PUBLISH',
                            channel: pubChannel.trim(),
                            message: pubMessage,
                          });
                          message.success(
                            t('Published to {channel} ({count} subscribers received)', {
                              channel: pubChannel.trim(),
                              count: res.receivers ?? 0,
                            })
                          );
                        } catch (err: any) {
                          message.error(err.message);
                        } finally {
                          setPublishing(false);
                        }
                      }}
                    >
                      {t('Publish Message')}
                    </Button>
                  )}
                </Space>
              ),
            },
          ]}
        />
      </Modal>

      {/* Rename / Duplicate Key Modal */}
      <Modal
        open={keyActionModal.open}
        title={keyActionModal.mode === 'rename' ? t('Rename Redis Key') : t('Duplicate Redis Key')}
        width="min(460px, calc(100vw - 16px))"
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
        width="min(460px, calc(100vw - 16px))"
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
