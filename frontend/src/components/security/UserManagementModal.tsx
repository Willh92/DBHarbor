import React, { useEffect, useMemo, useState } from 'react';
import {
  Alert,
  Button,
  Checkbox,
  Form,
  Input,
  Modal,
  Popconfirm,
  Radio,
  Select,
  Space,
  Switch,
  Tag,
  Tooltip,
  Typography,
  message,
} from 'antd';
import {
  CheckCircleOutlined,
  DeleteOutlined,
  DisconnectOutlined,
  EditOutlined,
  KeyOutlined,
  LockOutlined,
  PlusOutlined,
  ReloadOutlined,
  SafetyCertificateOutlined,
  SearchOutlined,
  TeamOutlined,
  UserOutlined,
} from '@ant-design/icons';
import { api } from '../../api/client';
import { useResponsive } from '../../hooks/useResponsive';
import { useI18n } from '../../i18n/useI18n';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import type { ManagedUser, UserRole } from '../../types';
import { ResizableTable } from '../common/ResizableTable';

interface Props {
  open: boolean;
  onClose: () => void;
}

export const UserManagementModal: React.FC<Props> = ({ open, onClose }) => {
  const { t } = useI18n();
  const { isMobile } = useResponsive();
  const { username: currentUsername, connections, reloadConnections } = useWorkbenchStore();

  const [users, setUsers] = useState<ManagedUser[]>([]);
  const [loading, setLoading] = useState(false);
  const [keyword, setKeyword] = useState('');

  // Create / Edit User form modal
  const [editorOpen, setEditorOpen] = useState(false);
  const [editingUser, setEditingUser] = useState<ManagedUser | null>(null);
  const [savingUser, setSavingUser] = useState(false);
  const [userForm] = Form.useForm();
  const watchedRole: UserRole = Form.useWatch('role', userForm) || 'readwrite';
  const watchedScope: 'all' | 'selected' = Form.useWatch('connScope', userForm) || 'all';

  // Reset Password modal
  const [resetTarget, setResetTarget] = useState<ManagedUser | null>(null);
  const [resetSaving, setResetSaving] = useState(false);
  const [resetForm] = Form.useForm();

  const loadUsers = async () => {
    setLoading(true);
    try {
      const list = await api.adminListUsers();
      setUsers(list);
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    if (open) {
      loadUsers();
      reloadConnections().catch(() => {});
    }
  }, [open]);

  const sharedConnections = useMemo(
    () => connections.filter((c) => c.visibility !== 'private'),
    [connections]
  );

  const filteredUsers = useMemo(() => {
    const q = keyword.trim().toLowerCase();
    if (!q) return users;
    return users.filter(
      (u) =>
        u.username.toLowerCase().includes(q) ||
        (u.displayName || '').toLowerCase().includes(q) ||
        u.role.toLowerCase().includes(q)
    );
  }, [users, keyword]);

  const openCreateModal = () => {
    setEditingUser(null);
    userForm.resetFields();
    userForm.setFieldsValue({
      username: '',
      displayName: '',
      password: '',
      forcePasswordChange: true,
      role: 'readwrite',
      canCreateConn: false,
      connScope: 'all',
      allowedConnIds: [],
    });
    setEditorOpen(true);
  };

  const openEditModal = (u: ManagedUser) => {
    setEditingUser(u);
    userForm.resetFields();
    userForm.setFieldsValue({
      username: u.username,
      displayName: u.displayName || '',
      role: u.role,
      canCreateConn: u.canCreateConn,
      connScope: u.connScope || 'all',
      allowedConnIds: u.allowedConnIds || [],
    });
    setEditorOpen(true);
  };

  const handleSaveUser = async () => {
    try {
      const vals = await userForm.validateFields();
      setSavingUser(true);
      const roleVal: UserRole = vals.role;
      const canCreate = roleVal === 'admin' ? true : roleVal === 'readwrite' ? Boolean(vals.canCreateConn) : false;
      const scopeVal: 'all' | 'selected' = roleVal === 'admin' ? 'all' : vals.connScope || 'all';
      const allowedIds: string[] = scopeVal === 'selected' ? vals.allowedConnIds || [] : [];

      if (editingUser) {
        await api.adminUpdateUser(editingUser.id, {
          displayName: vals.displayName?.trim() || '',
          role: roleVal,
          canCreateConn: canCreate,
          connScope: scopeVal,
          allowedConnIds: allowedIds,
        });
        message.success(t('User updated successfully'));
      } else {
        await api.adminCreateUser({
          username: vals.username.trim(),
          displayName: vals.displayName?.trim() || '',
          password: vals.password,
          role: roleVal,
          canCreateConn: canCreate,
          connScope: scopeVal,
          allowedConnIds: allowedIds,
          forcePasswordChange: Boolean(vals.forcePasswordChange),
        });
        message.success(t('User created successfully'));
      }
      setEditorOpen(false);
      await loadUsers();
    } catch (err: any) {
      if (!err?.errorFields) message.error(err.message);
    } finally {
      setSavingUser(false);
    }
  };

  const handleToggleEnabled = async (u: ManagedUser, enabled: boolean) => {
    try {
      await api.adminUpdateUser(u.id, { disabled: !enabled });
      message.success(enabled ? t('User account enabled') : t('User account disabled and sessions revoked'));
      await loadUsers();
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleConfirmResetPassword = async () => {
    if (!resetTarget) return;
    try {
      const vals = await resetForm.validateFields();
      setResetSaving(true);
      const res = await api.adminResetPassword(
        resetTarget.id,
        vals.newPassword,
        Boolean(vals.forcePasswordChange)
      );
      message.success(res.message || t('Password reset successfully'));
      setResetTarget(null);
      resetForm.resetFields();
      await loadUsers();
    } catch (err: any) {
      if (!err?.errorFields) message.error(err.message);
    } finally {
      setResetSaving(false);
    }
  };

  const renderRoleTag = (role: UserRole) => {
    if (role === 'admin') return <Tag color="gold">{t('Admin')}</Tag>;
    if (role === 'readwrite') return <Tag color="blue">{t('Read-Write')}</Tag>;
    return <Tag>{t('Read-Only')}</Tag>;
  };

  return (
    <>
      <Modal
        open={open}
        title={
          <Space>
            <TeamOutlined style={{ color: '#1677ff' }} />
            <span>{t('Team & RBAC User Governance')}</span>
          </Space>
        }
        width="min(960px, calc(100vw - 24px))"
        style={isMobile ? { top: 10 } : undefined}
        styles={{
          body: {
            maxHeight: isMobile ? 'calc(100dvh - 120px)' : 'calc(100dvh - 160px)',
            overflowY: 'auto',
          },
        }}
        onCancel={onClose}
        footer={null}
        destroyOnClose
      >
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 12 }}
          message={t(
            'Manage team accounts, 3-tier RBAC roles (Admin / Read-Write / Read-Only), shared connection visibility, and 2FA / session security.'
          )}
        />

        <div
          style={{
            display: 'flex',
            justifyContent: 'space-between',
            alignItems: 'center',
            gap: 8,
            flexWrap: 'wrap',
            marginBottom: 12,
          }}
        >
          <Input
            allowClear
            autoComplete="off"
            prefix={<SearchOutlined />}
            placeholder={t('Filter users by username, display name, or role...')}
            value={keyword}
            onChange={(e) => setKeyword(e.target.value)}
            style={{ width: 300, maxWidth: '100%' }}
          />
          <Space wrap>
            <Button icon={<ReloadOutlined />} onClick={loadUsers} loading={loading}>
              {t('Refresh')}
            </Button>
            <Button type="primary" icon={<PlusOutlined />} onClick={openCreateModal}>
              {t('Create User')}
            </Button>
          </Space>
        </div>

        <ResizableTable<ManagedUser>
          rowKey="id"
          size="small"
          loading={loading}
          dataSource={filteredUsers}
          scroll={{ x: 860 }}
          pagination={{ pageSize: 10, size: 'small', simple: isMobile, showSizeChanger: false }}
          columns={[
            {
              title: t('Account'),
              dataIndex: 'username',
              width: 175,
              render: (_, u) => (
                <div>
                  <Space size={6}>
                    <Typography.Text strong>{u.username}</Typography.Text>
                    {u.username === currentUsername && <Tag color="cyan">{t('You')}</Tag>}
                  </Space>
                  {u.displayName && (
                    <div style={{ fontSize: 12, opacity: 0.72 }}>{u.displayName}</div>
                  )}
                </div>
              ),
            },
            {
              title: t('Role'),
              dataIndex: 'role',
              width: 115,
              render: (r: UserRole) => renderRoleTag(r),
            },
            {
              title: t('Status'),
              dataIndex: 'disabled',
              width: 95,
              render: (_, u) => (
                <Tooltip
                  title={
                    u.username === currentUsername
                      ? t('Cannot disable your own active administrator account')
                      : u.disabled
                        ? t('Account disabled')
                        : t('Account active')
                  }
                >
                  <Switch
                    size="small"
                    checked={!u.disabled}
                    disabled={u.username === currentUsername}
                    onChange={(checked) => handleToggleEnabled(u, checked)}
                  />
                </Tooltip>
              ),
            },
            {
              title: t('Connection Access'),
              key: 'access',
              width: 185,
              render: (_, u) => {
                if (u.role === 'admin') {
                  return <Tag color="gold">{t('All Connections')}</Tag>;
                }
                return (
                  <Space size={[4, 4]} wrap>
                    {u.connScope === 'selected' ? (
                      <Tag color="geekblue">
                        {t('{count} Shared Selected', { count: (u.allowedConnIds || []).length })}
                      </Tag>
                    ) : (
                      <Tag color="processing">{t('All Shared')}</Tag>
                    )}
                    {u.canCreateConn && <Tag color="purple">{t('+ Private Conn')}</Tag>}
                  </Space>
                );
              },
            },
            {
              title: t('Security & Sessions'),
              key: 'security',
              width: 165,
              render: (_, u) => (
                <Space size={[4, 4]} wrap>
                  {u.totpEnabled ? (
                    <Tag icon={<CheckCircleOutlined />} color="success">
                      2FA
                    </Tag>
                  ) : (
                    <Tag>{t('No 2FA')}</Tag>
                  )}
                  {!u.passwordChanged && <Tag color="warning">{t('Initial Pwd')}</Tag>}
                  <Tag color={u.activeSessionsCount > 0 ? 'blue' : 'default'}>
                    {t('{count} Sessions', { count: u.activeSessionsCount || 0 })}
                  </Tag>
                </Space>
              ),
            },
            {
              title: t('Actions'),
              key: 'actions',
              width: 195,
              render: (_, u) => (
                <Space size={4} wrap>
                  <Tooltip title={t('Edit Role & Permissions')}>
                    <Button size="small" type="text" icon={<EditOutlined />} onClick={() => openEditModal(u)} />
                  </Tooltip>
                  <Tooltip title={t('Reset Password')}>
                    <Button
                      size="small"
                      type="text"
                      icon={<KeyOutlined />}
                      onClick={() => {
                        setResetTarget(u);
                        resetForm.setFieldsValue({ newPassword: '', forcePasswordChange: true });
                      }}
                    />
                  </Tooltip>
                  {u.totpEnabled && (
                    <Popconfirm
                      title={t('Reset 2FA for {user}? They will need to re-enroll an authenticator.', {
                        user: u.username,
                      })}
                      onConfirm={async () => {
                        try {
                          await api.adminReset2FA(u.id);
                          message.success(t('2FA reset for {user}', { user: u.username }));
                          loadUsers();
                        } catch (err: any) {
                          message.error(err.message);
                        }
                      }}
                    >
                      <Tooltip title={t('Reset 2FA')}>
                        <Button size="small" type="text" icon={<SafetyCertificateOutlined />} />
                      </Tooltip>
                    </Popconfirm>
                  )}
                  {u.activeSessionsCount > 0 && u.username !== currentUsername && (
                    <Popconfirm
                      title={t('Revoke all active sessions for {user}?', { user: u.username })}
                      onConfirm={async () => {
                        try {
                          const res = await api.adminRevokeUserSessions(u.id);
                          message.success(res.message);
                          loadUsers();
                        } catch (err: any) {
                          message.error(err.message);
                        }
                      }}
                    >
                      <Tooltip title={t('Force Sign Out All Devices')}>
                        <Button size="small" type="text" danger icon={<DisconnectOutlined />} />
                      </Tooltip>
                    </Popconfirm>
                  )}
                  {u.username !== currentUsername && (
                    <Popconfirm
                      title={t(
                        'Delete user {user}? Their private connections and active sessions will also be removed.',
                        { user: u.username }
                      )}
                      onConfirm={async () => {
                        try {
                          await api.adminDeleteUser(u.id);
                          message.success(t('User deleted'));
                          loadUsers();
                          reloadConnections().catch(() => {});
                        } catch (err: any) {
                          message.error(err.message);
                        }
                      }}
                    >
                      <Tooltip title={t('Delete User')}>
                        <Button size="small" type="text" danger icon={<DeleteOutlined />} />
                      </Tooltip>
                    </Popconfirm>
                  )}
                </Space>
              ),
            },
          ]}
        />
      </Modal>

      {/* Create / Edit User Modal */}
      <Modal
        open={editorOpen}
        title={editingUser ? t('Edit User: {user}', { user: editingUser.username }) : t('Create Team User')}
        width="min(560px, calc(100vw - 24px))"
        style={isMobile ? { top: 10 } : undefined}
        styles={{
          body: {
            maxHeight: isMobile ? 'calc(100dvh - 120px)' : 'calc(100dvh - 160px)',
            overflowY: 'auto',
          },
        }}
        onCancel={() => setEditorOpen(false)}
        onOk={handleSaveUser}
        confirmLoading={savingUser}
        okText={editingUser ? t('Save Changes') : t('Create User')}
        destroyOnClose
      >
        <Form form={userForm} layout="vertical" autoComplete="off">
          {!editingUser && (
            <Form.Item
              name="username"
              label={t('Username')}
              rules={[
                { required: true, message: t('Required') },
                {
                  pattern: /^[a-zA-Z0-9_.-]{3,48}$/,
                  message: t('3-48 characters: letters, digits, underscore, dot, or hyphen'),
                },
              ]}
            >
              <Input prefix={<UserOutlined />} placeholder="e.g. analyst_alice" autoComplete="off" />
            </Form.Item>
          )}

          <Form.Item name="displayName" label={t('Display Name')}>
            <Input placeholder={t('Optional friendly name, e.g. Alice Chen')} maxLength={64} autoComplete="off" />
          </Form.Item>

          {!editingUser && (
            <>
              <Form.Item
                name="password"
                label={t('Initial Password')}
                rules={[{ required: true, min: 6, message: t('Min 6 characters') }]}
              >
                <Input.Password
                  prefix={<LockOutlined />}
                  placeholder={t('At least 6 characters')}
                  autoComplete="new-password"
                />
              </Form.Item>
              <Form.Item name="forcePasswordChange" valuePropName="checked">
                <Checkbox>{t('Prompt user to change initial password after signing in')}</Checkbox>
              </Form.Item>
            </>
          )}

          <Form.Item
            name="role"
            label={t('RBAC Role')}
            rules={[{ required: true, message: t('Required') }]}
          >
            <Select
              disabled={Boolean(editingUser && editingUser.username === currentUsername)}
              optionRender={(opt) => (
                <div style={{ whiteSpace: 'normal', wordBreak: 'break-word', lineHeight: 1.4 }}>{opt.label}</div>
              )}
              options={[
                {
                  value: 'admin',
                  label: t('Admin — Full system, user & connection governance'),
                },
                {
                  value: 'readwrite',
                  label: t('Read-Write — Query, edit rows, DDL & data import/export on allowed connections'),
                },
                {
                  value: 'readonly',
                  label: t('Read-Only — Safe SELECT/SHOW/EXPLAIN & read-only browsing; blocks all mutations'),
                },
              ]}
            />
          </Form.Item>

          {watchedRole === 'readwrite' && (
            <Form.Item
              name="canCreateConn"
              label={t('Personal Private Connections')}
              valuePropName="checked"
              extra={t('When enabled, this user can create and manage their own private database connections.')}
            >
              <Switch />
            </Form.Item>
          )}

          {watchedRole !== 'admin' && (
            <>
              <Form.Item name="connScope" label={t('Shared Connection Visibility')}>
                <Radio.Group style={{ display: 'flex', flexDirection: isMobile ? 'column' : 'row', gap: 6 }}>
                  <Radio value="all">{t('All Shared Connections')}</Radio>
                  <Radio value="selected">{t('Selected Shared Connections Only')}</Radio>
                </Radio.Group>
              </Form.Item>

              {watchedScope === 'selected' && (
                <Form.Item
                  name="allowedConnIds"
                  label={t('Allowed Shared Connections')}
                  extra={t('Select which team-shared connections this user can see and connect to.')}
                >
                  <Select
                    mode="multiple"
                    allowClear
                    placeholder={t('Select shared connections...')}
                    options={sharedConnections.map((c) => ({
                      value: c.id,
                      label: `${c.name} (${c.type.toUpperCase()})`,
                    }))}
                  />
                </Form.Item>
              )}
            </>
          )}
        </Form>
      </Modal>

      {/* Admin Reset Password Modal */}
      <Modal
        open={Boolean(resetTarget)}
        title={t('Reset Password: {user}', { user: resetTarget?.username || '' })}
        width="min(440px, calc(100vw - 24px))"
        onCancel={() => setResetTarget(null)}
        onOk={handleConfirmResetPassword}
        confirmLoading={resetSaving}
        okText={t('Reset Password')}
        destroyOnClose
      >
        <Form form={resetForm} layout="vertical" autoComplete="off" initialValues={{ forcePasswordChange: true }}>
          <Form.Item
            name="newPassword"
            label={t('New Temporary Password')}
            rules={[{ required: true, min: 6, message: t('Min 6 characters') }]}
          >
            <Input.Password prefix={<LockOutlined />} placeholder={t('At least 6 characters')} autoComplete="new-password" />
          </Form.Item>
          <Form.Item name="forcePasswordChange" valuePropName="checked">
            <Checkbox>{t('Mark password as temporary and revoke active sessions')}</Checkbox>
          </Form.Item>
        </Form>
      </Modal>
    </>
  );
};
