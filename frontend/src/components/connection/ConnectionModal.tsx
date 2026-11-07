import React, { useEffect, useMemo, useState } from 'react';
import {
  Alert,
  Button,
  Col,
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
  Switch,
  Tag,
  Typography,
  Upload,
  message,
} from 'antd';
import {
  ArrowUpOutlined,
  CheckCircleFilled,
  FileOutlined,
  FolderFilled,
  FolderOpenOutlined,
  ThunderboltOutlined,
  UploadOutlined,
} from '@ant-design/icons';
import { api } from '../../api/client';
import { useResponsive } from '../../hooks/useResponsive';
import { useI18n } from '../../i18n/useI18n';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import type { ConnectionFormPayload, LocalFSBrowseResponse, SavedConnection, ServerInfo } from '../../types';
import { DB_SHORT_LABELS, formatBytes } from '../../utils/format';

const PRESET_BADGE_COLORS = [
  '#1677ff',
  '#13c2c2',
  '#52c41a',
  '#fa8c16',
  '#f5222d',
  '#722ed1',
  '#eb2f96',
  '#2f54eb',
];

const ColorSwatchPicker: React.FC<{ value?: string; onChange?: (hex: string) => void }> = ({ value, onChange }) => {
  const hex = typeof value === 'string' && /^#[0-9a-fA-F]{6}$/.test(value) ? value : '#1677ff';
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 5, flexWrap: 'wrap', minHeight: 32 }}>
      {PRESET_BADGE_COLORS.map((c) => {
        const active = hex.toLowerCase() === c.toLowerCase();
        return (
          <button
            key={c}
            type="button"
            onClick={() => onChange?.(c)}
            title={c}
            style={{
              width: 18,
              height: 18,
              padding: 0,
              borderRadius: 4,
              background: c,
              border: active ? '2px solid #fff' : '1px solid rgba(128,128,128,0.35)',
              boxShadow: active ? `0 0 0 2px ${c}` : 'none',
              cursor: 'pointer',
              flexShrink: 0,
            }}
          />
        );
      })}
      <label
        style={{
          display: 'inline-flex',
          alignItems: 'center',
          gap: 4,
          padding: '2px 6px',
          borderRadius: 6,
          border: '1px solid rgba(128,128,128,0.28)',
          cursor: 'pointer',
          fontSize: 11,
          fontFamily: 'monospace',
        }}
      >
        <input
          type="color"
          value={hex}
          onChange={(e) => onChange?.(e.target.value)}
          style={{ width: 16, height: 16, padding: 0, border: 'none', background: 'transparent', cursor: 'pointer' }}
        />
        <span>{hex.toUpperCase()}</span>
      </label>
    </div>
  );
};

interface Props {
  open: boolean;
  editing?: SavedConnection | null;
  onClose: () => void;
  onSaved: () => void;
  onOpenEphemeral?: () => void;
}

export const ConnectionModal: React.FC<Props> = ({ open, editing, onClose, onSaved, onOpenEphemeral }) => {
  const { drivers, isDesktop, role } = useWorkbenchStore();
  const { isMobile } = useResponsive();
  const { t } = useI18n();
  const [form] = Form.useForm();
  const [categoryFilter, setCategoryFilter] = useState<string>('all');
  const [selectedType, setSelectedType] = useState<string>('clickhouse');
  const [testing, setTesting] = useState(false);
  const [saving, setSaving] = useState(false);
  const [pingResult, setPingResult] = useState<ServerInfo | null>(null);

  // Local / Server database file picker & upload state (SQLite 3 / DuckDB)
  const [browsingNative, setBrowsingNative] = useState(false);
  const [uploadingDB, setUploadingDB] = useState(false);
  const [fsModalOpen, setFsModalOpen] = useState(false);
  const [fsLoading, setFsLoading] = useState(false);
  const [fsShowAll, setFsShowAll] = useState(false);
  const [fsData, setFsData] = useState<LocalFSBrowseResponse | null>(null);
  const [fsDirInput, setFsDirInput] = useState('');
  const [newDbFileName, setNewDbFileName] = useState('');

  const loadFSDir = async (dirPath = '', showAll = fsShowAll) => {
    setFsLoading(true);
    try {
      const res = await api.browseLocalFS(dirPath, showAll);
      setFsData(res);
      setFsDirInput(res.currentDir);
    } catch (err: any) {
      message.error(err.message || t('Failed to browse directory'));
    } finally {
      setFsLoading(false);
    }
  };

  const openServerFileBrowser = () => {
    const curVal = String(form.getFieldValue('filePath') || '').trim();
    const defaultNewName = selectedType === 'duckdb' ? 'analytics.duckdb' : 'app.sqlite';
    setNewDbFileName(defaultNewName);
    setFsModalOpen(true);
    const parentGuess =
      curVal && curVal !== ':memory:' && (curVal.includes('/') || curVal.includes('\\'))
        ? curVal.replace(/[\\/][^\\/]*$/, '')
        : '';
    loadFSDir(parentGuess, fsShowAll);
  };

  const handleBrowseLocalDBFile = async () => {
    const curVal = String(form.getFieldValue('filePath') || '').trim();
    if (isDesktop) {
      setBrowsingNative(true);
      try {
        const res = await api.pickDesktopFile(selectedType, curVal === ':memory:' ? '' : curVal);
        if (res.filePath) {
          form.setFieldValue('filePath', res.filePath);
          return;
        }
        if (res.canceled) {
          return;
        }
      } catch {
        // Fall through to built-in folder browser modal if native dialog fails
      } finally {
        setBrowsingNative(false);
      }
    }
    openServerFileBrowser();
  };

  const handleUploadDBFile = async (file: File) => {
    setUploadingDB(true);
    try {
      const res = await api.uploadLocalDBFile(file, true);
      form.setFieldValue('filePath', res.filePath);
      message.success(t('Uploaded database file: {name}', { name: res.fileName }));
      setFsModalOpen(false);
    } catch (err: any) {
      message.error(err.message || t('Failed to upload database file'));
    } finally {
      setUploadingDB(false);
    }
    return false;
  };

  const currentDriver = useMemo(
    () => drivers.find((d) => d.type === selectedType) || drivers[0],
    [drivers, selectedType]
  );

  const filteredDrivers = useMemo(() => {
    if (categoryFilter === 'all') return drivers;
    if (categoryFilter === 'nosql') {
      return drivers.filter((d) => d.category === 'keyvalue' || d.category === 'document');
    }
    return drivers.filter((d) => d.category === categoryFilter);
  }, [drivers, categoryFilter]);

  useEffect(() => {
    if (!open) return;
    setPingResult(null);
    setCategoryFilter('all');
    setFsModalOpen(false);
    setFsData(null);
    setFsShowAll(false);
    if (editing) {
      setSelectedType(editing.type);
      form.setFieldsValue({
        name: editing.name,
        color: editing.color || '#1677ff',
        visibility: editing.visibility || (role === 'admin' ? 'shared' : 'private'),
        host: editing.host,
        port: editing.port,
        username: editing.username,
        password: '',
        database: editing.database,
        filePath: editing.filePath,
        uri: '',
        options: editing.options || {},
      });
    } else {
      const defType = drivers[0]?.type || 'clickhouse';
      setSelectedType(defType);
      applyDriverDefaults(defType);
    }
  }, [open, editing, drivers]);

  const applyDriverDefaults = (type: string) => {
    const drv = drivers.find((d) => d.type === type);
    if (!drv) return;
    const rootDefaults: Record<string, any> = {
      name: `${drv.name} ${t('Connection')}`,
      color: drv.badgeColor || '#1677ff',
      visibility: role === 'admin' ? 'shared' : 'private',
      options: {},
    };
    drv.fields.forEach((f) => {
      if (f.defaultValue !== undefined && f.defaultValue !== '') {
        let val: any = f.defaultValue;
        if (f.type === 'number') val = Number(f.defaultValue);
        if (f.type === 'switch') val = f.defaultValue === 'true';
        if (f.target === 'options') {
          rootDefaults.options[f.key] = String(f.defaultValue);
        } else {
          rootDefaults[f.key] = val;
        }
      }
    });
    form.resetFields();
    form.setFieldsValue(rootDefaults);
  };

  const handleSelectDriver = (type: string) => {
    setSelectedType(type);
    setPingResult(null);
    if (!editing) {
      applyDriverDefaults(type);
    }
  };

  const buildPayload = async (): Promise<ConnectionFormPayload> => {
    const vals = await form.validateFields();
    const colorVal = typeof vals.color === 'string' ? vals.color : vals.color?.toHexString?.() || '#1677ff';
    const normOptions: Record<string, string> = {};
    if (vals.options) {
      Object.entries(vals.options).forEach(([k, v]) => {
        if (v !== undefined && v !== null) {
          normOptions[k] = String(v);
        }
      });
    }
    return {
      id: editing?.id,
      name: vals.name,
      type: selectedType,
      color: colorVal,
      host: vals.host,
      port: vals.port ? Number(vals.port) : currentDriver?.defaultPort || 0,
      username: vals.username,
      password: vals.password || '',
      database: vals.database !== undefined ? String(vals.database) : '',
      filePath: vals.filePath || '',
      uri: vals.uri || '',
      options: normOptions,
      keepExistingSecret: Boolean(editing),
      visibility: isDesktop ? 'shared' : role === 'admin' ? vals.visibility || 'shared' : 'private',
    };
  };

  const handleTest = async () => {
    try {
      const payload = await buildPayload();
      setTesting(true);
      setPingResult(null);
      const info = await api.testConnection(payload);
      setPingResult(info);
      message.success(
        t('Connection successful! ({product} {ver}, {ms}ms)', {
          product: info.product,
          ver: info.version,
          ms: info.latencyMs,
        })
      );
    } catch (err: any) {
      if (err?.errorFields) return;
      message.error(err.message || t('Connection test failed'));
    } finally {
      setTesting(false);
    }
  };

  const handleSave = async () => {
    try {
      const payload = await buildPayload();
      setSaving(true);
      if (editing) {
        await api.updateConnection(editing.id, payload);
        message.success(t('Connection updated'));
      } else {
        await api.createConnection(payload);
        message.success(t('Connection created'));
      }
      onSaved();
      onClose();
    } catch (err: any) {
      if (err?.errorFields) return;
      message.error(err.message || t('Failed to save connection'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      open={open}
      title={editing ? `${t('Edit Connection')} — ${editing.name}` : t('New Multi-Database Connection')}
      width="min(780px, calc(100vw - 24px))"
      style={isMobile ? { top: 12 } : undefined}
      styles={{ body: { maxHeight: isMobile ? 'calc(100dvh - 185px)' : 'calc(100dvh - 210px)', overflowY: 'auto' } }}
      onCancel={onClose}
      destroyOnClose
      footer={
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 8 }}>
          <Button icon={<ThunderboltOutlined />} loading={testing} onClick={handleTest}>
            {t('Test Connection')}
          </Button>
          <Space>
            <Button onClick={onClose}>{t('Cancel')}</Button>
            <Button type="primary" loading={saving} onClick={handleSave}>
              {t('Save Connection')}
            </Button>
          </Space>
        </div>
      }
    >
      {!editing && (
        <div style={{ marginBottom: 12 }}>
          <div style={{ marginBottom: 8, display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 6 }}>
            <Typography.Text strong>1. {t('All 14 Built-in Database Drivers')}</Typography.Text>
            <div style={{ maxWidth: '100%', overflowX: 'auto' }}>
              <Segmented
                size="small"
                value={categoryFilter}
                onChange={(v) => setCategoryFilter(String(v))}
                options={[
                  { label: t('All'), value: 'all' },
                  { label: isMobile ? 'OLAP' : t('OLAP Analytical'), value: 'olap' },
                  { label: isMobile ? 'RDBMS' : t('Relational RDBMS'), value: 'relational' },
                  { label: isMobile ? 'NoSQL' : t('KV & Document NoSQL'), value: 'nosql' },
                ]}
              />
            </div>
          </div>
          <div
            style={{
              display: 'grid',
              gridTemplateColumns: isMobile ? 'repeat(2, minmax(0, 1fr))' : 'repeat(auto-fill, minmax(150px, 1fr))',
              gap: isMobile ? 6 : 8,
              maxHeight: isMobile ? 'min(176px, 28vh)' : 'min(185px, 26vh)',
              overflowY: 'auto',
              padding: 2,
            }}
          >
            {filteredDrivers.map((drv) => {
              const active = drv.type === selectedType;
              const badge = DB_SHORT_LABELS[drv.type] || { code: 'DB', color: drv.badgeColor };
              return (
                <div
                  key={drv.type}
                  onClick={() => handleSelectDriver(drv.type)}
                  style={{
                    padding: '8px 10px',
                    borderRadius: 8,
                    cursor: 'pointer',
                    border: active ? `2px solid ${badge.color}` : '1px solid rgba(128,128,128,0.22)',
                    background: active ? 'rgba(22,119,255,0.06)' : 'transparent',
                    display: 'flex',
                    alignItems: 'center',
                    gap: 8,
                    transition: 'all 0.15s',
                  }}
                >
                  <span
                    style={{
                      width: 28,
                      height: 28,
                      borderRadius: 6,
                      background: badge.color,
                      color: '#fff',
                      fontWeight: 700,
                      fontSize: 12,
                      display: 'inline-flex',
                      alignItems: 'center',
                      justifyContent: 'center',
                      flexShrink: 0,
                    }}
                  >
                    {badge.code}
                  </span>
                  <div style={{ overflow: 'hidden' }}>
                    <div style={{ fontWeight: 600, fontSize: 13, whiteSpace: 'nowrap', textOverflow: 'ellipsis', overflow: 'hidden' }}>
                      {drv.name}
                    </div>
                    <div style={{ fontSize: 11, opacity: 0.65 }}>
                      {drv.defaultPort > 0 ? `Port ${drv.defaultPort}` : t('Local File')}
                    </div>
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      )}

      {currentDriver && (
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          message={
            <div style={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 8 }}>
              <strong style={{ whiteSpace: 'nowrap', flexShrink: 0 }}>{currentDriver.name}</strong>
              <Tag color={currentDriver.badgeColor} style={{ margin: 0, flexShrink: 0 }}>
                {currentDriver.category.toUpperCase()}
              </Tag>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {t(currentDriver.description)}
              </Typography.Text>
            </div>
          }
        />
      )}

      <Form form={form} layout="vertical">
        <Row gutter={12}>
          <Col xs={24} sm={!isDesktop && role === 'admin' ? 9 : 14}>
            <Form.Item name="name" label={t('Connection Name')} rules={[{ required: true }]}>
              <Input placeholder="Production ClickHouse / Orders MySQL" />
            </Form.Item>
          </Col>
          {!isDesktop && role === 'admin' && (
            <Col xs={24} sm={6}>
              <Form.Item name="visibility" label={t('Visibility')}>
                <Select
                  options={[
                    { value: 'shared', label: t('Team Shared') },
                    { value: 'private', label: t('Personal Private') },
                  ]}
                />
              </Form.Item>
            </Col>
          )}
          <Col xs={24} sm={!isDesktop && role === 'admin' ? 9 : 10}>
            <Form.Item name="color" label={t('Badge Color')}>
              <ColorSwatchPicker />
            </Form.Item>
          </Col>
        </Row>
        {!isDesktop && role !== 'admin' && (
          <div style={{ marginBottom: 12 }}>
            <Tag color="purple">{t('Personal Private Connection — visible only to your account and administrators')}</Tag>
          </div>
        )}

        <Row gutter={12}>
          {currentDriver?.fields.map((field) => {
            const namePath = field.target === 'options' ? ['options', field.key] : field.key;
            const smSpan = field.key === 'host' ? 16 : field.key === 'port' ? 8 : field.key === 'filePath' || field.key === 'uri' ? 24 : 12;

            if (field.type === 'switch') {
              return (
                <Col xs={24} sm={smSpan} key={field.key}>
                  <Form.Item
                    name={namePath}
                    label={t(field.label)}
                    valuePropName="checked"
                    getValueFromEvent={(checked) => (checked ? 'true' : 'false')}
                    getValueProps={(v) => ({ checked: v === 'true' || v === true })}
                  >
                    <Switch />
                  </Form.Item>
                </Col>
              );
            }

            if (field.key === 'filePath') {
              return (
                <Col xs={24} sm={smSpan} key={field.key}>
                  <Form.Item
                    label={t(field.label)}
                    required={field.required}
                    extra={
                      <Space size={[6, 6]} wrap style={{ marginTop: 6 }}>
                        <Tag
                          style={{ cursor: 'pointer', margin: 0 }}
                          onClick={() => form.setFieldValue('filePath', ':memory:')}
                        >
                          :memory: ({t('In-Memory DB')})
                        </Tag>
                        {isDesktop && (
                          <Tag
                            color="purple"
                            style={{ cursor: 'pointer', margin: 0 }}
                            onClick={openServerFileBrowser}
                          >
                            <FolderOpenOutlined /> {t('Folder Browser')}
                          </Tag>
                        )}
                        {!isDesktop && !editing && onOpenEphemeral && (
                          <Tag
                            color="orange"
                            style={{ cursor: 'pointer', margin: 0 }}
                            onClick={() => {
                              onClose();
                              onOpenEphemeral();
                            }}
                          >
                            <FolderOpenOutlined /> {t('Open Local DB (Temp Session / Save Back)')}
                          </Tag>
                        )}
                        {!isDesktop && (
                          <Upload
                            showUploadList={false}
                            accept=".db,.sqlite,.sqlite3,.db3,.s3db,.duckdb,.ddb"
                            beforeUpload={handleUploadDBFile}
                          >
                            <Tag color="blue" style={{ cursor: 'pointer', margin: 0 }}>
                              <UploadOutlined /> {uploadingDB ? t('Uploading...') : t('Upload Local DB File')}
                            </Tag>
                          </Upload>
                        )}
                      </Space>
                    }
                  >
                    <Space.Compact style={{ width: '100%' }}>
                      <Form.Item
                        name={namePath}
                        noStyle
                        rules={field.required ? [{ required: true }] : undefined}
                      >
                        <Input placeholder={field.placeholder} />
                      </Form.Item>
                      <Button
                        icon={<FolderOpenOutlined />}
                        loading={browsingNative}
                        onClick={handleBrowseLocalDBFile}
                      >
                        {t('Browse...')}
                      </Button>
                    </Space.Compact>
                  </Form.Item>
                </Col>
              );
            }

            return (
              <Col xs={24} sm={smSpan} key={field.key}>
                <Form.Item
                  name={namePath}
                  label={t(field.label)}
                  rules={field.required ? [{ required: true }] : undefined}
                >
                  {field.type === 'number' ? (
                    <InputNumber style={{ width: '100%' }} placeholder={field.placeholder} />
                  ) : field.type === 'password' ? (
                    <Input.Password
                      placeholder={
                        editing && ((field.key === 'password' && editing.hasPassword) || (field.key === 'uri' && editing.hasUri))
                          ? t('Encrypted (leave blank to keep existing secret)')
                          : field.placeholder
                      }
                    />
                  ) : field.type === 'select' ? (
                    <Select options={field.selectOptions?.map((o) => ({ ...o, label: t(o.label) }))} />
                  ) : (
                    <Input placeholder={field.placeholder} />
                  )}
                </Form.Item>
              </Col>
            );
          })}
        </Row>
      </Form>

      {pingResult && (
        <Alert
          type="success"
          icon={<CheckCircleFilled />}
          showIcon
          message={t('Connected: {product} — Version {ver} (Latency {ms} ms)', {
            product: pingResult.product,
            ver: pingResult.version,
            ms: pingResult.latencyMs,
          })}
        />
      )}

      <Modal
        open={fsModalOpen}
        title={t('Select or Create Local Database File')}
        width="min(640px, calc(100vw - 24px))"
        onCancel={() => setFsModalOpen(false)}
        footer={null}
        destroyOnClose
      >
        <Spin spinning={fsLoading}>
          {fsData?.roots && fsData.roots.length > 0 && (
            <div style={{ marginBottom: 8, display: 'flex', flexWrap: 'wrap', gap: 6, alignItems: 'center' }}>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {t('Quick Roots')}:
              </Typography.Text>
              {fsData.roots.map((r) => (
                <Tag
                  key={r.path}
                  color={fsData.currentDir === r.path ? 'blue' : undefined}
                  style={{ cursor: 'pointer', margin: 0 }}
                  onClick={() => loadFSDir(r.path, fsShowAll)}
                >
                  {t(r.label)}
                </Tag>
              ))}
            </div>
          )}

          <div style={{ display: 'flex', gap: 6, marginBottom: 8, alignItems: 'center', flexWrap: 'wrap' }}>
            <Space.Compact style={{ flex: 1, minWidth: 200 }}>
              <Button
                icon={<ArrowUpOutlined />}
                disabled={!fsData?.parentDir}
                onClick={() => fsData?.parentDir && loadFSDir(fsData.parentDir, fsShowAll)}
                title={t('Parent Folder')}
              />
              <Input
                value={fsDirInput}
                onChange={(e) => setFsDirInput(e.target.value)}
                onPressEnter={() => loadFSDir(fsDirInput, fsShowAll)}
                placeholder={t('Directory path')}
              />
              <Button onClick={() => loadFSDir(fsDirInput, fsShowAll)}>{t('Open')}</Button>
            </Space.Compact>
            <Space size={4}>
              <Switch
                size="small"
                checked={fsShowAll}
                onChange={(checked) => {
                  setFsShowAll(checked);
                  loadFSDir(fsData?.currentDir || fsDirInput, checked);
                }}
              />
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {t('All files')}
              </Typography.Text>
            </Space>
          </div>

          <div
            style={{
              maxHeight: 250,
              minHeight: 120,
              overflowY: 'auto',
              border: '1px solid rgba(128,128,128,0.2)',
              borderRadius: 8,
              padding: 4,
              marginBottom: 10,
            }}
          >
            {!fsData?.entries?.length ? (
              <Empty
                image={Empty.PRESENTED_IMAGE_SIMPLE}
                description={t('No matching database files in this folder')}
              />
            ) : (
              fsData.entries.map((entry) => (
                <div
                  key={entry.path}
                  onClick={() => {
                    if (entry.isDir) {
                      loadFSDir(entry.path, fsShowAll);
                    } else {
                      form.setFieldValue('filePath', entry.path);
                      setFsModalOpen(false);
                    }
                  }}
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'space-between',
                    gap: 8,
                    padding: '6px 10px',
                    borderRadius: 6,
                    cursor: 'pointer',
                  }}
                  className="sidebar-item-row"
                >
                  <Space size={8} style={{ minWidth: 0, overflow: 'hidden' }}>
                    {entry.isDir ? (
                      <FolderFilled style={{ color: '#faad14', flexShrink: 0 }} />
                    ) : (
                      <FileOutlined style={{ color: '#1677ff', flexShrink: 0 }} />
                    )}
                    <Typography.Text ellipsis style={{ fontSize: 13 }}>
                      {entry.name}
                    </Typography.Text>
                  </Space>
                  {!entry.isDir && (
                    <Typography.Text type="secondary" style={{ fontSize: 11, flexShrink: 0 }}>
                      {formatBytes(entry.sizeBytes)}
                    </Typography.Text>
                  )}
                </div>
              ))
            )}
          </div>

          <div
            style={{
              display: 'flex',
              justifyContent: 'space-between',
              alignItems: 'center',
              flexWrap: 'wrap',
              gap: 8,
              paddingTop: 8,
              borderTop: '1px dashed rgba(128,128,128,0.2)',
            }}
          >
            <Space.Compact style={{ flex: 1, minWidth: 220 }}>
              <Input
                size="small"
                value={newDbFileName}
                onChange={(e) => setNewDbFileName(e.target.value)}
                placeholder={selectedType === 'duckdb' ? 'analytics.duckdb' : 'app.sqlite'}
              />
              <Button
                size="small"
                type="primary"
                onClick={() => {
                  const baseDir = (fsData?.currentDir || fsDirInput || '.').replace(/[\\/]+$/, '');
                  const sep = baseDir.includes('\\') ? '\\' : '/';
                  const cleanName = newDbFileName.trim() || (selectedType === 'duckdb' ? 'analytics.duckdb' : 'app.sqlite');
                  form.setFieldValue('filePath', `${baseDir}${sep}${cleanName}`);
                  setFsModalOpen(false);
                }}
              >
                {t('Create / Use File Here')}
              </Button>
            </Space.Compact>

            {!isDesktop && (
              <Upload
                showUploadList={false}
                accept=".db,.sqlite,.sqlite3,.db3,.s3db,.duckdb,.ddb"
                beforeUpload={handleUploadDBFile}
              >
                <Button size="small" icon={<UploadOutlined />} loading={uploadingDB}>
                  {t('Upload Local DB File')}
                </Button>
              </Upload>
            )}
          </div>
        </Spin>
      </Modal>
    </Modal>
  );
};
