import React, { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Alert,
  AutoComplete,
  Button,
  Checkbox,
  Col,
  Form,
  Input,
  Modal,
  Row,
  Select,
  Space,
  Typography,
  message,
} from 'antd';
import { DeleteOutlined, EyeOutlined, PlusOutlined } from '@ant-design/icons';
import { api } from '../../api/client';
import { ResizableTable } from '../common/ResizableTable';
import { useI18n } from '../../i18n/useI18n';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import type { ColumnDef } from '../../types';

interface Props {
  open: boolean;
  connectionId: string;
  dbType: string;
  database: string;
  schema?: string;
  onClose: () => void;
  onCreated: (tableName: string) => void;
}

export const CreateTableModal: React.FC<Props> = ({
  open,
  connectionId,
  dbType,
  database,
  schema,
  onClose,
  onCreated,
}) => {
  const { drivers } = useWorkbenchStore();
  const { t } = useI18n();
  const drv = drivers.find((d) => d.type === dbType);
  const typeOptions = useMemo(
    () => (drv?.defaultTypes || ['BIGINT', 'INT', 'VARCHAR(255)', 'TEXT', 'TIMESTAMP']).map((tp) => ({ value: tp })),
    [drv?.defaultTypes]
  );

  const [tableName, setTableName] = useState('');
  const [comment, setComment] = useState('');
  const [engine, setEngine] = useState(dbType === 'clickhouse' ? 'MergeTree()' : 'InnoDB');
  const [orderBy, setOrderBy] = useState('');
  const [partitionBy, setPartitionBy] = useState('');
  const [columns, setColumns] = useState<ColumnDef[]>([]);
  const [previewDDL, setPreviewDDL] = useState('');
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (!open) return;
    setTableName('');
    setComment('');
    setPreviewDDL('');
    setEngine(dbType === 'clickhouse' ? 'MergeTree()' : 'InnoDB');
    setOrderBy('');
    setPartitionBy('');
    setColumns([
      {
        name: 'id',
        type: dbType === 'clickhouse' ? 'UInt64' : dbType === 'sqlite' ? 'INTEGER' : 'BIGINT',
        nullable: false,
        isPrimaryKey: true,
        isAutoIncrement: dbType !== 'clickhouse',
        comment: 'Primary Key ID',
      },
      {
        name: 'name',
        type: dbType === 'clickhouse' ? 'String' : dbType === 'sqlite' ? 'TEXT' : 'VARCHAR(255)',
        nullable: false,
        isPrimaryKey: false,
        isAutoIncrement: false,
        comment: 'Name',
      },
      {
        name: 'created_at',
        type: dbType === 'clickhouse' ? 'DateTime' : dbType === 'sqlite' ? 'TEXT' : 'TIMESTAMP',
        nullable: true,
        isPrimaryKey: false,
        isAutoIncrement: false,
        defaultValue: dbType === 'clickhouse' ? 'now()' : '',
        comment: 'Created At',
      },
    ]);
  }, [open, dbType]);

  const updateCol = useCallback((idx: number, patch: Partial<ColumnDef>) => {
    setColumns((prev) => prev.map((c, i) => (i === idx ? { ...c, ...patch } : c)));
  }, []);

  const colCount = columns.length;
  const colTableColumns = useMemo(
    () => [
      {
        title: t('Column Name'),
        width: 160,
        render: (_: any, r: ColumnDef, idx: number) => (
          <Input size="small" value={r.name} onChange={(e) => updateCol(idx, { name: e.target.value })} />
        ),
      },
      {
        title: t('Data Type'),
        width: 180,
        render: (_: any, r: ColumnDef, idx: number) => (
          <AutoComplete
            size="small"
            style={{ width: '100%' }}
            options={typeOptions}
            value={r.type}
            onChange={(val) => updateCol(idx, { type: val })}
          />
        ),
      },
      {
        title: t('Nullable'),
        width: 85,
        align: 'center' as const,
        render: (_: any, r: ColumnDef, idx: number) => (
          <Checkbox checked={r.nullable} onChange={(e) => updateCol(idx, { nullable: e.target.checked })} />
        ),
      },
      {
        title: t('PK'),
        width: 75,
        align: 'center' as const,
        render: (_: any, r: ColumnDef, idx: number) => (
          <Checkbox checked={r.isPrimaryKey} onChange={(e) => updateCol(idx, { isPrimaryKey: e.target.checked })} />
        ),
      },
      {
        title: t('Auto Inc'),
        width: 75,
        align: 'center' as const,
        render: (_: any, r: ColumnDef, idx: number) => (
          <Checkbox
            disabled={dbType === 'clickhouse'}
            checked={r.isAutoIncrement}
            onChange={(e) => updateCol(idx, { isAutoIncrement: e.target.checked })}
          />
        ),
      },
      {
        title: t('Default Expr'),
        width: 130,
        render: (_: any, r: ColumnDef, idx: number) => (
          <Input
            size="small"
            value={r.defaultValue || ''}
            onChange={(e) => updateCol(idx, { defaultValue: e.target.value })}
          />
        ),
      },
      {
        title: t('Comment'),
        width: 140,
        render: (_: any, r: ColumnDef, idx: number) => (
          <Input
            size="small"
            value={r.comment || ''}
            onChange={(e) => updateCol(idx, { comment: e.target.value })}
          />
        ),
      },
      {
        title: '',
        width: 45,
        render: (_: any, __: ColumnDef, idx: number) => (
          <Button
            size="small"
            type="text"
            danger
            disabled={colCount <= 1}
            icon={<DeleteOutlined />}
            onClick={() => setColumns((prev) => prev.filter((___, i) => i !== idx))}
          />
        ),
      },
    ],
    [t, typeOptions, dbType, colCount, updateCol]
  );

  const handlePreview = async () => {
    try {
      const ddl = await api.createTable(connectionId, {
        database,
        schema,
        table: tableName.trim() || 'new_table',
        columns,
        engine,
        orderBy,
        partitionBy,
        comment,
        previewOnly: true,
      });
      setPreviewDDL(ddl);
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleCreate = async () => {
    if (!tableName.trim()) {
      message.warning(t('Please enter a table name'));
      return;
    }
    setSubmitting(true);
    try {
      await api.createTable(connectionId, {
        database,
        schema,
        table: tableName.trim(),
        columns,
        engine,
        orderBy,
        partitionBy,
        comment,
        previewOnly: false,
      });
      message.success(t('Table {name} created successfully!', { name: tableName.trim() }));
      onCreated(tableName.trim());
      onClose();
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      open={open}
      title={`${t('Visual Create Table')} — ${database}${schema ? '.' + schema : ''} (${drv?.name || dbType})`}
      width="min(920px, calc(100vw - 16px))"
      onCancel={onClose}
      footer={
        <div style={{ display: 'flex', justifyContent: 'space-between', flexWrap: 'wrap', gap: 8 }}>
          <Button icon={<EyeOutlined />} onClick={handlePreview}>
            {t('Preview CREATE TABLE SQL')}
          </Button>
          <Space>
            <Button onClick={onClose}>{t('Cancel')}</Button>
            <Button type="primary" loading={submitting} onClick={handleCreate}>
              {t('Create Table Now')}
            </Button>
          </Space>
        </div>
      }
    >
      <Form layout="vertical">
        <Row gutter={12}>
          <Col xs={24} sm={8}>
            <Form.Item label={t('Table Name')} required>
              <Input
                placeholder="e.g. user_events"
                value={tableName}
                onChange={(e) => setTableName(e.target.value)}
              />
            </Form.Item>
          </Col>
          <Col xs={24} sm={16}>
            <Form.Item label={t('Table Comment')}>
              <Input value={comment} onChange={(e) => setComment(e.target.value)} />
            </Form.Item>
          </Col>
        </Row>

        {dbType === 'clickhouse' && (
          <Row gutter={12}>
            <Col xs={24} sm={8}>
              <Form.Item label={t('ClickHouse Table Engine')}>
                <Select
                  value={engine}
                  onChange={setEngine}
                  options={[
                    { label: 'MergeTree()', value: 'MergeTree()' },
                    { label: 'ReplacingMergeTree()', value: 'ReplacingMergeTree()' },
                    { label: 'SummingMergeTree()', value: 'SummingMergeTree()' },
                    { label: 'AggregatingMergeTree()', value: 'AggregatingMergeTree()' },
                    { label: 'Log', value: 'Log' },
                  ]}
                />
              </Form.Item>
            </Col>
            <Col xs={24} sm={8}>
              <Form.Item label={t('Sorting Key (ORDER BY)')}>
                <Input
                  placeholder={t('Defaults to PK, e.g. (id, created_at)')}
                  value={orderBy}
                  onChange={(e) => setOrderBy(e.target.value)}
                />
              </Form.Item>
            </Col>
            <Col xs={24} sm={8}>
              <Form.Item label={t('Partition Key (PARTITION BY)')}>
                <Input
                  placeholder={t('e.g. toYYYYMM(created_at)')}
                  value={partitionBy}
                  onChange={(e) => setPartitionBy(e.target.value)}
                />
              </Form.Item>
            </Col>
          </Row>
        )}
      </Form>

      <div style={{ marginBottom: 8, display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <Typography.Text strong>{t('Column Definitions ({count})', { count: columns.length })}</Typography.Text>
        <Button
          size="small"
          type="dashed"
          icon={<PlusOutlined />}
          onClick={() =>
            setColumns([
              ...columns,
              {
                name: `col_${columns.length + 1}`,
                type: typeOptions[0]?.value || 'VARCHAR(255)',
                nullable: true,
                isPrimaryKey: false,
                isAutoIncrement: false,
              },
            ])
          }
        >
          {t('Add Column')}
        </Button>
      </div>

      <ResizableTable<ColumnDef>
        size="small"
        pagination={false}
        rowKey={(_, idx) => String(idx)}
        dataSource={columns}
        scroll={{ x: 890 }}
        columns={colTableColumns}
      />

      {previewDDL && (
        <Alert
          style={{ marginTop: 12 }}
          type="info"
          message={t('DDL Preview:')}
          description={
            <pre style={{ margin: 0, fontSize: 12, whiteSpace: 'pre-wrap', fontFamily: 'monospace' }}>{previewDDL}</pre>
          }
        />
      )}
    </Modal>
  );
};
