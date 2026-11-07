import React, { useCallback, useEffect, useState } from 'react';
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
  InputNumber,
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
  CheckCircleOutlined,
  ClearOutlined,
  CodeOutlined,
  CopyOutlined,
  DeleteOutlined,
  EditOutlined,
  EyeOutlined,
  LinkOutlined,
  PlusOutlined,
  ReloadOutlined,
  SafetyCertificateOutlined,
  SettingOutlined,
  SyncOutlined,
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
  CheckConstraintDef,
  ColumnDef,
  ForeignKeyDef,
  IndexDef,
  ObjectItem,
  PartitionInfo,
  ProjectionDef,
  SkippingIndexDef,
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

const CLICKHOUSE_SKIPPING_TYPES = [
  { value: 'minmax' },
  { value: 'set(100)' },
  { value: 'bloom_filter(0.01)' },
  { value: 'tokenbf_v1(10240, 3, 0)' },
  { value: 'ngrambf_v1(4, 256, 2, 0)' },
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
  const tableFeatures = drv?.capabilities?.tableFeatures || [];
  const partitionActions = drv?.capabilities?.partitionActions || [];

  const hasFeature = (feat: string) => tableFeatures.includes(feat);
  const supportsPartitions = hasFeature('PARTITIONS') || Boolean(drv?.supportsPartitions);
  const supportsGeneratedCols = hasFeature('GENERATED_COLS');
  const supportsSkippingIndexes = hasFeature('SKIPPING_INDEXES');
  const supportsIndexes = hasFeature('INDEXES') || !supportsSkippingIndexes;
  const supportsForeignKeys = hasFeature('FOREIGN_KEYS');
  const supportsChecks = hasFeature('CHECKS');
  const supportsProjections = hasFeature('PROJECTIONS');
  const supportsTTL = hasFeature('TTL');
  const supportsPragmas = hasFeature('PRAGMAS');

  const [loading, setLoading] = useState(false);
  const [structure, setStructure] = useState<TableStructure | null>(null);
  const [partitions, setPartitions] = useState<PartitionInfo[]>([]);
  const [siblingTables, setSiblingTables] = useState<ObjectItem[]>([]);

  // Add / Modify Column Modal
  const [colModalOpen, setColModalOpen] = useState(false);
  const [editingCol, setEditingCol] = useState<ColumnDef | null>(null);
  const [colPreviewSQLs, setColPreviewSQLs] = useState<string[]>([]);
  const [colSaving, setColSaving] = useState(false);
  const [colIsGenerated, setColIsGenerated] = useState(false);
  const [colForm] = Form.useForm();

  // Add / Edit Index Modal
  const [idxModalOpen, setIdxModalOpen] = useState(false);
  const [editingIdx, setEditingIdx] = useState<IndexDef | null>(null);
  const [idxForm] = Form.useForm();

  // Add Skipping Index Modal (ClickHouse)
  const [skipIdxModalOpen, setSkipIdxModalOpen] = useState(false);
  const [skipIdxPreview, setSkipIdxPreview] = useState<string[]>([]);
  const [skipIdxSaving, setSkipIdxSaving] = useState(false);
  const [skipIdxForm] = Form.useForm();

  // Add Foreign Key Modal
  const [fkModalOpen, setFkModalOpen] = useState(false);
  const [fkRefCols, setFkRefCols] = useState<string[]>([]);
  const [fkForm] = Form.useForm();

  // Add Check Constraint Modal
  const [checkModalOpen, setCheckModalOpen] = useState(false);
  const [checkPreview, setCheckPreview] = useState<string[]>([]);
  const [checkSaving, setCheckSaving] = useState(false);
  const [checkForm] = Form.useForm();

  // Add Projection Modal (ClickHouse)
  const [projModalOpen, setProjModalOpen] = useState(false);
  const [projPreview, setProjPreview] = useState<string[]>([]);
  const [projSaving, setProjSaving] = useState(false);
  const [projForm] = Form.useForm();

  // Add Partition Modal
  const [partModalOpen, setPartModalOpen] = useState(false);
  const [partPreviewSql, setPartPreviewSql] = useState('');
  const [partSaving, setPartSaving] = useState(false);
  const [partForm] = Form.useForm();

  // Table Operations & Diagnostics state
  const [renameVal, setRenameVal] = useState('');
  const [commentVal, setCommentVal] = useState('');
  const [cloneName, setCloneName] = useState('');
  const [cloneMode, setCloneMode] = useState<'COPY_DATA' | 'COPY_STRUCTURE'>('COPY_DATA');
  const [ttlExpr, setTtlExpr] = useState('');
  const [opLoading, setOpLoading] = useState(false);
  const [pragmaCheckLoading, setPragmaCheckLoading] = useState(false);
  const [pragmaCheckResult, setPragmaCheckResult] = useState<{ title: string; output: string } | null>(null);

  const loadStructure = useCallback(async () => {
    if (!tab.table) return;
    setLoading(true);
    try {
      const st = await api.getTableStructure(tab.connectionId, tab.database, tab.schema || '', tab.table);
      setStructure(st);
      setRenameVal(tab.table);
      setCommentVal(st.comment || '');
      setCloneName(`${tab.table}_copy`);
      setTtlExpr(st.tableTtl || '');
      if (supportsPartitions) {
        try {
          const parts = await api.listPartitions(tab.connectionId, tab.database, tab.table, tab.schema || '');
          setPartitions(parts || []);
        } catch {
          setPartitions([]);
        }
      }
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setLoading(false);
    }
  }, [tab.connectionId, tab.database, tab.schema, tab.table, supportsPartitions]);

  useEffect(() => {
    void loadStructure();
  }, [loadStructure]);

  const typeOptions = Array.from(new Set([...(drv?.defaultTypes || []), ...COMMON_TYPES])).map((tp) => ({
    value: tp,
  }));

  const openAddColumnModal = () => {
    setEditingCol(null);
    setColPreviewSQLs([]);
    setColIsGenerated(false);
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
      generatedExpr: '',
      generatedStorage: 'VIRTUAL',
    });
    setColModalOpen(true);
  };

  const openEditColumnModal = (col: ColumnDef) => {
    setEditingCol(col);
    setColPreviewSQLs([]);
    setColIsGenerated(Boolean(col.generatedExpr));
    colForm.setFieldsValue({
      name: col.name,
      type: col.type,
      nullable: Boolean(col.nullable),
      defaultValue: col.defaultValue ?? '',
      comment: col.comment ?? '',
      isPrimaryKey: Boolean(col.isPrimaryKey),
      isAutoIncrement: Boolean(col.isAutoIncrement),
      afterColumn: '',
      generatedExpr: col.generatedExpr ?? '',
      generatedStorage: col.generatedStorage || 'VIRTUAL',
    });
    setColModalOpen(true);
  };

  const buildColumnAlterActions = (vals: any): AlterAction[] => {
    const colPayload: ColumnDef = {
      name: String(vals.name || '').trim(),
      type: String(vals.type || '').trim(),
      nullable: Boolean(vals.nullable),
      defaultValue: !colIsGenerated && vals.defaultValue !== undefined ? String(vals.defaultValue) : '',
      comment: vals.comment !== undefined ? String(vals.comment) : '',
      isPrimaryKey: Boolean(vals.isPrimaryKey),
      isAutoIncrement: Boolean(vals.isAutoIncrement),
      generatedExpr: colIsGenerated && vals.generatedExpr ? String(vals.generatedExpr).trim() : undefined,
      generatedStorage: colIsGenerated ? String(vals.generatedStorage || 'VIRTUAL') : undefined,
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

  // ClickHouse Skipping Index handlers
  const handleSkippingIndexAction = async (
    action: 'DROP_SKIPPING_INDEX' | 'MATERIALIZE_SKIPPING_INDEX' | 'CLEAR_SKIPPING_INDEX',
    item: SkippingIndexDef
  ) => {
    try {
      await api.alterTable(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        table: tab.table || '',
        actions: [{ action, oldName: item.name, skippingIndex: item }],
      });
      message.success(t('Executed {action} on {name}', { action, name: item.name }));
      await loadStructure();
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handlePreviewOrAddSkippingIndex = async (previewOnly: boolean) => {
    try {
      const vals = await skipIdxForm.validateFields();
      if (!previewOnly) setSkipIdxSaving(true);
      const sqls = await api.alterTable(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        table: tab.table || '',
        previewOnly,
        actions: [
          {
            action: 'ADD_SKIPPING_INDEX',
            skippingIndex: {
              name: String(vals.name || '').trim(),
              type: String(vals.type || 'minmax').trim(),
              expression: String(vals.expression || '').trim(),
              granularity: Number(vals.granularity || 4),
            },
          },
        ],
      });
      if (previewOnly) {
        setSkipIdxPreview(sqls);
      } else {
        message.success(t('Skipping index {name} added', { name: vals.name }));
        setSkipIdxModalOpen(false);
        skipIdxForm.resetFields();
        setSkipIdxPreview([]);
        await loadStructure();
      }
    } catch (err: any) {
      if (!err?.errorFields) message.error(err.message);
    } finally {
      if (!previewOnly) setSkipIdxSaving(false);
    }
  };

  // Check Constraint handlers
  const handlePreviewOrAddCheck = async (previewOnly: boolean) => {
    try {
      const vals = await checkForm.validateFields();
      const chkName = String(vals.name || '').trim() || `chk_${tab.table}_${Date.now().toString().slice(-4)}`;
      if (!previewOnly) setCheckSaving(true);
      const sqls = await api.alterTable(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        table: tab.table || '',
        previewOnly,
        actions: [
          {
            action: 'ADD_CHECK',
            check: {
              name: chkName,
              expression: String(vals.expression || '').trim(),
              enforced: vals.enforced !== false,
            },
          },
        ],
      });
      if (previewOnly) {
        setCheckPreview(sqls);
      } else {
        message.success(t('Check constraint {name} added', { name: chkName }));
        setCheckModalOpen(false);
        checkForm.resetFields();
        setCheckPreview([]);
        await loadStructure();
      }
    } catch (err: any) {
      if (!err?.errorFields) message.error(err.message);
    } finally {
      if (!previewOnly) setCheckSaving(false);
    }
  };

  const handleDropCheck = async (chk: CheckConstraintDef) => {
    try {
      await api.alterTable(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        table: tab.table || '',
        actions: [{ action: 'DROP_CHECK', oldName: chk.name, check: chk }],
      });
      message.success(t('Check constraint {name} dropped', { name: chk.name }));
      await loadStructure();
    } catch (err: any) {
      message.error(err.message);
    }
  };

  // ClickHouse Projection handlers
  const handleProjectionAction = async (
    action: 'DROP_PROJECTION' | 'MATERIALIZE_PROJECTION' | 'CLEAR_PROJECTION',
    proj: ProjectionDef
  ) => {
    try {
      await api.alterTable(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        table: tab.table || '',
        actions: [{ action, oldName: proj.name, projection: proj }],
      });
      message.success(t('Executed {action} on {name}', { action, name: proj.name }));
      await loadStructure();
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handlePreviewOrAddProjection = async (previewOnly: boolean) => {
    try {
      const vals = await projForm.validateFields();
      if (!previewOnly) setProjSaving(true);
      const sqls = await api.alterTable(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        table: tab.table || '',
        previewOnly,
        actions: [
          {
            action: 'ADD_PROJECTION',
            projection: {
              name: String(vals.name || '').trim(),
              query: String(vals.query || '').trim(),
            },
          },
        ],
      });
      if (previewOnly) {
        setProjPreview(sqls);
      } else {
        message.success(t('Projection {name} added', { name: vals.name }));
        setProjModalOpen(false);
        projForm.resetFields();
        setProjPreview([]);
        await loadStructure();
      }
    } catch (err: any) {
      if (!err?.errorFields) message.error(err.message);
    } finally {
      if (!previewOnly) setProjSaving(false);
    }
  };

  // ClickHouse TTL handlers
  const handleModifyTTL = async (remove = false) => {
    if (!remove && !ttlExpr.trim()) {
      message.warning(t('Please enter a TTL expression'));
      return;
    }
    setOpLoading(true);
    try {
      await api.alterTable(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        table: tab.table || '',
        actions: [
          remove
            ? { action: 'REMOVE_TTL' }
            : { action: 'MODIFY_TTL', ttlExpr: ttlExpr.trim() },
        ],
      });
      message.success(remove ? t('Table TTL removed') : t('Table TTL updated'));
      await loadStructure();
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setOpLoading(false);
    }
  };

  // SQLite PRAGMA & Integrity handlers
  const handleSetPragma = async (pragmaKey: string, pragmaValue: string) => {
    try {
      await api.alterTable(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        table: tab.table || '',
        actions: [{ action: 'SET_PRAGMA', pragmaKey, pragmaValue }],
      });
      message.success(`PRAGMA ${pragmaKey} = ${pragmaValue}`);
      await loadStructure();
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleRunIntegrityCheck = async (kind: 'quick_check' | 'foreign_key_check') => {
    setPragmaCheckLoading(true);
    try {
      const sql = kind === 'quick_check' ? 'PRAGMA quick_check;' : 'PRAGMA foreign_key_check;';
      const res = await api.executeQuery(tab.connectionId, tab.database, sql, 100);
      const first = res.results?.[0];
      const rows = first?.rows || [];
      const output =
        rows.length === 0
          ? 'OK (0 issues found)'
          : rows.map((r) => r.join(' | ')).join('\n');
      setPragmaCheckResult({
        title: sql,
        output,
      });
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setPragmaCheckLoading(false);
    }
  };

  const buildGeneratedSQL = (kind: 'select' | 'insert' | 'update' | 'ddl'): string => {
    const cols = structure?.columns || [];
    const tblName = tab.table || 'table_name';
    const q = (id: string) =>
      tab.dbType === 'mysql' ||
      tab.dbType === 'mariadb' ||
      tab.dbType === 'tidb' ||
      tab.dbType === 'oceanbase' ||
      tab.dbType === 'clickhouse'
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
    const writeCols = cols.filter((c) => !c.isAutoIncrement && !c.generatedExpr);
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

  const openAddFKModal = async () => {
    setFkRefCols([]);
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
      await api.managePartition(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        table: tab.table || '',
        partitionId: partId,
        action,
      });
      message.success(t('Executed {action} on partition {id}', { action, id: partId }));
      await loadStructure();
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handlePreviewOrAddPartition = async (previewOnly: boolean) => {
    try {
      const vals = await partForm.validateFields();
      if (!previewOnly) setPartSaving(true);
      const res = await api.managePartition(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        table: tab.table || '',
        partitionId: String(vals.partitionId || '').trim(),
        action: 'ADD',
        expression: String(vals.expression || '').trim(),
        previewOnly,
      });
      if (previewOnly) {
        setPartPreviewSql(res.sql || '');
      } else {
        message.success(res.message || t('Partition added'));
        setPartModalOpen(false);
        partForm.resetFields();
        setPartPreviewSql('');
        await loadStructure();
      }
    } catch (err: any) {
      if (!err?.errorFields) message.error(err.message);
    } finally {
      if (!previewOnly) setPartSaving(false);
    }
  };

  const totalIndexesCount =
    (structure?.indexes?.length || 0) + (structure?.skippingIndexes?.length || 0);

  const subTabs: any[] = [
    {
      key: 'columns',
      label: t('Columns ({count})', { count: structure?.columns.length || 0 }),
      children: (
        <div>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 10, flexWrap: 'wrap', gap: 8 }}>
            <Space wrap>
              {canWrite && (
                <Button
                  type="primary"
                  size="small"
                  style={isMobile ? { height: 34, paddingInline: 12 } : undefined}
                  icon={<PlusOutlined />}
                  onClick={openAddColumnModal}
                >
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
                trigger={['click']}
                placement="bottomRight"
                menu={{
                  items: [
                    { key: 'cp_sel', label: t('SELECT (Copy)'), onClick: () => handleGenerateSQL('select', false) },
                    { key: 'op_sel', label: t('SELECT (SQL Console)'), onClick: () => handleGenerateSQL('select', true) },
                    { key: 'cp_ins', label: t('INSERT (Copy)'), onClick: () => handleGenerateSQL('insert', false) },
                    { key: 'op_ins', label: t('INSERT (SQL Console)'), onClick: () => handleGenerateSQL('insert', true) },
                    { key: 'cp_upd', label: t('UPDATE (Copy)'), onClick: () => handleGenerateSQL('update', false) },
                    { key: 'op_upd', label: t('UPDATE (SQL Console)'), onClick: () => handleGenerateSQL('update', true) },
                    { type: 'divider' },
                    { key: 'cp_ddl', label: t('CREATE TABLE DDL (Copy)'), onClick: () => handleGenerateSQL('ddl', false) },
                  ],
                }}
              >
                <Button
                  size="small"
                  style={isMobile ? { height: 34, paddingInline: 10 } : undefined}
                  icon={<CodeOutlined />}
                >
                  {t('Generate SQL')}
                </Button>
              </Dropdown>
              <Button
                size="small"
                style={isMobile ? { height: 34, paddingInline: 10 } : undefined}
                icon={<ReloadOutlined />}
                onClick={() => void loadStructure()}
              >
                {isMobile ? '' : t('Refresh Structure')}
              </Button>
            </Space>
          </div>
          <ResizableTable<ColumnDef>
            rowKey="name"
            size="small"
            loading={loading}
            pagination={false}
            scroll={{ x: 880 }}
            dataSource={structure?.columns || []}
            columns={[
              {
                title: t('Column Name'),
                dataIndex: 'name',
                width: 210,
                render: (v, r) => (
                  <Space size={4} style={{ whiteSpace: 'nowrap' }}>
                    <Typography.Text strong>{v}</Typography.Text>
                    {r.isPrimaryKey && <Tag color="gold" style={{ margin: 0 }}>PK</Tag>}
                    {r.isAutoIncrement && <Tag color="cyan" style={{ margin: 0 }}>AUTO_INC</Tag>}
                    {r.generatedExpr && (
                      <Tooltip title={`${r.generatedStorage || 'VIRTUAL'}: ${r.generatedExpr}`}>
                        <Tag color="purple" style={{ margin: 0 }}>
                          {r.generatedStorage || 'GENERATED'}
                        </Tag>
                      </Tooltip>
                    )}
                  </Space>
                ),
              },
              { title: t('Data Type'), dataIndex: 'type', width: 145, render: (v) => <Tag color="blue" style={{ margin: 0 }}>{v}</Tag> },
              {
                title: t('Nullable'),
                dataIndex: 'nullable',
                width: 105,
                render: (v) =>
                  v ? (
                    <Tag color="default" style={{ margin: 0 }}>YES</Tag>
                  ) : (
                    <Tag color="processing" style={{ margin: 0 }}>NOT NULL</Tag>
                  ),
              },
              {
                title: t('Default / Generated'),
                dataIndex: 'defaultValue',
                width: 155,
                ellipsis: true,
                render: (v, r) =>
                  r.generatedExpr
                    ? `AS (${r.generatedExpr})`
                    : v !== undefined && v !== ''
                      ? v
                      : '-',
              },
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
                            onConfirm={() => void handleDropColumn(r.name)}
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
  ];

  if (supportsIndexes || supportsSkippingIndexes || totalIndexesCount > 0) {
    subTabs.push({
      key: 'indexes',
      label: t('Indexes ({count})', { count: totalIndexesCount }),
      children: (
        <div>
          {supportsIndexes && (
            <>
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
                                  onConfirm={() => void handleDropIndex(r.name)}
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
            </>
          )}

          {supportsSkippingIndexes && (
            <div style={{ marginTop: supportsIndexes ? 16 : 0 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 10, flexWrap: 'wrap', gap: 8 }}>
                <Typography.Text strong>{t('ClickHouse Data Skipping Indexes')}</Typography.Text>
                {canWrite && (
                  <Button
                    type="primary"
                    size="small"
                    icon={<PlusOutlined />}
                    onClick={() => {
                      skipIdxForm.resetFields();
                      skipIdxForm.setFieldsValue({ type: 'minmax', granularity: 4 });
                      setSkipIdxPreview([]);
                      setSkipIdxModalOpen(true);
                    }}
                  >
                    {t('Add Skipping Index')}
                  </Button>
                )}
              </div>
              <ResizableTable<SkippingIndexDef>
                rowKey="name"
                size="small"
                pagination={false}
                scroll={{ x: 800 }}
                dataSource={structure?.skippingIndexes || []}
                columns={[
                  { title: t('Index Name'), dataIndex: 'name', width: 160 },
                  {
                    title: t('Index Type'),
                    dataIndex: 'type',
                    width: 170,
                    render: (v) => <Tag color="cyan">{v}</Tag>,
                  },
                  { title: t('Expression'), dataIndex: 'expression', width: 190 },
                  { title: t('Granularity'), dataIndex: 'granularity', width: 105 },
                  {
                    title: t('Disk Size'),
                    dataIndex: 'bytesOnDisk',
                    width: 105,
                    render: (v?: number) => (v ? formatBytes(v) : '-'),
                  },
                  ...(canWrite
                    ? [
                        {
                          title: t('Actions'),
                          width: 220,
                          render: (_: any, r: SkippingIndexDef) => (
                            <Space size={4}>
                              <Button
                                size="small"
                                icon={<SyncOutlined />}
                                onClick={() => void handleSkippingIndexAction('MATERIALIZE_SKIPPING_INDEX', r)}
                              >
                                {t('Materialize')}
                              </Button>
                              <Button
                                size="small"
                                onClick={() => void handleSkippingIndexAction('CLEAR_SKIPPING_INDEX', r)}
                              >
                                {t('Clear')}
                              </Button>
                              <Popconfirm
                                title={t('Drop index {name}?', { name: r.name })}
                                onConfirm={() => void handleSkippingIndexAction('DROP_SKIPPING_INDEX', r)}
                              >
                                <Button size="small" danger icon={<DeleteOutlined />} />
                              </Popconfirm>
                            </Space>
                          ),
                        },
                      ]
                    : []),
                ]}
              />
            </div>
          )}
        </div>
      ),
    });
  }

  if (supportsForeignKeys || (structure?.foreignKeys?.length || 0) > 0) {
    subTabs.push({
      key: 'fks',
      label: t('Foreign Keys ({count})', { count: structure?.foreignKeys.length || 0 }),
      children: (
        <div>
          {canWrite && (
            <div style={{ marginBottom: 10 }}>
              <Button type="primary" size="small" icon={<LinkOutlined />} onClick={() => void openAddFKModal()}>
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
                          onConfirm={() => void handleDropForeignKey(r.name)}
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
    });
  }

  if (supportsChecks || (structure?.checkConstraints?.length || 0) > 0) {
    subTabs.push({
      key: 'checks',
      label: t('Checks ({count})', { count: structure?.checkConstraints?.length || 0 }),
      children: (
        <div>
          {canWrite && (
            <div style={{ marginBottom: 10 }}>
              <Button
                type="primary"
                size="small"
                icon={<PlusOutlined />}
                onClick={() => {
                  checkForm.resetFields();
                  checkForm.setFieldsValue({ enforced: true });
                  setCheckPreview([]);
                  setCheckModalOpen(true);
                }}
              >
                {t('Add Check Constraint')}
              </Button>
            </div>
          )}
          <ResizableTable<CheckConstraintDef>
            rowKey="name"
            size="small"
            pagination={false}
            scroll={{ x: 640 }}
            dataSource={structure?.checkConstraints || []}
            columns={[
              { title: t('Constraint Name'), dataIndex: 'name', width: 180 },
              {
                title: t('CHECK Expression'),
                dataIndex: 'expression',
                width: 300,
                render: (v) => <Typography.Text code>{v}</Typography.Text>,
              },
              {
                title: t('Status'),
                dataIndex: 'enforced',
                width: 110,
                render: (v: boolean) => (
                  <Tag color={v !== false ? 'success' : 'default'}>
                    {v !== false ? t('Enforced') : t('Not Enforced')}
                  </Tag>
                ),
              },
              ...(canWrite
                ? [
                    {
                      title: t('Actions'),
                      width: 90,
                      render: (_: any, r: CheckConstraintDef) => (
                        <Popconfirm
                          title={t('Drop check constraint {name}?', { name: r.name })}
                          onConfirm={() => void handleDropCheck(r)}
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
    });
  }

  if (supportsProjections || (structure?.projections?.length || 0) > 0) {
    subTabs.push({
      key: 'projections',
      label: t('Projections ({count})', { count: structure?.projections?.length || 0 }),
      children: (
        <div>
          {canWrite && (
            <div style={{ marginBottom: 10 }}>
              <Button
                type="primary"
                size="small"
                icon={<PlusOutlined />}
                onClick={() => {
                  projForm.resetFields();
                  projForm.setFieldsValue({ query: 'SELECT * ORDER BY tuple()' });
                  setProjPreview([]);
                  setProjModalOpen(true);
                }}
              >
                {t('Add Projection')}
              </Button>
            </div>
          )}
          <ResizableTable<ProjectionDef>
            rowKey="name"
            size="small"
            pagination={false}
            scroll={{ x: 820 }}
            dataSource={structure?.projections || []}
            columns={[
              { title: t('Projection Name'), dataIndex: 'name', width: 170 },
              {
                title: t('Query Definition'),
                dataIndex: 'query',
                width: 320,
                ellipsis: true,
                render: (v) => <Typography.Text code>{v}</Typography.Text>,
              },
              {
                title: t('Rows'),
                dataIndex: 'rows',
                width: 105,
                render: (v?: number) => (v !== undefined ? v.toLocaleString() : '-'),
              },
              {
                title: t('Disk Size'),
                dataIndex: 'bytesOnDisk',
                width: 105,
                render: (v?: number) => (v ? formatBytes(v) : '-'),
              },
              ...(canWrite
                ? [
                    {
                      title: t('Actions'),
                      width: 220,
                      render: (_: any, r: ProjectionDef) => (
                        <Space size={4}>
                          <Button
                            size="small"
                            icon={<SyncOutlined />}
                            onClick={() => void handleProjectionAction('MATERIALIZE_PROJECTION', r)}
                          >
                            {t('Materialize')}
                          </Button>
                          <Button
                            size="small"
                            onClick={() => void handleProjectionAction('CLEAR_PROJECTION', r)}
                          >
                            {t('Clear')}
                          </Button>
                          <Popconfirm
                            title={t('Drop projection {name}?', { name: r.name })}
                            onConfirm={() => void handleProjectionAction('DROP_PROJECTION', r)}
                          >
                            <Button size="small" danger icon={<DeleteOutlined />} />
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
    });
  }

  if (supportsPartitions) {
    subTabs.push({
      key: 'partitions',
      label: t('Partitions ({count})', { count: partitions.length }),
      children: (
        <div>
          {canWrite && partitionActions.includes('ADD') && (
            <div style={{ marginBottom: 10 }}>
              <Button
                type="primary"
                size="small"
                icon={<PlusOutlined />}
                onClick={() => {
                  partForm.resetFields();
                  setPartPreviewSql('');
                  setPartModalOpen(true);
                }}
              >
                {t('Add Partition')}
              </Button>
            </div>
          )}
          <ResizableTable<PartitionInfo>
            rowKey={(r) => `${r.id}:${r.name}:${r.detached ? 'd' : 'a'}`}
            size="small"
            pagination={false}
            scroll={{ x: 940 }}
            dataSource={partitions}
            columns={[
              {
                title: t('Partition'),
                dataIndex: 'name',
                width: 165,
                render: (v, r) => (
                  <Space size={4}>
                    <Typography.Text strong>{v || r.id}</Typography.Text>
                    {r.detached ? (
                      <Tag color="warning" style={{ margin: 0 }}>{t('Detached')}</Tag>
                    ) : (
                      <Tag color="success" style={{ margin: 0 }}>{t('Active')}</Tag>
                    )}
                  </Space>
                ),
              },
              {
                title: t('Strategy'),
                dataIndex: 'strategy',
                width: 100,
                render: (v?: string) => (v ? <Tag color="blue">{v}</Tag> : '-'),
              },
              {
                title: t('Expression / Key'),
                dataIndex: 'expression',
                width: 160,
                ellipsis: true,
                render: (v?: string) => v || '-',
              },
              {
                title: t('Bounds'),
                dataIndex: 'boundDescription',
                width: 180,
                ellipsis: true,
                render: (v?: string) => v || '-',
              },
              {
                title: t('Rows'),
                dataIndex: 'rows',
                width: 100,
                render: (v: number) => (v !== undefined ? v.toLocaleString() : '-'),
              },
              {
                title: t('Disk Size'),
                dataIndex: 'bytesOnDisk',
                width: 105,
                render: (v: number) => (v ? formatBytes(v) : '-'),
              },
              ...(canWrite && partitionActions.length > 0
                ? [
                    {
                      title: t('Partition Actions'),
                      width: 280,
                      render: (_: any, r: PartitionInfo) => {
                        const pid = r.id || r.name;
                        return (
                          <Space size={4} wrap>
                            {!r.detached && partitionActions.includes('OPTIMIZE') && (
                              <Button
                                size="small"
                                icon={<ThunderboltOutlined />}
                                onClick={() => void handlePartitionAction(pid, 'OPTIMIZE')}
                              >
                                {t('Optimize')}
                              </Button>
                            )}
                            {!r.detached && partitionActions.includes('ANALYZE') && (
                              <Button size="small" onClick={() => void handlePartitionAction(pid, 'ANALYZE')}>
                                {t('Analyze')}
                              </Button>
                            )}
                            {!r.detached && partitionActions.includes('TRUNCATE') && (
                              <Popconfirm
                                title={t('Truncate partition {id}?', { id: pid })}
                                onConfirm={() => void handlePartitionAction(pid, 'TRUNCATE')}
                              >
                                <Button size="small" danger>
                                  {t('Truncate')}
                                </Button>
                              </Popconfirm>
                            )}
                            {!r.detached && partitionActions.includes('DETACH') && (
                              <Popconfirm
                                title={t('Detach partition {id} (DETACH)?', { id: pid })}
                                onConfirm={() => void handlePartitionAction(pid, 'DETACH')}
                              >
                                <Button size="small">DETACH</Button>
                              </Popconfirm>
                            )}
                            {r.detached && partitionActions.includes('ATTACH') && (
                              <Button
                                size="small"
                                type="primary"
                                ghost
                                onClick={() => void handlePartitionAction(pid, 'ATTACH')}
                              >
                                ATTACH
                              </Button>
                            )}
                            {partitionActions.includes('DROP') && (
                              <Popconfirm
                                title={t('Danger: Drop partition {id} (DROP)?', { id: pid })}
                                onConfirm={() => void handlePartitionAction(pid, 'DROP')}
                              >
                                <Button size="small" danger>
                                  DROP
                                </Button>
                              </Popconfirm>
                            )}
                          </Space>
                        );
                      },
                    },
                  ]
                : []),
            ]}
          />
        </div>
      ),
    });
  }

  if (supportsPragmas) {
    const diag = structure?.diagnostics || {};
    subTabs.push({
      key: 'pragmas',
      label: (
        <Space size={4}>
          <SafetyCertificateOutlined />
          <span>{t('PRAGMA & Integrity')}</span>
        </Space>
      ),
      children: (
        <Row gutter={[14, 14]}>
          <Col xs={24} md={14}>
            <Card size="small" title={t('SQLite Runtime PRAGMA Settings')}>
              <Descriptions size="small" column={{ xs: 1, sm: 2 }} bordered>
                <Descriptions.Item label="journal_mode">
                  {canWrite ? (
                    <Select
                      size="small"
                      style={{ width: 130 }}
                      value={(diag.journal_mode || 'wal').toUpperCase()}
                      onChange={(val) => void handleSetPragma('journal_mode', val)}
                      options={['WAL', 'DELETE', 'TRUNCATE', 'MEMORY', 'OFF'].map((v) => ({ value: v, label: v }))}
                    />
                  ) : (
                    diag.journal_mode || '-'
                  )}
                </Descriptions.Item>
                <Descriptions.Item label="synchronous">
                  {canWrite ? (
                    <Select
                      size="small"
                      style={{ width: 130 }}
                      value={(diag.synchronous || 'NORMAL').toUpperCase()}
                      onChange={(val) => void handleSetPragma('synchronous', val)}
                      options={[
                        { value: 'OFF', label: 'OFF (0)' },
                        { value: 'NORMAL', label: 'NORMAL (1)' },
                        { value: 'FULL', label: 'FULL (2)' },
                        { value: 'EXTRA', label: 'EXTRA (3)' },
                      ]}
                    />
                  ) : (
                    diag.synchronous || '-'
                  )}
                </Descriptions.Item>
                <Descriptions.Item label="foreign_keys">
                  {canWrite ? (
                    <Select
                      size="small"
                      style={{ width: 130 }}
                      value={diag.foreign_keys === '1' || diag.foreign_keys?.toUpperCase() === 'ON' ? 'ON' : 'OFF'}
                      onChange={(val) => void handleSetPragma('foreign_keys', val)}
                      options={[
                        { value: 'ON', label: 'ON' },
                        { value: 'OFF', label: 'OFF' },
                      ]}
                    />
                  ) : (
                    diag.foreign_keys || '-'
                  )}
                </Descriptions.Item>
                <Descriptions.Item label="auto_vacuum">
                  {canWrite ? (
                    <Select
                      size="small"
                      style={{ width: 130 }}
                      value={(diag.auto_vacuum || 'NONE').toUpperCase()}
                      onChange={(val) => void handleSetPragma('auto_vacuum', val)}
                      options={['NONE', 'FULL', 'INCREMENTAL'].map((v) => ({ value: v, label: v }))}
                    />
                  ) : (
                    diag.auto_vacuum || '-'
                  )}
                </Descriptions.Item>
                {Object.entries(diag)
                  .filter(([k]) => !['journal_mode', 'synchronous', 'foreign_keys', 'auto_vacuum'].includes(k))
                  .map(([k, v]) => (
                    <Descriptions.Item key={k} label={k}>
                      {v}
                    </Descriptions.Item>
                  ))}
              </Descriptions>
            </Card>
          </Col>
          <Col xs={24} md={10}>
            <Card size="small" title={t('Integrity & Foreign Key Inspector')}>
              <Space wrap style={{ marginBottom: 10 }}>
                <Button
                  type="primary"
                  ghost
                  icon={<CheckCircleOutlined />}
                  loading={pragmaCheckLoading}
                  onClick={() => void handleRunIntegrityCheck('quick_check')}
                >
                  {t('Quick Check')}
                </Button>
                <Button
                  icon={<LinkOutlined />}
                  loading={pragmaCheckLoading}
                  onClick={() => void handleRunIntegrityCheck('foreign_key_check')}
                >
                  {t('Foreign Key Check')}
                </Button>
              </Space>
              {pragmaCheckResult && (
                <Alert
                  type={pragmaCheckResult.output.toLowerCase().startsWith('ok') ? 'success' : 'warning'}
                  showIcon
                  message={pragmaCheckResult.title}
                  description={
                    <pre style={{ margin: 0, maxHeight: 160, overflow: 'auto', fontSize: 12, fontFamily: 'monospace' }}>
                      {pragmaCheckResult.output}
                    </pre>
                  }
                />
              )}
            </Card>
          </Col>
        </Row>
      ),
    });
  }

  if (canWrite) {
    subTabs.push({
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
                <Button type="primary" loading={opLoading} onClick={() => void handleRenameTable()}>
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
                <Button type="primary" loading={opLoading} onClick={() => void handleUpdateTableComment()}>
                  {t('Save Comment')}
                </Button>
              </Space.Compact>
            </Card>
          </Col>

          {supportsTTL && (
            <Col xs={24} md={12}>
              <Card size="small" title={t('ClickHouse Table TTL')}>
                <Space direction="vertical" style={{ width: '100%' }} size={8}>
                  <Space.Compact style={{ width: '100%' }}>
                    <Input
                      value={ttlExpr}
                      onChange={(e) => setTtlExpr(e.target.value)}
                      placeholder="created_at + INTERVAL 30 DAY"
                    />
                    <Button type="primary" loading={opLoading} onClick={() => void handleModifyTTL(false)}>
                      {t('Modify TTL')}
                    </Button>
                  </Space.Compact>
                  {structure?.tableTtl && (
                    <Space>
                      <Tag color="blue">{structure.tableTtl}</Tag>
                      <Popconfirm
                        title={t('Remove table TTL policy?')}
                        onConfirm={() => void handleModifyTTL(true)}
                      >
                        <Button size="small" danger>
                          {t('Remove TTL')}
                        </Button>
                      </Popconfirm>
                    </Space>
                  )}
                </Space>
              </Card>
            </Col>
          )}

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
                  <Button type="primary" icon={<CopyOutlined />} loading={opLoading} onClick={() => void handleCloneTable()}>
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
                  onClick={() => void handleMaintenanceOp('OPTIMIZE')}
                >
                  {t('Optimize Table (OPTIMIZE)')}
                </Button>
                <Button loading={opLoading} onClick={() => void handleMaintenanceOp('ANALYZE')}>
                  {t('Analyze Table (ANALYZE)')}
                </Button>
                <Popconfirm
                  title={t('Truncate all rows in table {name}?', { name: tab.table || '' })}
                  onConfirm={() => void handleMaintenanceOp('TRUNCATE')}
                >
                  <Button danger icon={<ClearOutlined />} loading={opLoading}>
                    {t('Truncate table')}
                  </Button>
                </Popconfirm>
                <Popconfirm
                  title={t('Danger: Permanently drop {name}?', { name: tab.table || '' })}
                  onConfirm={() => void handleMaintenanceOp('DROP')}
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
    });
  }

  subTabs.push({
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
            onClick={async () => {
              await writeTextToClipboard(structure?.ddl || '');
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
  });

  return (
    <div style={{ padding: isMobile ? '6px 4px' : '12px 16px', height: '100%', minWidth: 0, minHeight: 0, overflowY: 'auto', overflowX: 'hidden' }}>
      <ContextNavBar tab={tab} activeSection="structure" onOpenTransfer={onOpenTransfer} />

      <Descriptions size="small" column={{ xs: 2, sm: 2, md: 4 }} style={{ marginBottom: 8 }}>
        <Descriptions.Item label={t('Table')}>
          <Typography.Text strong>{tab.table}</Typography.Text>
        </Descriptions.Item>
        <Descriptions.Item label={t('Engine')}>{structure?.engine || '-'}</Descriptions.Item>
        <Descriptions.Item label={t('Collation')}>{structure?.collation || '-'}</Descriptions.Item>
        <Descriptions.Item label={t('Table Comment')}>{structure?.comment || '-'}</Descriptions.Item>
      </Descriptions>

      <Tabs size={isMobile ? 'small' : 'middle'} tabBarGutter={isMobile ? 12 : undefined} items={subTabs} />

      {/* Add / Edit Column Modal */}
      <Modal
        open={colModalOpen}
        title={editingCol ? t('Modify Column — {name}', { name: editingCol.name }) : t('Add Column (ADD COLUMN)')}
        width="min(580px, calc(100vw - 16px))"
        onCancel={() => setColModalOpen(false)}
        footer={
          <Space wrap>
            <Button onClick={() => setColModalOpen(false)}>{t('Cancel')}</Button>
            <Button icon={<EyeOutlined />} onClick={() => void handlePreviewColumnSQL()}>
              {t('Preview SQL')}
            </Button>
            <Button type="primary" loading={colSaving} onClick={() => void handleSubmitColumn()}>
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
                <Input disabled={colIsGenerated} placeholder={t('Optional')} />
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

          {supportsGeneratedCols && (
            <div style={{ marginBottom: 12 }}>
              <Checkbox checked={colIsGenerated} onChange={(e) => setColIsGenerated(e.target.checked)}>
                {t('Generated / Computed Column')}
              </Checkbox>
              {colIsGenerated && (
                <Row gutter={12} style={{ marginTop: 8 }}>
                  <Col xs={24} sm={16}>
                    <Form.Item
                      name="generatedExpr"
                      label={t('Generation Expression')}
                      rules={[{ required: true, message: t('Required') }]}
                      style={{ marginBottom: 0 }}
                    >
                      <Input placeholder="price * quantity" />
                    </Form.Item>
                  </Col>
                  <Col xs={24} sm={8}>
                    <Form.Item name="generatedStorage" label={t('Storage Mode')} style={{ marginBottom: 0 }}>
                      <Select
                        options={[
                          { value: 'VIRTUAL', label: 'VIRTUAL' },
                          { value: 'STORED', label: 'STORED' },
                        ]}
                      />
                    </Form.Item>
                  </Col>
                </Row>
              )}
            </div>
          )}

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
              <Checkbox disabled={colIsGenerated}>{t('Auto Increment')}</Checkbox>
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
        width="min(520px, calc(100vw - 16px))"
        onCancel={() => {
          setIdxModalOpen(false);
          setEditingIdx(null);
        }}
        onOk={() => void handleAddIndex()}
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

      {/* Add ClickHouse Skipping Index Modal */}
      <Modal
        open={skipIdxModalOpen}
        title={t('Add Skipping Index')}
        width="min(560px, calc(100vw - 16px))"
        onCancel={() => setSkipIdxModalOpen(false)}
        footer={
          <Space wrap>
            <Button onClick={() => setSkipIdxModalOpen(false)}>{t('Cancel')}</Button>
            <Button icon={<EyeOutlined />} onClick={() => void handlePreviewOrAddSkippingIndex(true)}>
              {t('Preview SQL')}
            </Button>
            <Button type="primary" loading={skipIdxSaving} onClick={() => void handlePreviewOrAddSkippingIndex(false)}>
              {t('Create Now')}
            </Button>
          </Space>
        }
      >
        <Form form={skipIdxForm} layout="vertical">
          <Row gutter={12}>
            <Col xs={24} sm={12}>
              <Form.Item name="name" label={t('Index Name')} rules={[{ required: true, message: t('Required') }]}>
                <Input placeholder="idx_skip_user_id" />
              </Form.Item>
            </Col>
            <Col xs={24} sm={12}>
              <Form.Item name="type" label={t('Index Type')} rules={[{ required: true, message: t('Required') }]}>
                <AutoComplete options={CLICKHOUSE_SKIPPING_TYPES} placeholder="minmax / bloom_filter" />
              </Form.Item>
            </Col>
          </Row>
          <Row gutter={12}>
            <Col xs={24} sm={16}>
              <Form.Item
                name="expression"
                label={t('Column / Expression')}
                rules={[{ required: true, message: t('Required') }]}
              >
                <AutoComplete
                  options={(structure?.columns || []).map((c) => ({ value: c.name }))}
                  placeholder="user_id"
                />
              </Form.Item>
            </Col>
            <Col xs={24} sm={8}>
              <Form.Item name="granularity" label={t('Granularity')} rules={[{ required: true }]}>
                <InputNumber min={1} style={{ width: '100%' }} />
              </Form.Item>
            </Col>
          </Row>
          {skipIdxPreview.length > 0 && (
            <Alert
              type="info"
              message={t('SQL Preview')}
              description={
                <pre style={{ margin: 0, fontSize: 12, whiteSpace: 'pre-wrap', fontFamily: 'monospace' }}>
                  {skipIdxPreview.join(';\n')};
                </pre>
              }
            />
          )}
        </Form>
      </Modal>

      {/* Add Check Constraint Modal */}
      <Modal
        open={checkModalOpen}
        title={t('Add Check Constraint')}
        width="min(540px, calc(100vw - 16px))"
        onCancel={() => setCheckModalOpen(false)}
        footer={
          <Space wrap>
            <Button onClick={() => setCheckModalOpen(false)}>{t('Cancel')}</Button>
            <Button icon={<EyeOutlined />} onClick={() => void handlePreviewOrAddCheck(true)}>
              {t('Preview SQL')}
            </Button>
            <Button type="primary" loading={checkSaving} onClick={() => void handlePreviewOrAddCheck(false)}>
              {t('Create Now')}
            </Button>
          </Space>
        }
      >
        <Form form={checkForm} layout="vertical">
          <Form.Item name="name" label={t('Constraint Name (leave blank to auto-generate)')}>
            <Input placeholder={`chk_${tab.table}_amount`} />
          </Form.Item>
          <Form.Item
            name="expression"
            label={t('Boolean Expression')}
            rules={[{ required: true, message: t('Required') }]}
          >
            <Input placeholder="amount >= 0" />
          </Form.Item>
          <Form.Item name="enforced" valuePropName="checked">
            <Checkbox>{t('Enforced')}</Checkbox>
          </Form.Item>
          {checkPreview.length > 0 && (
            <Alert
              type="info"
              message={t('SQL Preview')}
              description={
                <pre style={{ margin: 0, fontSize: 12, whiteSpace: 'pre-wrap', fontFamily: 'monospace' }}>
                  {checkPreview.join(';\n')};
                </pre>
              }
            />
          )}
        </Form>
      </Modal>

      {/* Add ClickHouse Projection Modal */}
      <Modal
        open={projModalOpen}
        title={t('Add Projection')}
        width="min(580px, calc(100vw - 16px))"
        onCancel={() => setProjModalOpen(false)}
        footer={
          <Space wrap>
            <Button onClick={() => setProjModalOpen(false)}>{t('Cancel')}</Button>
            <Button icon={<EyeOutlined />} onClick={() => void handlePreviewOrAddProjection(true)}>
              {t('Preview SQL')}
            </Button>
            <Button type="primary" loading={projSaving} onClick={() => void handlePreviewOrAddProjection(false)}>
              {t('Create Now')}
            </Button>
          </Space>
        }
      >
        <Form form={projForm} layout="vertical">
          <Form.Item name="name" label={t('Projection Name')} rules={[{ required: true, message: t('Required') }]}>
            <Input placeholder="proj_by_user" />
          </Form.Item>
          <Form.Item
            name="query"
            label={t('SELECT ... ORDER BY / GROUP BY Query')}
            rules={[{ required: true, message: t('Required') }]}
          >
            <Input.TextArea rows={4} style={{ fontFamily: 'monospace', fontSize: 12 }} />
          </Form.Item>
          {projPreview.length > 0 && (
            <Alert
              type="info"
              message={t('SQL Preview')}
              description={
                <pre style={{ margin: 0, fontSize: 12, whiteSpace: 'pre-wrap', fontFamily: 'monospace' }}>
                  {projPreview.join(';\n')};
                </pre>
              }
            />
          )}
        </Form>
      </Modal>

      {/* Add Partition Modal */}
      <Modal
        open={partModalOpen}
        title={t('Add Partition')}
        width="min(540px, calc(100vw - 16px))"
        onCancel={() => setPartModalOpen(false)}
        footer={
          <Space wrap>
            <Button onClick={() => setPartModalOpen(false)}>{t('Cancel')}</Button>
            <Button icon={<EyeOutlined />} onClick={() => void handlePreviewOrAddPartition(true)}>
              {t('Preview SQL')}
            </Button>
            <Button type="primary" loading={partSaving} onClick={() => void handlePreviewOrAddPartition(false)}>
              {t('Create Now')}
            </Button>
          </Space>
        }
      >
        <Form form={partForm} layout="vertical">
          <Form.Item name="partitionId" label={t('Partition Name')} rules={[{ required: true, message: t('Required') }]}>
            <Input placeholder="p2027" />
          </Form.Item>
          <Form.Item
            name="expression"
            label={t('Partition Bound / Values Clause')}
            rules={[{ required: true, message: t('Required') }]}
          >
            <Input placeholder="VALUES LESS THAN (2027)" />
          </Form.Item>
          {partPreviewSql && (
            <Alert
              type="info"
              message={t('SQL Preview')}
              description={
                <pre style={{ margin: 0, fontSize: 12, whiteSpace: 'pre-wrap', fontFamily: 'monospace' }}>
                  {partPreviewSql}
                </pre>
              }
            />
          )}
        </Form>
      </Modal>

      {/* Add Foreign Key Modal */}
      <Modal
        open={fkModalOpen}
        title={t('Add Foreign Key (ADD FOREIGN KEY)')}
        width="min(560px, calc(100vw - 16px))"
        onCancel={() => setFkModalOpen(false)}
        onOk={() => void handleAddForeignKey()}
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
                  onChange={(v) => void handleRefTableChange(v)}
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
                <Select mode="multiple" options={fkRefCols.map((c) => ({ label: c, value: c }))} />
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
