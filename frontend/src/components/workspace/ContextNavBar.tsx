import React from 'react';
import { Breadcrumb, Button, Dropdown, Segmented, Space, Tag } from 'antd';
import type { MenuProps } from 'antd';
import {
  AppstoreOutlined,
  CodeOutlined,
  DatabaseOutlined,
  DownloadOutlined,
  EditOutlined,
  MoreOutlined,
  PlaySquareOutlined,
  TableOutlined,
  TeamOutlined,
  UploadOutlined,
} from '@ant-design/icons';
import { useResponsive } from '../../hooks/useResponsive';
import { useI18n } from '../../i18n/useI18n';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import type { WorkspaceTab } from '../../types';

export type TransferModalMode = 'export' | 'import' | 'sql_exec';

export interface TransferOpenOptions {
  customSQL?: string;
  tables?: string[];
}

interface Props {
  tab: WorkspaceTab;
  activeSection: 'overview' | 'data' | 'structure';
  onOpenTransfer?: (mode: TransferModalMode, tableName?: string, opts?: TransferOpenOptions) => void;
}

export const ContextNavBar: React.FC<Props> = ({ tab, activeSection, onOpenTransfer }) => {
  const { openTab, isDesktop, role } = useWorkbenchStore();
  const { isMobile } = useResponsive();
  const { t } = useI18n();
  const canWrite = isDesktop || role !== 'readonly';
  const supportsDbUsers = !['sqlite', 'duckdb'].includes(tab.dbType);

  const goOverview = () => {
    openTab({
      key: `overview:${tab.connectionId}:${tab.database}:${tab.schema || ''}`,
      title: `${tab.database} (${t('Overview')})`,
      kind: 'overview',
      connectionId: tab.connectionId,
      connName: tab.connName,
      dbType: tab.dbType,
      database: tab.database,
      schema: tab.schema,
    });
  };

  const goData = () => {
    if (!tab.table) return;
    openTab({
      key: `data:${tab.connectionId}:${tab.database}:${tab.schema || ''}:${tab.table}`,
      title: tab.table,
      kind: 'table_data',
      connectionId: tab.connectionId,
      connName: tab.connName,
      dbType: tab.dbType,
      database: tab.database,
      schema: tab.schema,
      table: tab.table,
    });
  };

  const goStructure = () => {
    if (!tab.table) return;
    openTab({
      key: `struct:${tab.connectionId}:${tab.database}:${tab.schema || ''}:${tab.table}`,
      title: `${tab.table} [${t('Structure')}]`,
      kind: 'table_structure',
      connectionId: tab.connectionId,
      connName: tab.connName,
      dbType: tab.dbType,
      database: tab.database,
      schema: tab.schema,
      table: tab.table,
    });
  };

  const goSQLConsole = () => {
    const defaultSQL = tab.table
      ? `SELECT * FROM ${tab.table} LIMIT 100;`
      : `-- Database: ${tab.database} (${tab.dbType})\n-- Ctrl+Enter / Cmd+Enter\n`;
    openTab({
      key: `sql:${tab.connectionId}:${tab.database}:${tab.table || 'db'}`,
      title: `SQL - ${tab.table || tab.database}`,
      kind: 'sql_console',
      connectionId: tab.connectionId,
      connName: tab.connName,
      dbType: tab.dbType,
      database: tab.database,
      schema: tab.schema,
      table: tab.table,
      initialSQL: defaultSQL,
    });
  };

  const goDbUsers = () => {
    openTab({
      key: `db_users:${tab.connectionId}`,
      title: `${tab.connName} (${t('Users & Grants')})`,
      kind: 'db_users',
      connectionId: tab.connectionId,
      connName: tab.connName,
      dbType: tab.dbType,
      database: tab.database,
      schema: tab.schema,
    });
  };

  if (isMobile) {
    const mobileMenuItems: MenuProps['items'] = [
      ...(supportsDbUsers
        ? [
            {
              key: 'users',
              icon: <TeamOutlined />,
              label: t('Users & Grants'),
              onClick: goDbUsers,
            },
          ]
        : []),
      ...(onOpenTransfer
        ? [
            {
              key: 'export',
              icon: <DownloadOutlined />,
              label: tab.table ? t('Export Table') : t('Export DB'),
              onClick: () => onOpenTransfer('export', tab.table),
            },
            ...(canWrite
              ? [
                  {
                    key: 'import',
                    icon: <UploadOutlined />,
                    label: t('Import File'),
                    onClick: () => onOpenTransfer('import', tab.table),
                  },
                  {
                    key: 'sql_exec',
                    icon: <PlaySquareOutlined />,
                    label: t('Run SQL Script'),
                    onClick: () => onOpenTransfer('sql_exec', tab.table),
                  },
                ]
              : []),
          ]
        : []),
    ];

    return (
      <div
        className="dbharbor-context-navbar dbharbor-no-scrollbar"
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          gap: 4,
          minWidth: 0,
          maxWidth: '100%',
          padding: '4px 6px',
          marginBottom: 6,
          borderRadius: 8,
          background: 'rgba(22, 119, 255, 0.05)',
          border: '1px solid rgba(22, 119, 255, 0.14)',
          flexShrink: 0,
          overflowX: 'auto',
        }}
      >
        <Segmented
          size="middle"
          style={{ flexShrink: 0 }}
          value={activeSection}
          onChange={(val) => {
            if (val === 'overview') goOverview();
            else if (val === 'data') goData();
            else if (val === 'structure') goStructure();
          }}
          options={[
            { value: 'overview', icon: <AppstoreOutlined />, label: t('Overview') },
            ...(tab.table
              ? [
                  { value: 'data', icon: <TableOutlined />, label: t('Data') },
                  { value: 'structure', icon: <EditOutlined />, label: t('Structure') },
                ]
              : []),
          ]}
        />
        <Space size={4} style={{ flexShrink: 0 }}>
          <Button
            size="small"
            icon={<CodeOutlined />}
            onClick={goSQLConsole}
            style={{ height: 34, paddingInline: 8 }}
          >
            SQL
          </Button>
          {mobileMenuItems.length > 0 && (
            <Dropdown trigger={['click']} menu={{ items: mobileMenuItems }} placement="bottomRight">
              <Button
                size="small"
                icon={<MoreOutlined />}
                aria-label={t('More Actions')}
                style={{ width: 34, height: 34, padding: 0 }}
              />
            </Dropdown>
          )}
        </Space>
      </div>
    );
  }

  return (
    <div
      className="dbharbor-context-navbar"
      style={{
        display: 'flex',
        justifyContent: 'space-between',
        alignItems: 'center',
        flexWrap: 'wrap',
        gap: 6,
        minWidth: 0,
        maxWidth: '100%',
        padding: '6px 10px',
        marginBottom: 8,
        borderRadius: 8,
        background: 'rgba(22, 119, 255, 0.05)',
        border: '1px solid rgba(22, 119, 255, 0.14)',
        flexShrink: 0,
      }}
    >
      <div style={{ minWidth: 0, overflow: 'hidden' }}>
        <Breadcrumb
          items={[
            {
              title: (
                <Space size={4}>
                  <DatabaseOutlined />
                  <span>{tab.connName}</span>
                </Space>
              ),
            },
            {
              title: (
                <a onClick={goOverview}>
                  {tab.database}
                  {tab.schema ? `.${tab.schema}` : ''}
                </a>
              ),
            },
            ...(tab.table
              ? [
                  {
                    title: (
                      <Tag color="blue" style={{ margin: 0, fontWeight: 600 }}>
                        {tab.table}
                      </Tag>
                    ),
                  },
                ]
              : []),
          ]}
        />
      </div>

      <Space size={4} wrap style={{ minWidth: 0, maxWidth: '100%' }}>
        <Button
          size="small"
          type={activeSection === 'overview' ? 'primary' : 'default'}
          icon={<AppstoreOutlined />}
          onClick={goOverview}
        >
          {t('Overview')}
        </Button>
        {tab.table && (
          <>
            <Button
              size="small"
              type={activeSection === 'data' ? 'primary' : 'default'}
              icon={<TableOutlined />}
              onClick={goData}
            >
              {t('Browse Data')}
            </Button>
            <Button
              size="small"
              type={activeSection === 'structure' ? 'primary' : 'default'}
              icon={<EditOutlined />}
              onClick={goStructure}
            >
              {canWrite ? t('Structure / Alter') : t('Table Structure')}
            </Button>
          </>
        )}
        <Button size="small" icon={<CodeOutlined />} onClick={goSQLConsole}>
          {t('Custom SQL')}
        </Button>
        {supportsDbUsers && (
          <Button size="small" icon={<TeamOutlined />} onClick={goDbUsers}>
            {t('Users & Grants')}
          </Button>
        )}
        {onOpenTransfer && (
          <>
            <Button
              size="small"
              icon={<DownloadOutlined />}
              onClick={() => onOpenTransfer('export', tab.table)}
            >
              {tab.table ? t('Export Table') : t('Export DB')}
            </Button>
            {canWrite && (
              <>
                <Button
                  size="small"
                  icon={<UploadOutlined />}
                  onClick={() => onOpenTransfer('import', tab.table)}
                >
                  {t('Import File')}
                </Button>
                <Button
                  size="small"
                  icon={<PlaySquareOutlined />}
                  onClick={() => onOpenTransfer('sql_exec', tab.table)}
                >
                  {t('Run SQL Script')}
                </Button>
              </>
            )}
          </>
        )}
      </Space>
    </div>
  );
};
