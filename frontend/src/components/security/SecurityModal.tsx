import React, { useEffect, useRef, useState } from 'react';
import {
  Alert,
  Badge,
  Button,
  Card,
  Checkbox,
  Divider,
  Form,
  Input,
  Modal,
  Popconfirm,
  Progress,
  Segmented,
  Select,
  Space,
  Tabs,
  Tag,
  Tooltip,
  Typography,
  message,
} from 'antd';
import {
  BgColorsOutlined,
  CheckCircleOutlined,
  CopyOutlined,
  DeleteOutlined,
  DesktopOutlined,
  DownloadOutlined,
  EditOutlined,
  KeyOutlined,
  LockOutlined,
  QrcodeOutlined,
  ReloadOutlined,
  SafetyCertificateOutlined,
  SearchOutlined,
  StopOutlined,
  UploadOutlined,
  UserOutlined,
} from '@ant-design/icons';
import { api } from '../../api/client';
import {
  DEFAULT_LOGIN_BRANDING,
  LoginBrandEmblem,
  readBrandingImageAsDataUrl,
} from '../common/LoginBrandEmblem';
import { ResizableTable } from '../common/ResizableTable';
import { useResponsive } from '../../hooks/useResponsive';
import { useI18n } from '../../i18n/useI18n';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import type { AuditLogEntry, AuthSession, LoginBrandingConfig, LoginIconMode, SecurityStatus } from '../../types';
import { writeTextToClipboard } from '../../utils/tableClipboard';

interface Props {
  open: boolean;
  initialTab?: string;
  onClose: () => void;
}

function calcPasswordStrength(pwd: string): { percent: number; label: string; color: string } {
  if (!pwd) return { percent: 0, label: 'Empty', color: '#d9d9d9' };
  let score = 0;
  if (pwd.length >= 6) score += 25;
  if (pwd.length >= 10) score += 25;
  if (/[A-Z]/.test(pwd) && /[a-z]/.test(pwd)) score += 25;
  if (/\d/.test(pwd) && /[^A-Za-z0-9]/.test(pwd)) score += 25;
  else if (/\d/.test(pwd) || /[^A-Za-z0-9]/.test(pwd)) score += 15;
  const pct = Math.min(100, score);
  if (pct >= 80) return { percent: pct, label: 'Strong', color: '#52c41a' };
  if (pct >= 50) return { percent: pct, label: 'Medium', color: '#faad14' };
  return { percent: pct, label: 'Weak', color: '#ff4d4f' };
}

export const SecurityModal: React.FC<Props> = ({ open, initialTab = '2fa', onClose }) => {
  const {
    isDesktop,
    darkMode,
    username: storeUsername,
    displayName: storeDisplayName,
    role: storeRole,
    canCreateConn: storeCanCreate,
    loginBranding: storeLoginBranding,
    setSecurityMeta,
    setLoginBranding,
  } = useWorkbenchStore();
  const { isMobile } = useResponsive();
  const { t } = useI18n();
  const [activeTab, setActiveTab] = useState(initialTab);
  const [status, setStatus] = useState<SecurityStatus | null>(null);
  const [loading, setLoading] = useState(false);

  // Self-service display name edit
  const [profileName, setProfileName] = useState('');
  const [profileSaving, setProfileSaving] = useState(false);

  // 2FA setup state
  const [setupInfo, setSetupInfo] = useState<{
    secret: string;
    otpauthUri: string;
    qrCodeDataUrl: string;
  } | null>(null);
  const [verifyCode, setVerifyCode] = useState('');
  const [generatedCodes, setGeneratedCodes] = useState<string[]>([]);

  // 2FA disable / regen modal
  const [actionMode, setActionMode] = useState<'disable' | 'regen' | null>(null);
  const [actionPwd, setActionPwd] = useState('');
  const [actionCode, setActionCode] = useState('');
  const [actionLoading, setActionLoading] = useState(false);

  // Password form
  const [pwdForm] = Form.useForm();
  const [newPwdWatch, setNewPwdWatch] = useState('');
  const [pwdSaving, setPwdSaving] = useState(false);

  // Sessions & Audit logs
  const [sessions, setSessions] = useState<AuthSession[]>([]);
  const [sessionsLoading, setSessionsLoading] = useState(false);
  const [auditLogs, setAuditLogs] = useState<AuditLogEntry[]>([]);
  const [auditLoading, setAuditLoading] = useState(false);
  const [auditKeyword, setAuditKeyword] = useState('');
  const [auditEventType, setAuditEventType] = useState<string>('');

  // Login Page Branding (Admin only)
  const [brandingForm, setBrandingForm] = useState<LoginBrandingConfig>(
    storeLoginBranding || DEFAULT_LOGIN_BRANDING
  );
  const [brandingLoading, setBrandingLoading] = useState(false);
  const [brandingSaving, setBrandingSaving] = useState(false);
  const brandingIconInputRef = useRef<HTMLInputElement | null>(null);

  const loadLoginBranding = async () => {
    if (isDesktop) return;
    setBrandingLoading(true);
    try {
      const res = await api.adminGetLoginBranding();
      const loaded = res?.branding || (res as unknown as LoginBrandingConfig);
      if (loaded && typeof loaded === 'object' && 'iconMode' in loaded) {
        setBrandingForm(loaded);
        setLoginBranding(loaded);
      }
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setBrandingLoading(false);
    }
  };

  const handleSaveLoginBranding = async (nextConfig: LoginBrandingConfig = brandingForm) => {
    setBrandingSaving(true);
    try {
      const res = await api.adminSaveLoginBranding(nextConfig);
      const saved = res?.branding || (res as unknown as LoginBrandingConfig);
      if (saved && typeof saved === 'object' && 'iconMode' in saved) {
        setBrandingForm(saved);
        setLoginBranding(saved);
      }
      message.success(t('Login page appearance saved'));
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setBrandingSaving(false);
    }
  };

  const handleBrandingFileSelect = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = '';
    if (!file) return;
    try {
      const dataUrl = await readBrandingImageAsDataUrl(file);
      setBrandingForm((prev) => ({ ...prev, iconMode: 'custom', customIconDataUrl: dataUrl }));
    } catch (err: any) {
      message.error(t(err.message || 'Failed to read image file'));
    }
  };

  const loadSecurityStatus = async () => {
    setLoading(true);
    try {
      const sec = await api.getSecurityStatus();
      setStatus(sec);
      setProfileName(sec.displayName ?? storeDisplayName ?? '');
      setSecurityMeta({
        displayName: sec.displayName,
        role: sec.role,
        canCreateConn: sec.canCreateConn,
        totpEnabled: sec.totpEnabled,
        passwordChanged: sec.passwordChanged,
        recoveryCodesRemaining: sec.recoveryCodesRemaining,
      });
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setLoading(false);
    }
  };

  const handleSaveProfile = async () => {
    setProfileSaving(true);
    try {
      const res = await api.updateProfile(profileName.trim());
      setSecurityMeta({ displayName: res.displayName });
      setStatus((prev) => (prev ? { ...prev, displayName: res.displayName } : prev));
      message.success(t('Profile updated'));
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setProfileSaving(false);
    }
  };

  const loadSessions = async () => {
    setSessionsLoading(true);
    try {
      const list = await api.listSessions();
      setSessions(list);
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setSessionsLoading(false);
    }
  };

  const loadAuditLogs = async (kw = auditKeyword, evType = auditEventType) => {
    setAuditLoading(true);
    try {
      const logs = await api.listAuditLogs({
        limit: 100,
        keyword: kw.trim() || undefined,
        eventType: evType || undefined,
      });
      setAuditLogs(logs);
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setAuditLoading(false);
    }
  };

  useEffect(() => {
    if (open) {
      setActiveTab(initialTab);
      setSetupInfo(null);
      setVerifyCode('');
      setGeneratedCodes([]);
      setActionMode(null);
      setActionPwd('');
      setActionCode('');
      pwdForm.resetFields();
      setNewPwdWatch('');
      if (storeLoginBranding) setBrandingForm(storeLoginBranding);
      loadSecurityStatus();
      if (initialTab === 'sessions') loadSessions();
      if (initialTab === 'audit') loadAuditLogs();
      if (initialTab === 'branding') loadLoginBranding();
    }
  }, [open, initialTab]);

  const handleTabChange = (k: string) => {
    setActiveTab(k);
    if (k === 'sessions') loadSessions();
    if (k === 'audit') loadAuditLogs();
    if (k === 'branding') loadLoginBranding();
  };

  const handleStart2FA = async () => {
    try {
      const res = await api.begin2FASetup();
      setSetupInfo(res);
      setVerifyCode('');
      setGeneratedCodes([]);
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleConfirmEnable2FA = async () => {
    if (!verifyCode.trim()) {
      message.warning(t('Please enter the 6-digit code from your authenticator app'));
      return;
    }
    setLoading(true);
    try {
      const res = await api.enable2FA(verifyCode.trim());
      setGeneratedCodes(res.recoveryCodes || []);
      setSetupInfo(null);
      message.success(t('Two-Factor Authentication (2FA) enabled! Please save your recovery codes.'));
      await loadSecurityStatus();
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setLoading(false);
    }
  };

  const handleRun2FAAction = async () => {
    if (!actionPwd || !actionCode.trim()) {
      message.warning(t('Please enter your password and current 2FA / recovery code'));
      return;
    }
    setActionLoading(true);
    try {
      if (actionMode === 'disable') {
        await api.disable2FA(actionPwd, actionCode.trim());
        setGeneratedCodes([]);
        message.success(t('Two-Factor Authentication disabled'));
      } else if (actionMode === 'regen') {
        const res = await api.regenerateRecoveryCodes(actionPwd, actionCode.trim());
        setGeneratedCodes(res.recoveryCodes || []);
        message.success(t('Regenerated 8 one-time recovery codes (previous codes invalidated)'));
      }
      setActionMode(null);
      setActionPwd('');
      setActionCode('');
      await loadSecurityStatus();
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setActionLoading(false);
    }
  };

  const handleChangePassword = async () => {
    try {
      const vals = await pwdForm.validateFields();
      if (vals.newPassword !== vals.confirmPassword) {
        message.error(t('New passwords do not match'));
        return;
      }
      setPwdSaving(true);
      const res = await api.changePassword(
        vals.oldPassword,
        vals.newPassword,
        Boolean(vals.revokeOtherSessions)
      );
      message.success(
        res.revokedSessions > 0
          ? t('Password updated and revoked {count} other session(s)', { count: res.revokedSessions })
          : t('Password updated successfully')
      );
      pwdForm.resetFields();
      setNewPwdWatch('');
      await loadSecurityStatus();
    } catch (err: any) {
      if (!err?.errorFields) message.error(err.message);
    } finally {
      setPwdSaving(false);
    }
  };

  const downloadRecoveryCodes = (codes: string[]) => {
    const content = [
      t('DBHarbor Two-Factor Authentication (2FA) Recovery Codes'),
      `${t('Account')}: ${status?.username || 'admin'}`,
      `${t('Generated At')}: ${new Date().toLocaleString()}`,
      t('Note: Each recovery code can only be used once. Store them securely offline.'),
      '----------------------------------------',
      ...codes,
      '',
    ].join('\n');
    const blob = new Blob([content], { type: 'text/plain;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `dbharbor-2fa-recovery-codes.txt`;
    a.click();
    URL.revokeObjectURL(url);
  };

  const strength = calcPasswordStrength(newPwdWatch);

  const eventColor = (ev?: string) => {
    const val = (ev || '').toUpperCase();
    if (val.includes('LOCKED') || val.includes('FAILED') || val.includes('DELETED')) return 'error';
    if (val.includes('ENABLED') || val.includes('SUCCESS') || val.includes('CREATED')) return 'success';
    if (val.includes('REVOKED') || val.includes('DISABLED') || val.includes('RESET')) return 'warning';
    return 'processing';
  };

  const effectiveRole = status?.role || storeRole || 'admin';
  const effectiveCanCreate = status?.canCreateConn ?? storeCanCreate;

  return (
    <Modal
      open={open}
      title={
        <Space>
          <SafetyCertificateOutlined style={{ color: '#1677ff' }} />
          <span>{t('Account Profile, Security & Two-Factor Auth (2FA)')}</span>
        </Space>
      }
      width="min(780px, calc(100vw - 24px))"
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
      <Card size="small" style={{ marginBottom: 12 }}>
        <div
          style={{
            display: 'flex',
            justifyContent: 'space-between',
            alignItems: 'center',
            gap: 10,
            flexWrap: 'wrap',
          }}
        >
          <Space size={8} wrap>
            <UserOutlined />
            <Typography.Text strong>{status?.username || storeUsername || 'admin'}</Typography.Text>
            <Tag color={effectiveRole === 'admin' ? 'gold' : effectiveRole === 'readwrite' ? 'blue' : 'default'}>
              {effectiveRole === 'admin'
                ? t('Admin')
                : effectiveRole === 'readwrite'
                  ? t('Read-Write')
                  : t('Read-Only')}
            </Tag>
            {effectiveRole !== 'admin' && effectiveCanCreate && (
              <Tag color="purple">{t('Private Connections Allowed')}</Tag>
            )}
          </Space>

          <Space.Compact style={{ width: 300, maxWidth: '100%' }}>
            <Input
              prefix={<EditOutlined style={{ opacity: 0.55 }} />}
              placeholder={t('Display Name')}
              maxLength={64}
              value={profileName}
              onChange={(e) => setProfileName(e.target.value)}
              onPressEnter={handleSaveProfile}
            />
            <Button type="primary" loading={profileSaving} onClick={handleSaveProfile}>
              {t('Save')}
            </Button>
          </Space.Compact>
        </div>
      </Card>

      <Tabs
        size={isMobile ? 'small' : 'middle'}
        tabBarGutter={isMobile ? 14 : undefined}
        activeKey={activeTab}
        onChange={handleTabChange}
        items={[
          {
            key: '2fa',
            label: (
              <Space size={6}>
                <QrcodeOutlined />
                <span>{t('Two-Factor Auth (2FA)')}</span>
                {status?.totpEnabled && <Badge status="success" />}
              </Space>
            ),
            children: (
              <div>
                <Card size="small" style={{ marginBottom: 14 }}>
                  <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 12 }}>
                    <Space size={16} wrap>
                      <Space size={6}>
                        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                          {t('TOTP 2FA Status')}:
                        </Typography.Text>
                        {status?.totpEnabled ? (
                          <Tag icon={<CheckCircleOutlined />} color="success" style={{ margin: 0 }}>
                            {t('Enabled')}
                          </Tag>
                        ) : (
                          <Tag color="warning" style={{ margin: 0 }}>
                            {t('Disabled')}
                          </Tag>
                        )}
                      </Space>
                      <Space size={6}>
                        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                          {t('Recovery Codes Left')}:
                        </Typography.Text>
                        <Typography.Text strong>
                          {status?.totpEnabled ? `${status.recoveryCodesRemaining} / 8` : '-'}
                        </Typography.Text>
                      </Space>
                    </Space>

                    {!status?.totpEnabled ? (
                      <Button type="primary" icon={<QrcodeOutlined />} onClick={handleStart2FA} loading={loading}>
                        {t('Setup 2FA Authenticator')}
                      </Button>
                    ) : (
                      <Space>
                        <Button
                          size="small"
                          icon={<ReloadOutlined />}
                          onClick={() => {
                            setActionMode('regen');
                            setActionPwd('');
                            setActionCode('');
                          }}
                        >
                          {t('Regenerate Recovery Codes')}
                        </Button>
                        <Button
                          size="small"
                          danger
                          icon={<StopOutlined />}
                          onClick={() => {
                            setActionMode('disable');
                            setActionPwd('');
                            setActionCode('');
                          }}
                        >
                          {t('Disable 2FA')}
                        </Button>
                      </Space>
                    )}
                  </div>
                </Card>

                {generatedCodes.length > 0 && (
                  <Alert
                    type="success"
                    showIcon
                    style={{ marginBottom: 16 }}
                    message={t('Back up these 8 one-time recovery codes immediately (shown only once)')}
                    description={
                      <div>
                        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                          {t('If you lose your phone or authenticator access, any code below can complete 2FA once:')}
                        </Typography.Text>
                        <div
                          style={{
                            display: 'grid',
                            gridTemplateColumns: 'repeat(auto-fit, minmax(120px, 1fr))',
                            gap: 8,
                            margin: '10px 0',
                            padding: 10,
                            borderRadius: 6,
                            background: 'rgba(128,128,128,0.12)',
                            fontFamily: 'monospace',
                            fontWeight: 700,
                          }}
                        >
                          {generatedCodes.map((c) => (
                            <span key={c}>{c}</span>
                          ))}
                        </div>
                        <Space>
                          <Button
                            size="small"
                            icon={<CopyOutlined />}
                            onClick={async () => {
                              await writeTextToClipboard(generatedCodes.join('\n'));
                              message.success(t('All recovery codes copied'));
                            }}
                          >
                            {t('Copy All Codes')}
                          </Button>
                          <Button
                            size="small"
                            type="primary"
                            ghost
                            icon={<DownloadOutlined />}
                            onClick={() => downloadRecoveryCodes(generatedCodes)}
                          >
                            {t('Download .txt Backup')}
                          </Button>
                        </Space>
                      </div>
                    }
                  />
                )}

                {setupInfo && !status?.totpEnabled && (
                  <Card size="small" title={t('Step 1: Scan QR Code with Authenticator App (generated locally offline)')}>
                    <div style={{ display: 'flex', gap: 20, flexWrap: 'wrap', alignItems: 'center' }}>
                      <div
                        style={{
                          padding: 8,
                          background: '#fff',
                          borderRadius: 8,
                          border: '1px solid rgba(128,128,128,0.25)',
                        }}
                      >
                        <img
                          src={setupInfo.qrCodeDataUrl}
                          alt="TOTP QR Code"
                          style={{ width: 176, height: 176, display: 'block' }}
                        />
                      </div>

                      <div style={{ flex: 1, minWidth: isMobile ? 0 : 260, width: isMobile ? '100%' : undefined }}>
                        <Typography.Paragraph style={{ marginBottom: 8, fontSize: 13 }}>
                          {t(
                            'Supports Google Authenticator, Microsoft Authenticator, 1Password, Bitwarden, Authy, and any RFC 6238 TOTP app.'
                          )}
                        </Typography.Paragraph>
                        <div style={{ marginBottom: 12 }}>
                          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                            {t('Cannot scan? Enter Base32 secret manually:')}
                          </Typography.Text>
                          <div style={{ display: 'flex', gap: 6, marginTop: 4 }}>
                            <Input size="small" readOnly value={setupInfo.secret} style={{ fontFamily: 'monospace' }} />
                            <Button
                              size="small"
                              icon={<CopyOutlined />}
                              onClick={async () => {
                                await writeTextToClipboard(setupInfo.secret);
                                message.success(t('Secret key copied'));
                              }}
                            >
                              {t('Copy')}
                            </Button>
                          </div>
                        </div>

                        <Divider style={{ margin: '10px 0' }} />

                        <Typography.Text strong style={{ display: 'block', marginBottom: 6 }}>
                          {t('Step 2: Enter the 6-digit code from your app to activate')}
                        </Typography.Text>
                        <Space.Compact style={{ width: '100%' }}>
                          <Input
                            style={{ minWidth: 0 }}
                            placeholder={t('Enter 6-digit code, e.g. 123456')}
                            maxLength={8}
                            value={verifyCode}
                            onChange={(e) => setVerifyCode(e.target.value)}
                            onPressEnter={handleConfirmEnable2FA}
                          />
                          <Button type="primary" loading={loading} onClick={handleConfirmEnable2FA}>
                            {t('Verify & Enable 2FA')}
                          </Button>
                        </Space.Compact>
                      </div>
                    </div>
                  </Card>
                )}

                {actionMode && (
                  <Card
                    size="small"
                    style={{ marginTop: 12 }}
                    title={
                      actionMode === 'disable'
                        ? t('Verify Identity to Disable 2FA')
                        : t('Verify Identity to Regenerate Recovery Codes')
                    }
                    extra={
                      <Button size="small" type="text" onClick={() => setActionMode(null)}>
                        {t('Cancel')}
                      </Button>
                    }
                  >
                    <Space direction="vertical" style={{ width: '100%' }} size={10}>
                      <Input.Password
                        prefix={<LockOutlined />}
                        placeholder={t('Enter current login password')}
                        value={actionPwd}
                        onChange={(e) => setActionPwd(e.target.value)}
                      />
                      <Input
                        prefix={<KeyOutlined />}
                        placeholder={t('Enter current 6-digit TOTP or recovery code')}
                        value={actionCode}
                        onChange={(e) => setActionCode(e.target.value)}
                        onPressEnter={handleRun2FAAction}
                      />
                      <Button
                        type="primary"
                        danger={actionMode === 'disable'}
                        loading={actionLoading}
                        onClick={handleRun2FAAction}
                      >
                        {actionMode === 'disable' ? t('Confirm Disable 2FA') : t('Generate New Recovery Codes')}
                      </Button>
                    </Space>
                  </Card>
                )}
              </div>
            ),
          },
          {
            key: 'password',
            label: (
              <Space size={6}>
                <KeyOutlined />
                <span>{t('Password Security')}</span>
                {status && !status.passwordChanged && <Badge status="warning" />}
              </Space>
            ),
            children: (
              <div>
                {status && !status.passwordChanged && (
                  <Alert
                    type="warning"
                    showIcon
                    style={{ marginBottom: 14 }}
                    message={t(
                      'Security Notice: You are still using the default initial password. Please change your password and enable TOTP 2FA.'
                    )}
                  />
                )}
                <div
                  style={{
                    display: 'grid',
                    gridTemplateColumns: isMobile ? '1fr' : 'minmax(300px, 1.18fr) minmax(240px, 0.82fr)',
                    gap: 16,
                    alignItems: 'start',
                  }}
                >
                  <Card size="small" title={t('Change Account Password')}>
                    <Form
                      form={pwdForm}
                      layout="vertical"
                      initialValues={{ revokeOtherSessions: true }}
                    >
                      <Form.Item
                        name="oldPassword"
                        label={t('Current Password')}
                        rules={[{ required: true, message: t('Required') }]}
                        style={{ marginBottom: 14 }}
                      >
                        <Input.Password prefix={<LockOutlined />} />
                      </Form.Item>
                      <Form.Item
                        name="newPassword"
                        label={t('New Password (min 6 chars, mix case, digits & symbols)')}
                        rules={[{ required: true, min: 6, message: t('Min 6 characters') }]}
                        style={{ marginBottom: 14 }}
                      >
                        <Input.Password
                          prefix={<LockOutlined />}
                          onChange={(e) => setNewPwdWatch(e.target.value)}
                        />
                      </Form.Item>
                      <Form.Item
                        name="confirmPassword"
                        label={t('Confirm New Password')}
                        rules={[{ required: true, message: t('Required') }]}
                        style={{ marginBottom: 12 }}
                      >
                        <Input.Password prefix={<LockOutlined />} />
                      </Form.Item>
                      <Form.Item name="revokeOtherSessions" valuePropName="checked" style={{ marginBottom: 14 }}>
                        <Checkbox>{t('Revoke all other active device sessions after changing password')}</Checkbox>
                      </Form.Item>
                      <Button type="primary" block={isMobile} loading={pwdSaving} onClick={handleChangePassword}>
                        {t('Update Password')}
                      </Button>
                    </Form>
                  </Card>

                  <Card size="small" title={t('Password Security Checklist')}>
                    <Space direction="vertical" size={12} style={{ width: '100%' }}>
                      <div>
                        <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 4 }}>
                          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                            {t('Password Strength')}
                          </Typography.Text>
                          <Typography.Text strong style={{ fontSize: 12, color: newPwdWatch ? strength.color : undefined }}>
                            {newPwdWatch ? t(strength.label) : '-'}
                          </Typography.Text>
                        </div>
                        <Progress
                          percent={newPwdWatch ? strength.percent : 0}
                          strokeColor={strength.color}
                          showInfo={false}
                          size="small"
                        />
                      </div>

                      <Divider style={{ margin: '2px 0' }} />

                      {[
                        {
                          ok: newPwdWatch.length >= 8,
                          text: t('At least 8 characters recommended (min 6)'),
                        },
                        {
                          ok: /[a-z]/.test(newPwdWatch) && /[A-Z]/.test(newPwdWatch),
                          text: t('Mix uppercase & lowercase letters'),
                        },
                        {
                          ok: /\d/.test(newPwdWatch) && /[^a-zA-Z0-9]/.test(newPwdWatch),
                          text: t('Include numbers & special symbols'),
                        },
                        {
                          ok: true,
                          text: t('Bcrypt salted hash & multi-device session revocation'),
                        },
                      ].map((item, idx) => (
                        <div key={idx} style={{ display: 'flex', alignItems: 'flex-start', gap: 8, fontSize: 12.5 }}>
                          <CheckCircleOutlined
                            style={{
                              marginTop: 3,
                              color: item.ok ? '#52c41a' : 'rgba(128,128,128,0.45)',
                              flexShrink: 0,
                            }}
                          />
                          <Typography.Text type={item.ok ? undefined : 'secondary'} style={{ fontSize: 12.5 }}>
                            {item.text}
                          </Typography.Text>
                        </div>
                      ))}
                    </Space>
                  </Card>
                </div>
              </div>
            ),
          },
          {
            key: 'sessions',
            label: (
              <Space size={6}>
                <DesktopOutlined />
                <span>{t('Active Sessions')}</span>
              </Space>
            ),
            children: (
              <div>
                <div
                  style={{
                    display: 'flex',
                    justifyContent: 'space-between',
                    alignItems: 'center',
                    flexWrap: 'wrap',
                    gap: 8,
                    marginBottom: 10,
                  }}
                >
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    {t('JWT Session Control')}
                  </Typography.Text>
                  <Space>
                    <Button size="small" icon={<ReloadOutlined />} onClick={loadSessions}>
                      {t('Refresh')}
                    </Button>
                    <Popconfirm
                      title={t('Revoke all device sessions except this browser?')}
                      onConfirm={async () => {
                        try {
                          const res = await api.revokeOtherSessions();
                          message.success(res.message);
                          loadSessions();
                        } catch (err: any) {
                          message.error(err.message);
                        }
                      }}
                    >
                      <Button size="small" danger>
                        {t('Revoke Other Sessions')}
                      </Button>
                    </Popconfirm>
                  </Space>
                </div>
                <ResizableTable<AuthSession>
                  rowKey="id"
                  size="small"
                  loading={sessionsLoading}
                  dataSource={sessions}
                  scroll={{ x: 680 }}
                  pagination={false}
                  columns={[
                    {
                      title: t('Status'),
                      width: 115,
                      render: (_, r) =>
                        r.current ? <Tag color="green">{t('Current Device')}</Tag> : <Tag color="blue">{t('Active Session')}</Tag>,
                    },
                    { title: t('IP Address'), dataIndex: 'ip', width: 130 },
                    {
                      title: t('Browser (User-Agent)'),
                      dataIndex: 'userAgent',
                      width: 185,
                      ellipsis: true,
                      render: (ua: string) => (
                        <Tooltip title={ua}>
                          <span>{ua || '-'}</span>
                        </Tooltip>
                      ),
                    },
                    {
                      title: t('Last Active'),
                      dataIndex: 'lastSeenAt',
                      width: 165,
                      render: (v: string) => (v ? new Date(v).toLocaleString() : '-'),
                    },
                    {
                      title: t('Actions'),
                      width: 85,
                      render: (_, r) =>
                        !r.current ? (
                          <Popconfirm
                            title={t('Revoke this device session?')}
                            onConfirm={async () => {
                              try {
                                await api.revokeSession(r.id);
                                message.success(t('Session revoked'));
                                loadSessions();
                              } catch (err: any) {
                                message.error(err.message);
                              }
                            }}
                          >
                            <Button size="small" danger type="link" icon={<DeleteOutlined />}>
                              {t('Revoke')}
                            </Button>
                          </Popconfirm>
                        ) : null,
                    },
                  ]}
                />
              </div>
            ),
          },
          {
            key: 'audit',
            label: t('Security Audit Logs'),
            children: (
              <div>
                <div
                  style={{
                    display: 'flex',
                    justifyContent: 'space-between',
                    alignItems: 'center',
                    gap: 8,
                    flexWrap: 'wrap',
                    marginBottom: 8,
                  }}
                >
                  <Space size={6} wrap>
                    <Input
                      size="small"
                      allowClear
                      prefix={<SearchOutlined />}
                      placeholder={t('Filter account, IP, or detail...')}
                      value={auditKeyword}
                      onChange={(e) => setAuditKeyword(e.target.value)}
                      onPressEnter={() => loadAuditLogs(auditKeyword, auditEventType)}
                      style={{ width: 210, maxWidth: '100%' }}
                    />
                    <Select
                      size="small"
                      allowClear
                      placeholder={t('All Event Types')}
                      value={auditEventType || undefined}
                      onChange={(val) => {
                        const next = val || '';
                        setAuditEventType(next);
                        loadAuditLogs(auditKeyword, next);
                      }}
                      style={{ width: 175 }}
                      options={[
                        { value: 'ADMIN_INIT_SETUP', label: 'ADMIN_INIT_SETUP' },
                        { value: 'LOGIN_BRANDING_UPDATED', label: 'LOGIN_BRANDING_UPDATED' },
                        { value: 'LOGIN_SUCCESS', label: 'LOGIN_SUCCESS' },
                        { value: 'LOGIN_FAILED', label: 'LOGIN_FAILED' },
                        { value: 'ACCOUNT_LOCKED', label: 'ACCOUNT_LOCKED' },
                        { value: 'PASSWORD_CHANGED', label: 'PASSWORD_CHANGED' },
                        { value: '2FA_ENABLED', label: '2FA_ENABLED' },
                        { value: '2FA_DISABLED', label: '2FA_DISABLED' },
                        { value: 'USER_CREATED', label: 'USER_CREATED' },
                        { value: 'USER_UPDATED', label: 'USER_UPDATED' },
                        { value: 'USER_DELETED', label: 'USER_DELETED' },
                        { value: 'ADMIN_RESET_PASSWORD', label: 'ADMIN_RESET_PASSWORD' },
                        { value: 'ADMIN_RESET_2FA', label: 'ADMIN_RESET_2FA' },
                        { value: 'PROFILE_UPDATED', label: 'PROFILE_UPDATED' },
                      ]}
                    />
                  </Space>
                  <Button
                    size="small"
                    icon={<ReloadOutlined />}
                    onClick={() => loadAuditLogs(auditKeyword, auditEventType)}
                  >
                    {t('Refresh Logs')}
                  </Button>
                </div>
                <ResizableTable<AuditLogEntry>
                  rowKey="id"
                  size="small"
                  loading={auditLoading}
                  dataSource={auditLogs}
                  scroll={{ x: 760 }}
                  pagination={{ pageSize: 10, size: 'small', simple: isMobile }}
                  columns={[
                    {
                      title: t('Time'),
                      dataIndex: 'createdAt',
                      width: 165,
                      render: (v: string) => (v ? new Date(v).toLocaleString() : '-'),
                    },
                    {
                      title: t('Security Event'),
                      dataIndex: 'eventType',
                      width: 175,
                      render: (ev: string, row: any) => {
                        const label = ev || row?.event || '-';
                        return <Tag color={eventColor(label)}>{label}</Tag>;
                      },
                    },
                    { title: t('Account'), dataIndex: 'username', width: 95 },
                    { title: t('IP Address'), dataIndex: 'ip', width: 125 },
                    { title: t('Details'), dataIndex: 'detail', width: 205, ellipsis: true },
                  ]}
                />
              </div>
            ),
          },
          ...(!isDesktop && effectiveRole === 'admin'
            ? [
                {
                  key: 'branding',
                  label: (
                    <Space size={6}>
                      <BgColorsOutlined />
                      <span>{t('Login Page')}</span>
                    </Space>
                  ),
                  children: (
                    <div
                      style={{
                        display: 'grid',
                        gridTemplateColumns: isMobile ? '1fr' : 'minmax(280px, 1.15fr) minmax(240px, 0.85fr)',
                        gap: 16,
                        alignItems: 'start',
                      }}
                    >
                      <Card
                        size="small"
                        loading={brandingLoading}
                        title={t('Login Page Branding & Title')}
                      >
                        <Space direction="vertical" size={12} style={{ width: '100%' }}>
                          <div>
                            <Typography.Text strong style={{ display: 'block', marginBottom: 6, fontSize: 13 }}>
                              {t('Top Emblem Style')}
                            </Typography.Text>
                            <div className="dbharbor-no-scrollbar" style={{ overflowX: 'auto', maxWidth: '100%' }}>
                              <Segmented
                                block={!isMobile}
                                size={isMobile ? 'middle' : 'small'}
                                value={brandingForm.iconMode}
                                onChange={(val) =>
                                  setBrandingForm((prev) => ({ ...prev, iconMode: val as LoginIconMode }))
                                }
                                options={[
                                  { value: 'shield', label: t('Shield') },
                                  { value: 'database', label: t('Database') },
                                  { value: 'brand', label: t('App Logo') },
                                  { value: 'custom', label: t('Custom') },
                                  { value: 'none', label: t('Hidden') },
                                ]}
                              />
                            </div>
                          </div>

                          {brandingForm.iconMode === 'custom' && (
                            <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
                              <input
                                ref={brandingIconInputRef}
                                type="file"
                                accept="image/png,image/jpeg,image/webp,image/gif,image/svg+xml"
                                style={{ display: 'none' }}
                                onChange={handleBrandingFileSelect}
                              />
                              <Button
                                icon={<UploadOutlined />}
                                onClick={() => brandingIconInputRef.current?.click()}
                              >
                                {t('Upload Icon (<= 128 KB)')}
                              </Button>
                              {brandingForm.customIconDataUrl && (
                                <Button
                                  danger
                                  type="text"
                                  icon={<DeleteOutlined />}
                                  onClick={() =>
                                    setBrandingForm((prev) => ({ ...prev, customIconDataUrl: '' }))
                                  }
                                >
                                  {t('Clear')}
                                </Button>
                              )}
                            </div>
                          )}

                          <div>
                            <Typography.Text strong style={{ display: 'block', marginBottom: 4, fontSize: 13 }}>
                              {t('Browser Tab Title')}
                            </Typography.Text>
                            <Input
                              maxLength={80}
                              allowClear
                              placeholder={t('Default: Sign In')}
                              value={brandingForm.pageTitle}
                              onChange={(e) =>
                                setBrandingForm((prev) => ({ ...prev, pageTitle: e.target.value }))
                              }
                            />
                          </div>

                          <div>
                            <Typography.Text strong style={{ display: 'block', marginBottom: 4, fontSize: 13 }}>
                              {t('Heading Below Icon')}
                            </Typography.Text>
                            <Input
                              maxLength={80}
                              allowClear
                              placeholder={t('Default: Sign In')}
                              value={brandingForm.heading}
                              onChange={(e) =>
                                setBrandingForm((prev) => ({ ...prev, heading: e.target.value }))
                              }
                            />
                          </div>

                          <div>
                            <Typography.Text strong style={{ display: 'block', marginBottom: 4, fontSize: 13 }}>
                              {t('Subtitle Below Heading (Optional)')}
                            </Typography.Text>
                            <Input
                              maxLength={160}
                              allowClear
                              placeholder={t('Optional subtitle below heading')}
                              value={brandingForm.subtitle}
                              onChange={(e) =>
                                setBrandingForm((prev) => ({ ...prev, subtitle: e.target.value }))
                              }
                            />
                          </div>

                          <Space wrap style={{ paddingTop: 4 }}>
                            <Button
                              type="primary"
                              loading={brandingSaving}
                              onClick={() => handleSaveLoginBranding(brandingForm)}
                            >
                              {t('Save Appearance')}
                            </Button>
                            <Button
                              disabled={brandingSaving}
                              onClick={() => handleSaveLoginBranding(DEFAULT_LOGIN_BRANDING)}
                            >
                              {t('Reset to Default')}
                            </Button>
                          </Space>
                        </Space>
                      </Card>

                      <Card size="small" title={t('Live Sign-In Card Preview')}>
                        <div
                          style={{
                            padding: '22px 18px',
                            borderRadius: 14,
                            textAlign: 'center',
                            background: darkMode ? '#0b1120' : '#f4f6fb',
                            border: darkMode
                              ? '1px solid rgba(255,255,255,0.08)'
                              : '1px solid rgba(15,23,42,0.08)',
                          }}
                        >
                          <div
                            style={{
                              fontSize: 11,
                              opacity: 0.65,
                              marginBottom: 14,
                              padding: '3px 8px',
                              borderRadius: 6,
                              display: 'inline-block',
                              background: darkMode ? 'rgba(255,255,255,0.06)' : 'rgba(15,23,42,0.05)',
                            }}
                          >
                            {t('Tab')}: {brandingForm.pageTitle.trim() || t('Sign In')}
                          </div>
                          {brandingForm.iconMode !== 'none' && (
                            <div style={{ marginBottom: 12 }}>
                              <LoginBrandEmblem
                                iconMode={brandingForm.iconMode}
                                customIconDataUrl={brandingForm.customIconDataUrl}
                                size={48}
                                darkMode={darkMode}
                              />
                            </div>
                          )}
                          <Typography.Title level={4} style={{ margin: 0, fontSize: 19 }}>
                            {brandingForm.heading.trim() || t('Sign In')}
                          </Typography.Title>
                          {brandingForm.subtitle.trim() && (
                            <Typography.Text
                              type="secondary"
                              style={{ display: 'block', marginTop: 4, fontSize: 12.5 }}
                            >
                              {brandingForm.subtitle.trim()}
                            </Typography.Text>
                          )}

                          <div
                            style={{
                              marginTop: 18,
                              display: 'flex',
                              flexDirection: 'column',
                              gap: 8,
                              textAlign: 'left',
                            }}
                          >
                            <div
                              style={{
                                height: 28,
                                borderRadius: 7,
                                border: darkMode ? '1px solid rgba(255,255,255,0.1)' : '1px solid rgba(15,23,42,0.1)',
                                background: darkMode ? 'rgba(255,255,255,0.03)' : '#ffffff',
                              }}
                            />
                            <div
                              style={{
                                height: 28,
                                borderRadius: 7,
                                border: darkMode ? '1px solid rgba(255,255,255,0.1)' : '1px solid rgba(15,23,42,0.1)',
                                background: darkMode ? 'rgba(255,255,255,0.03)' : '#ffffff',
                              }}
                            />
                            <div style={{ display: 'flex', justifyContent: 'flex-end', fontSize: 10.5, opacity: 0.6 }}>
                              ✓ {t('Remember me (7 days)')}
                            </div>
                            <div
                              style={{
                                height: 30,
                                borderRadius: 7,
                                background: '#1677ff',
                                color: '#ffffff',
                                fontSize: 12,
                                fontWeight: 600,
                                display: 'flex',
                                alignItems: 'center',
                                justifyContent: 'center',
                              }}
                            >
                              {t('Sign In')}
                            </div>
                          </div>
                        </div>
                      </Card>
                    </div>
                  ),
                },
              ]
            : []),
        ]}
      />
    </Modal>
  );
};
