import React, { useEffect, useState } from 'react';
import {
  Alert,
  AutoComplete,
  Button,
  Checkbox,
  Form,
  Input,
  InputNumber,
  Modal,
  Radio,
  Select,
  Space,
  Tabs,
  Upload,
  message,
} from 'antd';
import {
  DownloadOutlined,
  InboxOutlined,
  PlayCircleOutlined,
  UploadOutlined,
} from '@ant-design/icons';
import { api } from '../../api/client';
import { useI18n } from '../../i18n/useI18n';
import type { TransferModalMode } from './ContextNavBar';

interface Props {
  open: boolean;
  initialMode: TransferModalMode;
  connectionId: string;
  database: string;
  schema?: string;
  table?: string;
  initialTables?: string[];
  customSQL?: string;
  onClose: () => void;
  onImported?: () => void;
}

export const ImportExportModal: React.FC<Props> = ({
  open,
  initialMode,
  connectionId,
  database,
  schema,
  table,
  initialTables,
  customSQL,
  onClose,
  onImported,
}) => {
  const { t } = useI18n();
  const [activeKey, setActiveKey] = useState<string>(initialMode);
  const [availableTables, setAvailableTables] = useState<string[]>([]);

  // Export state
  const isFullDBExport = !table && !customSQL;
  const [exportFormat, setExportFormat] = useState<string>('sql');
  const [selectedTables, setSelectedTables] = useState<string[]>([]);
  const [includeDDL, setIncludeDDL] = useState<boolean>(true);
  const [dropIfExists, setDropIfExists] = useState<boolean>(false);
  const [maxRows, setMaxRows] = useState<number>(100000);
  const [exporting, setExporting] = useState<boolean>(false);

  // File Import state
  const [importFormat, setImportFormat] = useState<string>('sql');
  const [targetTable, setTargetTable] = useState<string>(table || '');
  const [continueOnError, setContinueOnError] = useState<boolean>(false);
  const [file, setFile] = useState<File | null>(null);
  const [importing, setImporting] = useState(false);

  // Direct SQL Script Execution state
  const [sqlScript, setSqlScript] = useState<string>('');
  const [sqlContinueOnError, setSqlContinueOnError] = useState<boolean>(false);
  const [sqlRunning, setSqlRunning] = useState<boolean>(false);

  useEffect(() => {
    if (!open) return;
    setActiveKey(initialMode);
    setTargetTable(table || '');
    setImportFormat(table ? 'csv' : 'sql');
    setExportFormat(!table && !customSQL ? 'sql' : 'csv_bom');
    setSelectedTables(initialTables && initialTables.length > 0 ? [...initialTables] : []);
    setFile(null);

    if (connectionId && database) {
      api
        .listObjects(connectionId, database, schema || '')
        .then((res) => {
          setAvailableTables((res.tables || []).map((tbl) => tbl.name));
        })
        .catch(() => setAvailableTables([]));
    }
  }, [open, initialMode, connectionId, database, schema, table, initialTables, customSQL]);

  const handleTriggerExport = async () => {
    setExporting(true);
    try {
      const res = await api.triggerExportDownload(connectionId, {
        database,
        schema,
        table,
        tables: isFullDBExport && selectedTables.length > 0 ? selectedTables : undefined,
        sql: customSQL,
        format: exportFormat,
        includeDdl: includeDDL,
        dropIfExists,
        maxRows,
      });
      if (res?.canceled) {
        return;
      }
      if (res?.savedPath) {
        message.success(t('Export saved to {path}', { path: res.savedPath }));
      } else {
        message.success(t('One-time security ticket issued; streaming export download started'));
      }
      onClose();
    } catch (err: any) {
      message.error(err.message || t('Failed to start export'));
    } finally {
      setExporting(false);
    }
  };

  const handleRunImport = async () => {
    if (!file) {
      message.warning(t('Please select a file to import'));
      return;
    }
    if (importFormat !== 'sql' && !targetTable.trim()) {
      message.warning(t('Target table name is required for CSV / JSON / JSONL imports'));
      return;
    }
    setImporting(true);
    try {
      const res = await api.importFile(connectionId, {
        database,
        schema,
        table: targetTable.trim(),
        format: importFormat,
        continueOnError,
        file,
      });
      if (res.warnings && res.warnings.length > 0) {
        message.warning(
          t('Import completed: {ok} succeeded, {err} skipped ({ms} ms)', {
            ok: res.importedRows,
            err: res.failedCount || res.warnings.length,
            ms: res.elapsedMs,
          })
        );
      } else {
        message.success(
          t('Import completed! Processed {count} rows/statements ({ms} ms)', {
            count: res.importedRows,
            ms: res.elapsedMs,
          })
        );
      }
      onImported?.();
      onClose();
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setImporting(false);
    }
  };

  const handleRunSQLScript = async () => {
    if (!sqlScript.trim()) {
      message.warning(t('Please paste or enter a SQL script to execute'));
      return;
    }
    setSqlRunning(true);
    try {
      const res = await api.importSQLText(connectionId, {
        database,
        schema,
        sql: sqlScript,
        continueOnError: sqlContinueOnError,
      });
      if (res.warnings && res.warnings.length > 0) {
        message.warning(
          t('SQL script finished: {ok} succeeded, {err} ignored ({ms} ms)', {
            ok: res.importedRows,
            err: res.warnings.length,
            ms: res.elapsedMs,
          })
        );
      } else {
        message.success(
          t('SQL script executed! {count} statements succeeded ({ms} ms)', {
            count: res.importedRows,
            ms: res.elapsedMs,
          })
        );
      }
      onImported?.();
      onClose();
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setSqlRunning(false);
    }
  };

  return (
    <Modal
      open={open}
      title={t('Export, Batch Import & SQL Script Runner')}
      width="min(680px, 95vw)"
      onCancel={onClose}
      footer={null}
      destroyOnClose
    >
      <Tabs
        activeKey={activeKey}
        onChange={setActiveKey}
        items={[
          {
            key: 'export',
            label: t('Stream Export / Dump'),
            children: (
              <Form layout="vertical">
                <Alert
                  type="info"
                  showIcon
                  style={{ marginBottom: 14 }}
                  message={
                    customSQL
                      ? t('Export Source: Custom SQL Query Stream')
                      : table
                      ? `${database}${schema ? '.' + schema : ''}.${table}`
                      : `${database}${schema ? '.' + schema : ''} (${availableTables.length})`
                  }
                />

                {isFullDBExport && availableTables.length > 0 && (
                  <Form.Item label={t('Tables to Export (leave empty to export all tables)')} style={{ marginBottom: 14 }}>
                    <Select
                      mode="multiple"
                      allowClear
                      placeholder={t('Export all tables by default, or select specific tables...')}
                      value={selectedTables}
                      onChange={setSelectedTables}
                      options={availableTables.map((tName) => ({ value: tName, label: tName }))}
                    />
                  </Form.Item>
                )}

                <Form.Item label={t('Export Format')} style={{ marginBottom: 14 }}>
                  <Radio.Group
                    className="dbharbor-pill-radios"
                    value={exportFormat}
                    onChange={(e) => setExportFormat(e.target.value)}
                    style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}
                  >
                    <Radio.Button value="sql">SQL (INSERT / Dump)</Radio.Button>
                    {!isFullDBExport && (
                      <>
                        <Radio.Button value="csv_bom">CSV (UTF-8 BOM)</Radio.Button>
                        <Radio.Button value="csv">CSV</Radio.Button>
                        <Radio.Button value="markdown">Markdown</Radio.Button>
                      </>
                    )}
                    <Radio.Button value="json">JSON</Radio.Button>
                    <Radio.Button value="jsonl">JSONL</Radio.Button>
                  </Radio.Group>
                </Form.Item>

                {exportFormat === 'sql' && !customSQL && (
                  <Form.Item style={{ marginBottom: 14 }}>
                    <Space direction="vertical" size={6}>
                      <Checkbox checked={includeDDL} onChange={(e) => setIncludeDDL(e.target.checked)}>
                        {t('Include CREATE TABLE DDL')}
                      </Checkbox>
                      <Checkbox checked={dropIfExists} onChange={(e) => setDropIfExists(e.target.checked)}>
                        {t('Include DROP TABLE IF EXISTS before CREATE')}
                      </Checkbox>
                    </Space>
                  </Form.Item>
                )}

                <Form.Item label={t('Max Rows Limit')} style={{ marginBottom: 16 }}>
                  <InputNumber
                    min={1}
                    max={5000000}
                    value={maxRows}
                    onChange={(v) => setMaxRows(v || 100000)}
                    style={{ width: 220, maxWidth: '100%' }}
                  />
                </Form.Item>

                <Button
                  type="primary"
                  icon={<DownloadOutlined />}
                  block
                  loading={exporting}
                  onClick={handleTriggerExport}
                >
                  {t('Download Export Stream')}
                </Button>
              </Form>
            ),
          },
          {
            key: 'import',
            label: t('Import File'),
            children: (
              <Form layout="vertical">
                <Form.Item label={t('Import File Format')} style={{ marginBottom: 12 }}>
                  <Radio.Group
                    className="dbharbor-pill-radios"
                    value={importFormat}
                    onChange={(e) => setImportFormat(e.target.value)}
                    style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}
                  >
                    <Radio.Button value="sql">.sql</Radio.Button>
                    <Radio.Button value="csv">CSV</Radio.Button>
                    <Radio.Button value="json">JSON</Radio.Button>
                    <Radio.Button value="jsonl">JSONL (NDJSON)</Radio.Button>
                  </Radio.Group>
                </Form.Item>

                {importFormat !== 'sql' && (
                  <Form.Item label={t('Target Table Name')} required style={{ marginBottom: 12 }}>
                    <AutoComplete
                      placeholder={t('Target Table Name')}
                      value={targetTable}
                      onChange={setTargetTable}
                      options={availableTables.map((tName) => ({ value: tName }))}
                    />
                  </Form.Item>
                )}

                <Form.Item label={t('Select Local Data or .sql File')} style={{ marginBottom: 12 }}>
                  <Upload.Dragger
                    multiple={false}
                    beforeUpload={(f) => {
                      setFile(f);
                      const lower = f.name.toLowerCase();
                      if (lower.endsWith('.sql')) setImportFormat('sql');
                      else if (lower.endsWith('.jsonl') || lower.endsWith('.ndjson')) setImportFormat('jsonl');
                      else if (lower.endsWith('.json')) setImportFormat('json');
                      else if (lower.endsWith('.csv')) setImportFormat('csv');
                      return false;
                    }}
                    fileList={file ? [{ uid: '1', name: file.name, status: 'done' }] : []}
                    onRemove={() => setFile(null)}
                  >
                    <p className="ant-upload-drag-icon" style={{ marginBottom: 8 }}>
                      <InboxOutlined />
                    </p>
                    <p className="ant-upload-text" style={{ marginBottom: 4 }}>
                      {t('Click or drag .sql / .csv / .json / .jsonl file here')}
                    </p>
                    <p className="ant-upload-hint" style={{ margin: 0 }}>
                      {t('Streaming batched transactional execution keeps memory usage low even on large SQL dumps.')}
                    </p>
                  </Upload.Dragger>
                </Form.Item>

                <Form.Item style={{ marginBottom: 14 }}>
                  <Checkbox checked={continueOnError} onChange={(e) => setContinueOnError(e.target.checked)}>
                    {t('Continue on Error')}
                  </Checkbox>
                </Form.Item>

                <Button
                  type="primary"
                  icon={<UploadOutlined />}
                  block
                  loading={importing}
                  onClick={handleRunImport}
                >
                  {t('Start Batch Import')}
                </Button>
              </Form>
            ),
          },
          {
            key: 'sql_exec',
            label: t('Execute SQL Script'),
            children: (
              <Form layout="vertical">
                <Alert
                  type="info"
                  showIcon
                  style={{ marginBottom: 10 }}
                  message={`${database}${schema ? '.' + schema : ''}`}
                />
                <Form.Item label={t('SQL Script Content (semicolon-separated statements)')} style={{ marginBottom: 12 }}>
                  <Input.TextArea
                    rows={7}
                    value={sqlScript}
                    onChange={(e) => setSqlScript(e.target.value)}
                    style={{ fontFamily: "'JetBrains Mono', Consolas, monospace", fontSize: 12, maxHeight: '36vh' }}
                    placeholder={`CREATE TABLE IF NOT EXISTS products (\n  id INTEGER PRIMARY KEY,\n  name VARCHAR(120) NOT NULL,\n  price DECIMAL(10,2) DEFAULT 0\n);\n\nINSERT INTO products (id, name, price) VALUES (1, 'DBHarbor Pro', 99.00);`}
                  />
                </Form.Item>
                <Form.Item style={{ marginBottom: 14 }}>
                  <Checkbox checked={sqlContinueOnError} onChange={(e) => setSqlContinueOnError(e.target.checked)}>
                    {t('Ignore individual statement errors and continue')}
                  </Checkbox>
                </Form.Item>
                <Button
                  type="primary"
                  icon={<PlayCircleOutlined />}
                  block
                  loading={sqlRunning}
                  onClick={handleRunSQLScript}
                >
                  {t('Execute Full SQL Script')}
                </Button>
              </Form>
            ),
          },
        ]}
      />
    </Modal>
  );
};
