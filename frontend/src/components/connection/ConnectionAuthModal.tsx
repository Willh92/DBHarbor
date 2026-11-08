import React, { useEffect, useRef, useState } from 'react';
import { Alert, Button, Checkbox, Form, Input, Modal, Space, Tag, Typography, message } from 'antd';
import type { InputRef } from 'antd';
import { KeyOutlined, WarningOutlined } from '@ant-design/icons';
import {
  api,
  settleConnectionAuthPrompt,
  subscribeConnectionAuthPrompt,
  type PendingConnectionAuthPrompt,
} from '../../api/client';
import { useResponsive } from '../../hooks/useResponsive';
import { useI18n } from '../../i18n/useI18n';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import { DB_SHORT_LABELS } from '../../utils/format';

export const ConnectionAuthModal: React.FC = () => {
  const { connections, reloadConnections, bumpTreeRefresh } = useWorkbenchStore();
  const { isMobile } = useResponsive();
  const { t } = useI18n();
  const [form] = Form.useForm();
  const [prompt, setPrompt] = useState<PendingConnectionAuthPrompt | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [inlineError, setInlineError] = useState<string>('');
  const passwordRef = useRef<InputRef | null>(null);
  const rememberTouchedRef = useRef(false);
  const rememberVal = Form.useWatch('rememberPassword', form);

  useEffect(() => {
    return subscribeConnectionAuthPrompt((next) => {
      setPrompt(next);
    });
  }, []);

  const conn = prompt ? connections.find((c) => c.id === prompt.connectionId) : undefined;
  const canEdit = Boolean(conn && conn.editable !== false && !conn.ephemeral);

  useEffect(() => {
    rememberTouchedRef.current = false;
    if (!prompt) {
      setInlineError('');
      setSubmitting(false);
      return;
    }
    setInlineError('');
    const defaultRemember =
      prompt.reason === 'DB_AUTH_FAILED' ? Boolean(conn?.rememberPassword) : false;
    form.setFieldsValue({
      username: conn?.username || '',
      password: '',
      uri: '',
      rememberPassword: canEdit ? defaultRemember : false,
    });
    if (!isMobile && !window.matchMedia?.('(pointer: coarse)').matches) {
      const timer = window.setTimeout(() => {
        passwordRef.current?.focus();
      }, 80);
      return () => window.clearTimeout(timer);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [prompt?.connectionId, prompt?.reason]);

  useEffect(() => {
    if (!prompt || !conn) return;
    const curUsername = form.getFieldValue('username');
    if (!curUsername && conn.username) {
      form.setFieldValue('username', conn.username);
    }
    if (!rememberTouchedRef.current) {
      const defaultRemember =
        prompt.reason === 'DB_AUTH_FAILED' ? Boolean(conn.rememberPassword) : false;
      form.setFieldValue('rememberPassword', canEdit ? defaultRemember : false);
    }
  }, [prompt, conn, canEdit, form]);

  const handleCancel = () => {
    if (!prompt || submitting) return;
    settleConnectionAuthPrompt(prompt.connectionId, false);
  };

  const handleUnlock = async () => {
    if (!prompt) return;
    try {
      const vals = await form.validateFields();
      setSubmitting(true);
      setInlineError('');
      const res = await api.unlockConnection(prompt.connectionId, {
        username: vals.username !== undefined ? String(vals.username).trim() : undefined,
        password: vals.password || '',
        uri: vals.uri ? String(vals.uri).trim() : undefined,
        rememberPassword: canEdit ? Boolean(vals.rememberPassword) : false,
      });
      await reloadConnections();
      bumpTreeRefresh();
      message.success(
        t('Connected to {name}', {
          name: res.connection?.name || conn?.name || prompt.connectionId,
        })
      );
      settleConnectionAuthPrompt(prompt.connectionId, true);
    } catch (err: any) {
      if (err?.errorFields) return;
      setInlineError(err?.message || t('Authentication failed'));
    } finally {
      setSubmitting(false);
    }
  };

  const isAuthFailed = prompt?.reason === 'DB_AUTH_FAILED';
  const badge = conn ? DB_SHORT_LABELS[conn.type] || { code: 'DB', color: conn.color || '#1677ff' } : null;

  return (
    <Modal
      open={Boolean(prompt)}
      title={
        <Space size={8}>
          {isAuthFailed ? (
            <WarningOutlined style={{ color: '#faad14' }} />
          ) : (
            <KeyOutlined style={{ color: '#1677ff' }} />
          )}
          <span>{isAuthFailed ? t('Database Authentication Failed') : t('Enter Database Credentials')}</span>
        </Space>
      }
      width="min(460px, calc(100vw - 24px))"
      style={isMobile ? { top: 20 } : undefined}
      onCancel={handleCancel}
      maskClosable={false}
      destroyOnClose
      footer={
        <Space style={{ width: '100%', justifyContent: 'flex-end' }}>
          <Button onClick={handleCancel} disabled={submitting}>
            {t('Cancel')}
          </Button>
          <Button type="primary" loading={submitting} onClick={handleUnlock}>
            {t('Connect')}
          </Button>
        </Space>
      }
    >
      {conn && badge && (
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 10,
            padding: '8px 12px',
            marginBottom: 12,
            borderRadius: 8,
            border: '1px solid rgba(128,128,128,0.2)',
            background: 'rgba(128,128,128,0.04)',
          }}
        >
          <span
            style={{
              width: 28,
              height: 28,
              borderRadius: 6,
              background: conn.color || badge.color,
              color: '#fff',
              fontWeight: 700,
              fontSize: 11,
              display: 'inline-flex',
              alignItems: 'center',
              justifyContent: 'center',
              flexShrink: 0,
            }}
          >
            {badge.code}
          </span>
          <div style={{ minWidth: 0, flex: 1 }}>
            <div style={{ fontWeight: 600, fontSize: 13, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {conn.name}
            </div>
            <Typography.Text type="secondary" style={{ fontSize: 11, wordBreak: 'break-all' }}>
              {conn.host ? `${conn.host}${conn.port ? `:${conn.port}` : ''}` : conn.type.toUpperCase()}
            </Typography.Text>
          </div>
          <Tag style={{ margin: 0 }}>{conn.type.toUpperCase()}</Tag>
        </div>
      )}

      <Alert
        type={isAuthFailed ? 'warning' : 'info'}
        showIcon
        style={{ marginBottom: 14 }}
        message={
          isAuthFailed
            ? t('Database username or password was rejected. Please re-enter credentials.')
            : t('Password is not saved for this connection. Please enter credentials for the current session.')
        }
        description={
          isAuthFailed && prompt?.errorMessage ? (
            <div style={{ fontSize: 12, wordBreak: 'break-word', marginTop: 2 }}>{prompt.errorMessage}</div>
          ) : undefined
        }
      />

      {inlineError && (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 12 }}
          message={<span style={{ wordBreak: 'break-word', fontSize: 12 }}>{inlineError}</span>}
        />
      )}

      <Form
        form={form}
        layout="vertical"
        onFinish={handleUnlock}
        onValuesChange={(changed) => {
          if ('rememberPassword' in changed) {
            rememberTouchedRef.current = true;
          }
        }}
      >
        <Form.Item name="username" label={t('Username')} style={{ marginBottom: 12 }}>
          <Input
            placeholder={conn?.type === 'redis' ? t('Optional (Redis 6+ ACL user)') : t('Username')}
            autoComplete="off"
          />
        </Form.Item>

        <Form.Item name="password" label={t('Password')} style={{ marginBottom: 12 }}>
          <Input.Password
            ref={passwordRef}
            placeholder={t('Enter database password')}
            autoComplete="new-password"
            onPressEnter={handleUnlock}
          />
        </Form.Item>

        {conn?.type === 'mongodb' && conn.hasUri && canEdit && (
          <Form.Item name="uri" label={t('Connection URI (Optional Override)')} style={{ marginBottom: 12 }}>
            <Input.Password placeholder="mongodb://user:pass@host:27017/admin" autoComplete="off" />
          </Form.Item>
        )}

        {canEdit && (
          <div
            style={{
              padding: '8px 10px',
              borderRadius: 8,
              border: '1px solid rgba(128,128,128,0.18)',
              background: 'rgba(128,128,128,0.03)',
            }}
          >
            <Form.Item name="rememberPassword" valuePropName="checked" style={{ marginBottom: 2 }}>
              <Checkbox>{t('Remember password')}</Checkbox>
            </Form.Item>
            <Typography.Text type="secondary" style={{ fontSize: 11 }}>
              {rememberVal
                ? t('Password is encrypted with AES-256-GCM on disk for automatic reconnection.')
                : t('Password is kept only for this sign-in session and will not be saved to disk; you will be prompted on next login.')}
            </Typography.Text>
          </div>
        )}
      </Form>
    </Modal>
  );
};
