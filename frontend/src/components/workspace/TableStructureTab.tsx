import React, { useEffect, useState } from 'react';
import {
  Alert,
  AutoComplete,
  Button,
  Card,
  Checkbox,
  Col,
  Descriptions,
  Dropdown,
  Form,
  Input,
  Modal,
  Popconfirm,
  Radio,
  Row,
  Select,
  Space,
  Tabs,
  Tag,
  Tooltip,
  Typography,
  message,
} from 'antd';
import {
  ClearOutlined,
  CodeOutlined,
  CopyOutlined,
  DeleteOutlined,
  EditOutlined,
  EyeOutlined,
  LinkOutlined,
  PlusOutlined,
  ReloadOutlined,
  SettingOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons';
import { api } from '../../api/client';
import { ResizableTable } from '../common/ResizableTable';
import { SqlCodeViewer } from '../common/SqlCodeViewer';
import { useResponsive } from '../../hooks/useResponsive';
import { useI18n } from '../../i18n/useI18n';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import type {
  AlterAction,
  ColumnDef,
  ForeignKeyDef,
  IndexDef,
  ObjectItem,
  PartitionInfo,
  TableStructure,
  WorkspaceTab,
} from '../../types';
import { formatBytes } from '../../utils/format';
import { writeTextToClipboard } from '../../utils/tableClipboard';
import { ContextNavBar, type TransferModalMode, type TransferOpenOptions } from './ContextNavBar';

const COMMON_TYPES = [
  'BIGINT',
  'INT',
  'SMALLINT',
  'VARCHAR(64)',
  'VARCHAR(255)',
  'TEXT',
  'BOOLEAN',
  'DECIMAL(18,2)',
  'DOUBLE',
  'DATE',
  'DATETIME',
  'TIMESTAMP',
  'JSON',
];

interface Props {
  tab: WorkspaceTab;
  onOpenTransfer?: (mode: TransferModalMode, tableName?: string, opts?: TransferOpenOptions) => void;
}

export const TableStructureTab: React.FC<Props> = ({ tab, onOpenTransfer }) => {
  const { drivers, darkMode, updateTab, closeTab, bumpTreeRefresh, openTab, isDesktop, role } =
    useWorkbenchStore();
  const canWrite = isDesktop || role !== 'readonly';
  const { isMobile } = useResponsive();
  const { t } = useI18n();
  const drv = drivers.find((d) => d.type === tab.dbType);
  const [loading, setLoading] = useState(false);
  const [structure, setStructure] = useState<TableStructure | null>(null);
  const [partitions, setPartitions] = useState<PartitionInfo[]>([]);
  const [siblingTables, setSiblingTables] = useState<ObjectItem[]>([]);

  // Add / Modify Column Modal
  const [colModalOpen, setColModalOpen] = useState(false);
  const [editingCol, setEditingCol] = useState<ColumnDef | null>(null);
  const [colPreviewSQLs, setColPreviewSQLs] = useState<string[]>([]);
  const [colSaving, setColSaving] = useState(false);
  const [colForm] = Form.useForm();

  // Add / Edit Index Modal
  const [idxModalOpen, setIdxModalOpen] = useState(false);
  const [editingIdx, setEditingIdx] = useState<IndexDef | null>(null);
  const [idxForm] = Form.useForm();

  // Add Foreign Key Modal
  const [fkModalOpen, setFkModalOpen] = useState(false);
  const [fkRefCols, setFkRefCols] = useState<string[]>([]);
  const [fkForm] = Form.useForm();

  // Table Operations state
  const [renameVal, setRenameVal] = useState('');
  const [commentVal, setCommentVal] = useState('');
  const [cloneName, setCloneName] = useState('');
  const [cloneMode, setCloneMode] = useState<'COPY_DATA' | 'COPY_STRUCTURE'>('COPY_DATA');
  const [opLoading, setOpLoading] = useState(false);

  const loadStructure = async () => {
    if (!tab.table) return;
    setLoading(true);
    try {
      const st = await api.getTableStructure(tab.connectionId, tab.database, tab.schema || '', tab.table);
      setStructure(st);
      setRenameVal(tab.table);
      setCommentVal(st.comment || '');
      setCloneName(`${tab.table}_copy`);
      if (drv?.supportsPartitions) {
        const parts = await api.listPartitions(tab.connectionId, tab.database, tab.table);
        setPartitions(parts || []);
      }
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    loadStructure();
  }, [tab.connectionId, tab.database, tab.schema, tab.table]);

  const typeOptions = Array.from(new Set([...(drv?.defaultTypes || []), ...COMMON_TYPES])).map((tp) => ({
    value: tp,
  }));

  const openAddColumnModal = () => {
    setEditingCol(null);
    setColPreviewSQLs([]);
    colForm.resetFields();
    colForm.setFieldsValue({
      name: '',
      type: drv?.defaultTypes?.[0] || 'VARCHAR(255)',
      nullable: true,
      defaultValue: '',
      comment: '',
      isPrimaryKey: false,
      isAutoIncrement: false,
      afterColumn: '',
    });
    setColModalOpen(true);
  };

  const openEditColumnModal = (col: ColumnDef) => {
    setEditingCol(col);
    setColPreviewSQLs([]);
    colForm.setFieldsValue({
      name: col.name,
      type: col.type,
      nullable: Boolean(col.nullable),
      defaultValue: col.defaultValue ?? '',
      comment: col.comment ?? '',
      isPrimaryKey: Boolean(col.isPrimaryKey),
      isAutoIncrement: Boolean(col.isAutoIncrement),
      afterColumn: '',
    });
    setColModalOpen(true);
  };

  const buildColumnAlterActions = (vals: any): AlterAction[] => {
    const colPayload: ColumnDef = {
      name: String(vals.name || '').trim(),
      type: String(vals.type || '').trim(),
      nullable: Boolean(vals.nullable),
      defaultValue: vals.defaultValue !== undefined ? String(vals.defaultValue) : '',
      comment: vals.comment !== undefined ? String(vals.comment) : '',
      isPrimaryKey: Boolean(vals.isPrimaryKey),
      isAutoIncrement: Boolean(vals.isAutoIncrement),
    };
    if (!editingCol) {
      return [
        {
          action: 'ADD_COLUMN',
          column: colPayload,
          afterColumn: vals.afterColumn || undefined,
        },
      ];
    }
    return [
      {
        action: 'MODIFY_COLUMN',
        oldName: editingCol.name,
        column: colPayload,
        afterColumn: vals.afterColumn || undefined,
      },
    ];
  };

  const handlePreviewColumnSQL = async () => {
    try {
      const vals = await colForm.validateFields();
      const sqls = await api.alterTable(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        table: tab.table || '',
        actions: buildColumnAlterActions(vals),
        previewOnly: true,
      });
      setColPreviewSQLs(sqls);
    } catch (err: any) {
      if (!err?.errorFields) message.error(err.message);
    }
  };

  const handleSubmitColumn = async () => {
    try {
      const vals = await colForm.validateFields();
      setColSaving(true);
      await api.alterTable(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        table: tab.table || '',
        actions: buildColumnAlterActions(vals),
      });
      message.success(
        editingCol ? t('Column {name} updated', { name: vals.name }) : t('Column {name} added', { name: vals.name })
      );
      setColModalOpen(false);
      colForm.resetFields();
      await loadStructure();
    } catch (err: any) {
      if (!err?.errorFields) message.error(err.message);
    } finally {
      setColSaving(false);
    }
  };

  const handleDropColumn = async (colName: string) => {
    try {
      await api.alterTable(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        table: tab.table || '',
        actions: [
          {
            action: 'DROP_COLUMN',
            oldName: colName,
            column: { name: colName, type: '', nullable: true, isPrimaryKey: false, isAutoIncrement: false },
          },
        ],
      });
      message.success(t('Column {name} dropped', { name: colName }));
      await loadStructure();
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleAddIndex = async () => {
    try {
      const vals = await idxForm.validateFields();
      const cols: string[] = vals.columns || [];
      const idxName = String(vals.name || '').trim() || `idx_${tab.table}_${cols.join('_')}`;
      const actions: AlterAction[] = [];
      if (editingIdx) {
        actions.push({
          action: 'DROP_INDEX',
          index: { name: editingIdx.name, columns: [], unique: false, primary: false },
        });
      }
      actions.push({
        action: 'ADD_INDEX',
        index: {
          name: idxName,
          columns: cols,
          unique: Boolean(vals.unique),
          primary: false,
        },
      });
      await api.alterTable(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        table: tab.table || '',
        actions,
      });
      message.success(
        editingIdx ? t('Index {name} updated', { name: idxName }) : t('Index {name} created', { name: idxName })
      );
      setIdxModalOpen(false);
      setEditingIdx(null);
      idxForm.resetFields();
      await loadStructure();
    } catch (err: any) {
      if (!err?.errorFields) message.error(err.message);
    }
  };

  const buildGeneratedSQL = (kind: 'select' | 'insert' | 'update' | 'ddl'): string => {
    const cols = structure?.columns || [];
    const tblName = tab.table || 'table_name';
    const q = (id: string) =>
      tab.dbType === 'mysql' || tab.dbType === 'mariadb' || tab.dbType === 'tidb' || tab.dbType === 'oceanbase' || tab.dbType === 'clickhouse'
        ? `\`${id}\``
        : tab.dbType === 'sqlserver'
          ? `[${id}]`
          : `"${id}"`;
    if (kind === 'ddl') {
      return structure?.ddl || `-- DDL for ${tblName}`;
    }
    const colNames = cols.length ? cols.map((c) => q(c.name)) : ['*'];
    if (kind === 'select') {
      return `SELECT ${colNames.join(', ')}\nFROM ${q(tblName)}\nLIMIT 100;`;
    }
    const writeCols = cols.filter((c) => !c.isAutoIncrement);
    const targetCols = writeCols.length ? writeCols : cols;
    if (kind === 'insert') {
      const names = targetCols.map((c) => q(c.name)).join(', ');
      const placeholders = targetCols.map((c) => `'${c.name}_val'`).join(', ');
      return `INSERT INTO ${q(tblName)} (${names})\nVALUES (${placeholders});`;
    }
    const pkCols = cols.filter((c) => c.isPrimaryKey);
    const setCols = targetCols.filter((c) => !c.isPrimaryKey);
    const setClause = (setCols.length ? setCols : targetCols)
      .map((c) => `  ${q(c.name)} = '${c.name}_val'`)
      .join(',\n');
    const whereClause = pkCols.length
      ? pkCols.map((c) => `${q(c.name)} = 1`).join(' AND ')
      : '1 = 0';
    return `UPDATE ${q(tblName)}\nSET\n${setClause}\nWHERE ${whereClause};`;
  };

  const handleGenerateSQL = (kind: 'select' | 'insert' | 'update' | 'ddl', openConsole: boolean) => {
    const sql = buildGeneratedSQL(kind);
    if (openConsole) {
      openTab({
        key: `sql:${tab.connectionId}:${tab.database}:gen:${tab.table}:${kind}`,
        title: `${kind.toUpperCase()} - ${tab.table}`,
        kind: 'sql_console',
        connectionId: tab.connectionId,
        connName: tab.connName,
        dbType: tab.dbType,
        database: tab.database,
        schema: tab.schema,
        table: tab.table,
        initialSQL: sql,
      });
    } else {
      writeTextToClipboard(sql);
      message.success(t('Copied SQL template to clipboard'));
    }
  };

  const handleDropIndex = async (idxName: string) => {
    try {
      await api.alterTable(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        table: tab.table || '',
        actions: [{ action: 'DROP_INDEX', index: { name: idxName, columns: [], unique: false, primary: false } }],
      });
      message.success(t('Index {name} dropped', { name: idxName }));
      await loadStructure();
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const openAddFKModal = async () => {
    fkForm.resetFields();
    fkForm.setFieldsValue({ onDelete: 'RESTRICT', onUpdate: 'RESTRICT' });
    setFkModalOpen(true);
    try {
      const objs = await api.listObjects(tab.connectionId, tab.database, tab.schema || '');
      setSiblingTables(objs.tables || []);
    } catch {
      setSiblingTables([]);
    }
  };

  const handleRefTableChange = async (refTable: string) => {
    fkForm.setFieldValue('refColumns', []);
    try {
      const st = await api.getTableStructure(tab.connectionId, tab.database, tab.schema || '', refTable);
      setFkRefCols((st.columns || []).map((c) => c.name));
    } catch {
      setFkRefCols([]);
    }
  };

  const handleAddForeignKey = async () => {
    try {
      const vals = await fkForm.validateFields();
      const fkName =
        String(vals.name || '').trim() || `fk_${tab.table}_${(vals.columns || []).join('_')}`;
      await api.alterTable(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        table: tab.table || '',
        actions: [
          {
            action: 'ADD_FOREIGN_KEY',
            foreignKey: {
              name: fkName,
              columns: vals.columns,
              refTable: vals.refTable,
              refColumns: vals.refColumns,
              onDelete: vals.onDelete,
              onUpdate: vals.onUpdate,
            },
          },
        ],
      });
      message.success(t('Foreign key {name} created', { name: fkName }));
      setFkModalOpen(false);
      await loadStructure();
    } catch (err: any) {
      if (!err?.errorFields) message.error(err.message);
    }
  };

  const handleDropForeignKey = async (fkName: string) => {
    try {
      await api.alterTable(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        table: tab.table || '',
        actions: [
          {
            action: 'DROP_FOREIGN_KEY',
            oldName: fkName,
            foreignKey: { name: fkName, columns: [], refTable: '', refColumns: [] },
          },
        ],
      });
      message.success(t('Foreign key {name} dropped', { name: fkName }));
      await loadStructure();
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleRenameTable = async () => {
    const newTbl = renameVal.trim();
    if (!newTbl || newTbl === tab.table) {
      message.warning(t('Please enter a different table name'));
      return;
    }
    setOpLoading(true);
    try {
      await api.objectAction(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        objectName: tab.table || '',
        objectType: 'TABLE',
        action: 'RENAME',
        targetName: newTbl,
      });
      message.success(t('Table renamed to {name}', { name: newTbl }));
      bumpTreeRefresh();
      updateTab(tab.key, {
        key: `struct:${tab.connectionId}:${tab.database}:${tab.schema || ''}:${newTbl}`,
        title: `${newTbl} [${t('Structure')}]`,
        table: newTbl,
      });
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setOpLoading(false);
    }
  };

  const handleUpdateTableComment = async () => {
    setOpLoading(true);
    try {
      await api.objectAction(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        objectName: tab.table || '',
        objectType: 'TABLE',
        action: 'COMMENT',
        comment: commentVal,
      });
      message.success(t('Table comment updated'));
      await loadStructure();
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setOpLoading(false);
    }
  };

  const handleCloneTable = async () => {
    const target = cloneName.trim();
    if (!target) {
      message.warning(t('Please enter target table name'));
      return;
    }
    setOpLoading(true);
    try {
      await api.objectAction(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        objectName: tab.table || '',
        objectType: 'TABLE',
        action: cloneMode,
        targetName: target,
      });
      message.success(t('Cloned table to {name}', { name: target }));
      bumpTreeRefresh();
      openTab({
        key: `data:${tab.connectionId}:${tab.database}:${tab.schema || ''}:${target}`,
        title: target,
        kind: 'table_data',
        connectionId: tab.connectionId,
        connName: tab.connName,
        dbType: tab.dbType,
        database: tab.database,
        schema: tab.schema,
        table: target,
      });
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setOpLoading(false);
    }
  };

  const handleMaintenanceOp = async (action: 'OPTIMIZE' | 'ANALYZE' | 'TRUNCATE' | 'DROP') => {
    setOpLoading(true);
    try {
      await api.objectAction(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        objectName: tab.table || '',
        objectType: 'TABLE',
        action,
      });
      message.success(t('Executed {action} on table {name}', { action, name: tab.table || '' }));
      bumpTreeRefresh();
      if (action === 'DROP') {
        closeTab(tab.key);
      } else {
        await loadStructure();
      }
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setOpLoading(false);
    }
  };

  const handlePartitionAction = async (partId: string, action: string) => {
    try {
      await api.managePartition(tab.connectionId, tab.database, tab.table || '', partId, action);
      message.success(t('Executed {action} on partition {id}', { action, id: partId }));
      await loadStructure();
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const subTabs: any[] = [
    {
      key: 'columns',
      label: t('Columns ({count})', { count: structure?.columns.length || 0 }),
      children: (
        <div>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 10, flexWrap: 'wrap', gap: 8 }}>
            <Space wrap>
              {canWrite && (
                <Button type="primary" size="small" icon={<PlusOutlined />} onClick={openAddColumnModal}>
                  {t('Add Column (ADD COLUMN)')}
                </Button>
              )}
              {canWrite && !isMobile && (
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  {t(
                    'Click "Modify" on any column to rename or alter type, NULL constraint, default value, or comment (SQLite uses transactional lossless rebuild).'
                  )}
                </Typography.Text>
              )}
            </Space>
            <Space size={6} wrap>
              <Dropdown
                menu={{
                  items: [
                    { key: 'cp_sel', label: 'SELECT (Copy)', onClick: () => handleGenerateSQL('select', false) },
                    { key: 'op_sel', label: 'SELECT (SQL Console)', onClick: () => handleGenerateSQL('select', true) },
                    { key: 'cp_ins', label: 'INSERT (Copy)', onClick: () => handleGenerateSQL('insert', false) },
                    { key: 'op_ins', label: 'INSERT (SQL Console)', onClick: () => handleGenerateSQL('insert', true) },
                    { key: 'cp_upd', label: 'UPDATE (Copy)', onClick: () => handleGenerateSQL('update', false) },
                    { key: 'op_upd', label: 'UPDATE (SQL Console)', onClick: () => handleGenerateSQL('update', true) },
                    { type: 'divider' },
                    { key: 'cp_ddl', label: 'CREATE TABLE DDL (Copy)', onClick: () => handleGenerateSQL('ddl', false) },
                  ],
                }}
              >
                <Button size="small" icon={<CodeOutlined />}>
                  {t('Generate SQL')}
                </Button>
              </Dropdown>
              <Button size="small" icon={<ReloadOutlined />} onClick={loadStructure}>
                {t('Refresh Structure')}
              </Button>
            </Space>
          </div>
          <ResizableTable<ColumnDef>
            rowKey="name"
            size="small"
            loading={loading}
            pagination={false}
            scroll={{ x: 860 }}
            dataSource={structure?.columns || []}
            columns={[
              {
                title: t('Column Name'),
                dataIndex: 'name',
                width: 190,
                render: (v, r) => (
                  <Space size={4} style={{ whiteSpace: 'nowrap' }}>
                    <Typography.Text strong>{v}</Typography.Text>
                    {r.isPrimaryKey && <Tag color="gold" style={{ margin: 0 }}>PK</Tag>}
                    {r.isAutoIncrement && <Tag color="cyan" style={{ margin: 0 }}>AUTO_INC</Tag>}
                  </Space>
                ),
              },
              { title: t('Data Type'), dataIndex: 'type', width: 145, render: (v) => <Tag color="blue" style={{ margin: 0 }}>{v}</Tag> },
              {
                title: t('Nullable'),
                dataIndex: 'nullable',
                width: 105,
                render: (v) => (v ? <Tag color="default" style={{ margin: 0 }}>YES</Tag> : <Tag color="processing" style={{ margin: 0 }}>NOT NULL</Tag>),
              },
              { title: t('Default'), dataIndex: 'defaultValue', width: 120, ellipsis: true, render: (v) => (v !== undefined && v !== '' ? v : '-') },
              { title: t('Comment'), dataIndex: 'comment', width: 165, ellipsis: true, render: (v) => v || '-' },
              ...(canWrite
                ? [
                    {
                      title: t('Actions'),
                      width: 135,
                      render: (_: any, r: ColumnDef) => (
                        <Space size={4} style={{ whiteSpace: 'nowrap' }}>
                          <Button
                            size="small"
                            type="link"
                            icon={<EditOutlined />}
                            onClick={() => openEditColumnModal(r)}
                          >
                            {t('Modify')}
                          </Button>
                          <Popconfirm
                            title={t('Drop column {name}?', { name: r.name })}
                            onConfirm={() => handleDropColumn(r.name)}
                          >
                            <Tooltip title={r.isPrimaryKey ? 'PK' : t('Actions')}>
                              <Button size="small" type="text" danger icon={<DeleteOutlined />} disabled={r.isPrimaryKey} />
                            </Tooltip>
                          </Popconfirm>
                        </Space>
                      ),
                    },
                  ]
                : []),
            ]}
          />
        </div>
      ),
    },
    {
      key: 'indexes',
      label: t('Indexes ({count})', { count: structure?.indexes.length || 0 }),
      children: (
        <div>
          {canWrite && (
            <div style={{ marginBottom: 10 }}>
              <Button
                type="primary"
                size="small"
                icon={<PlusOutlined />}
                onClick={() => {
                  setEditingIdx(null);
                  idxForm.resetFields();
                  setIdxModalOpen(true);
                }}
              >
                {t('Create Index (CREATE INDEX)')}
              </Button>
            </div>
          )}
          <ResizableTable<IndexDef>
            rowKey="name"
            size="small"
            pagination={false}
            scroll={{ x: 760 }}
            dataSource={structure?.indexes || []}
            columns={[
              { title: t('Index Name'), dataIndex: 'name', width: 180 },
              {
                title: t('Columns'),
                dataIndex: 'columns',
                width: 200,
                render: (cols: string[]) => (cols?.length ? cols.map((c) => <Tag key={c}>{c}</Tag>) : '-'),
              },
              {
                title: t('Constraints'),
                width: 140,
                render: (_, r) => (
                  <Space>
                    {r.primary && <Tag color="gold">PRIMARY</Tag>}
                    {r.unique && <Tag color="green">UNIQUE</Tag>}
                  </Space>
                ),
              },
              { title: t('Type / Definition'), dataIndex: 'indexType', width: 160, ellipsis: true },
              ...(canWrite
                ? [
                    {
                      title: t('Actions'),
                      width: 115,
                      render: (_: any, r: IndexDef) =>
                        !r.primary ? (
                          <Space size={4}>
                            <Button
                              size="small"
                              type="text"
                              icon={<EditOutlined />}
                              title={t('Modify')}
                              onClick={() => {
                                setEditingIdx(r);
                                idxForm.setFieldsValue({
                                  name: r.name,
                                  columns: r.columns || [],
                                  unique: Boolean(r.unique),
                                });
                                setIdxModalOpen(true);
                              }}
                            />
                            <Popconfirm
                              title={t('Drop index {name}?', { name: r.name })}
                              onConfirm={() => handleDropIndex(r.name)}
                            >
                              <Button size="small" type="text" danger icon={<DeleteOutlined />} />
                            </Popconfirm>
                          </Space>
                        ) : null,
                    },
                  ]
                : []),
            ]}
          />
        </div>
      ),
    },
    {
      key: 'fks',
      label: t('Foreign Keys ({count})', { count: structure?.foreignKeys.length || 0 }),
      children: (
        <div>
          {canWrite && (
            <div style={{ marginBottom: 10 }}>
              <Button type="primary" size="small" icon={<LinkOutlined />} onClick={openAddFKModal}>
                {t('Add Foreign Key (ADD FOREIGN KEY)')}
              </Button>
            </div>
          )}
          <ResizableTable<ForeignKeyDef>
            rowKey="name"
            size="small"
            pagination={false}
            scroll={{ x: 920 }}
            dataSource={structure?.foreignKeys || []}
            columns={[
              { title: t('FK Name'), dataIndex: 'name', width: 170 },
              { title: t('Local Columns'), dataIndex: 'columns', width: 150, render: (c: string[]) => c?.join(', ') },
              { title: t('Referenced Table'), dataIndex: 'refTable', width: 150, render: (v) => <Tag color="purple">{v}</Tag> },
              { title: t('Referenced Columns'), dataIndex: 'refColumns', width: 150, render: (c: string[]) => c?.join(', ') },
              { title: 'ON UPDATE', dataIndex: 'onUpdate', width: 110, render: (v) => v || '-' },
              { title: 'ON DELETE', dataIndex: 'onDelete', width: 110, render: (v) => v || '-' },
              ...(canWrite
                ? [
                    {
                      title: t('Actions'),
                      width: 90,
                      render: (_: any, r: ForeignKeyDef) => (
                        <Popconfirm
                          title={t('Drop foreign key {name}?', { name: r.name })}
                          onConfirm={() => handleDropForeignKey(r.name)}
                        >
                          <Button size="small" type="text" danger icon={<DeleteOutlined />} />
                        </Popconfirm>
                      ),
                    },
                  ]
                : []),
            ]}
          />
        </div>
      ),
    },
    ...(canWrite
      ? [
          {
            key: 'operations',
      label: (
        <Space size={4}>
          <SettingOutlined />
          <span>{t('Operations')}</span>
        </Space>
      ),
      children: (
        <Row gutter={[14, 14]}>
          <Col xs={24} md={12}>
            <Card size="small" title={t('Rename Table (RENAME TABLE)')}>
              <Space.Compact style={{ width: '100%' }}>
                <Input
                  value={renameVal}
                  onChange={(e) => setRenameVal(e.target.value)}
                  placeholder={t('Target Table Name')}
                />
                <Button type="primary" loading={opLoading} onClick={handleRenameTable}>
                  {t('Rename')}
                </Button>
              </Space.Compact>
            </Card>
          </Col>

          <Col xs={24} md={12}>
            <Card size="small" title={t('Table Comment')}>
              <Space.Compact style={{ width: '100%' }}>
                <Input
                  value={commentVal}
                  onChange={(e) => setCommentVal(e.target.value)}
                  placeholder={t('Table Comment')}
                />
                <Button type="primary" loading={opLoading} onClick={handleUpdateTableComment}>
                  {t('Save Comment')}
                </Button>
              </Space.Compact>
            </Card>
          </Col>

          <Col xs={24} md={12}>
            <Card size="small" title={t('Clone / Copy Table')}>
              <Space direction="vertical" style={{ width: '100%' }} size={10}>
                <Radio.Group value={cloneMode} onChange={(e) => setCloneMode(e.target.value)}>
                  <Radio value="COPY_DATA">{t('Structure & Data')}</Radio>
                  <Radio value="COPY_STRUCTURE">{t('Structure Only')}</Radio>
                </Radio.Group>
                <Space.Compact style={{ width: '100%' }}>
                  <Input
                    value={cloneName}
                    onChange={(e) => setCloneName(e.target.value)}
                    placeholder={t('Target Table Name')}
                  />
                  <Button type="primary" icon={<CopyOutlined />} loading={opLoading} onClick={handleCloneTable}>
                    {t('Clone Now')}
                  </Button>
                </Space.Compact>
              </Space>
            </Card>
          </Col>

          <Col xs={24} md={12}>
            <Card size="small" title={t('Maintenance & Danger Zone')}>
              <Space wrap>
                <Button
                  icon={<ThunderboltOutlined />}
                  loading={opLoading}
                  onClick={() => handleMaintenanceOp('OPTIMIZE')}
                >
                  {t('Optimize Table (OPTIMIZE)')}
                </Button>
                <Button loading={opLoading} onClick={() => handleMaintenanceOp('ANALYZE')}>
                  {t('Analyze Table (ANALYZE)')}
                </Button>
                <Popconfirm
                  title={t('Truncate all rows in table {name}?', { name: tab.table || '' })}
                  onConfirm={() => handleMaintenanceOp('TRUNCATE')}
                >
                  <Button danger icon={<ClearOutlined />} loading={opLoading}>
                    {t('Truncate table')}
                  </Button>
                </Popconfirm>
                <Popconfirm
                  title={t('Danger: Permanently drop {name}?', { name: tab.table || '' })}
                  onConfirm={() => handleMaintenanceOp('DROP')}
                >
                  <Button danger type="primary" icon={<DeleteOutlined />} loading={opLoading}>
                    {t('Drop Table (DROP TABLE)')}
                  </Button>
                </Popconfirm>
              </Space>
            </Card>
          </Col>
        </Row>
      ),
    },
        ]
      : []),
    {
      key: 'ddl',
      label: t('DDL Source'),
      children: (
        <div>
          <div style={{ marginBottom: 8, display: 'flex', justifyContent: 'flex-end', gap: 8, flexWrap: 'wrap' }}>
            <Button
              size="small"
              icon={<CodeOutlined />}
              onClick={() =>
                openTab({
                  key: `sql:${tab.connectionId}:${tab.database}:ddl:${tab.table}`,
                  title: `DDL - ${tab.table}`,
                  kind: 'sql_console',
                  connectionId: tab.connectionId,
                  connName: tab.connName,
                  dbType: tab.dbType,
                  database: tab.database,
                  schema: tab.schema,
                  table: tab.table,
                  initialSQL: structure?.ddl || `-- DDL for ${tab.table}`,
                })
              }
            >
              {t('Open in SQL Console')}
            </Button>
            <Button
              size="small"
              icon={<CopyOutlined />}
              onClick={() => {
                navigator.clipboard.writeText(structure?.ddl || '');
                message.success(t('DDL copied to clipboard'));
              }}
            >
              {t('Copy DDL')}
            </Button>
          </div>
          <div style={{ height: 'clamp(200px, calc(100vh - 280px), 520px)', border: '1px solid rgba(128,128,128,0.2)', borderRadius: 8, overflow: 'hidden' }}>
            <SqlCodeViewer code={structure?.ddl || '-- DDL'} darkMode={darkMode} height="100%" />
          </div>
        </div>
      ),
    },
  ];

  if (drv?.supportsPartitions) {
    subTabs.push({
      key: 'partitions',
      label: t('ClickHouse Partitions ({count})', { count: partitions.length }),
      children: (
        <ResizableTable<PartitionInfo>
          rowKey="id"
          size="small"
          pagination={false}
          scroll={{ x: 860 }}
          dataSource={partitions}
          columns={[
            { title: t('Partition ID'), dataIndex: 'id', width: 140 },
            { title: t('Partition Expression'), dataIndex: 'name', width: 160, render: (v) => <Tag color="orange">{v}</Tag> },
            { title: t('Rows'), dataIndex: 'rows', width: 110, render: (v: number) => v.toLocaleString() },
            { title: t('Disk Size'), dataIndex: 'bytesOnDisk', width: 110, render: (v: number) => formatBytes(v) },
            { title: t('Active Parts'), dataIndex: 'partsCount', width: 105 },
            ...(canWrite
              ? [
                  {
                    title: t('Partition Actions'),
                    width: 260,
                    render: (_: any, r: PartitionInfo) => (
                      <Space>
                        <Button
                          size="small"
                          icon={<ThunderboltOutlined />}
                          onClick={() => handlePartitionAction(r.id, 'OPTIMIZE')}
                        >
                          {t('Optimize (Merge)')}
                        </Button>
                        <Popconfirm
                          title={t('Detach partition {id} (DETACH)?', { id: r.id })}
                          onConfirm={() => handlePartitionAction(r.id, 'DETACH')}
                        >
                          <Button size="small">DETACH</Button>
                        </Popconfirm>
                        <Popconfirm
                          title={t('Danger: Drop partition {id} (DROP)?', { id: r.id })}
                          onConfirm={() => handlePartitionAction(r.id, 'DROP')}
                        >
                          <Button size="small" danger>
                            DROP
                          </Button>
                        </Popconfirm>
                      </Space>
                    ),
                  },
                ]
              : []),
          ]}
        />
      ),
    });
  }

  return (
    <div style={{ padding: isMobile ? '6px 4px' : '12px 16px', height: '100%', minWidth: 0, minHeight: 0, overflowY: 'auto' }}>
      <ContextNavBar tab={tab} activeSection="structure" onOpenTransfer={onOpenTransfer} />

      <Descriptions size="small" column={{ xs: 2, sm: 2, md: 4 }} style={{ marginBottom: 8 }}>
        <Descriptions.Item label={t('Table')}>
          <Typography.Text strong>{tab.table}</Typography.Text>
        </Descriptions.Item>
        <Descriptions.Item label={t('Engine')}>{structure?.engine || '-'}</Descriptions.Item>
        <Descriptions.Item label={t('Collation')}>{structure?.collation || '-'}</Descriptions.Item>
        <Descriptions.Item label={t('Table Comment')}>{structure?.comment || '-'}</Descriptions.Item>
      </Descriptions>

      <Tabs size={isMobile ? 'small' : 'middle'} tabBarGutter={isMobile ? 14 : undefined} items={subTabs} />

      {/* Add / Edit Column Modal */}
      <Modal
        open={colModalOpen}
        title={editingCol ? t('Modify Column — {name}', { name: editingCol.name }) : t('Add Column (ADD COLUMN)')}
        width="min(580px, calc(100vw - 24px))"
        onCancel={() => setColModalOpen(false)}
        footer={
          <Space wrap>
            <Button onClick={() => setColModalOpen(false)}>{t('Cancel')}</Button>
            <Button icon={<EyeOutlined />} onClick={handlePreviewColumnSQL}>
              {t('Preview SQL')}
            </Button>
            <Button type="primary" loading={colSaving} onClick={handleSubmitColumn}>
              {editingCol ? t('Save Column Changes') : t('Add Column Now')}
            </Button>
          </Space>
        }
      >
        <Form form={colForm} layout="vertical">
          <Row gutter={12}>
            <Col xs={24} sm={12}>
              <Form.Item name="name" label={t('Column Name')} rules={[{ required: true, message: t('Required') }]}>
                <Input placeholder="e.g. status" />
              </Form.Item>
            </Col>
            <Col xs={24} sm={12}>
              <Form.Item
                name="type"
                label={t('Data Type (free input)')}
                rules={[{ required: true, message: t('Required') }]}
              >
                <AutoComplete options={typeOptions} placeholder="VARCHAR(255) / BIGINT" />
              </Form.Item>
            </Col>
          </Row>

          <Row gutter={12}>
            <Col xs={24} sm={12}>
              <Form.Item name="defaultValue" label={t('Default Value (optional)')}>
                <Input placeholder="Optional" />
              </Form.Item>
            </Col>
            <Col xs={24} sm={12}>
              <Form.Item name="afterColumn" label={t('Column Position (AFTER)')}>
                <Select
                  allowClear
                  placeholder="AFTER ..."
                  options={(structure?.columns || [])
                    .filter((c) => c.name !== editingCol?.name)
                    .map((c) => ({ label: `AFTER ${c.name}`, value: c.name }))}
                />
              </Form.Item>
            </Col>
          </Row>

          <Form.Item name="comment" label={t('Comment (optional)')}>
            <Input />
          </Form.Item>

          <Space wrap size={16}>
            <Form.Item name="nullable" valuePropName="checked" noStyle>
              <Checkbox>{t('Allow NULL')}</Checkbox>
            </Form.Item>
            <Form.Item name="isPrimaryKey" valuePropName="checked" noStyle>
              <Checkbox>{t('Primary Key (PK)')}</Checkbox>
            </Form.Item>
            <Form.Item name="isAutoIncrement" valuePropName="checked" noStyle>
              <Checkbox>{t('Auto Increment')}</Checkbox>
            </Form.Item>
          </Space>

          {colPreviewSQLs.length > 0 && (
            <Alert
              type="info"
              style={{ marginTop: 12 }}
              message={t('DDL / Transactional Rebuild SQL Preview:')}
              description={
                <pre
                  style={{
                    margin: '6px 0 0',
                    maxHeight: 180,
                    overflow: 'auto',
                    fontSize: 12,
                    fontFamily: 'monospace',
                    whiteSpace: 'pre-wrap',
                  }}
                >
                  {colPreviewSQLs.join(';\n')};
                </pre>
              }
            />
          )}
        </Form>
      </Modal>

      {/* Create / Edit Index Modal */}
      <Modal
        open={idxModalOpen}
        title={editingIdx ? `${t('Modify')} — ${editingIdx.name}` : t('Create Index (CREATE INDEX)')}
        width="min(520px, calc(100vw - 24px))"
        onCancel={() => {
          setIdxModalOpen(false);
          setEditingIdx(null);
        }}
        onOk={handleAddIndex}
      >
        <Form form={idxForm} layout="vertical">
          <Form.Item name="name" label={t('Index Name (leave blank to auto-generate)')}>
            <Input placeholder={t('e.g. idx_orders_created_at')} />
          </Form.Item>
          <Form.Item
            name="columns"
            label={t('Index Columns (multi-select for composite index)')}
            rules={[{ required: true, message: t('Please select at least one column') }]}
          >
            <Select
              mode="multiple"
              options={(structure?.columns || []).map((c) => ({ label: c.name, value: c.name }))}
            />
          </Form.Item>
          <Form.Item name="unique" valuePropName="checked">
            <Checkbox>{t('Unique Index (UNIQUE)')}</Checkbox>
          </Form.Item>
        </Form>
      </Modal>

      {/* Add Foreign Key Modal */}
      <Modal
        open={fkModalOpen}
        title={t('Add Foreign Key (ADD FOREIGN KEY)')}
        width="min(560px, calc(100vw - 24px))"
        onCancel={() => setFkModalOpen(false)}
        onOk={handleAddForeignKey}
      >
        <Form form={fkForm} layout="vertical">
          <Form.Item name="name" label={t('Constraint Name (leave blank to auto-generate)')}>
            <Input placeholder={`e.g. fk_${tab.table}_user_id`} />
          </Form.Item>
          <Form.Item
            name="columns"
            label={t('Local Columns')}
            rules={[{ required: true, message: t('Please select local columns') }]}
          >
            <Select
              mode="multiple"
              options={(structure?.columns || []).map((c) => ({ label: c.name, value: c.name }))}
            />
          </Form.Item>
          <Row gutter={12}>
            <Col xs={24} sm={12}>
              <Form.Item
                name="refTable"
                label={t('Referenced Table')}
                rules={[{ required: true, message: t('Please select referenced table') }]}
              >
                <Select
                  showSearch
                  onChange={handleRefTableChange}
                  options={siblingTables.map((tbl) => ({ label: tbl.name, value: tbl.name }))}
                />
              </Form.Item>
            </Col>
            <Col xs={24} sm={12}>
              <Form.Item
                name="refColumns"
                label={t('Referenced Columns')}
                rules={[{ required: true, message: t('Please select referenced columns') }]}
              >
                <Select
                  mode="multiple"
                  options={fkRefCols.map((c) => ({ label: c, value: c }))}
                />
              </Form.Item>
            </Col>
          </Row>
          <Row gutter={12}>
            <Col xs={24} sm={12}>
              <Form.Item name="onDelete" label={t('ON DELETE Rule')}>
                <Select
                  options={[
                    { value: 'RESTRICT', label: 'RESTRICT' },
                    { value: 'CASCADE', label: 'CASCADE' },
                    { value: 'SET NULL', label: 'SET NULL' },
                    { value: 'NO ACTION', label: 'NO ACTION' },
                  ]}
                />
              </Form.Item>
            </Col>
            <Col xs={24} sm={12}>
              <Form.Item name="onUpdate" label={t('ON UPDATE Rule')}>
                <Select
                  options={[
                    { value: 'RESTRICT', label: 'RESTRICT' },
                    { value: 'CASCADE', label: 'CASCADE' },
                    { value: 'SET NULL', label: 'SET NULL' },
                    { value: 'NO ACTION', label: 'NO ACTION' },
                  ]}
                />
              </Form.Item>
            </Col>
          </Row>
        </Form>
      </Modal>
    </div>
  );
};
