import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Checkbox,
  Drawer,
  Empty,
  Form,
  Input,
  Modal,
  Popconfirm,
  Segmented,
  Select,
  Space,
  Spin,
  Switch,
  Tag,
  Tooltip,
  Typography,
  message,
} from 'antd';
import {
  CopyOutlined,
  DeleteOutlined,
  KeyOutlined,
  LockOutlined,
  PlusOutlined,
  ReloadOutlined,
  SafetyCertificateOutlined,
  SearchOutlined,
  TeamOutlined,
  UnlockOutlined,
} from '@ant-design/icons';
import { api, isAuthPromptCancelledError } from '../../api/client';
import { useResponsive } from '../../hooks/useResponsive';
import { useI18n } from '../../i18n/useI18n';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import type {
  DbAccountInfo,
  DbGrantEntry,
  DbPrivilegeActionPayload,
  DbPrivilegeCapabilities,
  WorkspaceTab,
} from '../../types';
import { writeTextToClipboard } from '../../utils/tableClipboard';
import { ResizableTable, calcSmartColumnWidth } from '../common/ResizableTable';
import type { ResizableColumnType } from '../common/ResizableTable';

interface Props {
  tab: WorkspaceTab;
}

const READONLY_PRESET = ['SELECT', 'SHOW VIEW', 'CONNECT', 'USAGE', 'VIEW DEFINITION', 'SHOW'];
const READWRITE_PRESET = [
  'SELECT',
  'INSERT',
  'UPDATE',
  'DELETE',
  'SHOW VIEW',
  'EXECUTE',
  'CONNECT',
  'USAGE',
];
const DDL_DEV_PRESET = [
  'SELECT',
  'INSERT',
  'UPDATE',
  'DELETE',
  'CREATE',
  'ALTER',
  'DROP',
  'INDEX',
  'REFERENCES',
  'CREATE VIEW',
  'SHOW VIEW',
  'TRIGGER',
  'EXECUTE',
  'CONNECT',
  'USAGE',
  'TEMPORARY',
];

export const DbUsersTab: React.FC<Props> = ({ tab }) => {
  const { t } = useI18n();
  const { isMobile } = useResponsive();
  const { isDesktop, role, connections, treeRefreshTick } = useWorkbenchStore();
  const conn = connections.find((c) => c.id === tab.connectionId);
  const canWrite = isDesktop || role !== 'readonly';

  const [caps, setCaps] = useState<DbPrivilegeCapabilities | null>(null);
  const [users, setUsers] = useState<DbAccountInfo[]>([]);
  const [databases, setDatabases] = useState<string[]>([]);
  const [selectedUserDB, setSelectedUserDB] = useState<string>(tab.database || '');
  const [loading, setLoading] = useState(false);
  const [search, setSearch] = useState('');
  const reqSeqRef = useRef(0);
  const needsUnlockReloadRef = useRef(false);

  // Sync external database / schema updates when Users & Grants is opened from another database
  useEffect(() => {
    if (tab.database && tab.database !== selectedUserDB) {
      setSelectedUserDB(tab.database);
      setTargetDB(tab.database);
      setTargetTable('');
    }
    if (tab.schema && tab.schema !== targetSchema) {
      setTargetSchema(tab.schema);
    }
  }, [tab.database, tab.schema]);

  // Create User Modal
  const [createOpen, setCreateOpen] = useState(false);
  const [creating, setCreating] = useState(false);
  const [createForm] = Form.useForm();
  const [createPreview, setCreatePreview] = useState<string[]>([]);

  // Password Change Modal
  const [pwdTarget, setPwdTarget] = useState<DbAccountInfo | null>(null);
  const [newPassword, setNewPassword] = useState('');
  const [pwdSaving, setPwdSaving] = useState(false);

  // Grant & Revoke Drawer
  const [grantTarget, setGrantTarget] = useState<DbAccountInfo | null>(null);
  const [grants, setGrants] = useState<DbGrantEntry[]>([]);
  const [loadingGrants, setLoadingGrants] = useState(false);
  const [grantActionMode, setGrantActionMode] = useState<'GRANT' | 'REVOKE'>('GRANT');
  const [scopeType, setScopeType] = useState<'global' | 'database' | 'table'>('database');
  const [targetDB, setTargetDB] = useState<string>(tab.database || '');
  const [targetSchema, setTargetSchema] = useState<string>(tab.schema || 'public');
  const [targetTable, setTargetTable] = useState<string>('');
  const [availableTables, setAvailableTables] = useState<string[]>([]);
  const [availableSchemas, setAvailableSchemas] = useState<string[]>([]);
  const [selectedPrivs, setSelectedPrivs] = useState<string[]>([]);
  const [selectedRoles, setSelectedRoles] = useState<string[]>([]);
  const [withGrantOption, setWithGrantOption] = useState(false);
  const [redisPreset, setRedisPreset] = useState<'readonly' | 'readwrite' | 'admin' | 'custom'>('readwrite');
  const [redisRules, setRedisRules] = useState('~* +@read +@write -@dangerous');
  const [previewStmts, setPreviewStmts] = useState<string[]>([]);
  const [executingGrant, setExecutingGrant] = useState(false);

  const loadUsersAndCaps = useCallback(async () => {
    const seq = ++reqSeqRef.current;
    setLoading(true);
    try {
      const [capRes, dbList] = await Promise.all([
        api.dbPrivCapabilities(tab.connectionId),
        api.listDatabases(tab.connectionId).catch((err) => {
          if (isAuthPromptCancelledError(err)) throw err;
          return [];
        }),
      ]);
      if (seq !== reqSeqRef.current) return;
      setCaps(capRes);
      const dbNames = dbList.map((d) => d.name);
      setDatabases(dbNames);
      const effectiveDB = selectedUserDB || tab.database || dbNames[0] || '';
      if (!selectedUserDB && effectiveDB) {
        setSelectedUserDB(effectiveDB);
      }
      if (capRes.supported) {
        const list = await api.dbListUsers(tab.connectionId, effectiveDB);
        if (seq !== reqSeqRef.current) return;
        setUsers(list);
      } else {
        setUsers([]);
      }
      needsUnlockReloadRef.current = false;
    } catch (err: any) {
      if (seq === reqSeqRef.current) {
        if (isAuthPromptCancelledError(err)) {
          needsUnlockReloadRef.current = true;
        } else {
          message.error(err.message);
        }
      }
    } finally {
      if (seq === reqSeqRef.current) {
        setLoading(false);
      }
    }
  }, [tab.connectionId, tab.database, selectedUserDB]);

  useEffect(() => {
    loadUsersAndCaps();
  }, [loadUsersAndCaps]);

  useEffect(() => {
    if (needsUnlockReloadRef.current && conn?.unlocked) {
      needsUnlockReloadRef.current = false;
      void loadUsersAndCaps();
    }
  }, [conn?.unlocked, treeRefreshTick, loadUsersAndCaps]);

  // Load tables/schemas when targetDB changes inside Grant Drawer
  useEffect(() => {
    if (!grantTarget || !targetDB || !caps?.supportsTableScope) {
      setAvailableTables([]);
      return;
    }
    let active = true;
    (async () => {
      try {
        if (tab.dbType === 'postgres' || tab.dbType === 'kingbase' || tab.dbType === 'sqlserver') {
          const scList = await api.listSchemas(tab.connectionId, targetDB);
          if (!active) return;
          setAvailableSchemas(scList);
          const sc = scList.includes(targetSchema) ? targetSchema : scList[0] || '';
          if (sc && sc !== targetSchema) setTargetSchema(sc);
          const objs = await api.listObjects(tab.connectionId, targetDB, sc);
          if (!active) return;
          setAvailableTables([...(objs.tables || []), ...(objs.views || [])].map((o) => o.name));
        } else {
          const objs = await api.listObjects(tab.connectionId, targetDB, '');
          if (!active) return;
          setAvailableTables([...(objs.tables || []), ...(objs.views || [])].map((o) => o.name));
        }
      } catch {
        if (active) setAvailableTables([]);
      }
    })();
    return () => {
      active = false;
    };
  }, [grantTarget, targetDB, targetSchema, caps?.supportsTableScope, tab.connectionId, tab.dbType]);

  const loadUserGrants = useCallback(
    async (acct: DbAccountInfo) => {
      setLoadingGrants(true);
      try {
        const list = await api.dbGetUserGrants(
          tab.connectionId,
          acct.username,
          acct.host || '',
          selectedUserDB || tab.database || ''
        );
        setGrants(list);
      } catch (err: any) {
        message.error(err.message);
        setGrants([]);
      } finally {
        setLoadingGrants(false);
      }
    },
    [tab.connectionId, tab.database, selectedUserDB]
  );

  const openGrantDrawer = (acct: DbAccountInfo) => {
    setGrantTarget(acct);
    setGrantActionMode('GRANT');
    const defaultScope = caps?.supportsGlobalScope && !tab.database ? 'global' : 'database';
    setScopeType(caps?.supportsGlobalScope === false ? 'database' : defaultScope);
    setTargetDB(selectedUserDB || tab.database || databases[0] || '');
    setTargetTable('');
    setSelectedPrivs([]);
    setSelectedRoles([]);
    setWithGrantOption(false);
    setPreviewStmts([]);
    loadUserGrants(acct);
  };

  // Live SQL/Command preview inside Grant Drawer
  useEffect(() => {
    if (!grantTarget || !caps?.supported) {
      setPreviewStmts([]);
      return;
    }
    const hasSelection =
      tab.dbType === 'redis' ||
      selectedPrivs.length > 0 ||
      selectedRoles.length > 0;
    if (!hasSelection) {
      setPreviewStmts([]);
      return;
    }
    const payload: DbPrivilegeActionPayload = {
      action: grantActionMode,
      username: grantTarget.username,
      host: grantTarget.host,
      scopeType,
      database: scopeType === 'global' && tab.dbType !== 'mongodb' ? '' : targetDB,
      schema: targetSchema,
      table: scopeType === 'table' ? targetTable : '',
      privileges: selectedPrivs,
      roles: selectedRoles,
      withGrantOption,
      aclPreset: redisPreset,
      aclRules: redisPreset === 'custom' ? redisRules : undefined,
    };
    let cancelled = false;
    api
      .dbPreviewPrivilegeAction(tab.connectionId, payload)
      .then((res) => {
        if (!cancelled) setPreviewStmts(res.statements || []);
      })
      .catch(() => {
        if (!cancelled) setPreviewStmts([]);
      });
    return () => {
      cancelled = true;
    };
  }, [
    grantTarget,
    caps?.supported,
    grantActionMode,
    scopeType,
    targetDB,
    targetSchema,
    targetTable,
    selectedPrivs,
    selectedRoles,
    withGrantOption,
    redisPreset,
    redisRules,
    tab.connectionId,
    tab.dbType,
  ]);

  const applyPreset = (preset: 'readonly' | 'readwrite' | 'ddl' | 'admin') => {
    if (!caps) return;
    if (tab.dbType === 'mongodb') {
      if (preset === 'readonly') setSelectedRoles(['read']);
      else if (preset === 'readwrite') setSelectedRoles(['readWrite']);
      else if (preset === 'ddl') setSelectedRoles(['readWrite', 'dbAdmin']);
      else setSelectedRoles(['dbAdmin', 'userAdmin']);
      return;
    }
    if (tab.dbType === 'redis') {
      if (preset === 'readonly') setRedisPreset('readonly');
      else if (preset === 'admin') setRedisPreset('admin');
      else setRedisPreset('readwrite');
      return;
    }
    const avail = caps.availablePrivileges || [];
    if (preset === 'admin') {
      if (avail.includes('ALL PRIVILEGES')) {
        setSelectedPrivs(['ALL PRIVILEGES']);
      } else if (avail.includes('ALL')) {
        setSelectedPrivs(['ALL']);
      } else {
        setSelectedPrivs([...avail]);
      }
      return;
    }
    const source =
      preset === 'readonly' ? READONLY_PRESET : preset === 'readwrite' ? READWRITE_PRESET : DDL_DEV_PRESET;
    setSelectedPrivs(avail.filter((p) => source.includes(p)));
  };

  const handleExecuteGrantOrRevoke = async () => {
    if (!grantTarget) return;
    setExecutingGrant(true);
    try {
      const payload: DbPrivilegeActionPayload = {
        action: grantActionMode,
        username: grantTarget.username,
        host: grantTarget.host,
        scopeType,
        database: scopeType === 'global' && tab.dbType !== 'mongodb' ? '' : targetDB,
        schema: targetSchema,
        table: scopeType === 'table' ? targetTable : '',
        privileges: selectedPrivs,
        roles: selectedRoles,
        withGrantOption,
        aclPreset: redisPreset,
        aclRules: redisPreset === 'custom' ? redisRules : undefined,
      };
      const res = await api.dbExecutePrivilegeAction(tab.connectionId, payload);
      message.success(res.message);
      await Promise.all([loadUserGrants(grantTarget), loadUsersAndCaps()]);
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setExecutingGrant(false);
    }
  };

  const updateCreatePreview = async () => {
    const vals = createForm.getFieldsValue();
    if (!vals.username?.trim()) {
      setCreatePreview([]);
      return;
    }
    try {
      const res = await api.dbPreviewPrivilegeAction(tab.connectionId, {
        action: 'CREATE_USER',
        username: vals.username.trim(),
        host: vals.host || '%',
        password: vals.password || '',
        database: vals.database || selectedUserDB || tab.database || '',
        scopeType: vals.database ? 'database' : 'global',
        privileges: vals.privileges || [],
        roles: vals.roles || [],
        isSuper: Boolean(vals.isSuper),
        canCreateDB: Boolean(vals.canCreateDB),
        canCreateRole: Boolean(vals.canCreateRole),
        aclPreset: vals.aclPreset || 'readwrite',
      });
      setCreatePreview(res.statements || []);
    } catch {
      setCreatePreview([]);
    }
  };

  const handleCreateUser = async () => {
    try {
      const vals = await createForm.validateFields();
      setCreating(true);
      await api.dbExecutePrivilegeAction(tab.connectionId, {
        action: 'CREATE_USER',
        username: vals.username.trim(),
        host: vals.host || '%',
        password: vals.password || '',
        database: vals.database || selectedUserDB || tab.database || '',
        scopeType: vals.database ? 'database' : 'global',
        privileges: vals.privileges || [],
        roles: vals.roles || [],
        isSuper: Boolean(vals.isSuper),
        canCreateDB: Boolean(vals.canCreateDB),
        canCreateRole: Boolean(vals.canCreateRole),
        aclPreset: vals.aclPreset || 'readwrite',
      });
      message.success(t('Database user created successfully'));
      setCreateOpen(false);
      createForm.resetFields();
      setCreatePreview([]);
      await loadUsersAndCaps();
    } catch (err: any) {
      if (err?.message) message.error(err.message);
    } finally {
      setCreating(false);
    }
  };

  const handleChangePassword = async () => {
    if (!pwdTarget || !newPassword) return;
    setPwdSaving(true);
    try {
      await api.dbExecutePrivilegeAction(tab.connectionId, {
        action: 'ALTER_USER',
        username: pwdTarget.username,
        host: pwdTarget.host,
        database: selectedUserDB || tab.database || '',
        password: newPassword,
      });
      message.success(t('Password updated successfully'));
      setPwdTarget(null);
      setNewPassword('');
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setPwdSaving(false);
    }
  };

  const handleToggleLock = async (acct: DbAccountInfo) => {
    try {
      await api.dbExecutePrivilegeAction(tab.connectionId, {
        action: acct.locked ? 'UNLOCK_USER' : 'LOCK_USER',
        username: acct.username,
        host: acct.host,
        database: selectedUserDB || tab.database || '',
      });
      message.success(acct.locked ? t('Account unlocked') : t('Account locked'));
      await loadUsersAndCaps();
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleDropUser = async (acct: DbAccountInfo) => {
    try {
      await api.dbExecutePrivilegeAction(tab.connectionId, {
        action: 'DROP_USER',
        username: acct.username,
        host: acct.host,
        database: selectedUserDB || tab.database || '',
      });
      message.success(t('Database user {name} deleted', { name: acct.identity }));
      if (grantTarget?.identity === acct.identity) {
        setGrantTarget(null);
      }
      await loadUsersAndCaps();
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const filteredUsers = useMemo(() => {
    const q = search.trim().toLowerCase();
    if (!q) return users;
    return users.filter(
      (u) =>
        u.username.toLowerCase().includes(q) ||
        (u.host || '').toLowerCase().includes(q) ||
        (u.roles || []).some((r) => r.toLowerCase().includes(q)) ||
        (u.extra || '').toLowerCase().includes(q)
    );
  }, [users, search]);

  const columns = useMemo<ResizableColumnType<DbAccountInfo>[]>(() => {
    const cols: ResizableColumnType<DbAccountInfo>[] = [
      {
        title: t('Account / Role'),
        dataIndex: 'username',
        key: 'username',
        width: calcSmartColumnWidth(t('Account / Role'), { minBodyWidth: 180 }),
        render: (_, rec) => (
          <Space size={6} wrap>
            <Typography.Text strong>{rec.username}</Typography.Text>
            {rec.isSuper && (
              <Tag color="gold" style={{ margin: 0, fontSize: 10 }}>
                SUPER
              </Tag>
            )}
          </Space>
        ),
      },
    ];

    if (caps?.supportsHost || tab.dbType === 'mongodb') {
      cols.push({
        title: tab.dbType === 'mongodb' ? t('Auth DB') : t('Host'),
        dataIndex: 'host',
        key: 'host',
        width: calcSmartColumnWidth(tab.dbType === 'mongodb' ? t('Auth DB') : t('Host'), { minBodyWidth: 120 }),
        render: (v: string) => <Tag style={{ margin: 0 }}>{v || '%'}</Tag>,
      });
    }

    cols.push({
      title: t('Status'),
      key: 'status',
      width: calcSmartColumnWidth(t('Status'), { minBodyWidth: 110 }),
      render: (_, rec) =>
        rec.locked ? (
          <Tag color="error" icon={<LockOutlined />} style={{ margin: 0 }}>
            {t('Locked')}
          </Tag>
        ) : (
          <Tag color="success" style={{ margin: 0 }}>
            {t('Active')}
          </Tag>
        ),
    });

    cols.push({
      title: t('Roles / Auth Info'),
      key: 'roles',
      width: calcSmartColumnWidth(t('Roles / Auth Info'), { minBodyWidth: 240 }),
      render: (_, rec) => (
        <Space size={4} wrap>
          {rec.authPlugin && <Tag color="blue">{rec.authPlugin}</Tag>}
          {(rec.roles || []).map((r) => (
            <Tag key={r} color="geekblue" style={{ margin: 0 }}>
              {r}
            </Tag>
          ))}
          {rec.extra && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {rec.extra}
            </Typography.Text>
          )}
        </Space>
      ),
    });

    cols.push({
      title: t('Actions'),
      key: 'actions',
      width: 280,
      render: (_, rec) => (
        <Space size={4} wrap>
          <Button
            size="small"
            type="primary"
            ghost
            icon={<SafetyCertificateOutlined />}
            onClick={() => openGrantDrawer(rec)}
          >
            {canWrite ? t('Assign Privileges') : t('Inspect Grants')}
          </Button>
          {canWrite && (
            <>
              <Tooltip title={t('Change Password')}>
                <Button
                  size="small"
                  icon={<KeyOutlined />}
                  onClick={() => {
                    setPwdTarget(rec);
                    setNewPassword('');
                  }}
                />
              </Tooltip>
              {caps?.supportsLock && (
                <Tooltip title={rec.locked ? t('Unlock Account') : t('Lock Account')}>
                  <Button
                    size="small"
                    icon={rec.locked ? <UnlockOutlined /> : <LockOutlined />}
                    onClick={() => handleToggleLock(rec)}
                  />
                </Tooltip>
              )}
              <Popconfirm
                title={t('Delete database account {name}?', { name: rec.identity })}
                okButtonProps={{ danger: true }}
                onConfirm={() => handleDropUser(rec)}
              >
                <Button size="small" danger icon={<DeleteOutlined />} />
              </Popconfirm>
            </>
          )}
        </Space>
      ),
    });

    return cols;
  }, [caps, tab.dbType, canWrite, t]);

  if (caps && !caps.supported) {
    return (
      <div style={{ padding: 24 }}>
        <Alert
          type="info"
          showIcon
          message={t('Database-Level User Accounts Not Applicable')}
          description={t(caps.reason || 'Embedded file engines do not use server-level accounts.')}
        />
      </div>
    );
  }

  return (
    <div style={{ height: '100%', display: 'flex', flexDirection: 'column', padding: isMobile ? 8 : '10px 14px', overflow: 'hidden' }}>
      {/* Top Toolbar */}
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          flexWrap: 'wrap',
          gap: 8,
          marginBottom: 10,
          flexShrink: 0,
        }}
      >
        <Space size={8} wrap>
          <TeamOutlined style={{ fontSize: 16, color: '#1677ff' }} />
          <Typography.Text strong style={{ fontSize: 14 }}>
            {t('Database Users & Privilege Assignment')}
          </Typography.Text>
          <Tag color="geekblue" style={{ margin: 0 }}>
            {tab.connName} ({tab.dbType.toUpperCase()})
          </Tag>
          {(tab.dbType === 'mongodb' || tab.dbType === 'sqlserver') && databases.length > 0 && (
            <Select
              size="small"
              style={{ minWidth: 150 }}
              value={selectedUserDB || undefined}
              onChange={(v) => setSelectedUserDB(v)}
              options={databases.map((d) => ({ value: d, label: `${t('Database')}: ${d}` }))}
            />
          )}
        </Space>

        <Space size={6} wrap>
          <Input
            size="small"
            allowClear
            prefix={<SearchOutlined />}
            placeholder={t('Search database accounts or roles...')}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            style={{ width: 220 }}
          />
          <Button size="small" icon={<ReloadOutlined />} loading={loading} onClick={loadUsersAndCaps}>
            {t('Refresh')}
          </Button>
          {canWrite && (
            <Button
              size="small"
              type="primary"
              icon={<PlusOutlined />}
              onClick={() => {
                createForm.resetFields();
                createForm.setFieldsValue({
                  host: '%',
                  database: selectedUserDB || tab.database || '',
                  aclPreset: 'readwrite',
                });
                setCreatePreview([]);
                setCreateOpen(true);
              }}
            >
              {t('New DB User')}
            </Button>
          )}
        </Space>
      </div>

      {/* Main Accounts Table */}
      <div style={{ flex: 1, minHeight: 0, overflow: 'auto' }}>
        <ResizableTable<DbAccountInfo>
          size="small"
          bordered
          loading={loading}
          rowKey="identity"
          columns={columns}
          dataSource={filteredUsers}
          pagination={{ pageSize: 50, simple: isMobile, showSizeChanger: !isMobile }}
          locale={{ emptyText: <Empty description={t('No database accounts returned')} /> }}
        />
      </div>

      {/* Create Database User Modal */}
      <Modal
        open={createOpen}
        title={t('Create Database Account')}
        width="min(640px, 95vw)"
        onCancel={() => setCreateOpen(false)}
        onOk={handleCreateUser}
        confirmLoading={creating}
        okText={t('Create Now')}
      >
        <Form form={createForm} layout="vertical" onValuesChange={updateCreatePreview}>
          <div style={{ display: 'grid', gridTemplateColumns: !isMobile && caps?.supportsHost ? '2fr 1fr' : '1fr', gap: 12 }}>
            <Form.Item
              name="username"
              label={t('Account Username')}
              rules={[{ required: true, message: t('Username is required') }]}
            >
              <Input placeholder="app_service" autoComplete="off" />
            </Form.Item>
            {caps?.supportsHost && (
              <Form.Item name="host" label={t('Allowed Host')}>
                <Input placeholder="% or 10.0.%" />
              </Form.Item>
            )}
          </div>

          <Form.Item name="password" label={t('Password')}>
            <Input.Password placeholder={t('Account password')} autoComplete="new-password" />
          </Form.Item>

          {(tab.dbType === 'postgres' || tab.dbType === 'kingbase') && (
            <Space size={16} wrap style={{ marginBottom: 12 }}>
              <Form.Item name="isSuper" valuePropName="checked" noStyle>
                <Checkbox>SUPERUSER</Checkbox>
              </Form.Item>
              <Form.Item name="canCreateDB" valuePropName="checked" noStyle>
                <Checkbox>CREATEDB</Checkbox>
              </Form.Item>
              <Form.Item name="canCreateRole" valuePropName="checked" noStyle>
                <Checkbox>CREATEROLE</Checkbox>
              </Form.Item>
            </Space>
          )}

          {tab.dbType === 'redis' ? (
            <Form.Item label={t('Initial ACL Permission Preset')}>
              <div className="dbharbor-no-scrollbar" style={{ overflowX: 'auto', maxWidth: '100%' }}>
                <Form.Item name="aclPreset" noStyle>
                  <Segmented
                    options={[
                      { value: 'readonly', label: t('Read-Only') },
                      { value: 'readwrite', label: t('Read-Write') },
                      { value: 'admin', label: t('Full Admin') },
                    ]}
                  />
                </Form.Item>
              </div>
            </Form.Item>
          ) : (
            <>
              {databases.length > 0 && (
                <Form.Item name="database" label={t('Initial Target Database (Optional)')}>
                  <Select
                    allowClear
                    placeholder={t('Global / All Databases')}
                    options={databases.map((d) => ({ value: d, label: d }))}
                  />
                </Form.Item>
              )}
              {caps?.supportsRoles && (caps.availableRoles || []).length > 0 && (
                <Form.Item name="roles" label={t('Initial Database Roles')}>
                  <Select
                    mode="multiple"
                    allowClear
                    options={(caps.availableRoles || []).map((r) => ({ value: r, label: r }))}
                  />
                </Form.Item>
              )}
              {(caps?.availablePrivileges || []).length > 0 && (
                <Form.Item name="privileges" label={t('Initial Privileges')}>
                  <Select
                    mode="multiple"
                    allowClear
                    placeholder={t('Select initial privileges to grant immediately...')}
                    options={(caps?.availablePrivileges || []).map((p) => ({ value: p, label: p }))}
                  />
                </Form.Item>
              )}
            </>
          )}

          {createPreview.length > 0 && (
            <Card size="small" title={t('SQL / Command Preview')} style={{ marginTop: 8 }}>
              <pre style={{ margin: 0, fontSize: 12, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
                {createPreview.join(';\n')}
              </pre>
            </Card>
          )}
        </Form>
      </Modal>

      {/* Change Password Modal */}
      <Modal
        open={Boolean(pwdTarget)}
        title={t('Change Password: {name}', { name: pwdTarget?.identity || '' })}
        onCancel={() => setPwdTarget(null)}
        onOk={handleChangePassword}
        confirmLoading={pwdSaving}
        okButtonProps={{ disabled: !newPassword }}
        okText={t('Update Password')}
      >
        <Input.Password
          placeholder={t('Enter new password')}
          value={newPassword}
          onChange={(e) => setNewPassword(e.target.value)}
          onPressEnter={handleChangePassword}
          autoComplete="new-password"
        />
      </Modal>

      {/* Visual Privilege Assignment & Grants Drawer */}
      <Drawer
        open={Boolean(grantTarget)}
        title={
          <Space size={8} wrap>
            <SafetyCertificateOutlined style={{ color: '#1677ff' }} />
            <span>
              {t('Privileges & Grants')}: <strong>{grantTarget?.identity}</strong>
            </span>
          </Space>
        }
        width="min(720px, 95vw)"
        styles={{ body: { padding: isMobile ? 12 : 24 } }}
        onClose={() => setGrantTarget(null)}
      >
        {grantTarget && (
          <Space direction="vertical" size={14} style={{ width: '100%' }}>
            {/* Current Active Grants Section */}
            <Card
              size="small"
              title={t('Current Effective Grants ({count})', { count: grants.length })}
              extra={
                <Button size="small" icon={<ReloadOutlined />} onClick={() => loadUserGrants(grantTarget)}>
                  {t('Refresh')}
                </Button>
              }
            >
              <Spin spinning={loadingGrants}>
                {grants.length === 0 ? (
                  <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('No explicit grants found')} />
                ) : (
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 6, maxHeight: 210, overflowY: 'auto' }}>
                    {grants.map((g, idx) => (
                      <div
                        key={idx}
                        style={{
                          padding: '6px 10px',
                          borderRadius: 6,
                          background: 'rgba(128,128,128,0.08)',
                          display: 'flex',
                          justifyContent: 'space-between',
                          alignItems: 'center',
                          gap: 8,
                        }}
                      >
                        <div style={{ minWidth: 0, flex: 1 }}>
                          <Typography.Text
                            code
                            style={{ fontSize: 12, wordBreak: 'break-all', whiteSpace: 'pre-wrap' }}
                          >
                            {g.statement}
                          </Typography.Text>
                        </div>
                        <Tooltip title={t('Copy')}>
                          <Button
                            size="small"
                            type="text"
                            icon={<CopyOutlined />}
                            onClick={() => {
                              writeTextToClipboard(g.statement);
                              message.success(t('Copied to clipboard'));
                            }}
                          />
                        </Tooltip>
                      </div>
                    ))}
                  </div>
                )}
              </Spin>
            </Card>

            {/* Visual Grant / Revoke Builder */}
            {canWrite && (
              <Card
                size="small"
                title={t('Assign or Revoke Database Privileges')}
                extra={
                  !isMobile ? (
                    <Segmented
                      size="small"
                      value={grantActionMode}
                      onChange={(v) => setGrantActionMode(v as 'GRANT' | 'REVOKE')}
                      options={[
                        { value: 'GRANT', label: t('Grant Privileges') },
                        { value: 'REVOKE', label: t('Revoke Privileges') },
                      ]}
                    />
                  ) : undefined
                }
              >
                <Space direction="vertical" size={12} style={{ width: '100%' }}>
                  {isMobile && (
                    <Segmented
                      block
                      size="middle"
                      value={grantActionMode}
                      onChange={(v) => setGrantActionMode(v as 'GRANT' | 'REVOKE')}
                      options={[
                        { value: 'GRANT', label: t('Grant Privileges') },
                        { value: 'REVOKE', label: t('Revoke Privileges') },
                      ]}
                    />
                  )}
                  {/* Quick Presets */}
                  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: 6 }}>
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      {t('Quick Role Presets:')}
                    </Typography.Text>
                    <Space size={6} wrap>
                      <Button size="small" onClick={() => applyPreset('readonly')}>
                        {t('Read-Only')}
                      </Button>
                      <Button size="small" onClick={() => applyPreset('readwrite')}>
                        {t('Read-Write')}
                      </Button>
                      {tab.dbType !== 'redis' && (
                        <Button size="small" onClick={() => applyPreset('ddl')}>
                          {t('DDL Developer')}
                        </Button>
                      )}
                      <Button size="small" onClick={() => applyPreset('admin')}>
                        {t('Full Admin')}
                      </Button>
                      {tab.dbType !== 'redis' && (
                        <Button
                          size="small"
                          type="text"
                          onClick={() => {
                            setSelectedPrivs([]);
                            setSelectedRoles([]);
                          }}
                        >
                          {t('Clear')}
                        </Button>
                      )}
                    </Space>
                  </div>

                  {tab.dbType === 'redis' ? (
                    <>
                      <div className="dbharbor-no-scrollbar" style={{ overflowX: 'auto', maxWidth: '100%' }}>
                        <Segmented
                          value={redisPreset}
                          onChange={(v) => setRedisPreset(v as any)}
                          options={[
                            { value: 'readonly', label: t('Read-Only') },
                            { value: 'readwrite', label: t('Read-Write') },
                            { value: 'admin', label: t('Full Admin') },
                            { value: 'custom', label: t('Custom ACL Rules') },
                          ]}
                        />
                      </div>
                      {redisPreset === 'custom' && (
                        <Input
                          value={redisRules}
                          onChange={(e) => setRedisRules(e.target.value)}
                          placeholder="~* +@read +@write -@dangerous"
                        />
                      )}
                    </>
                  ) : (
                    <>
                      {/* Target Scope Selector */}
                      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(170px, 1fr))', gap: 8 }}>
                        <div>
                          <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
                            {t('Privilege Scope')}
                          </Typography.Text>
                          <Select
                            size="small"
                            style={{ width: '100%' }}
                            value={scopeType}
                            onChange={(v) => setScopeType(v)}
                            options={[
                              ...(caps?.supportsGlobalScope
                                ? [{ value: 'global', label: t('Global (*.*)') }]
                                : []),
                              { value: 'database', label: t('Database Scope') },
                              ...(caps?.supportsTableScope
                                ? [{ value: 'table', label: t('Specific Table / View') }]
                                : []),
                            ]}
                          />
                        </div>

                        {(scopeType !== 'global' || tab.dbType === 'mongodb') && (
                          <div>
                            <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
                              {t('Database')}
                            </Typography.Text>
                            <Select
                              size="small"
                              showSearch
                              style={{ width: '100%' }}
                              value={targetDB || undefined}
                              onChange={(v) => {
                                setTargetDB(v);
                                setTargetTable('');
                              }}
                              options={databases.map((d) => ({ value: d, label: d }))}
                            />
                          </div>
                        )}

                        {scopeType !== 'global' && availableSchemas.length > 0 && (
                          <div>
                            <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
                              Schema
                            </Typography.Text>
                            <Select
                              size="small"
                              style={{ width: '100%' }}
                              value={targetSchema || undefined}
                              onChange={(v) => setTargetSchema(v)}
                              options={availableSchemas.map((s) => ({ value: s, label: s }))}
                            />
                          </div>
                        )}

                        {scopeType === 'table' && (
                          <div>
                            <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
                              {t('Table / View')}
                            </Typography.Text>
                            <Select
                              size="small"
                              showSearch
                              style={{ width: '100%' }}
                              value={targetTable || undefined}
                              onChange={(v) => setTargetTable(v)}
                              placeholder={t('Select table')}
                              options={availableTables.map((tb) => ({ value: tb, label: tb }))}
                            />
                          </div>
                        )}
                      </div>

                      {/* Roles Checkboxes */}
                      {caps?.supportsRoles && (caps.availableRoles || []).length > 0 && (
                        <div>
                          <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 6 }}>
                            {t('Database Roles')}
                          </Typography.Text>
                          <Checkbox.Group
                            value={selectedRoles}
                            onChange={(vals) => setSelectedRoles(vals as string[])}
                            style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}
                          >
                            {(caps.availableRoles || []).map((r) => (
                              <Checkbox key={r} value={r}>
                                {r}
                              </Checkbox>
                            ))}
                          </Checkbox.Group>
                        </div>
                      )}

                      {/* Privileges Checkboxes */}
                      {(caps?.availablePrivileges || []).length > 0 && (
                        <div>
                          <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 6 }}>
                            {t('SQL Privileges')}
                          </Typography.Text>
                          <Checkbox.Group
                            value={selectedPrivs}
                            onChange={(vals) => setSelectedPrivs(vals as string[])}
                            style={{
                              display: 'grid',
                              gridTemplateColumns: 'repeat(auto-fill, minmax(150px, 1fr))',
                              gap: 6,
                            }}
                          >
                            {(caps?.availablePrivileges || []).map((p) => (
                              <Checkbox key={p} value={p}>
                                {p}
                              </Checkbox>
                            ))}
                          </Checkbox.Group>
                        </div>
                      )}

                      {grantActionMode === 'GRANT' && tab.dbType !== 'mongodb' && (
                        <Space size={8}>
                          <Switch size="small" checked={withGrantOption} onChange={setWithGrantOption} />
                          <Typography.Text style={{ fontSize: 12 }}>
                            WITH GRANT OPTION ({t('Allow user to delegate these privileges')})
                          </Typography.Text>
                        </Space>
                      )}
                    </>
                  )}

                  {/* Live Statement Preview */}
                  {previewStmts.length > 0 && (
                    <div
                      style={{
                        padding: '8px 10px',
                        borderRadius: 6,
                        background: 'rgba(22, 119, 255, 0.06)',
                        border: '1px solid rgba(22, 119, 255, 0.2)',
                      }}
                    >
                      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 4 }}>
                        <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                          {t('SQL / Command Preview')}
                        </Typography.Text>
                        <Button
                          size="small"
                          type="link"
                          icon={<CopyOutlined />}
                          style={{ padding: 0, height: 20 }}
                          onClick={() => {
                            writeTextToClipboard(previewStmts.join(';\n'));
                            message.success(t('Copied to clipboard'));
                          }}
                        >
                          {t('Copy')}
                        </Button>
                      </div>
                      <pre style={{ margin: 0, fontSize: 12, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
                        {previewStmts.join(';\n')}
                      </pre>
                    </div>
                  )}

                  <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
                    <Button
                      type="primary"
                      danger={grantActionMode === 'REVOKE'}
                      loading={executingGrant}
                      disabled={previewStmts.length === 0}
                      onClick={handleExecuteGrantOrRevoke}
                    >
                      {grantActionMode === 'GRANT' ? t('Execute GRANT') : t('Execute REVOKE')}
                    </Button>
                  </div>
                </Space>
              </Card>
            )}
          </Space>
        )}
      </Drawer>
    </div>
  );
};
