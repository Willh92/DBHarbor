import React, { useEffect, useRef, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Checkbox,
  Collapse,
  Form,
  Input,
  Progress,
  Segmented,
  Space,
  Spin,
  Tooltip,
  Typography,
  message,
} from 'antd';
import {
  ArrowLeftOutlined,
  BgColorsOutlined,
  DeleteOutlined,
  KeyOutlined,
  LockOutlined,
  MoonOutlined,
  ReloadOutlined,
  SafetyCertificateOutlined,
  SunOutlined,
  UploadOutlined,
  UserOutlined,
} from '@ant-design/icons';
import { ApiError, api } from '../api/client';
import { LanguageSwitcher } from '../components/common/LanguageSwitcher';
import {
  DEFAULT_LOGIN_BRANDING,
  LoginBrandEmblem,
  readBrandingImageAsDataUrl,
} from '../components/common/LoginBrandEmblem';
import { useResponsive } from '../hooks/useResponsive';
import { useI18n } from '../i18n/useI18n';
import { useWorkbenchStore } from '../store/useWorkbenchStore';
import type { LoginBrandingConfig, LoginIconMode } from '../types';

function evalSetupPasswordStrength(pwd: string): { percent: number; label: string; color: string } {
  if (!pwd) return { percent: 0, label: 'Empty', color: '#d9d9d9' };
  let score = 0;
  if (pwd.length >= 8) score += 30;
  if (pwd.length >= 12) score += 20;
  if (/[A-Z]/.test(pwd) && /[a-z]/.test(pwd)) score += 25;
  if (/\d/.test(pwd) && /[^A-Za-z0-9]/.test(pwd)) score += 25;
  else if (/\d/.test(pwd) || /[^A-Za-z0-9]/.test(pwd)) score += 15;
  const pct = Math.min(100, score);
  if (pct >= 80) return { percent: pct, label: 'Strong', color: '#52c41a' };
  if (pct >= 50) return { percent: pct, label: 'Medium', color: '#faad14' };
  return { percent: pct, label: 'Weak', color: '#ff4d4f' };
}

export const LoginPage: React.FC = () => {
  const { setAuth, loginBranding: storeBranding, setLoginBranding, darkMode, toggleDarkMode } = useWorkbenchStore();
  const { isMobile } = useResponsive();
  const { t } = useI18n();

  const [statusLoading, setStatusLoading] = useState(true);
  const [needsSetup, setNeedsSetup] = useState(false);
  const [branding, setBranding] = useState<LoginBrandingConfig>(storeBranding || DEFAULT_LOGIN_BRANDING);

  const [loading, setLoading] = useState(false);
  const [loginForm] = Form.useForm();
  const [setupForm] = Form.useForm();
  const [setupPwdWatch, setSetupPwdWatch] = useState('');
  const [setupBranding, setSetupBranding] = useState<LoginBrandingConfig>(DEFAULT_LOGIN_BRANDING);
  const setupIconInputRef = useRef<HTMLInputElement | null>(null);

  // Anti-bruteforce & Captcha state
  const [requiresCaptcha, setRequiresCaptcha] = useState(false);
  const [captchaToken, setCaptchaToken] = useState('');
  const [captchaImage, setCaptchaImage] = useState('');
  const [lockSeconds, setLockSeconds] = useState<number>(0);
  const [errorBanner, setErrorBanner] = useState<string>('');

  // 2FA Challenge step state
  const [challengeToken, setChallengeToken] = useState<string>('');
  const [rememberMeCached, setRememberMeCached] = useState<boolean>(false);
  const [totpCode, setTotpCode] = useState<string>('');

  useEffect(() => {
    let active = true;
    api
      .getAuthStatus()
      .then((st) => {
        if (!active) return;
        setNeedsSetup(Boolean(st.needsSetup));
        if (st.branding) {
          setBranding(st.branding);
          setLoginBranding(st.branding);
        }
      })
      .catch(() => {
        // Keep clean sign-in fallback if network transiently fails
      })
      .finally(() => {
        if (active) setStatusLoading(false);
      });
    return () => {
      active = false;
    };
  }, [setLoginBranding]);

  // Sync browser <title> and <link rel="icon"> with active login branding
  const effectiveBranding = needsSetup ? setupBranding : branding;
  useEffect(() => {
    const customTitle = effectiveBranding.pageTitle?.trim();
    document.title = customTitle || (needsSetup ? t('Initial Administrator Setup') : t('Sign In'));

    const iconLink = document.querySelector('link[rel="icon"]') as HTMLLinkElement | null;
    if (iconLink) {
      if (effectiveBranding.iconMode === 'custom' && effectiveBranding.customIconDataUrl) {
        const mimeMatch = /^data:(image\/[a-zA-Z0-9+.-]+);base64,/i.exec(effectiveBranding.customIconDataUrl);
        iconLink.type = mimeMatch?.[1] || 'image/png';
        iconLink.href = effectiveBranding.customIconDataUrl;
      } else {
        iconLink.type = 'image/svg+xml';
        iconLink.href = '/favicon.svg';
      }
    }
  }, [effectiveBranding, needsSetup, t]);

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
      loginForm.setFieldValue('captchaAnswer', '');
    } catch (err: any) {
      message.error(err.message || t('Failed to refresh captcha'));
    }
  };

  const handleInitialSetupFinish = async (values: {
    username: string;
    displayName?: string;
    password: string;
    confirmPassword: string;
    rememberMe?: boolean;
  }) => {
    if (values.password !== values.confirmPassword) {
      setErrorBanner(t('New passwords do not match'));
      return;
    }
    setLoading(true);
    setErrorBanner('');
    try {
      const hasCustomBranding =
        Boolean(setupBranding.pageTitle.trim()) ||
        Boolean(setupBranding.heading.trim()) ||
        Boolean(setupBranding.subtitle.trim()) ||
        setupBranding.iconMode !== 'shield' ||
        Boolean(setupBranding.customIconDataUrl);

      const res = await api.setupInitialAdmin({
        username: values.username.trim(),
        displayName: (values.displayName || '').trim(),
        password: values.password,
        rememberMe: Boolean(values.rememberMe),
        branding: hasCustomBranding ? setupBranding : undefined,
      });

      if (res.branding) {
        setBranding(res.branding);
        setLoginBranding(res.branding);
      }
      setNeedsSetup(false);

      if (res.token) {
        const uname = res.username || values.username.trim();
        setAuth(res.token, uname, {
          displayName: res.displayName,
          role: res.role,
          canCreateConn: res.canCreateConn,
          totpEnabled: res.totpEnabled,
          passwordChanged: res.passwordChanged,
          recoveryCodesRemaining: res.recoveryCodesRemaining,
        });
        message.success(t('Administrator account initialized'));
      }
    } catch (err: any) {
      if (err instanceof ApiError && err.status === 403) {
        setNeedsSetup(false);
      }
      setErrorBanner(err?.message || t('Initialization failed'));
    } finally {
      setLoading(false);
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
        setRememberMeCached(Boolean(values.rememberMe));
        setTotpCode('');
        return;
      }

      if (res.token) {
        const uname = res.username || values.username.trim();
        setAuth(res.token, uname, {
          displayName: res.displayName,
          role: res.role,
          canCreateConn: res.canCreateConn,
          totpEnabled: res.totpEnabled,
          passwordChanged: res.passwordChanged,
          recoveryCodesRemaining: res.recoveryCodesRemaining,
        });
        message.success(t('Welcome back, {user}!', { user: res.displayName || uname }));
      }
    } catch (err: any) {
      if (err instanceof ApiError) {
        const d = err.data || {};
        if (err.status === 403) {
          // Setup not completed yet -> reload setup mode
          api
            .getAuthStatus()
            .then((st) => {
              if (st.needsSetup) setNeedsSetup(true);
            })
            .catch(() => {});
        }
        if (d.locked || err.status === 429) {
          setLockSeconds(Number(d.retryAfterSec) || 60);
        }
        if (d.requiresCaptcha) {
          setRequiresCaptcha(true);
          if (d.captchaToken && d.captchaImage) {
            setCaptchaToken(d.captchaToken);
            setCaptchaImage(d.captchaImage);
            loginForm.setFieldValue('captchaAnswer', '');
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
        const uname = res.username || '';
        setAuth(res.token, uname, {
          displayName: res.displayName,
          role: res.role,
          canCreateConn: res.canCreateConn,
          totpEnabled: true,
          passwordChanged: res.passwordChanged,
          recoveryCodesRemaining: res.recoveryCodesRemaining,
        });
        if (res.usedRecoveryCode) {
          message.warning(
            t(
              'Signed in with a one-time recovery code ({count} remaining). Please regenerate codes in Security Center.',
              { count: res.recoveryCodesRemaining ?? 0 }
            )
          );
        } else {
          message.success(t('2FA verified. Welcome back, {user}!', { user: res.displayName || uname }));
        }
      }
    } catch (err: any) {
      if (err instanceof ApiError && (err.data?.locked || err.status === 429)) {
        setLockSeconds(Number(err.data?.retryAfterSec) || 60);
      }
      setErrorBanner(err?.message || t('2FA verification failed'));
    } finally {
      setLoading(false);
    }
  };

  const handleSetupIconFileChange = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = '';
    if (!file) return;
    try {
      const dataUrl = await readBrandingImageAsDataUrl(file);
      setSetupBranding((prev) => ({ ...prev, iconMode: 'custom', customIconDataUrl: dataUrl }));
    } catch (err: any) {
      message.error(t(err.message || 'Failed to read image file'));
    }
  };

  const setupStrength = evalSetupPasswordStrength(setupPwdWatch);
  const resolvedHeading = needsSetup
    ? setupBranding.heading.trim() || t('Initial Administrator Setup')
    : branding.heading.trim() || t('Sign In');
  const resolvedSubtitle = needsSetup
    ? setupBranding.subtitle.trim() || t('Create the primary administrator account to initialize this workspace.')
    : branding.subtitle.trim();

  const inputHeightStyle: React.CSSProperties = { height: isMobile ? 44 : 42, borderRadius: 10 };
  const primaryBtnStyle: React.CSSProperties = {
    height: 44,
    borderRadius: 10,
    fontWeight: 600,
    fontSize: 15,
  };

  return (
    <div
      style={{
        minHeight: '100dvh',
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        justifyContent: 'center',
        padding: isMobile ? '60px 14px 24px' : '32px 24px',
        position: 'relative',
        overflow: 'hidden',
        background: darkMode
          ? 'radial-gradient(900px circle at 15% 15%, rgba(37, 99, 235, 0.14) 0%, transparent 55%), radial-gradient(800px circle at 85% 85%, rgba(13, 148, 136, 0.10) 0%, transparent 55%), #090d16'
          : 'radial-gradient(900px circle at 15% 15%, rgba(37, 99, 235, 0.08) 0%, transparent 55%), radial-gradient(800px circle at 85% 85%, rgba(13, 148, 136, 0.06) 0%, transparent 55%), #f4f6fb',
      }}
    >
      <div style={{ position: 'fixed', top: isMobile ? 12 : 20, right: isMobile ? 12 : 24, zIndex: 10 }}>
        <Space size={8}>
          <LanguageSwitcher size="middle" />
          <Button
            shape="circle"
            style={{ width: 38, height: 38 }}
            icon={darkMode ? <SunOutlined /> : <MoonOutlined />}
            onClick={toggleDarkMode}
            title={t('Toggle Theme')}
          />
        </Space>
      </div>

      <Card
        style={{
          width: '100%',
          maxWidth: needsSetup ? 476 : 412,
          borderRadius: 18,
          border: darkMode ? '1px solid rgba(255,255,255,0.08)' : '1px solid rgba(15,23,42,0.07)',
          background: darkMode ? 'rgba(17, 24, 39, 0.88)' : 'rgba(255, 255, 255, 0.96)',
          backdropFilter: 'blur(14px)',
          boxShadow: darkMode
            ? '0 24px 60px rgba(0, 0, 0, 0.55)'
            : '0 20px 50px rgba(15, 23, 42, 0.08)',
        }}
        styles={{
          body: {
            padding: isMobile ? '26px 20px 24px' : '32px 30px 28px',
          },
        }}
      >
        {statusLoading ? (
          <div style={{ padding: '48px 0', textAlign: 'center' }}>
            <Spin size="large" />
          </div>
        ) : (
          <>
            <div style={{ textAlign: 'center', marginBottom: 24 }}>
              {effectiveBranding.iconMode !== 'none' && (
                <div style={{ marginBottom: 14 }}>
                  <LoginBrandEmblem
                    iconMode={effectiveBranding.iconMode}
                    customIconDataUrl={effectiveBranding.customIconDataUrl}
                    size={54}
                    darkMode={darkMode}
                  />
                </div>
              )}
              <Typography.Title
                level={3}
                style={{
                  margin: 0,
                  fontSize: isMobile ? 21 : 23,
                  fontWeight: 700,
                  letterSpacing: -0.3,
                }}
              >
                {resolvedHeading}
              </Typography.Title>
              {resolvedSubtitle && (
                <Typography.Text
                  type="secondary"
                  style={{ display: 'block', marginTop: 6, fontSize: 13.5, lineHeight: 1.5 }}
                >
                  {resolvedSubtitle}
                </Typography.Text>
              )}
            </div>

            {lockSeconds > 0 && (
              <Alert
                type="error"
                showIcon
                style={{ marginBottom: 16, borderRadius: 10 }}
                message={t('Temporary lockout active: retry in {sec}s', { sec: lockSeconds })}
                description={t('Too many failed verification attempts. Please wait before trying again.')}
              />
            )}

            {lockSeconds === 0 && errorBanner && (
              <Alert
                type="warning"
                showIcon
                closable
                onClose={() => setErrorBanner('')}
                style={{ marginBottom: 16, borderRadius: 10 }}
                message={errorBanner}
              />
            )}

            {needsSetup ? (
              <Form
                form={setupForm}
                layout="vertical"
                initialValues={{ username: '', displayName: '', password: '', confirmPassword: '', rememberMe: false }}
                onFinish={handleInitialSetupFinish}
                requiredMark={false}
              >
                <Form.Item
                  name="username"
                  label={t('Administrator Username')}
                  rules={[
                    { required: true, message: t('Please enter username') },
                    {
                      pattern: /^[a-zA-Z0-9._@-]{2,64}$/,
                      message: t('2-64 chars: letters, digits, ., _, -, @'),
                    },
                  ]}
                  style={{ marginBottom: 14 }}
                >
                  <Input
                    size="large"
                    autoFocus
                    autoComplete="username"
                    prefix={<UserOutlined style={{ opacity: 0.65 }} />}
                    placeholder={t('Enter administrator username')}
                    style={inputHeightStyle}
                  />
                </Form.Item>

                <Form.Item name="displayName" label={t('Display Name (Optional)')} style={{ marginBottom: 14 }}>
                  <Input
                    size="large"
                    maxLength={64}
                    placeholder={t('Optional display name')}
                    style={inputHeightStyle}
                  />
                </Form.Item>

                <Form.Item
                  name="password"
                  label={t('Administrator Password')}
                  rules={[
                    { required: true, message: t('Please enter password') },
                    { min: 8, message: t('Password must be at least 8 characters') },
                  ]}
                  style={{ marginBottom: setupPwdWatch ? 6 : 14 }}
                >
                  <Input.Password
                    size="large"
                    autoComplete="new-password"
                    prefix={<LockOutlined style={{ opacity: 0.65 }} />}
                    placeholder={t('At least 8 characters')}
                    style={inputHeightStyle}
                    onChange={(e) => setSetupPwdWatch(e.target.value)}
                  />
                </Form.Item>

                {setupPwdWatch && (
                  <div style={{ marginBottom: 12, display: 'flex', alignItems: 'center', gap: 10 }}>
                    <Progress
                      percent={setupStrength.percent}
                      strokeColor={setupStrength.color}
                      showInfo={false}
                      size="small"
                      style={{ flex: 1, margin: 0 }}
                    />
                    <Typography.Text type="secondary" style={{ fontSize: 12, flexShrink: 0 }}>
                      {t(setupStrength.label)}
                    </Typography.Text>
                  </div>
                )}

                <Form.Item
                  name="confirmPassword"
                  label={t('Confirm Password')}
                  dependencies={['password']}
                  rules={[
                    { required: true, message: t('Please confirm password') },
                    ({ getFieldValue }) => ({
                      validator(_, value) {
                        if (!value || getFieldValue('password') === value) {
                          return Promise.resolve();
                        }
                        return Promise.reject(new Error(t('New passwords do not match')));
                      },
                    }),
                  ]}
                  style={{ marginBottom: 12 }}
                >
                  <Input.Password
                    size="large"
                    autoComplete="new-password"
                    prefix={<LockOutlined style={{ opacity: 0.65 }} />}
                    placeholder={t('Re-enter administrator password')}
                    style={inputHeightStyle}
                  />
                </Form.Item>

                <Collapse
                  ghost
                  size="small"
                  style={{ marginBottom: 14 }}
                  items={[
                    {
                      key: 'branding',
                      label: (
                        <Space size={6}>
                          <BgColorsOutlined />
                          <span style={{ fontSize: 13 }}>{t('Customize Login Page Appearance (Optional)')}</span>
                        </Space>
                      ),
                      children: (
                        <Space direction="vertical" size={10} style={{ width: '100%', paddingTop: 4 }}>
                          <div>
                            <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 6 }}>
                              {t('Top Emblem Style')}
                            </Typography.Text>
                            <div className="dbharbor-no-scrollbar" style={{ overflowX: 'auto', maxWidth: '100%' }}>
                              <Segmented
                                block={!isMobile}
                                size={isMobile ? 'middle' : 'small'}
                                value={setupBranding.iconMode}
                                onChange={(val) =>
                                  setSetupBranding((prev) => ({ ...prev, iconMode: val as LoginIconMode }))
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

                          {setupBranding.iconMode === 'custom' && (
                            <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
                              <input
                                ref={setupIconInputRef}
                                type="file"
                                accept="image/png,image/jpeg,image/webp,image/gif,image/svg+xml"
                                style={{ display: 'none' }}
                                onChange={handleSetupIconFileChange}
                              />
                              <Button
                                icon={<UploadOutlined />}
                                onClick={() => setupIconInputRef.current?.click()}
                              >
                                {t('Upload Icon (<= 128 KB)')}
                              </Button>
                              {setupBranding.customIconDataUrl && (
                                <Button
                                  danger
                                  type="text"
                                  icon={<DeleteOutlined />}
                                  onClick={() =>
                                    setSetupBranding((prev) => ({ ...prev, customIconDataUrl: '' }))
                                  }
                                >
                                  {t('Clear')}
                                </Button>
                              )}
                            </div>
                          )}

                          <Input
                            maxLength={80}
                            placeholder={t('Browser Tab Title (default: Sign In)')}
                            value={setupBranding.pageTitle}
                            onChange={(e) =>
                              setSetupBranding((prev) => ({ ...prev, pageTitle: e.target.value }))
                            }
                          />
                          <Input
                            maxLength={80}
                            placeholder={t('Heading Below Icon (default: Sign In)')}
                            value={setupBranding.heading}
                            onChange={(e) =>
                              setSetupBranding((prev) => ({ ...prev, heading: e.target.value }))
                            }
                          />
                          <Input
                            maxLength={160}
                            placeholder={t('Optional subtitle below heading')}
                            value={setupBranding.subtitle}
                            onChange={(e) =>
                              setSetupBranding((prev) => ({ ...prev, subtitle: e.target.value }))
                            }
                          />
                        </Space>
                      ),
                    },
                  ]}
                />

                <div style={{ marginBottom: 16, display: 'flex', justifyContent: 'flex-end' }}>
                  <Form.Item name="rememberMe" valuePropName="checked" noStyle>
                    <Checkbox
                      className="dbharbor-checkbox-end"
                      style={{ minHeight: 32, display: 'inline-flex', alignItems: 'center' }}
                    >
                      {t('Remember me (7 days)')}
                    </Checkbox>
                  </Form.Item>
                </div>

                <Button
                  type="primary"
                  htmlType="submit"
                  size="large"
                  block
                  loading={loading}
                  style={primaryBtnStyle}
                >
                  {t('Create Administrator & Sign In')}
                </Button>
              </Form>
            ) : !challengeToken ? (
              <Form
                form={loginForm}
                layout="vertical"
                initialValues={{ username: '', password: '', rememberMe: false }}
                onFinish={handleStep1Finish}
                requiredMark={false}
              >
                <Form.Item
                  name="username"
                  label={t('Account')}
                  rules={[{ required: true, message: t('Please enter username') }]}
                  style={{ marginBottom: 16 }}
                >
                  <Input
                    size="large"
                    autoFocus
                    autoComplete="username"
                    prefix={<UserOutlined style={{ opacity: 0.65 }} />}
                    placeholder={t('Enter username')}
                    disabled={lockSeconds > 0}
                    style={inputHeightStyle}
                  />
                </Form.Item>

                <Form.Item
                  name="password"
                  label={t('Password')}
                  rules={[{ required: true, message: t('Please enter password') }]}
                  style={{ marginBottom: requiresCaptcha ? 16 : 10 }}
                >
                  <Input.Password
                    size="large"
                    autoComplete="current-password"
                    prefix={<LockOutlined style={{ opacity: 0.65 }} />}
                    placeholder={t('Enter password')}
                    disabled={lockSeconds > 0}
                    style={inputHeightStyle}
                  />
                </Form.Item>

                {requiresCaptcha && (
                  <Form.Item
                    name="captchaAnswer"
                    label={t('Verification Code')}
                    rules={[{ required: true, message: t('Please enter the verification characters') }]}
                    style={{ marginBottom: 10 }}
                  >
                    <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                      <Input
                        size="large"
                        prefix={<SafetyCertificateOutlined style={{ opacity: 0.65 }} />}
                        placeholder={t('Enter code')}
                        maxLength={8}
                        style={{ ...inputHeightStyle, flex: 1 }}
                        disabled={lockSeconds > 0}
                      />
                      <Tooltip title={t('Click to refresh captcha')}>
                        <div
                          onClick={refreshCaptcha}
                          style={{
                            height: isMobile ? 44 : 42,
                            width: 136,
                            borderRadius: 10,
                            overflow: 'hidden',
                            cursor: 'pointer',
                            border: '1px solid rgba(128,128,128,0.28)',
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

                <div style={{ marginBottom: 16, display: 'flex', justifyContent: 'flex-end' }}>
                  <Form.Item name="rememberMe" valuePropName="checked" noStyle>
                    <Checkbox
                      className="dbharbor-checkbox-end"
                      disabled={lockSeconds > 0}
                      style={{ minHeight: 34, display: 'inline-flex', alignItems: 'center' }}
                    >
                      {t('Remember me (7 days)')}
                    </Checkbox>
                  </Form.Item>
                </div>

                <Button
                  type="primary"
                  htmlType="submit"
                  size="large"
                  block
                  loading={loading}
                  disabled={lockSeconds > 0}
                  style={primaryBtnStyle}
                >
                  {t('Sign In')}
                </Button>
              </Form>
            ) : (
              <div>
                <Alert
                  type="info"
                  showIcon
                  icon={<SafetyCertificateOutlined />}
                  style={{ marginBottom: 18, borderRadius: 10 }}
                  message={t('Two-Factor Verification Required')}
                  description={t(
                    'Enter the 6-digit verification code from your authenticator app or a one-time recovery code.'
                  )}
                />

                <div style={{ marginBottom: 18 }}>
                  <Typography.Text strong style={{ display: 'block', marginBottom: 8 }}>
                    {t('Verification Code')}
                  </Typography.Text>
                  <Input
                    size="large"
                    autoFocus
                    autoComplete="one-time-code"
                    prefix={<KeyOutlined style={{ opacity: 0.65 }} />}
                    placeholder={t('6-digit code or recovery code')}
                    value={totpCode}
                    maxLength={12}
                    disabled={lockSeconds > 0}
                    style={{ ...inputHeightStyle, letterSpacing: 2, fontWeight: 600 }}
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
                    style={primaryBtnStyle}
                    onClick={handleVerify2FA}
                  >
                    {t('Verify & Sign In')}
                  </Button>
                  <Button
                    block
                    size="large"
                    style={{ height: 42, borderRadius: 10 }}
                    icon={<ArrowLeftOutlined />}
                    onClick={() => {
                      setChallengeToken('');
                      setTotpCode('');
                      setErrorBanner('');
                    }}
                  >
                    {t('Back to Sign In')}
                  </Button>
                </Space>
              </div>
            )}
          </>
        )}
      </Card>
    </div>
  );
};
