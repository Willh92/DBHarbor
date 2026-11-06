import React, { Suspense, lazy, useEffect, useState } from 'react';
import {
  Button,
  Checkbox,
  Form,
  Input,
  Modal,
  Radio,
  Select,
  Space,
  Spin,
  Typography,
  message,
} from 'antd';
import {
  CodeOutlined,
  CopyOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons';
import { api } from '../../api/client';
import { useI18n } from '../../i18n/useI18n';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import { writeTextToClipboard } from '../../utils/tableClipboard';

const MonacoCodeEditor = lazy(() => import('../common/MonacoCodeEditorInner'));

export const ROUTINE_CAPABLE_ENGINES = new Set([
  'mysql',
  'mariadb',
  'tidb',
  'oceanbase',
  'postgres',
  'kingbase',
  'sqlserver',
  'oracle',
  'dameng',
]);

export const TRIGGER_CAPABLE_ENGINES = new Set([
  'mysql',
  'mariadb',
  'tidb',
  'oceanbase',
  'postgres',
  'kingbase',
  'sqlite',
  'sqlserver',
  'oracle',
  'dameng',
]);

export interface RoutineTriggerModalProps {
  open: boolean;
  mode: 'routine' | 'trigger';
  connectionId: string;
  connName: string;
  dbType: string;
  database: string;
  schema?: string;
  availableTables?: string[];
  editObject?: {
    name: string;
    objectType: 'PROCEDURE' | 'FUNCTION' | 'TRIGGER';
  } | null;
  canWrite: boolean;
  onClose: () => void;
  onSaved?: () => void;
}

function quoteIdent(dbType: string, raw: string, schema?: string): string {
  const clean = raw.trim() || 'sample_name';
  const q = (s: string) => {
    if (['mysql', 'mariadb', 'tidb', 'oceanbase'].includes(dbType)) {
      return `\`${s.replace(/`/g, '``')}\``;
    }
    if (dbType === 'sqlserver') {
      return `[${s.replace(/\]/g, ']]')}]`;
    }
    return `"${s.replace(/"/g, '""')}"`;
  };
  if (schema && ['postgres', 'kingbase', 'sqlserver', 'oracle', 'dameng'].includes(dbType)) {
    return `${q(schema)}.${q(clean)}`;
  }
  return q(clean);
}

export function buildRoutineTemplateSQL(opts: {
  dbType: string;
  schema?: string;
  kind: 'PROCEDURE' | 'FUNCTION';
  name: string;
  params: string;
  returns: string;
}): string {
  const { dbType, schema, kind, name, params, returns } = opts;
  const target = quoteIdent(dbType, name || (kind === 'PROCEDURE' ? 'sp_sample' : 'fn_sample'), schema);
  const paramStr = params.trim();
  const retType = returns.trim() || (['oracle', 'dameng'].includes(dbType) ? 'NUMBER' : 'INT');

  if (['mysql', 'mariadb', 'tidb', 'oceanbase'].includes(dbType)) {
    if (kind === 'PROCEDURE') {
      return `CREATE PROCEDURE ${target}(${paramStr || 'IN p_id INT'})\nBEGIN\n  SELECT p_id AS result_id;\nEND`;
    }
    return `CREATE FUNCTION ${target}(${paramStr || 'p_val INT'})\nRETURNS ${retType}\nDETERMINISTIC\nBEGIN\n  RETURN p_val * 2;\nEND`;
  }

  if (['postgres', 'kingbase'].includes(dbType)) {
    if (kind === 'PROCEDURE') {
      return `CREATE OR REPLACE PROCEDURE ${target}(${paramStr || 'p_id INT'})\nLANGUAGE plpgsql\nAS $$\nBEGIN\n  RAISE NOTICE 'ID = %', p_id;\nEND;\n$$;`;
    }
    return `CREATE OR REPLACE FUNCTION ${target}(${paramStr || 'p_val INT'})\nRETURNS ${retType}\nLANGUAGE plpgsql\nAS $$\nBEGIN\n  RETURN p_val * 2;\nEND;\n$$;`;
  }

  if (dbType === 'sqlserver') {
    if (kind === 'PROCEDURE') {
      return `CREATE OR ALTER PROCEDURE ${target}\n  ${paramStr || '@p_id INT = 1'}\nAS\nBEGIN\n  SET NOCOUNT ON;\n  SELECT @p_id AS result_id;\nEND`;
    }
    return `CREATE OR ALTER FUNCTION ${target}(${paramStr || '@p_val INT'})\nRETURNS ${retType}\nAS\nBEGIN\n  RETURN @p_val * 2;\nEND`;
  }

  if (['oracle', 'dameng'].includes(dbType)) {
    if (kind === 'PROCEDURE') {
      return `CREATE OR REPLACE PROCEDURE ${target}(${paramStr || 'p_id IN NUMBER'})\nIS\nBEGIN\n  NULL;\nEND;`;
    }
    return `CREATE OR REPLACE FUNCTION ${target}(${paramStr || 'p_val IN NUMBER'})\nRETURN ${retType}\nIS\nBEGIN\n  RETURN p_val * 2;\nEND;`;
  }

  return `CREATE ${kind} ${target}(${paramStr})\nBEGIN\n  SELECT 1;\nEND;`;
}

export function buildTriggerTemplateSQL(opts: {
  dbType: string;
  schema?: string;
  name: string;
  tableName: string;
  timing: 'BEFORE' | 'AFTER' | 'INSTEAD OF';
  event: 'INSERT' | 'UPDATE' | 'DELETE';
}): string {
  const { dbType, schema, name, tableName, timing, event } = opts;
  const trgIdent = quoteIdent(dbType, name || `trg_${tableName || 'table'}_${event.toLowerCase()}`, schema);
  const tblIdent = quoteIdent(dbType, tableName || 'target_table', schema);

  if (dbType === 'sqlite') {
    return `CREATE TRIGGER ${trgIdent}\n${timing} ${event} ON ${quoteIdent(dbType, tableName || 'target_table')}\nFOR EACH ROW\nBEGIN\n  SELECT 1;\nEND;`;
  }

  if (['mysql', 'mariadb', 'tidb', 'oceanbase'].includes(dbType)) {
    const safeTiming = timing === 'INSTEAD OF' ? 'AFTER' : timing;
    return `CREATE TRIGGER ${trgIdent}\n${safeTiming} ${event} ON ${tblIdent}\nFOR EACH ROW\nBEGIN\n  -- Example: SET NEW.updated_at = NOW();\nEND`;
  }

  if (['postgres', 'kingbase'].includes(dbType)) {
    return `CREATE TRIGGER ${ quoteIdent(dbType, name || `trg_${tableName || 'table'}_${event.toLowerCase()}`) }\n${timing} ${event} ON ${tblIdent}\nFOR EACH ROW\nEXECUTE FUNCTION trigger_handler_fn();`;
  }

  if (dbType === 'sqlserver') {
    const sqlTiming = timing === 'BEFORE' ? 'INSTEAD OF' : timing;
    return `CREATE OR ALTER TRIGGER ${trgIdent}\nON ${tblIdent}\n${sqlTiming} ${event}\nAS\nBEGIN\n  SET NOCOUNT ON;\nEND`;
  }

  if (['oracle', 'dameng'].includes(dbType)) {
    return `CREATE OR REPLACE TRIGGER ${trgIdent}\n${timing} ${event} ON ${tblIdent}\nFOR EACH ROW\nBEGIN\n  NULL;\nEND;`;
  }

  return `CREATE TRIGGER ${trgIdent}\n${timing} ${event} ON ${tblIdent}\nFOR EACH ROW\nBEGIN\n  SELECT 1;\nEND;`;
}

export const RoutineTriggerModal: React.FC<RoutineTriggerModalProps> = ({
  open,
  mode,
  connectionId,
  connName,
  dbType,
  database,
  schema,
  availableTables = [],
  editObject,
  canWrite,
  onClose,
  onSaved,
}) => {
  const { t } = useI18n();
  const { darkMode, openTab } = useWorkbenchStore();

  const [loadingDDL, setLoadingDDL] = useState(false);
  const [saving, setSaving] = useState(false);

  // Routine builder fields
  const [routineKind, setRoutineKind] = useState<'PROCEDURE' | 'FUNCTION'>('PROCEDURE');
  const [objectName, setObjectName] = useState('');
  const [paramsText, setParamsText] = useState('');
  const [returnType, setReturnType] = useState('INT');

  // Trigger builder fields
  const [targetTable, setTargetTable] = useState('');
  const [triggerTiming, setTriggerTiming] = useState<'BEFORE' | 'AFTER' | 'INSTEAD OF'>('AFTER');
  const [triggerEvent, setTriggerEvent] = useState<'INSERT' | 'UPDATE' | 'DELETE'>('INSERT');

  const [replaceExisting, setReplaceExisting] = useState(true);
  const [sqlText, setSqlText] = useState('');

  useEffect(() => {
    if (!open) return;

    if (editObject) {
      setObjectName(editObject.name);
      setReplaceExisting(true);
      if (editObject.objectType === 'PROCEDURE' || editObject.objectType === 'FUNCTION') {
        setRoutineKind(editObject.objectType);
      }
      setLoadingDDL(true);
      api
        .getObjectDDL(connectionId, database, schema || '', editObject.objectType, editObject.name)
        .then((res) => {
          setSqlText(res.ddl || '');
        })
        .catch((err: any) => {
          setSqlText(`-- ${err.message || 'DDL unavailable'}`);
        })
        .finally(() => setLoadingDDL(false));
      return;
    }

    setLoadingDDL(false);
    setReplaceExisting(true);
    if (mode === 'routine') {
      const initKind: 'PROCEDURE' | 'FUNCTION' = 'PROCEDURE';
      const initName = 'sp_new_routine';
      const initParams = ['mysql', 'mariadb', 'tidb', 'oceanbase'].includes(dbType)
        ? 'IN p_id INT'
        : dbType === 'sqlserver'
          ? '@p_id INT = 1'
          : ['oracle', 'dameng'].includes(dbType)
            ? 'p_id IN NUMBER'
            : 'p_id INT';
      const initRet = ['oracle', 'dameng'].includes(dbType) ? 'NUMBER' : 'INT';
      setRoutineKind(initKind);
      setObjectName(initName);
      setParamsText(initParams);
      setReturnType(initRet);
      setSqlText(
        buildRoutineTemplateSQL({
          dbType,
          schema,
          kind: initKind,
          name: initName,
          params: initParams,
          returns: initRet,
        })
      );
    } else {
      const firstTable = availableTables[0] || 'sample_table';
      const initTrgName = `trg_${firstTable}_after_insert`;
      setTargetTable(firstTable);
      setTriggerTiming('AFTER');
      setTriggerEvent('INSERT');
      setObjectName(initTrgName);
      setSqlText(
        buildTriggerTemplateSQL({
          dbType,
          schema,
          name: initTrgName,
          tableName: firstTable,
          timing: 'AFTER',
          event: 'INSERT',
        })
      );
    }
  }, [open, mode, editObject, connectionId, database, schema, dbType, availableTables]);

  const handleRegenerateTemplate = () => {
    if (mode === 'routine') {
      setSqlText(
        buildRoutineTemplateSQL({
          dbType,
          schema,
          kind: routineKind,
          name: objectName,
          params: paramsText,
          returns: returnType,
        })
      );
    } else {
      setSqlText(
        buildTriggerTemplateSQL({
          dbType,
          schema,
          name: objectName,
          tableName: targetTable,
          timing: triggerTiming,
          event: triggerEvent,
        })
      );
    }
  };

  const handleSave = async () => {
    const cleanSQL = sqlText.trim();
    const cleanName = objectName.trim();
    if (!cleanSQL) {
      message.warning(t('SQL definition cannot be empty'));
      return;
    }
    if (!cleanName) {
      message.warning(t('Please enter an object name'));
      return;
    }
    setSaving(true);
    try {
      await api.objectAction(connectionId, {
        database,
        schema,
        objectName: cleanName,
        objectType: mode === 'trigger' ? 'TRIGGER' : routineKind,
        action: mode === 'trigger' ? 'CREATE_TRIGGER' : 'CREATE_ROUTINE',
        routineSql: cleanSQL,
        replaceExisting,
        targetName: mode === 'trigger' ? targetTable : undefined,
      });
      message.success(
        mode === 'trigger'
          ? t('Trigger {name} saved successfully', { name: cleanName })
          : t('Routine {name} saved successfully', { name: cleanName })
      );
      onSaved?.();
      onClose();
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setSaving(false);
    }
  };

  const modalTitle = editObject
    ? `${editObject.objectType}: ${editObject.name}`
    : mode === 'trigger'
      ? t('Create Trigger')
      : t('Create Routine (Procedure / Function)');

  return (
    <Modal
      open={open}
      destroyOnClose
      title={modalTitle}
      width="min(820px, 96vw)"
      onCancel={onClose}
      footer={
        <Space wrap>
          <Button
            icon={<CopyOutlined />}
            onClick={() => {
              writeTextToClipboard(sqlText);
              message.success(t('Copied to clipboard'));
            }}
          >
            {t('Copy SQL')}
          </Button>
          <Button
            icon={<CodeOutlined />}
            onClick={() => {
              openTab({
                key: `sql:${connectionId}:${Date.now()}`,
                title: `SQL - ${objectName || database}`,
                kind: 'sql_console',
                connectionId,
                connName,
                dbType,
                database,
                schema,
                initialSQL: sqlText,
              });
              onClose();
            }}
          >
            {t('Open in SQL Console')}
          </Button>
          {canWrite && (
            <Button type="primary" loading={saving} onClick={handleSave}>
              {editObject ? t('Save & Replace') : t('Execute & Create')}
            </Button>
          )}
          <Button onClick={onClose}>{t('Close')}</Button>
        </Space>
      }
    >
      <Spin spinning={loadingDDL}>
        {canWrite && (
          <Form layout="vertical" style={{ marginBottom: 10 }}>
            {mode === 'routine' ? (
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'flex-end' }}>
                <Form.Item label={t('Routine Type')} style={{ marginBottom: 4 }}>
                  <Radio.Group
                    size="small"
                    value={routineKind}
                    onChange={(e) => {
                      const nextKind = e.target.value as 'PROCEDURE' | 'FUNCTION';
                      setRoutineKind(nextKind);
                      if (!editObject) {
                        const nextName =
                          nextKind === 'FUNCTION' && objectName === 'sp_new_routine'
                            ? 'fn_new_routine'
                            : nextKind === 'PROCEDURE' && objectName === 'fn_new_routine'
                              ? 'sp_new_routine'
                              : objectName;
                        setObjectName(nextName);
                        setSqlText(
                          buildRoutineTemplateSQL({
                            dbType,
                            schema,
                            kind: nextKind,
                            name: nextName,
                            params: paramsText,
                            returns: returnType,
                          })
                        );
                      }
                    }}
                  >
                    <Radio.Button value="PROCEDURE">PROCEDURE</Radio.Button>
                    <Radio.Button value="FUNCTION">FUNCTION</Radio.Button>
                  </Radio.Group>
                </Form.Item>

                <Form.Item label={t('Routine Name')} style={{ marginBottom: 4, flex: '1 1 160px' }}>
                  <Input
                    size="small"
                    value={objectName}
                    onChange={(e) => setObjectName(e.target.value)}
                    placeholder="sp_sample"
                  />
                </Form.Item>

                {!editObject && (
                  <>
                    <Form.Item label={t('Parameters')} style={{ marginBottom: 4, flex: '1 1 180px' }}>
                      <Input
                        size="small"
                        value={paramsText}
                        onChange={(e) => setParamsText(e.target.value)}
                        placeholder="IN p_id INT"
                      />
                    </Form.Item>
                    {routineKind === 'FUNCTION' && (
                      <Form.Item label={t('Return Type')} style={{ marginBottom: 4, width: 120 }}>
                        <Input
                          size="small"
                          value={returnType}
                          onChange={(e) => setReturnType(e.target.value)}
                          placeholder="INT"
                        />
                      </Form.Item>
                    )}
                    <Form.Item style={{ marginBottom: 4 }}>
                      <Button size="small" icon={<ThunderboltOutlined />} onClick={handleRegenerateTemplate}>
                        {t('Generate Template')}
                      </Button>
                    </Form.Item>
                  </>
                )}
              </div>
            ) : (
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'flex-end' }}>
                <Form.Item label={t('Trigger Name')} style={{ marginBottom: 4, flex: '1 1 170px' }}>
                  <Input
                    size="small"
                    value={objectName}
                    onChange={(e) => setObjectName(e.target.value)}
                    placeholder="trg_orders_after_insert"
                  />
                </Form.Item>

                {!editObject && (
                  <>
                    <Form.Item label={t('Target Table')} style={{ marginBottom: 4, flex: '1 1 150px' }}>
                      {availableTables.length > 0 ? (
                        <Select
                          size="small"
                          showSearch
                          value={targetTable}
                          onChange={setTargetTable}
                          options={availableTables.map((tbl) => ({ value: tbl, label: tbl }))}
                        />
                      ) : (
                        <Input
                          size="small"
                          value={targetTable}
                          onChange={(e) => setTargetTable(e.target.value)}
                          placeholder="table_name"
                        />
                      )}
                    </Form.Item>

                    <Form.Item label={t('Timing')} style={{ marginBottom: 4, width: 125 }}>
                      <Select
                        size="small"
                        value={triggerTiming}
                        onChange={setTriggerTiming}
                        options={[
                          ...(dbType !== 'sqlserver' ? [{ value: 'BEFORE', label: 'BEFORE' }] : []),
                          { value: 'AFTER', label: 'AFTER' },
                          ...(!['mysql', 'mariadb', 'tidb', 'oceanbase'].includes(dbType)
                            ? [{ value: 'INSTEAD OF', label: 'INSTEAD OF' }]
                            : []),
                        ]}
                      />
                    </Form.Item>

                    <Form.Item label={t('Event')} style={{ marginBottom: 4, width: 115 }}>
                      <Select
                        size="small"
                        value={triggerEvent}
                        onChange={setTriggerEvent}
                        options={[
                          { value: 'INSERT', label: 'INSERT' },
                          { value: 'UPDATE', label: 'UPDATE' },
                          { value: 'DELETE', label: 'DELETE' },
                        ]}
                      />
                    </Form.Item>

                    <Form.Item style={{ marginBottom: 4 }}>
                      <Button size="small" icon={<ThunderboltOutlined />} onClick={handleRegenerateTemplate}>
                        {t('Generate Template')}
                      </Button>
                    </Form.Item>
                  </>
                )}
              </div>
            )}

            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginTop: 4 }}>
              <Checkbox checked={replaceExisting} onChange={(e) => setReplaceExisting(e.target.checked)}>
                <Typography.Text style={{ fontSize: 12 }}>
                  {t('Drop / Replace existing object with same name before creating')}
                </Typography.Text>
              </Checkbox>
              <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                {`${database}${schema ? '.' + schema : ''} (${dbType})`}
              </Typography.Text>
            </div>
          </Form>
        )}

        <div
          style={{
            height: 'min(360px, 50dvh)',
            border: '1px solid rgba(128,128,128,0.25)',
            borderRadius: 6,
            overflow: 'hidden',
          }}
        >
          <Suspense
            fallback={
              <div style={{ padding: 32, textAlign: 'center' }}>
                <Spin />
              </div>
            }
          >
            <MonacoCodeEditor
              value={sqlText}
              language="sql"
              darkMode={darkMode}
              readOnly={!canWrite}
              onChange={(val) => setSqlText(val)}
            />
          </Suspense>
        </div>
      </Spin>
    </Modal>
  );
};
