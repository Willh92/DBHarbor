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
  Segmented,
  Select,
  Space,
  Typography,
  message,
} from 'antd';
import { DeleteOutlined, EyeOutlined, PlusOutlined } from '@ant-design/icons';
import { api } from '../../api/client';
import { ResizableTable } from '../common/ResizableTable';
import { useResponsive } from '../../hooks/useResponsive';
import { useI18n } from '../../i18n/useI18n';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import type { CheckConstraintDef, ColumnDef } from '../../types';

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
  const { isMobile } = useResponsive();
  const { t } = useI18n();
  const drv = drivers.find((d) => d.type === dbType);
  const tableFeatures = drv?.capabilities?.tableFeatures || [];
  const supportsVirtualTables = tableFeatures.includes('VIRTUAL_TABLES') || dbType === 'sqlite';
  const supportsGeneratedCols = tableFeatures.includes('GENERATED_COLS');
  const supportsChecks = tableFeatures.includes('CHECKS');

  const typeOptions = useMemo(
    () => (drv?.defaultTypes || ['BIGINT', 'INT', 'VARCHAR(255)', 'TEXT', 'TIMESTAMP']).map((tp) => ({ value: tp })),
    [drv?.defaultTypes]
  );

  const [tableMode, setTableMode] = useState<'standard' | 'virtual'>('standard');
  const [virtualModule, setVirtualModule] = useState<string>('fts5');
  const [virtualArgs, setVirtualArgs] = useState<string>("title, body, tokenize='unicode61'");
  const [strictTable, setStrictTable] = useState(false);
  const [withoutRowId, setWithoutRowId] = useState(false);

  const [tableName, setTableName] = useState('');
  const [comment, setComment] = useState('');
  const [engine, setEngine] = useState(dbType === 'clickhouse' ? 'MergeTree()' : 'InnoDB');
  const [orderBy, setOrderBy] = useState('');
  const [partitionBy, setPartitionBy] = useState('');
  const [columns, setColumns] = useState<ColumnDef[]>([]);
  const [checks, setChecks] = useState<CheckConstraintDef[]>([]);
  const [previewDDL, setPreviewDDL] = useState('');
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (!open) return;
    setTableMode('standard');
    setVirtualModule('fts5');
    setVirtualArgs("title, body, tokenize='unicode61'");
    setStrictTable(false);
    setWithoutRowId(false);
    setTableName('');
    setComment('');
    setPreviewDDL('');
    setEngine(dbType === 'clickhouse' ? 'MergeTree()' : 'InnoDB');
    setOrderBy('');
    setPartitionBy('');
    setChecks([]);
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

  const updateCheck = useCallback((idx: number, patch: Partial<CheckConstraintDef>) => {
    setChecks((prev) => prev.map((ck, i) => (i === idx ? { ...ck, ...patch } : ck)));
  }, []);

  const colCount = columns.length;
  const colTableColumns = useMemo(
    () => [
      {
        title: t('Column Name'),
        width: 150,
        render: (_: any, r: ColumnDef, idx: number) => (
          <Input size="small" value={r.name} onChange={(e) => updateCol(idx, { name: e.target.value })} />
        ),
      },
      {
        title: t('Data Type'),
        width: 165,
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
        width: 78,
        align: 'center' as const,
        render: (_: any, r: ColumnDef, idx: number) => (
          <Checkbox checked={r.nullable} onChange={(e) => updateCol(idx, { nullable: e.target.checked })} />
        ),
      },
      {
        title: t('PK'),
        width: 64,
        align: 'center' as const,
        render: (_: any, r: ColumnDef, idx: number) => (
          <Checkbox checked={r.isPrimaryKey} onChange={(e) => updateCol(idx, { isPrimaryKey: e.target.checked })} />
        ),
      },
      {
        title: t('Auto Inc'),
        width: 74,
        align: 'center' as const,
        render: (_: any, r: ColumnDef, idx: number) => (
          <Checkbox
            disabled={dbType === 'clickhouse' || Boolean(r.generatedExpr?.trim())}
            checked={r.isAutoIncrement}
            onChange={(e) => updateCol(idx, { isAutoIncrement: e.target.checked })}
          />
        ),
      },
      {
        title: t('Default Expr'),
        width: 120,
        render: (_: any, r: ColumnDef, idx: number) => (
          <Input
            size="small"
            disabled={Boolean(r.generatedExpr?.trim())}
            value={r.defaultValue || ''}
            onChange={(e) => updateCol(idx, { defaultValue: e.target.value })}
          />
        ),
      },
      ...(supportsGeneratedCols
        ? [
            {
              title: t('Generated Expr'),
              width: 215,
              render: (_: any, r: ColumnDef, idx: number) => (
                <Space.Compact style={{ width: '100%' }}>
                  <Input
                    size="small"
                    placeholder={t('Optional expr')}
                    value={r.generatedExpr || ''}
                    onChange={(e) =>
                      updateCol(idx, {
                        generatedExpr: e.target.value,
                        generatedStorage: e.target.value.trim()
                          ? r.generatedStorage || (dbType === 'postgres' || dbType === 'kingbase' ? 'STORED' : 'VIRTUAL')
                          : undefined,
                      })
                    }
                  />
                  {r.generatedExpr?.trim() && (
                    <Select
                      size="small"
                      style={{ width: 88 }}
                      value={r.generatedStorage || 'VIRTUAL'}
                      onChange={(val) => updateCol(idx, { generatedStorage: val })}
                      options={[
                        ...(dbType === 'postgres' || dbType === 'kingbase'
                          ? []
                          : [{ label: 'VIRTUAL', value: 'VIRTUAL' }]),
                        { label: 'STORED', value: 'STORED' },
                      ]}
                    />
                  )}
                </Space.Compact>
              ),
            },
          ]
        : []),
      {
        title: t('Comment'),
        width: 130,
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
        width: 44,
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
    [t, typeOptions, dbType, colCount, updateCol, supportsGeneratedCols]
  );

  const buildPayload = (previewOnly: boolean) => {
    const isVirtual = supportsVirtualTables && tableMode === 'virtual';
    const validChecks = checks
      .map((ck) => ({
        name: ck.name.trim(),
        expression: ck.expression.trim(),
        enforced: ck.enforced !== false,
      }))
      .filter((ck) => ck.expression.length > 0);

    return {
      database,
      schema,
      table: tableName.trim() || (previewOnly ? 'new_table' : ''),
      columns: isVirtual ? [] : columns,
      checkConstraints: !isVirtual && validChecks.length > 0 ? validChecks : undefined,
      engine,
      orderBy,
      partitionBy,
      comment,
      strict: dbType === 'sqlite' && !isVirtual ? strictTable : undefined,
      withoutRowId: dbType === 'sqlite' && !isVirtual ? withoutRowId : undefined,
      virtualModule: isVirtual ? virtualModule : undefined,
      virtualArgs: isVirtual ? virtualArgs.trim() : undefined,
      previewOnly,
    };
  };

  const handlePreview = async () => {
    try {
      const ddl = await api.createTable(connectionId, buildPayload(true));
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
    if (supportsVirtualTables && tableMode === 'virtual' && !virtualArgs.trim()) {
      message.warning(t('Please enter virtual table columns or arguments'));
      return;
    }
    setSubmitting(true);
    try {
      await api.createTable(connectionId, buildPayload(false));
      message.success(t('Table {name} created successfully!', { name: tableName.trim() }));
      onCreated(tableName.trim());
      onClose();
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setSubmitting(false);
    }
  };

  const isVirtualMode = supportsVirtualTables && tableMode === 'virtual';

  return (
    <Modal
      open={open}
      title={`${t('Visual Create Table')} — ${database}${schema ? '.' + schema : ''} (${drv?.name || dbType})`}
      width="min(940px, calc(100vw - 16px))"
      style={isMobile ? { top: 10 } : undefined}
      styles={{
        body: {
          maxHeight: isMobile ? 'calc(100dvh - 150px)' : 'calc(100dvh - 190px)',
          overflowY: 'auto',
        },
      }}
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
      {supportsVirtualTables && (
        <div style={{ marginBottom: 12, display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 8 }}>
          <Segmented
            size="small"
            value={tableMode}
            onChange={(v) => setTableMode(v as 'standard' | 'virtual')}
            options={[
              { label: t('Standard Table'), value: 'standard' },
              { label: t('Virtual Table (FTS5 / RTree)'), value: 'virtual' },
            ]}
          />
          {dbType === 'sqlite' && tableMode === 'standard' && (
            <Space wrap>
              <Checkbox checked={strictTable} onChange={(e) => setStrictTable(e.target.checked)}>
                STRICT
              </Checkbox>
              <Checkbox checked={withoutRowId} onChange={(e) => setWithoutRowId(e.target.checked)}>
                WITHOUT ROWID
              </Checkbox>
            </Space>
          )}
        </div>
      )}

      <Form layout="vertical" autoComplete="off">
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
              <Input disabled={isVirtualMode} value={comment} onChange={(e) => setComment(e.target.value)} />
            </Form.Item>
          </Col>
        </Row>

        {isVirtualMode && (
          <Row gutter={12}>
            <Col xs={24} sm={7}>
              <Form.Item label={t('Virtual Module')} required>
                <Select
                  value={virtualModule}
                  onChange={(val) => {
                    setVirtualModule(val);
                    if (val === 'rtree') {
                      setVirtualArgs('id, minX, maxX, minY, maxY');
                    } else if (val === 'fts5') {
                      setVirtualArgs("title, body, tokenize='unicode61'");
                    } else {
                      setVirtualArgs('title, body');
                    }
                  }}
                  options={[
                    { label: 'fts5 (Full-Text Search 5)', value: 'fts5' },
                    { label: 'fts4 (Full-Text Search 4)', value: 'fts4' },
                    { label: 'rtree (Spatial R-Tree)', value: 'rtree' },
                  ]}
                />
              </Form.Item>
            </Col>
            <Col xs={24} sm={17}>
              <Form.Item label={t('Module Columns / Arguments')} required>
                <Input
                  style={{ fontFamily: 'monospace' }}
                  placeholder="title, body, tokenize='unicode61'"
                  value={virtualArgs}
                  onChange={(e) => setVirtualArgs(e.target.value)}
                />
              </Form.Item>
            </Col>
          </Row>
        )}

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

      {!isVirtualMode && (
        <>
          <div style={{ marginBottom: 8, display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 8 }}>
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
            scroll={{ x: supportsGeneratedCols ? 1040 : 860 }}
            columns={colTableColumns}
          />

          {supportsChecks && (
            <div style={{ marginTop: 12 }}>
              <div style={{ marginBottom: 6, display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 8 }}>
                <Typography.Text strong>{t('Check Constraints ({count})', { count: checks.length })}</Typography.Text>
                <Button
                  size="small"
                  type="dashed"
                  icon={<PlusOutlined />}
                  onClick={() =>
                    setChecks((prev) => [
                      ...prev,
                      {
                        name: `chk_${(tableName.trim() || 'table')}_${prev.length + 1}`,
                        expression: '',
                        enforced: true,
                      },
                    ])
                  }
                >
                  {t('Add Check')}
                </Button>
              </div>
              {checks.map((ck, idx) => (
                <div key={idx} style={{ display: 'flex', gap: 8, marginBottom: 6, flexWrap: 'wrap', alignItems: 'center' }}>
                  <Input
                    size="small"
                    style={{ width: isMobile ? '100%' : 180 }}
                    placeholder={t('Constraint Name')}
                    value={ck.name}
                    onChange={(e) => updateCheck(idx, { name: e.target.value })}
                  />
                  <Input
                    size="small"
                    style={{ flex: 1, minWidth: 180, fontFamily: 'monospace' }}
                    placeholder={t('Check Expression, e.g. amount >= 0')}
                    value={ck.expression}
                    onChange={(e) => updateCheck(idx, { expression: e.target.value })}
                  />
                  <Button
                    size="small"
                    type="text"
                    danger
                    icon={<DeleteOutlined />}
                    onClick={() => setChecks((prev) => prev.filter((_, i) => i !== idx))}
                  />
                </div>
              ))}
            </div>
          )}
        </>
      )}

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
