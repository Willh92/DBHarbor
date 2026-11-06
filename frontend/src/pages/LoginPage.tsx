import React, { useEffect, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Checkbox,
  Form,
  Input,
  Space,
  Tag,
  Tooltip,
  Typography,
  message,
} from 'antd';
import {
  ArrowLeftOutlined,
  GithubOutlined,
  KeyOutlined,
  LockOutlined,
  MoonOutlined,
  ReloadOutlined,
  SafetyCertificateOutlined,
  SunOutlined,
  UserOutlined,
} from '@ant-design/icons';
import { ApiError, api } from '../api/client';
import { BrandLogo } from '../components/common/BrandLogo';
import { LanguageSwitcher } from '../components/common/LanguageSwitcher';
import { useResponsive } from '../hooks/useResponsive';
import { useI18n } from '../i18n/useI18n';
import { useWorkbenchStore } from '../store/useWorkbenchStore';

const ENGINE_PILLS = [
  { name: 'ClickHouse', color: 'gold' },
  { name: 'MySQL / MariaDB', color: 'blue' },
  { name: 'PostgreSQL', color: 'geekblue' },
  { name: 'SQLite / DuckDB', color: 'cyan' },
  { name: 'Redis', color: 'red' },
  { name: 'MongoDB', color: 'green' },
  { name: 'SQL Server / Oracle', color: 'volcano' },
  { name: 'TiDB / OceanBase / Dameng DM8 / KingbaseES', color: 'purple' },
];

export const LoginPage: React.FC = () => {
  const [loading, setLoading] = useState(false);
  const { setAuth, darkMode, toggleDarkMode } = useWorkbenchStore();
  const { isMobile } = useResponsive();
  const { t } = useI18n();
  const [form] = Form.useForm();

  // Anti-bruteforce & Captcha state
  const [requiresCaptcha, setRequiresCaptcha] = useState(false);
  const [captchaToken, setCaptchaToken] = useState('');
  const [captchaImage, setCaptchaImage] = useState('');
  const [remainingAttempts, setRemainingAttempts] = useState<number | null>(null);
  const [lockSeconds, setLockSeconds] = useState<number>(0);
  const [errorBanner, setErrorBanner] = useState<string>('');

  // 2FA Challenge step state
  const [challengeToken, setChallengeToken] = useState<string>('');
  const [challengeUsername, setChallengeUsername] = useState<string>('');
  const [rememberMeCached, setRememberMeCached] = useState<boolean>(false);
  const [totpCode, setTotpCode] = useState<string>('');
  const [appVersion, setAppVersion] = useState<string>('v1.0.0');
  const [repoUrl, setRepoUrl] = useState<string>('https://github.com/Willh92/DBHarbor');

  useEffect(() => {
    api.getVersion()
      .then((v) => {
        if (v.displayVersion) setAppVersion(v.displayVersion);
        if (v.repoUrl) setRepoUrl(v.repoUrl);
      })
      .catch(() => {});
  }, []);

  useEffect(() => {
    if (lockSeconds <= 0) return;
    const timer = setInterval(() => {
      setLockSeconds((prev) => (prev > 1 ? prev - 1 : 0));
    }, 1000);
    return () => clearInterval(timer);
  }, [lockSeconds]);

  const refreshCaptcha = async () => {
    try {
      const cap = await api.getCaptcha();
      setCaptchaToken(cap.captchaToken);
      setCaptchaImage(cap.captchaImage);
      form.setFieldValue('captchaAnswer', '');
    } catch (err: any) {
      message.error(err.message || t('Failed to refresh captcha'));
    }
  };

  const handleStep1Finish = async (values: {
    username: string;
    password: string;
    captchaAnswer?: string;
    rememberMe?: boolean;
  }) => {
    if (lockSeconds > 0) return;
    setLoading(true);
    setErrorBanner('');
    try {
      const res = await api.login({
        username: values.username.trim(),
        password: values.password,
        captchaToken: requiresCaptcha ? captchaToken : undefined,
        captchaAnswer: requiresCaptcha ? (values.captchaAnswer || '').trim() : undefined,
        rememberMe: Boolean(values.rememberMe),
      });

      if (res.requires2FA && res.challengeToken) {
        setChallengeToken(res.challengeToken);
        setChallengeUsername(res.username);
        setRememberMeCached(Boolean(values.rememberMe));
        setTotpCode('');
        setRemainingAttempts(null);
        return;
      }

      if (res.token) {
        setAuth(res.token, res.username, {
          displayName: res.displayName,
          role: res.role,
          canCreateConn: res.canCreateConn,
          totpEnabled: res.totpEnabled,
          passwordChanged: res.passwordChanged,
          recoveryCodesRemaining: res.recoveryCodesRemaining,
        });
        message.success(t('Welcome back, {user}!', { user: res.displayName || res.username }));
      }
    } catch (err: any) {
      if (err instanceof ApiError) {
        const d = err.data || {};
        if (d.locked || err.status === 429) {
          setLockSeconds(Number(d.retryAfterSec) || 60);
          setRemainingAttempts(0);
        } else if (typeof d.remainingAttempts === 'number') {
          setRemainingAttempts(d.remainingAttempts);
        }
        if (d.requiresCaptcha) {
          setRequiresCaptcha(true);
          if (d.captchaToken && d.captchaImage) {
            setCaptchaToken(d.captchaToken);
            setCaptchaImage(d.captchaImage);
            form.setFieldValue('captchaAnswer', '');
          } else {
            refreshCaptcha();
          }
        }
        setErrorBanner(err.message);
      } else {
        setErrorBanner(err?.message || t('Sign-in failed'));
      }
    } finally {
      setLoading(false);
    }
  };

  const handleVerify2FA = async () => {
    const clean = totpCode.trim();
    if (!clean) {
      message.warning(t('Please enter a 6-digit TOTP code or 8-character recovery code'));
      return;
    }
    setLoading(true);
    setErrorBanner('');
    try {
      const res = await api.verify2FALogin({
        challengeToken,
        code: clean,
        rememberMe: rememberMeCached,
      });
      if (res.token) {
        setAuth(res.token, res.username, {
          displayName: res.displayName,
          role: res.role,
          canCreateConn: res.canCreateConn,
          totpEnabled: true,
          passwordChanged: res.passwordChanged,
          recoveryCodesRemaining: res.recoveryCodesRemaining,
        });
        if (res.usedRecoveryCode) {
          message.warning(
            t('Signed in with a one-time recovery code ({count} remaining). Please regenerate codes in Security Center.', {
              count: res.recoveryCodesRemaining ?? 0,
            })
          );
        } else {
          message.success(t('2FA verified. Welcome back, {user}!', { user: res.username }));
        }
      }
    } catch (err: any) {
      if (err instanceof ApiError) {
        if (err.data?.locked || err.status === 429) {
          setLockSeconds(Number(err.data?.retryAfterSec) || 60);
        }
        if (typeof err.data?.remainingAttempts === 'number') {
          setRemainingAttempts(err.data.remainingAttempts);
        }
      }
      setErrorBanner(err?.message || t('2FA verification failed'));
    } finally {
      setLoading(false);
    }
  };

  return (
    <div
      style={{
        minHeight: '100dvh',
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        justifyContent: 'center',
        padding: isMobile ? '52px 12px 16px' : 24,
        background: darkMode
          ? 'radial-gradient(circle at 20% 20%, #111d2c 0%, #0a0f18 70%)'
          : 'radial-gradient(circle at 20% 20%, #e6f4ff 0%, #f5f7fa 70%)',
      }}
    >
      <div style={{ position: 'fixed', top: isMobile ? 10 : 20, right: isMobile ? 12 : 24, zIndex: 10 }}>
        <Space size={8}>
          <LanguageSwitcher size="middle" />
          <Button
            shape="circle"
            icon={darkMode ? <SunOutlined /> : <MoonOutlined />}
            onClick={toggleDarkMode}
            title={t('Toggle Theme')}
          />
        </Space>
      </div>

      <Card
        style={{
          width: '100%',
          maxWidth: 460,
          borderRadius: 16,
          boxShadow: darkMode ? '0 16px 48px rgba(0,0,0,0.55)' : '0 16px 48px rgba(22,119,255,0.10)',
        }}
      >
        <div style={{ textAlign: 'center', marginBottom: 20 }}>
          <div style={{ marginBottom: 12 }}>
            <BrandLogo size={62} glow />
          </div>
          <div style={{ display: 'flex', justifyContent: 'center', alignItems: 'center', flexWrap: 'wrap', gap: 8 }}>
            <Typography.Title level={3} style={{ margin: 0, fontSize: isMobile ? 19 : 24 }}>
              {t('DBHarbor Data Workbench')}
            </Typography.Title>
            <Tag color="geekblue" style={{ margin: 0 }}>
              {appVersion}
            </Tag>
          </div>
          <Typography.Text type="secondary" style={{ display: 'block', marginTop: 4 }}>
            {t('Cross-Platform Multi-Model Web Database Workbench (RDBMS · OLAP · KV · Document)')}
          </Typography.Text>
        </div>

        {lockSeconds > 0 && (
          <Alert
            type="error"
            showIcon
            style={{ marginBottom: 14 }}
            message={t('Brute-force protection active: retry in {sec}s', { sec: lockSeconds })}
            description={t(
              'Too many failed login attempts detected from this IP or account. Temporary lockout is active.'
            )}
          />
        )}

        {lockSeconds === 0 && errorBanner && (
          <Alert
            type="warning"
            showIcon
            closable
            onClose={() => setErrorBanner('')}
            style={{ marginBottom: 14 }}
            message={errorBanner}
            description={
              remainingAttempts !== null && remainingAttempts > 0
                ? t('Remaining attempts: {count} (15-minute lockout upon exhaustion)', { count: remainingAttempts })
                : undefined
            }
          />
        )}

        {!challengeToken ? (
          <Form
            form={form}
            layout="vertical"
            initialValues={{ username: 'admin', password: '', rememberMe: false }}
            onFinish={handleStep1Finish}
          >
            <Form.Item
              name="username"
              label={t('Admin Username')}
              rules={[{ required: true, message: t('Please enter username') }]}
            >
              <Input size="large" prefix={<UserOutlined />} placeholder="admin" autoFocus disabled={lockSeconds > 0} />
            </Form.Item>
            <Form.Item
              name="password"
              label={t('Password')}
              rules={[{ required: true, message: t('Please enter password') }]}
            >
              <Input.Password
                size="large"
                prefix={<LockOutlined />}
                placeholder={t('Default password: admin123')}
                disabled={lockSeconds > 0}
              />
            </Form.Item>

            {requiresCaptcha && (
              <Form.Item
                name="captchaAnswer"
                label={t('Security Captcha')}
                rules={[{ required: true, message: t('Please enter the captcha characters') }]}
              >
                <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                  <Input
                    size="large"
                    prefix={<SafetyCertificateOutlined />}
                    placeholder={t('Enter 5-char code')}
                    maxLength={8}
                    style={{ flex: 1 }}
                    disabled={lockSeconds > 0}
                  />
                  <Tooltip title={t('Click to refresh captcha')}>
                    <div
                      onClick={refreshCaptcha}
                      style={{
                        height: 40,
                        width: 132,
                        borderRadius: 6,
                        overflow: 'hidden',
                        cursor: 'pointer',
                        border: '1px solid rgba(128,128,128,0.3)',
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'center',
                        flexShrink: 0,
                      }}
                    >
                      {captchaImage ? (
                        <img
                          src={captchaImage}
                          alt="captcha"
                          style={{ width: '100%', height: '100%', objectFit: 'cover' }}
                        />
                      ) : (
                        <ReloadOutlined />
                      )}
                    </div>
                  </Tooltip>
                </div>
              </Form.Item>
            )}

            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 6, marginBottom: 12 }}>
              <Form.Item name="rememberMe" valuePropName="checked" noStyle>
                <Checkbox disabled={lockSeconds > 0} style={{ whiteSpace: 'nowrap' }}>
                  {t('Remember me (7 days)')}
                </Checkbox>
              </Form.Item>
              <Space size={4} style={{ whiteSpace: 'nowrap' }}>
                <SafetyCertificateOutlined style={{ color: '#52c41a', fontSize: 13 }} />
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  {t('Rate-Limit & TOTP 2FA Protected')}
                </Typography.Text>
              </Space>
            </div>

            <Button
              type="primary"
              htmlType="submit"
              size="large"
              block
              loading={loading}
              disabled={lockSeconds > 0}
            >
              {t('Sign In to Workbench')}
            </Button>
          </Form>
        ) : (
          <div>
            <Alert
              type="info"
              showIcon
              icon={<SafetyCertificateOutlined />}
              style={{ marginBottom: 16 }}
              message={t('Two-Factor Authentication (2FA) is enabled for {user}', { user: challengeUsername })}
              description={t(
                'Enter the 6-digit TOTP code from your authenticator app (Google / Microsoft Authenticator, 1Password, etc.) or an XXXX-XXXX backup recovery code.'
              )}
            />

            <div style={{ marginBottom: 16 }}>
              <Typography.Text strong style={{ display: 'block', marginBottom: 6 }}>
                {t('6-Digit TOTP Code / Recovery Code')}
              </Typography.Text>
              <Input
                size="large"
                autoFocus
                prefix={<KeyOutlined />}
                placeholder={t('e.g. 123456 or ABCD-EFGH')}
                value={totpCode}
                maxLength={12}
                disabled={lockSeconds > 0}
                style={{ letterSpacing: 2, fontWeight: 600 }}
                onChange={(e) => setTotpCode(e.target.value)}
                onPressEnter={handleVerify2FA}
              />
            </div>

            <Space direction="vertical" style={{ width: '100%' }} size={10}>
              <Button
                type="primary"
                size="large"
                block
                loading={loading}
                disabled={lockSeconds > 0}
                onClick={handleVerify2FA}
              >
                {t('Verify & Sign In')}
              </Button>
              <Button
                block
                icon={<ArrowLeftOutlined />}
                onClick={() => {
                  setChallengeToken('');
                  setTotpCode('');
                  setErrorBanner('');
                }}
              >
                {t('Back to Username & Password')}
              </Button>
            </Space>
          </div>
        )}

        <div style={{ marginTop: 22, paddingTop: 16, borderTop: '1px solid rgba(128,128,128,0.15)' }}>
          <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 8 }}>
            {t('Natively supports 14 database engines:')}
          </Typography.Text>
          <Space size={[4, 6]} wrap>
            {ENGINE_PILLS.map((p) => (
              <Tag key={p.name} color={p.color} style={{ margin: 0 }}>
                {t(p.name)}
              </Tag>
            ))}
          </Space>
          <div style={{ marginTop: 12, display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t('Current Version: {ver}', { ver: appVersion })}
            </Typography.Text>
            <Typography.Link href={repoUrl} target="_blank" rel="noreferrer" style={{ fontSize: 12 }}>
              <GithubOutlined /> GitHub: Willh92/DBHarbor
            </Typography.Link>
          </div>
        </div>
      </Card>
    </div>
  );
};
