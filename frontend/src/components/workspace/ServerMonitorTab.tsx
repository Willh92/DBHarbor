import React, { useCallback, useDeferredValue, useEffect, useMemo, useRef, useState } from 'react';
import {
  Button,
  Card,
  Col,
  Input,
  Popconfirm,
  Progress,
  Row,
  Select,
  Space,
  Statistic,
  Tag,
  Typography,
  message,
} from 'antd';
import { ReloadOutlined, SearchOutlined, StopOutlined } from '@ant-design/icons';
import { api, isAuthPromptCancelledError } from '../../api/client';
import { ResizableTable } from '../common/ResizableTable';
import { useResponsive } from '../../hooks/useResponsive';
import { useI18n } from '../../i18n/useI18n';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import type { MonitorSnapshot, SessionItem, StorageItem, WorkspaceTab } from '../../types';
import { formatBytes, formatDuration } from '../../utils/format';

export const ServerMonitorTab: React.FC<{ tab: WorkspaceTab }> = ({ tab }) => {
  const { activeTabKey, isDesktop, role, connections, treeRefreshTick } = useWorkbenchStore();
  const conn = connections.find((c) => c.id === tab.connectionId);
  const { isMobile } = useResponsive();
  const canWrite = isDesktop || role !== 'readonly';
  const { t } = useI18n();
  const [snapshot, setSnapshot] = useState<MonitorSnapshot | null>(null);
  const [loading, setLoading] = useState(false);
  const [pollSec, setPollSec] = useState<number>(0);
  const [sessionFilter, setSessionFilter] = useState('');
  const needsUnlockReloadRef = useRef(false);
  const inFlightRef = useRef(false);
  const abortCtrlRef = useRef<AbortController | null>(null);

  const fetchMonitor = useCallback(
    async (silent = false) => {
      if (inFlightRef.current) return;
      inFlightRef.current = true;
      const ctrl = new AbortController();
      abortCtrlRef.current = ctrl;
      if (!silent) {
        setLoading(true);
      }
      try {
        const snap = await api.getMonitor(tab.connectionId, {
          signal: ctrl.signal,
          skipDbAuthPrompt: silent,
        });
        needsUnlockReloadRef.current = false;
        setSnapshot(snap);
      } catch (err: any) {
        if (err?.name === 'AbortError') {
          return;
        }
        if (isAuthPromptCancelledError(err) || err?.status === 428) {
          needsUnlockReloadRef.current = true;
          setPollSec(0);
          return;
        }
        if (!silent) {
          message.error(err.message);
        } else {
          setPollSec(0);
        }
      } finally {
        if (abortCtrlRef.current === ctrl) {
          abortCtrlRef.current = null;
        }
        inFlightRef.current = false;
        if (!silent) {
          setLoading(false);
        }
      }
    },
    [tab.connectionId]
  );

  useEffect(() => {
    void fetchMonitor(false);
    return () => {
      abortCtrlRef.current?.abort();
    };
  }, [fetchMonitor]);

  useEffect(() => {
    if (needsUnlockReloadRef.current && conn?.unlocked) {
      needsUnlockReloadRef.current = false;
      void fetchMonitor(false);
    }
  }, [conn?.unlocked, treeRefreshTick, fetchMonitor]);

  useEffect(() => {
    if (pollSec <= 0 || activeTabKey !== tab.key) return;
    const timer = setInterval(() => {
      if (!document.hidden && !inFlightRef.current) {
        void fetchMonitor(true);
      }
    }, pollSec * 1000);
    return () => clearInterval(timer);
  }, [pollSec, tab.key, activeTabKey, fetchMonitor]);

  const handleKill = useCallback(
    async (sessionId: string) => {
      try {
        await api.killSession(tab.connectionId, sessionId);
        message.success(t('Terminated session {id}', { id: sessionId }));
        await fetchMonitor();
      } catch (err: any) {
        message.error(err.message);
      }
    },
    [tab.connectionId, t, fetchMonitor]
  );

  const deferredFilter = useDeferredValue(sessionFilter);
  const maxStorageBytes = useMemo(
    () => Math.max(1, ...(snapshot?.storage.map((s) => s.sizeBytes || s.rows) || [1])),
    [snapshot?.storage]
  );
  const filteredSessions = useMemo(() => {
    const list = snapshot?.sessions || [];
    const q = deferredFilter.trim().toLowerCase();
    if (!q) return list;
    return list.filter(
      (s) =>
        s.query.toLowerCase().includes(q) ||
        s.user.toLowerCase().includes(q) ||
        s.id.toLowerCase().includes(q)
    );
  }, [snapshot?.sessions, deferredFilter]);

  const sessionColumns = useMemo(
    () => [
      { title: t('ID / PID'), dataIndex: 'id', width: 90 },
      { title: t('User'), dataIndex: 'user', width: 95 },
      { title: t('Host'), dataIndex: 'host', width: 125, ellipsis: true },
      { title: t('Database'), dataIndex: 'database', width: 90, ellipsis: true },
      {
        title: t('Duration'),
        dataIndex: 'durationSec',
        width: 80,
        sorter: (a: SessionItem, b: SessionItem) => a.durationSec - b.durationSec,
        render: (sec: number) => (
          <Tag color={sec >= 10 ? 'error' : sec >= 3 ? 'warning' : 'default'}>{sec}s</Tag>
        ),
      },
      { title: t('Active SQL / Command'), dataIndex: 'query', width: 210, ellipsis: true },
      ...(snapshot?.canKillSession && canWrite
        ? [
            {
              title: t('Action'),
              width: 75,
              render: (_: any, r: SessionItem) => (
                <Popconfirm title={t('Kill session {id}?', { id: r.id })} onConfirm={() => handleKill(r.id)}>
                  <Button size="small" danger type="link" icon={<StopOutlined />}>
                    {t('Kill')}
                  </Button>
                </Popconfirm>
              ),
            },
          ]
        : []),
    ],
    [t, snapshot?.canKillSession, canWrite, handleKill]
  );

  const storageColumns = useMemo(
    () => [
      {
        title: t('Object'),
        dataIndex: 'name',
        width: 150,
        ellipsis: true,
        render: (v: string, r: StorageItem) => (
          <div>
            <Typography.Text strong style={{ fontSize: 12 }}>{v}</Typography.Text>
            <div>
              <Progress
                percent={Math.min(100, Math.round(((r.sizeBytes || r.rows) / maxStorageBytes) * 100))}
                showInfo={false}
                size="small"
              />
            </div>
          </div>
        ),
      },
      {
        title: t('Rows / Keys'),
        dataIndex: 'rows',
        width: 85,
        align: 'right' as const,
        render: (v: number) => (v ? v.toLocaleString() : '-'),
      },
      {
        title: t('Size'),
        dataIndex: 'sizeBytes',
        width: 90,
        align: 'right' as const,
        render: (v: number) => (v > 0 ? formatBytes(v) : '-'),
      },
    ],
    [t, maxStorageBytes]
  );

  const sessionFilterInput = (
    <Input
      size="small"
      allowClear
      prefix={<SearchOutlined />}
      placeholder={t('Filter user / SQL / ID')}
      style={{ width: isMobile ? '100%' : 240 }}
      value={sessionFilter}
      onChange={(e) => setSessionFilter(e.target.value)}
    />
  );

  return (
    <div style={{ padding: isMobile ? 8 : 16, height: '100%', overflowY: 'auto' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 14, flexWrap: 'wrap', gap: 8 }}>
        <Space wrap>
          <Typography.Title level={5} style={{ margin: 0 }}>
            {tab.connName} — {t('Live Runtime Monitor & Session Governance')}
          </Typography.Title>
          {snapshot?.version && <Tag color="blue">{snapshot.version}</Tag>}
          {(snapshot?.uptimeSec ?? 0) > 0 && <Tag color="green">{formatDuration(snapshot?.uptimeSec)}</Tag>}
        </Space>

        <Space wrap>
          <Select
            size="small"
            value={pollSec}
            onChange={setPollSec}
            style={{ width: 155 }}
            options={[
              { label: t('Auto-refresh: Off'), value: 0 },
              { label: t('Refresh every 5s'), value: 5 },
              { label: t('Refresh every 10s'), value: 10 },
            ]}
          />
          <Button size="small" type="primary" icon={<ReloadOutlined />} loading={loading} onClick={() => void fetchMonitor(false)}>
            {t('Refresh Now')}
          </Button>
        </Space>
      </div>

      {/* KPI Metrics */}
      <Row gutter={[12, 10]} className="dbharbor-kpi-row" style={{ marginBottom: 14 }}>
        {(snapshot?.metrics || []).map((m) => (
          <Col xs={12} sm={8} md={6} key={m.key}>
            <Card size="small">
              <Statistic title={t(m.label)} value={m.value} suffix={m.unit || ''} valueStyle={{ fontSize: 20 }} />
            </Card>
          </Col>
        ))}
        <Col xs={12} sm={8} md={6}>
          <Card size="small">
            <Statistic title={t('Captured Sessions')} value={snapshot?.sessions.length || 0} valueStyle={{ fontSize: 20 }} />
          </Card>
        </Col>
      </Row>

      <Row gutter={14}>
        {/* Active Sessions & Kill Query */}
        <Col xs={24} lg={15} style={{ marginBottom: 16 }}>
          <Card
            size="small"
            title={t('Active Sessions / Processlist')}
            extra={!isMobile ? sessionFilterInput : undefined}
          >
            {isMobile && <div style={{ marginBottom: 8 }}>{sessionFilterInput}</div>}
            <ResizableTable<SessionItem>
              rowKey="id"
              size="small"
              loading={loading}
              dataSource={filteredSessions}
              scroll={{ x: 760 }}
              pagination={{ pageSize: 15, size: 'small', responsive: true }}
              columns={sessionColumns}
            />
          </Card>
        </Col>

        {/* Storage Breakdown */}
        <Col xs={24} lg={9}>
          <Card size="small" title={t('Top Storage by Table / DB')}>
            <ResizableTable<StorageItem>
              rowKey="name"
              size="small"
              pagination={false}
              scroll={{ x: 320 }}
              dataSource={snapshot?.storage || []}
              columns={storageColumns}
            />
          </Card>
        </Col>
      </Row>
    </div>
  );
};
