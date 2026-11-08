import React, { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Alert,
  AutoComplete,
  Button,
  Col,
  Divider,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Row,
  Segmented,
  Select,
  Space,
  Spin,
  Switch,
  Tabs,
  Tag,
  Tooltip,
  Typography,
  message,
} from 'antd';
import {
  CheckCircleOutlined,
  CodeOutlined,
  CopyOutlined,
  DeleteOutlined,
  EyeOutlined,
  FormOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
  PlusOutlined,
  ReloadOutlined,
  SyncOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons';
import { api } from '../../api/client';
import { useResponsive } from '../../hooks/useResponsive';
import { useI18n } from '../../i18n/useI18n';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import type { DriverCapabilities, ObjectActionPayload, ObjectItem, ObjectSpec } from '../../types';
import { writeTextToClipboard } from '../../utils/tableClipboard';
import { LazyCodeEditor } from '../common/LazyCodeEditor';

const { Text } = Typography;

export type SchemaObjectType =
  | 'VIEW'
  | 'MATERIALIZED_VIEW'
  | 'SEQUENCE'
  | 'FUNCTION'
  | 'PROCEDURE'
  | 'TRIGGER'
  | 'EVENT'
  | 'TYPE'
  | 'EXTENSION'
  | 'PACKAGE'
  | 'PACKAGE_BODY'
  | 'SYNONYM'
  | 'DB_LINK'
  | 'DICTIONARY'
  | 'MACRO';

export interface SchemaObjectModalProps {
  open: boolean;
  mode: 'create' | 'edit';
  initialType: SchemaObjectType | string;
  connId: string;
  dbType: string;
  database: string;
  schema?: string;
  tables?: string[];
  availableExtensions?: ObjectItem[];
  capabilities?: DriverCapabilities;
  canWrite: boolean;
  isDesktop?: boolean;
  userRole?: string;
  editObject?: {
    name: string;
    type: string;
    targetName?: string;
    status?: string;
    comment?: string;
    extra?: Record<string, string>;
  } | null;
  onClose: () => void;
  onSuccess: () => void;
}

const ALL_MODAL_TYPES: SchemaObjectType[] = [
  'VIEW',
  'MATERIALIZED_VIEW',
  'SEQUENCE',
  'FUNCTION',
  'PROCEDURE',
  'TRIGGER',
  'EVENT',
  'TYPE',
  'EXTENSION',
  'PACKAGE',
  'PACKAGE_BODY',
  'SYNONYM',
  'DB_LINK',
  'DICTIONARY',
  'MACRO',
];

function quoteIdent(dbType: string, name: string): string {
  const clean = name.trim();
  if (!clean) return 'object_name';
  const low = dbType.toLowerCase();
  if (low === 'mysql' || low === 'mariadb' || low === 'tidb' || low === 'oceanbase' || low === 'clickhouse') {
    return `\`${clean.replace(/`/g, '``')}\``;
  }
  if (low === 'sqlserver') {
    return `[${clean.replace(/]/g, ']]')}]`;
  }
  return `"${clean.replace(/"/g, '""')}"`;
}

function qualifiedIdent(dbType: string, schema: string | undefined, name: string): string {
  const low = dbType.toLowerCase();
  const qName = quoteIdent(dbType, name);
  if (schema && (low === 'postgres' || low === 'kingbase' || low === 'sqlserver' || low === 'oracle' || low === 'dameng' || low === 'dm8')) {
    return `${quoteIdent(dbType, schema)}.${qName}`;
  }
  return qName;
}

function normalizeObjectType(raw: string): SchemaObjectType {
  const up = (raw || 'VIEW').toUpperCase();
  if (up === 'TABLE_MACRO') return 'MACRO';
  if ((ALL_MODAL_TYPES as string[]).includes(up)) {
    return up as SchemaObjectType;
  }
  return 'VIEW';
}

export const SchemaObjectModal: React.FC<SchemaObjectModalProps> = ({
  open,
  mode,
  initialType,
  connId,
  dbType,
  database,
  schema,
  tables = [],
  availableExtensions = [],
  capabilities,
  canWrite,
  isDesktop = false,
  userRole,
  editObject,
  onClose,
  onSuccess,
}) => {
  const { t } = useI18n();
  const { isMobile } = useResponsive();
  const darkMode = useWorkbenchStore((s) => s.darkMode);
  const lowDb = dbType.toLowerCase();

  const [activeTab, setActiveTab] = useState<'form' | 'sql'>('form');
  const [objType, setObjType] = useState<SchemaObjectType>('VIEW');
  const [objName, setObjName] = useState('');
  const [replaceExisting, setReplaceExisting] = useState(true);

  // Shared SQL / body state
  const [bodySql, setBodySql] = useState('');
  const [rawSql, setRawSql] = useState('');
  const [rawSqlDirty, setRawSqlDirty] = useState(false);
  const [loadingDDL, setLoadingDDL] = useState(false);
  const [previewLoading, setPreviewLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [lifecycleLoading, setLifecycleLoading] = useState<string | null>(null);

  // Trigger / Routine fields
  const [targetTable, setTargetTable] = useState('');
  const [trigTiming, setTrigTiming] = useState<'BEFORE' | 'AFTER' | 'INSTEAD OF'>('BEFORE');
  const [trigEvent, setTrigEvent] = useState<'INSERT' | 'UPDATE' | 'DELETE'>('INSERT');
  const [fnParams, setFnParams] = useState('');
  const [fnReturns, setFnReturns] = useState(' VARCHAR(255)');
  const [procParams, setProcParams] = useState('');

  // Sequence fields
  const [seqDataType, setSeqDataType] = useState('BIGINT');
  const [seqStart, setSeqStart] = useState<number | null>(1);
  const [seqIncrement, setSeqIncrement] = useState<number | null>(1);
  const [seqMin, setSeqMin] = useState<number | null>(null);
  const [seqMax, setSeqMax] = useState<number | null>(null);
  const [seqCache, setSeqCache] = useState<number | null>(20);
  const [seqCycle, setSeqCycle] = useState(false);
  const [seqRestartVal, setSeqRestartVal] = useState<number | null>(1);

  // Materialized View fields
  const [mvPopulate, setMvPopulate] = useState(true);
  const [mvToTable, setMvToTable] = useState('');
  const [mvEngine, setMvEngine] = useState('MergeTree() ORDER BY tuple()');
  const [mvRefreshMode, setMvRefreshMode] = useState('COMPLETE');

  // Event fields (MySQL / MariaDB)
  const [evScheduleKind, setEvScheduleKind] = useState<'EVERY' | 'AT'>('EVERY');
  const [evEveryVal, setEvEveryVal] = useState<number | null>(1);
  const [evEveryUnit, setEvEveryUnit] = useState('HOUR');
  const [evExecuteAt, setEvExecuteAt] = useState('');
  const [evStartsAt, setEvStartsAt] = useState('');
  const [evEndsAt, setEvEndsAt] = useState('');
  const [evPreserve, setEvPreserve] = useState(true);
  const [evEnabled, setEvEnabled] = useState(true);

  // Extension fields (PG / DuckDB)
  const [extVersion, setExtVersion] = useState('');
  const [extCascade, setExtCascade] = useState(false);

  // Type fields (PG ENUM / DOMAIN)
  const [typeKind, setTypeKind] = useState<'ENUM' | 'DOMAIN'>('ENUM');
  const [enumValues, setEnumValues] = useState<string[]>(['active', 'pending', 'archived']);
  const [newEnumValue, setNewEnumValue] = useState('');
  const [domainBaseType, setDomainBaseType] = useState('VARCHAR(255)');
  const [domainCheckExpr, setDomainCheckExpr] = useState('');

  // Synonym & DB Link fields
  const [synTargetOwner, setSynTargetOwner] = useState('');
  const [synTargetObject, setSynTargetObject] = useState('');
  const [isPublicObj, setIsPublicObj] = useState(false);
  const [dbLinkHost, setDbLinkHost] = useState('');
  const [dbLinkUser, setDbLinkUser] = useState('');
  const [dbLinkPass, setDbLinkPass] = useState('');

  // Macro fields (DuckDB)
  const [isTableMacro, setIsTableMacro] = useState(false);
  const [macroParams, setMacroParams] = useState<string[]>(['x']);

  // Dictionary preset (ClickHouse)
  const [dictPreset, setDictPreset] = useState<'clickhouse' | 'postgresql' | 'http'>('clickhouse');

  const creatableTypes = useMemo<SchemaObjectType[]>(() => {
    const capTypes = (capabilities?.objectTypes || [])
      .map((s) => s.toUpperCase())
      .filter((s) => s !== 'TABLE' && (ALL_MODAL_TYPES as string[]).includes(s)) as SchemaObjectType[];
    const list: SchemaObjectType[] =
      capTypes.length > 0 ? [...capTypes] : ['VIEW', 'FUNCTION', 'PROCEDURE', 'TRIGGER'];
    if (list.includes('PACKAGE') && !list.includes('PACKAGE_BODY')) {
      list.push('PACKAGE_BODY');
    }
    return list;
  }, [capabilities]);

  const extensionRestricted = useMemo(() => {
    if (objType !== 'EXTENSION') return false;
    if (isDesktop) return false;
    if (userRole && userRole !== 'admin') return true;
    return false;
  }, [objType, isDesktop, userRole]);

  const typeLabel = useCallback(
    (tp: SchemaObjectType | string): string => {
      switch (tp) {
        case 'VIEW':
          return t('View');
        case 'MATERIALIZED_VIEW':
          return t('Materialized View');
        case 'SEQUENCE':
          return t('Sequence');
        case 'FUNCTION':
          return t('Function');
        case 'PROCEDURE':
          return t('Procedure');
        case 'TRIGGER':
          return t('Trigger');
        case 'EVENT':
          return t('Scheduled Event');
        case 'TYPE':
          return t('Custom Type');
        case 'EXTENSION':
          return t('Extension');
        case 'PACKAGE':
          return t('Package Spec');
        case 'PACKAGE_BODY':
          return t('Package Body');
        case 'SYNONYM':
          return t('Synonym');
        case 'DB_LINK':
          return t('Database Link');
        case 'DICTIONARY':
          return t('Dictionary');
        case 'MACRO':
          return t('Macro');
        default:
          return tp;
      }
    },
    [t]
  );

  const buildDefaultBodyForType = useCallback(
    (tp: SchemaObjectType, nameHint = ''): string => {
      const qName = qualifiedIdent(dbType, schema, nameHint || 'new_object');
      if (tp === 'VIEW' || tp === 'MATERIALIZED_VIEW') {
        const sampleTbl = tables[0] ? qualifiedIdent(dbType, schema, tables[0]) : 'your_table';
        return `SELECT *\nFROM ${sampleTbl}\nLIMIT 100`;
      }
      if (tp === 'EVENT') {
        return 'SELECT 1;';
      }
      if (tp === 'MACRO') {
        return isTableMacro ? 'SELECT 1 AS id, x AS val' : 'x + 1';
      }
      if (tp === 'PACKAGE') {
        return `CREATE OR REPLACE PACKAGE ${qName} AS\n  PROCEDURE noop;\nEND ${nameHint || 'new_object'};`;
      }
      if (tp === 'PACKAGE_BODY') {
        return `CREATE OR REPLACE PACKAGE BODY ${qName} AS\n  PROCEDURE noop IS\n  BEGIN\n    NULL;\n  END noop;\nEND ${nameHint || 'new_object'};`;
      }
      if (tp === 'DICTIONARY') {
        const srcTbl = tables[0] || 'source_table';
        if (dictPreset === 'postgresql') {
          return `CREATE OR REPLACE DICTIONARY ${qName}\n(\n  id UInt64,\n  name String\n)\nPRIMARY KEY id\nSOURCE(POSTGRESQL(host '127.0.0.1' port 5432 user 'postgres' password '' db 'postgres' table '${srcTbl}'))\nLIFETIME(MIN 60 MAX 300)\nLAYOUT(HASHED());`;
        }
        if (dictPreset === 'http') {
          return `CREATE OR REPLACE DICTIONARY ${qName}\n(\n  id UInt64,\n  name String\n)\nPRIMARY KEY id\nSOURCE(HTTP(url 'http://127.0.0.1:8080/dict' format 'JSONEachRow'))\nLIFETIME(300)\nLAYOUT(FLAT());`;
        }
        return `CREATE OR REPLACE DICTIONARY ${qName}\n(\n  id UInt64,\n  name String\n)\nPRIMARY KEY id\nSOURCE(CLICKHOUSE(DB '${database || 'default'}' TABLE '${srcTbl}'))\nLIFETIME(MIN 60 MAX 300)\nLAYOUT(FLAT());`;
      }
      if (tp === 'FUNCTION') {
        if (lowDb === 'clickhouse') {
          return 'x -> x + 1';
        }
        if (lowDb === 'postgres' || lowDb === 'kingbase') {
          return 'BEGIN\n  RETURN NULL;\nEND;';
        }
        if (lowDb === 'sqlserver' || lowDb === 'oracle' || lowDb === 'dameng' || lowDb === 'dm8') {
          return '  RETURN NULL;';
        }
        return 'RETURN NULL;';
      }
      if (tp === 'PROCEDURE') {
        if (lowDb === 'postgres' || lowDb === 'kingbase') {
          return 'BEGIN\n  -- SQL statements\nEND;';
        }
        return '-- SQL statements\nSELECT 1;';
      }
      if (tp === 'TRIGGER') {
        if (lowDb === 'postgres' || lowDb === 'kingbase') {
          return `-- Note: PostgreSQL triggers execute an existing trigger function:\nEXECUTE FUNCTION trigger_handler_fn()`;
        }
        return '-- Trigger SQL statements';
      }
      return '';
    },
    [dbType, schema, tables, isTableMacro, dictPreset, database, lowDb]
  );

  // Initialize modal state on open or target context change (without resetting on internal form state edits)
  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setRawSqlDirty(false);
    setNewEnumValue('');

    if (mode === 'edit' && editObject) {
      const normalized = normalizeObjectType(editObject.type || initialType);
      setObjType(normalized);
      setObjName(editObject.name);
      setTargetTable(editObject.targetName || tables[0] || '');
      setIsTableMacro((editObject.type || '').toUpperCase() === 'TABLE_MACRO');
      setRawSql('');
      setBodySql('');
      setActiveTab(
        normalized === 'SEQUENCE' ||
          normalized === 'MATERIALIZED_VIEW' ||
          normalized === 'EVENT' ||
          normalized === 'EXTENSION' ||
          normalized === 'TYPE'
          ? 'form'
          : 'sql'
      );
      setLoadingDDL(true);
      api
        .getObjectDDL(connId, database, schema || '', editObject.type || normalized, editObject.name)
        .then((res) => {
          if (cancelled) return;
          const ddl = res.ddl || '';
          setRawSql(ddl);
          setBodySql(ddl);
        })
        .catch((e: any) => {
          if (cancelled) return;
          setRawSql(`-- ${e?.message || t('Failed to fetch DDL')}`);
        })
        .finally(() => {
          if (!cancelled) setLoadingDDL(false);
        });
    } else {
      const initNorm = normalizeObjectType(initialType);
      const validInit = creatableTypes.includes(initNorm) ? initNorm : creatableTypes[0] || 'VIEW';
      setObjType(validInit);
      setObjName('');
      setReplaceExisting(true);
      setTargetTable(tables[0] || '');
      setTrigTiming('BEFORE');
      setTrigEvent('INSERT');
      setFnParams('');
      setFnReturns(lowDb === 'postgres' || lowDb === 'kingbase' ? 'TEXT' : 'VARCHAR(255)');
      setProcParams('');
      setSeqDataType('BIGINT');
      setSeqStart(1);
      setSeqIncrement(1);
      setSeqMin(null);
      setSeqMax(null);
      setSeqCache(20);
      setSeqCycle(false);
      setMvPopulate(true);
      setMvToTable('');
      setMvEngine('MergeTree() ORDER BY tuple()');
      setMvRefreshMode('COMPLETE');
      setEvScheduleKind('EVERY');
      setEvEveryVal(1);
      setEvEveryUnit('HOUR');
      setEvExecuteAt('');
      setEvStartsAt('');
      setEvEndsAt('');
      setEvPreserve(true);
      setEvEnabled(true);
      setExtVersion('');
      setExtCascade(false);
      setTypeKind('ENUM');
      setEnumValues(['active', 'pending', 'archived']);
      setDomainBaseType('VARCHAR(255)');
      setDomainCheckExpr('');
      setSynTargetOwner(schema || '');
      setSynTargetObject(tables[0] || '');
      setIsPublicObj(false);
      setDbLinkHost('');
      setDbLinkUser('');
      setDbLinkPass('');
      setIsTableMacro(false);
      setMacroParams(['x']);
      setDictPreset('clickhouse');
      setActiveTab('form');
      const defaultBody = buildDefaultBodyForType(validInit, '');
      setBodySql(defaultBody);
      setRawSql('');
    }
    return () => {
      cancelled = true;
    };
  }, [open, mode, editObject?.name, editObject?.type, initialType, connId, database, schema, lowDb]);

  const buildSpec = useCallback((): ObjectSpec | undefined => {
    switch (objType) {
      case 'SEQUENCE':
        return {
          dataType: seqDataType || undefined,
          startWith: seqStart ?? undefined,
          incrementBy: seqIncrement ?? undefined,
          minValue: seqMin ?? undefined,
          maxValue: seqMax ?? undefined,
          cache: seqCache ?? undefined,
          cycle: seqCycle,
        };
      case 'MATERIALIZED_VIEW':
        return {
          selectSql: bodySql.trim(),
          populate: mvPopulate,
          targetTable: mvToTable.trim() || undefined,
          engineClause: mvEngine.trim() || undefined,
          refreshMode: mvRefreshMode || undefined,
        };
      case 'EVENT':
        return {
          scheduleKind: evScheduleKind,
          everyValue: evScheduleKind === 'EVERY' ? evEveryVal ?? 1 : undefined,
          everyUnit: evScheduleKind === 'EVERY' ? evEveryUnit : undefined,
          executeAt: evScheduleKind === 'AT' ? evExecuteAt.trim() : undefined,
          startsAt: evScheduleKind === 'EVERY' && evStartsAt.trim() ? evStartsAt.trim() : undefined,
          endsAt: evScheduleKind === 'EVERY' && evEndsAt.trim() ? evEndsAt.trim() : undefined,
          preserve: evPreserve,
          enabled: evEnabled,
          bodySql: bodySql.trim(),
        };
      case 'EXTENSION':
        return {
          extensionVersion: extVersion.trim() || undefined,
          cascade: extCascade,
        };
      case 'TYPE':
        return {
          typeKind,
          enumValues: typeKind === 'ENUM' ? enumValues.filter(Boolean) : undefined,
          baseType: typeKind === 'DOMAIN' ? domainBaseType.trim() : undefined,
          checkExpr: typeKind === 'DOMAIN' && domainCheckExpr.trim() ? domainCheckExpr.trim() : undefined,
        };
      case 'SYNONYM':
        return {
          targetOwner: synTargetOwner.trim() || undefined,
          targetObject: synTargetObject.trim(),
          public: isPublicObj,
        };
      case 'DB_LINK':
        return {
          dbLinkHost: dbLinkHost.trim(),
          dbLinkUser: dbLinkUser.trim() || undefined,
          dbLinkPass: dbLinkPass || undefined,
          public: isPublicObj,
        };
      case 'MACRO':
        return {
          isTableMacro,
          parameters: macroParams.map((p) => p.trim()).filter(Boolean),
          macroExpr: bodySql.trim(),
        };
      default:
        return undefined;
    }
  }, [
    objType,
    seqDataType,
    seqStart,
    seqIncrement,
    seqMin,
    seqMax,
    seqCache,
    seqCycle,
    bodySql,
    mvPopulate,
    mvToTable,
    mvEngine,
    mvRefreshMode,
    evScheduleKind,
    evEveryVal,
    evEveryUnit,
    evExecuteAt,
    evStartsAt,
    evEndsAt,
    evPreserve,
    evEnabled,
    extVersion,
    extCascade,
    typeKind,
    enumValues,
    domainBaseType,
    domainCheckExpr,
    synTargetOwner,
    synTargetObject,
    isPublicObj,
    dbLinkHost,
    dbLinkUser,
    dbLinkPass,
    isTableMacro,
    macroParams,
  ]);

  const buildLocalPreviewSQL = useCallback((): string => {
    const cleanName = objName.trim() || `new_${objType.toLowerCase()}`;
    const qObj = qualifiedIdent(dbType, schema, cleanName);
    const qTbl = targetTable ? qualifiedIdent(dbType, schema, targetTable) : qualifiedIdent(dbType, schema, 'table_name');

    switch (objType) {
      case 'VIEW': {
        const orReplace = replaceExisting && lowDb !== 'sqlite' ? 'OR REPLACE ' : '';
        return `CREATE ${orReplace}VIEW ${qObj} AS\n${bodySql.trim() || 'SELECT 1'};`;
      }
      case 'MATERIALIZED_VIEW': {
        if (lowDb === 'clickhouse') {
          const toPart = mvToTable.trim()
            ? ` TO ${quoteIdent(dbType, mvToTable.trim())}`
            : ` ENGINE = ${mvEngine.trim() || 'MergeTree() ORDER BY tuple()'}`;
          const popPart = !mvToTable.trim() && mvPopulate ? ' POPULATE' : '';
          return `CREATE MATERIALIZED VIEW IF NOT EXISTS ${qObj}${toPart}${popPart} AS\n${bodySql.trim() || 'SELECT 1'};`;
        }
        if (lowDb === 'oracle' || lowDb === 'dameng' || lowDb === 'dm8') {
          const buildClause = mvPopulate ? 'BUILD IMMEDIATE' : 'BUILD DEFERRED';
          return `CREATE MATERIALIZED VIEW ${qObj}\n${buildClause}\nREFRESH ${mvRefreshMode || 'COMPLETE'} ON DEMAND\nAS\n${bodySql.trim() || 'SELECT 1 FROM DUAL'};`;
        }
        const withData = mvPopulate ? 'WITH DATA' : 'WITH NO DATA';
        return `CREATE MATERIALIZED VIEW IF NOT EXISTS ${qObj} AS\n${bodySql.trim() || 'SELECT 1'}\n${withData};`;
      }
      case 'SEQUENCE': {
        const parts: string[] = [`CREATE SEQUENCE ${qObj}`];
        if ((lowDb === 'postgres' || lowDb === 'kingbase' || lowDb === 'sqlserver') && seqDataType) {
          parts.push(`AS ${seqDataType}`);
        }
        if (seqIncrement !== null) parts.push(`INCREMENT BY ${seqIncrement}`);
        if (seqMin !== null) parts.push(`MINVALUE ${seqMin}`);
        if (seqMax !== null) parts.push(`MAXVALUE ${seqMax}`);
        if (seqStart !== null) parts.push(`START WITH ${seqStart}`);
        if (seqCache !== null && seqCache > 0 && lowDb !== 'duckdb') parts.push(`CACHE ${seqCache}`);
        if (seqCycle) parts.push('CYCLE');
        return `${parts.join(' ')};`;
      }
      case 'EVENT': {
        const sched =
          evScheduleKind === 'AT'
            ? `AT '${(evExecuteAt || '2026-12-31 23:59:59').replace(/'/g, "''")}'`
            : `EVERY ${evEveryVal ?? 1} ${evEveryUnit}${evStartsAt ? ` STARTS '${evStartsAt.replace(/'/g, "''")}'` : ''}${evEndsAt ? ` ENDS '${evEndsAt.replace(/'/g, "''")}'` : ''}`;
        const preserveStr = evPreserve ? 'ON COMPLETION PRESERVE' : 'ON COMPLETION NOT PRESERVE';
        const enableStr = evEnabled ? 'ENABLE' : 'DISABLE';
        return `CREATE EVENT IF NOT EXISTS ${qObj}\nON SCHEDULE ${sched}\n${preserveStr}\n${enableStr}\nDO\nBEGIN\n  ${bodySql.trim() || 'SELECT 1;'}\nEND;`;
      }
      case 'EXTENSION': {
        if (lowDb === 'duckdb') {
          return `INSTALL ${cleanName};\nLOAD ${cleanName};`;
        }
        const verPart = extVersion.trim() ? ` VERSION '${extVersion.trim().replace(/'/g, "''")}'` : '';
        const casPart = extCascade ? ' CASCADE' : '';
        return `CREATE EXTENSION IF NOT EXISTS ${quoteIdent(dbType, cleanName)}${verPart}${casPart};`;
      }
      case 'TYPE': {
        if (typeKind === 'DOMAIN') {
          const chk = domainCheckExpr.trim() ? ` CHECK (${domainCheckExpr.trim()})` : '';
          return `CREATE DOMAIN ${qObj} AS ${domainBaseType.trim() || 'VARCHAR(255)'}${chk};`;
        }
        const vals = enumValues.map((v) => `'${v.replace(/'/g, "''")}'`).join(', ');
        return `CREATE TYPE ${qObj} AS ENUM (${vals});`;
      }
      case 'PACKAGE':
      case 'PACKAGE_BODY':
      case 'DICTIONARY': {
        return bodySql.trim() || buildDefaultBodyForType(objType, cleanName);
      }
      case 'SYNONYM': {
        const targetRef = synTargetOwner.trim()
          ? `${quoteIdent(dbType, synTargetOwner.trim())}.${quoteIdent(dbType, synTargetObject.trim() || 'target_object')}`
          : quoteIdent(dbType, synTargetObject.trim() || 'target_object');
        if (lowDb === 'sqlserver') {
          return `CREATE SYNONYM ${qObj} FOR ${targetRef};`;
        }
        const pub = isPublicObj ? 'PUBLIC ' : '';
        const orRep = replaceExisting ? 'OR REPLACE ' : '';
        return `CREATE ${orRep}${pub}SYNONYM ${isPublicObj ? quoteIdent(dbType, cleanName) : qObj} FOR ${targetRef};`;
      }
      case 'DB_LINK': {
        const pub = isPublicObj ? 'PUBLIC ' : '';
        const auth = dbLinkUser.trim()
          ? ` CONNECT TO ${quoteIdent(dbType, dbLinkUser.trim())} IDENTIFIED BY "${dbLinkPass ? '******' : ''}"`
          : '';
        return `CREATE ${pub}DATABASE LINK ${quoteIdent(dbType, cleanName)}${auth} USING '${dbLinkHost.trim().replace(/'/g, "''")}';`;
      }
      case 'MACRO': {
        const args = macroParams.map((p) => p.trim()).filter(Boolean).join(', ');
        if (isTableMacro) {
          return `CREATE OR REPLACE MACRO ${qObj}(${args}) AS TABLE\n${bodySql.trim() || 'SELECT 1'};`;
        }
        return `CREATE OR REPLACE MACRO ${qObj}(${args}) AS (${bodySql.trim() || 'x + 1'});`;
      }
      case 'FUNCTION': {
        if (lowDb === 'clickhouse') {
          return `CREATE OR REPLACE FUNCTION ${quoteIdent(dbType, cleanName)} AS ${bodySql.trim() || '(x -> x + 1)'};`;
        }
        if (lowDb === 'postgres' || lowDb === 'kingbase') {
          return `CREATE OR REPLACE FUNCTION ${qObj}(${fnParams})\nRETURNS ${fnReturns || 'TEXT'}\nLANGUAGE plpgsql\nAS $$\n${bodySql.trim() || 'BEGIN\n  RETURN NULL;\nEND;'}\n$$;`;
        }
        if (lowDb === 'sqlserver') {
          return `CREATE OR ALTER FUNCTION ${qObj}(${fnParams})\nRETURNS ${fnReturns || 'NVARCHAR(255)'}\nAS\nBEGIN\n${bodySql.trim() || '  RETURN NULL;'}\nEND;`;
        }
        if (lowDb === 'oracle' || lowDb === 'dameng' || lowDb === 'dm8') {
          return `CREATE OR REPLACE FUNCTION ${qObj}${fnParams ? `(${fnParams})` : ''}\nRETURN ${fnReturns || 'VARCHAR2'}\nIS\nBEGIN\n${bodySql.trim() || '  RETURN NULL;'}\nEND;`;
        }
        return `CREATE FUNCTION ${qObj}(${fnParams})\nRETURNS ${fnReturns || 'VARCHAR(255)'}\nDETERMINISTIC\nBEGIN\n  ${bodySql.trim() || 'RETURN NULL;'}\nEND`;
      }
      case 'PROCEDURE': {
        if (lowDb === 'postgres' || lowDb === 'kingbase') {
          return `CREATE OR REPLACE PROCEDURE ${qObj}(${procParams})\nLANGUAGE plpgsql\nAS $$\n${bodySql.trim() || 'BEGIN\nEND;'}\n$$;`;
        }
        if (lowDb === 'sqlserver') {
          return `CREATE OR ALTER PROCEDURE ${qObj}${procParams ? ` ${procParams}` : ''}\nAS\nBEGIN\n  SET NOCOUNT ON;\n  ${bodySql.trim() || 'SELECT 1;'}\nEND;`;
        }
        if (lowDb === 'oracle' || lowDb === 'dameng' || lowDb === 'dm8') {
          return `CREATE OR REPLACE PROCEDURE ${qObj}${procParams ? `(${procParams})` : ''}\nIS\nBEGIN\n  ${bodySql.trim() || 'NULL;'}\nEND;`;
        }
        return `CREATE PROCEDURE ${qObj}(${procParams})\nBEGIN\n  ${bodySql.trim() || 'SELECT 1;'}\nEND`;
      }
      case 'TRIGGER': {
        if (lowDb === 'sqlite') {
          return `CREATE TRIGGER IF NOT EXISTS ${qObj}\n${trigTiming} ${trigEvent} ON ${qTbl}\nFOR EACH ROW\nBEGIN\n  ${bodySql.trim() || 'SELECT 1;'}\nEND;`;
        }
        if (lowDb === 'postgres' || lowDb === 'kingbase') {
          return `CREATE OR REPLACE TRIGGER ${quoteIdent(dbType, cleanName)}\n${trigTiming} ${trigEvent} ON ${qTbl}\nFOR EACH ROW\n${bodySql.trim() || 'EXECUTE FUNCTION trigger_handler_fn()'};`;
        }
        if (lowDb === 'sqlserver') {
          const mssqlTiming = trigTiming === 'INSTEAD OF' ? 'INSTEAD OF' : 'AFTER';
          return `CREATE OR ALTER TRIGGER ${qObj}\nON ${qTbl}\n${mssqlTiming} ${trigEvent}\nAS\nBEGIN\n  SET NOCOUNT ON;\n  ${bodySql.trim()}\nEND;`;
        }
        if (lowDb === 'oracle' || lowDb === 'dameng' || lowDb === 'dm8') {
          return `CREATE OR REPLACE TRIGGER ${qObj}\n${trigTiming} ${trigEvent} ON ${qTbl}\nFOR EACH ROW\nBEGIN\n  ${bodySql.trim() || 'NULL;'}\nEND;`;
        }
        return `CREATE TRIGGER ${qObj}\n${trigTiming === 'INSTEAD OF' ? 'BEFORE' : trigTiming} ${trigEvent} ON ${qTbl}\nFOR EACH ROW\nBEGIN\n  ${bodySql.trim()}\nEND`;
      }
      default:
        return bodySql;
    }
  }, [
    objName,
    objType,
    dbType,
    schema,
    targetTable,
    replaceExisting,
    lowDb,
    bodySql,
    mvToTable,
    mvEngine,
    mvPopulate,
    mvRefreshMode,
    seqDataType,
    seqIncrement,
    seqMin,
    seqMax,
    seqStart,
    seqCache,
    seqCycle,
    evScheduleKind,
    evExecuteAt,
    evEveryVal,
    evEveryUnit,
    evStartsAt,
    evEndsAt,
    evPreserve,
    evEnabled,
    extVersion,
    extCascade,
    typeKind,
    domainCheckExpr,
    domainBaseType,
    enumValues,
    buildDefaultBodyForType,
    synTargetOwner,
    synTargetObject,
    isPublicObj,
    dbLinkUser,
    dbLinkPass,
    dbLinkHost,
    macroParams,
    isTableMacro,
    fnParams,
    fnReturns,
    procParams,
    trigTiming,
    trigEvent,
  ]);

  const buildActionPayload = useCallback(
    (previewOnly = false): ObjectActionPayload => {
      const cleanName = objName.trim() || `new_${objType.toLowerCase()}`;
      const generatedSql = rawSqlDirty && rawSql.trim() ? rawSql.trim() : buildLocalPreviewSQL();
      const spec = buildSpec();

      if (objType === 'VIEW') {
        return {
          database,
          schema: schema || '',
          objectName: cleanName,
          objectType: 'VIEW',
          action: 'CREATE_VIEW',
          viewSql: rawSqlDirty && rawSql.trim() ? rawSql.trim() : bodySql.trim(),
          replaceExisting,
          previewOnly,
        };
      }
      if (objType === 'FUNCTION' || objType === 'PROCEDURE') {
        return {
          database,
          schema: schema || '',
          objectName: cleanName,
          objectType: objType,
          action: 'CREATE_ROUTINE',
          routineSql: generatedSql,
          replaceExisting: true,
          previewOnly,
        };
      }
      if (objType === 'TRIGGER') {
        return {
          database,
          schema: schema || '',
          objectName: cleanName,
          objectType: 'TRIGGER',
          action: 'CREATE_TRIGGER',
          targetName: targetTable || editObject?.targetName,
          routineSql: generatedSql,
          replaceExisting: true,
          previewOnly,
        };
      }
      if (objType === 'PACKAGE' || objType === 'PACKAGE_BODY' || objType === 'DICTIONARY') {
        return {
          database,
          schema: schema || '',
          objectName: cleanName,
          objectType: objType,
          action: 'CREATE_OR_REPLACE',
          routineSql: generatedSql,
          replaceExisting: true,
          previewOnly,
        };
      }
      if (objType === 'EXTENSION' && lowDb === 'duckdb') {
        return {
          database,
          schema: schema || '',
          objectName: cleanName,
          objectType: 'EXTENSION',
          action: 'INSTALL',
          routineSql: rawSqlDirty ? rawSql.trim() : undefined,
          spec,
          previewOnly,
        };
      }
      return {
        database,
        schema: schema || '',
        objectName: cleanName,
        objectType: objType === 'MACRO' && isTableMacro ? 'TABLE_MACRO' : objType,
        action: 'CREATE_OR_REPLACE',
        viewSql: objType === 'MATERIALIZED_VIEW' ? (rawSqlDirty ? rawSql.trim() : bodySql.trim()) : undefined,
        routineSql: rawSqlDirty ? rawSql.trim() : undefined,
        replaceExisting,
        spec,
        previewOnly,
      };
    },
    [
      objName,
      objType,
      rawSqlDirty,
      rawSql,
      buildLocalPreviewSQL,
      buildSpec,
      database,
      schema,
      bodySql,
      replaceExisting,
      targetTable,
      editObject?.targetName,
      lowDb,
      isTableMacro,
    ]
  );

  const refreshSqlPreview = useCallback(async () => {
    if (mode === 'edit' && !rawSqlDirty && rawSql.trim()) {
      return;
    }
    const fallbackSql = buildLocalPreviewSQL();
    if (!rawSqlDirty) {
      setRawSql(fallbackSql);
    }
    if (!objName.trim() && mode === 'create') {
      return;
    }
    setPreviewLoading(true);
    try {
      const payload = buildActionPayload(true);
      const res = await api.objectAction(connId, payload);
      if (res.sqls && res.sqls.length > 0 && !res.sqls[0].trim().startsWith('/*')) {
        if (!rawSqlDirty) {
          setRawSql(res.sqls.join(';\n\n'));
        }
      }
    } catch {
      // Keep client-side dialect preview if backend preview errors on incomplete draft
    } finally {
      setPreviewLoading(false);
    }
  }, [mode, rawSqlDirty, rawSql, buildLocalPreviewSQL, objName, buildActionPayload, connId]);

  const handleTabChange = (nextKey: string) => {
    const key = nextKey as 'form' | 'sql';
    setActiveTab(key);
    if (key === 'sql' && !rawSqlDirty && mode === 'create') {
      void refreshSqlPreview();
    }
  };

  const handleTypeSwitch = (nextVal: string) => {
    const next = nextVal as SchemaObjectType;
    setObjType(next);
    setRawSqlDirty(false);
    setBodySql(buildDefaultBodyForType(next, objName));
  };

  const runLifecycleAction = async (action: string, extraSpec?: ObjectSpec) => {
    const targetObjName = (editObject?.name || objName).trim();
    if (!targetObjName) return;
    setLifecycleLoading(action);
    try {
      const res = await api.objectAction(connId, {
        database,
        schema: schema || '',
        objectName: targetObjName,
        objectType: editObject?.type || objType,
        targetName: targetTable || editObject?.targetName,
        action,
        spec: extraSpec,
      });
      message.success(res.message || t('Operation completed'));
      onSuccess();
      if (action === 'ADD_ENUM_VALUE') {
        setNewEnumValue('');
      }
    } catch (e: any) {
      message.error(e?.message || t('Operation failed'));
    } finally {
      setLifecycleLoading(null);
    }
  };

  const handleDrop = async () => {
    if (!editObject) return;
    setSaving(true);
    try {
      await api.objectAction(connId, {
        database,
        schema: schema || '',
        objectName: editObject.name,
        objectType: editObject.type || objType,
        targetName: editObject.targetName || targetTable || undefined,
        action: 'DROP',
        spec: extCascade ? { cascade: true } : undefined,
      });
      message.success(t('Object dropped'));
      onSuccess();
      onClose();
    } catch (e: any) {
      message.error(e?.message || t('Drop failed'));
    } finally {
      setSaving(false);
    }
  };

  const handleSubmit = async () => {
    if (!objName.trim()) {
      message.warning(t('Please enter an object name'));
      return;
    }
    if (objType === 'TRIGGER' && !targetTable && !rawSqlDirty) {
      message.warning(t('Please select a target table for the trigger'));
      return;
    }
    if (objType === 'SYNONYM' && !synTargetObject.trim() && !rawSqlDirty) {
      message.warning(t('Please enter the target object name'));
      return;
    }
    if (objType === 'DB_LINK' && !dbLinkHost.trim() && !rawSqlDirty) {
      message.warning(t('Please enter the remote host or connect string'));
      return;
    }

    setSaving(true);
    try {
      if (mode === 'edit' && activeTab === 'sql' && rawSql.trim()) {
        // When user edits raw DDL in Edit mode, execute via objectAction if supported or raw query
        const payload = buildActionPayload(false);
        payload.routineSql = rawSql.trim();
        if (objType === 'VIEW' || objType === 'MATERIALIZED_VIEW') {
          payload.viewSql = rawSql.trim();
        }
        await api.objectAction(connId, payload);
      } else {
        const payload = buildActionPayload(false);
        await api.objectAction(connId, payload);
      }
      message.success(
        mode === 'create'
          ? `${typeLabel(objType)} "${objName.trim()}" ${t('created')}`
          : t('Definition updated successfully')
      );
      onSuccess();
      onClose();
    } catch (e: any) {
      message.error(e?.message || t('Execution failed'));
    } finally {
      setSaving(false);
    }
  };

  const extensionOptions = useMemo(() => {
    return availableExtensions.map((ext) => ({
      value: ext.name,
      label: (
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 8 }}>
          <span style={{ fontWeight: 500 }}>{ext.name}</span>
          <Space size={4}>
            {ext.status && <Tag style={{ margin: 0, fontSize: 11 }}>{ext.status}</Tag>}
            {ext.comment && (
              <Text type="secondary" ellipsis style={{ maxWidth: 260, fontSize: 11 }}>
                {ext.comment}
              </Text>
            )}
          </Space>
        </div>
      ),
    }));
  }, [availableExtensions]);

  const renderLifecycleToolbar = () => {
    if (mode !== 'edit' || !canWrite || !editObject) return null;
    const items: React.ReactNode[] = [];

    if (objType === 'MATERIALIZED_VIEW') {
      items.push(
        <Button
          key="refresh"
          size="small"
          type="primary"
          ghost
          icon={<SyncOutlined />}
          loading={lifecycleLoading === 'REFRESH'}
          onClick={() => void runLifecycleAction('REFRESH')}
        >
          {t('Refresh View')}
        </Button>
      );
      if (
        capabilities?.matViewActions?.includes('REFRESH_CONCURRENTLY') ||
        lowDb === 'postgres' ||
        lowDb === 'kingbase'
      ) {
        items.push(
          <Button
            key="refresh-conc"
            size="small"
            icon={<SyncOutlined />}
            loading={lifecycleLoading === 'REFRESH_CONCURRENTLY'}
            onClick={() => void runLifecycleAction('REFRESH_CONCURRENTLY')}
          >
            {t('Refresh Concurrently')}
          </Button>
        );
      }
    }

    if (objType === 'SEQUENCE') {
      items.push(
        <div
          key="seq-restart"
          style={{ display: 'flex', flexWrap: 'wrap', gap: 6, width: isMobile ? '100%' : undefined }}
        >
          <Space.Compact size="small" style={isMobile ? { width: '100%' } : undefined}>
            <InputNumber
              value={seqRestartVal}
              onChange={(v) => setSeqRestartVal(v)}
              placeholder="1"
              style={{ width: isMobile ? 88 : 110 }}
            />
            <Button
              type="primary"
              ghost
              style={isMobile ? { flex: 1 } : undefined}
              icon={<ReloadOutlined />}
              loading={lifecycleLoading === 'RESTART'}
              onClick={() => void runLifecycleAction('RESTART', { restartWith: seqRestartVal ?? 1 })}
            >
              {t('Restart Sequence')}
            </Button>
          </Space.Compact>
        </div>
      );
    }

    if (objType === 'EVENT') {
      items.push(
        <Button
          key="ev-enable"
          size="small"
          type="primary"
          ghost
          icon={<PlayCircleOutlined />}
          loading={lifecycleLoading === 'ENABLE'}
          onClick={() => void runLifecycleAction('ENABLE')}
        >
          {t('Enable')}
        </Button>,
        <Button
          key="ev-disable"
          size="small"
          icon={<PauseCircleOutlined />}
          loading={lifecycleLoading === 'DISABLE'}
          onClick={() => void runLifecycleAction('DISABLE')}
        >
          {t('Disable')}
        </Button>
      );
    }

    if (objType === 'PACKAGE' || objType === 'PACKAGE_BODY') {
      items.push(
        <Button
          key="pkg-compile"
          size="small"
          type="primary"
          ghost
          icon={<CheckCircleOutlined />}
          loading={lifecycleLoading === 'COMPILE'}
          onClick={() => void runLifecycleAction('COMPILE')}
        >
          {t('Compile')}
        </Button>,
        <Button
          key="pkg-compile-body"
          size="small"
          icon={<ThunderboltOutlined />}
          loading={lifecycleLoading === 'COMPILE_BODY'}
          onClick={() => void runLifecycleAction('COMPILE_BODY')}
        >
          {t('Compile Body')}
        </Button>
      );
    }

    if (objType === 'DICTIONARY') {
      items.push(
        <Button
          key="dict-reload"
          size="small"
          type="primary"
          ghost
          icon={<ReloadOutlined />}
          loading={lifecycleLoading === 'RELOAD'}
          onClick={() => void runLifecycleAction('RELOAD')}
        >
          {t('Reload Dictionary')}
        </Button>
      );
    }

    if (objType === 'EXTENSION' && lowDb === 'duckdb' && !extensionRestricted) {
      items.push(
        <Button
          key="ext-load"
          size="small"
          type="primary"
          ghost
          icon={<PlayCircleOutlined />}
          loading={lifecycleLoading === 'LOAD'}
          onClick={() => void runLifecycleAction('LOAD')}
        >
          {t('Load Extension')}
        </Button>
      );
    }

    if (objType === 'TYPE') {
      items.push(
        <div
          key="enum-add"
          style={{ display: 'flex', flexWrap: 'wrap', gap: 6, width: isMobile ? '100%' : undefined }}
        >
          <Space.Compact size="small" style={isMobile ? { width: '100%' } : undefined}>
            <Input
              value={newEnumValue}
              onChange={(e) => setNewEnumValue(e.target.value)}
              placeholder={t('New enum label')}
              style={{ width: isMobile ? undefined : 150, flex: isMobile ? 1 : undefined, minWidth: 0 }}
            />
            <Button
              type="primary"
              ghost
              icon={<PlusOutlined />}
              disabled={!newEnumValue.trim()}
              loading={lifecycleLoading === 'ADD_ENUM_VALUE'}
              onClick={() => void runLifecycleAction('ADD_ENUM_VALUE', { newEnumValue: newEnumValue.trim() })}
            >
              {t('Add Enum Value')}
            </Button>
          </Space.Compact>
        </div>
      );
    }

    if (items.length === 0) return null;
    return (
      <div
        style={{
          marginBottom: 12,
          padding: '8px 12px',
          borderRadius: 8,
          background: 'rgba(22, 119, 255, 0.06)',
          border: '1px solid rgba(22, 119, 255, 0.18)',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          flexWrap: 'wrap',
          gap: 8,
        }}
      >
        <Space size={6} wrap>
          <Text strong style={{ fontSize: 12 }}>
            {t('Quick Actions')}:
          </Text>
          {editObject.status && <Tag color="blue" style={{ margin: 0 }}>{editObject.status}</Tag>}
        </Space>
        <Space size={6} wrap style={isMobile ? { width: '100%' } : undefined}>
          {items}
        </Space>
      </div>
    );
  };

  const renderFormFields = () => {
    return (
      <Form layout="vertical" size="small">
        {mode === 'create' && creatableTypes.length > 1 && (
          <Form.Item label={t('Object Category')} style={{ marginBottom: 10 }}>
            <Select
              value={objType}
              onChange={handleTypeSwitch}
              options={creatableTypes.map((tp) => ({ value: tp, label: typeLabel(tp) }))}
            />
          </Form.Item>
        )}

        <Row gutter={[10, 8]}>
          <Col xs={24} sm={objType === 'TRIGGER' ? 12 : 16}>
            <Form.Item label={t('Object Name')} required style={{ marginBottom: 10 }}>
              {objType === 'EXTENSION' && mode === 'create' ? (
                <AutoComplete
                  value={objName}
                  onChange={setObjName}
                  options={extensionOptions}
                  filterOption={(inputValue, option) =>
                    String(option?.value || '')
                      .toLowerCase()
                      .includes(inputValue.toLowerCase())
                  }
                  placeholder={lowDb === 'duckdb' ? 'httpfs / parquet / json' : 'uuid-ossp / pg_trgm / postgis'}
                />
              ) : (
                <Input
                  value={objName}
                  disabled={mode === 'edit'}
                  onChange={(e) => setObjName(e.target.value)}
                  placeholder={`my_${objType.toLowerCase()}`}
                />
              )}
            </Form.Item>
          </Col>

          {objType === 'TRIGGER' && (
            <Col xs={24} sm={12}>
              <Form.Item label={t('Target Table')} required style={{ marginBottom: 10 }}>
                <Select
                  showSearch
                  value={targetTable || undefined}
                  onChange={setTargetTable}
                  placeholder={t('Select table')}
                  options={tables.map((tbl) => ({ label: tbl, value: tbl }))}
                />
              </Form.Item>
            </Col>
          )}

          {(objType === 'VIEW' || objType === 'SYNONYM') && (
            <Col xs={24} sm={8}>
              <Form.Item label={t('Replace Existing')} style={{ marginBottom: 10 }}>
                <Switch checked={replaceExisting} onChange={setReplaceExisting} />
              </Form.Item>
            </Col>
          )}
        </Row>

        {objType === 'SEQUENCE' && (
          <Row gutter={[10, 8]}>
            {(lowDb === 'postgres' || lowDb === 'kingbase' || lowDb === 'sqlserver') && (
              <Col xs={12} sm={6}>
                <Form.Item label={t('Data Type')} style={{ marginBottom: 10 }}>
                  <Select
                    value={seqDataType}
                    onChange={setSeqDataType}
                    options={[
                      { value: 'BIGINT', label: 'BIGINT' },
                      { value: 'INTEGER', label: 'INTEGER' },
                      { value: 'SMALLINT', label: 'SMALLINT' },
                    ]}
                  />
                </Form.Item>
              </Col>
            )}
            <Col xs={12} sm={6}>
              <Form.Item label={t('Start With')} style={{ marginBottom: 10 }}>
                <InputNumber value={seqStart} onChange={setSeqStart} style={{ width: '100%' }} />
              </Form.Item>
            </Col>
            <Col xs={12} sm={6}>
              <Form.Item label={t('Increment By')} style={{ marginBottom: 10 }}>
                <InputNumber value={seqIncrement} onChange={setSeqIncrement} style={{ width: '100%' }} />
              </Form.Item>
            </Col>
            <Col xs={12} sm={6}>
              <Form.Item label={t('Cache Size')} style={{ marginBottom: 10 }}>
                <InputNumber value={seqCache} min={0} onChange={setSeqCache} style={{ width: '100%' }} />
              </Form.Item>
            </Col>
            <Col xs={12} sm={8}>
              <Form.Item label={t('Min Value')} style={{ marginBottom: 10 }}>
                <InputNumber
                  value={seqMin}
                  onChange={setSeqMin}
                  placeholder={t('Default')}
                  style={{ width: '100%' }}
                />
              </Form.Item>
            </Col>
            <Col xs={12} sm={8}>
              <Form.Item label={t('Max Value')} style={{ marginBottom: 10 }}>
                <InputNumber
                  value={seqMax}
                  onChange={setSeqMax}
                  placeholder={t('Default')}
                  style={{ width: '100%' }}
                />
              </Form.Item>
            </Col>
            <Col xs={12} sm={8}>
              <Form.Item label={t('Cycle on Limit')} style={{ marginBottom: 10 }}>
                <Switch checked={seqCycle} onChange={setSeqCycle} />
              </Form.Item>
            </Col>
          </Row>
        )}

        {objType === 'MATERIALIZED_VIEW' && (
          <>
            <Row gutter={[10, 8]}>
              <Col xs={12} sm={6}>
                <Form.Item label={t('Populate Now')} style={{ marginBottom: 10 }}>
                  <Switch checked={mvPopulate} onChange={setMvPopulate} />
                </Form.Item>
              </Col>
              {lowDb === 'clickhouse' && (
                <>
                  <Col xs={24} sm={9}>
                    <Form.Item label={t('Target Table (TO table)')} style={{ marginBottom: 10 }}>
                      <AutoComplete
                        value={mvToTable}
                        onChange={setMvToTable}
                        options={tables.map((tbl) => ({ value: tbl }))}
                        placeholder={t('Optional target table')}
                      />
                    </Form.Item>
                  </Col>
                  <Col xs={24} sm={9}>
                    <Form.Item label={t('Storage Engine')} style={{ marginBottom: 10 }}>
                      <Input
                        value={mvEngine}
                        disabled={!!mvToTable.trim()}
                        onChange={(e) => setMvEngine(e.target.value)}
                        placeholder="SummingMergeTree() ORDER BY id"
                      />
                    </Form.Item>
                  </Col>
                </>
              )}
              {(lowDb === 'oracle' || lowDb === 'dameng' || lowDb === 'dm8') && (
                <Col xs={12} sm={10}>
                  <Form.Item label={t('Refresh Mode')} style={{ marginBottom: 10 }}>
                    <Select
                      value={mvRefreshMode}
                      onChange={setMvRefreshMode}
                      options={[
                        { value: 'COMPLETE', label: 'COMPLETE' },
                        { value: 'FAST', label: 'FAST' },
                        { value: 'FORCE', label: 'FORCE' },
                      ]}
                    />
                  </Form.Item>
                </Col>
              )}
            </Row>
            <Form.Item label={t('SELECT Query')} required style={{ marginBottom: 0 }}>
              <div style={{ border: '1px solid rgba(128,128,128,0.25)', borderRadius: 8, overflow: 'hidden' }}>
                <LazyCodeEditor value={bodySql} onChange={setBodySql} language="sql" height={200} darkMode={darkMode} readOnly={!canWrite} />
              </div>
            </Form.Item>
          </>
        )}

        {objType === 'VIEW' && (
          <Form.Item label={t('SELECT Query')} required style={{ marginBottom: 0 }}>
            <div style={{ border: '1px solid rgba(128,128,128,0.25)', borderRadius: 8, overflow: 'hidden' }}>
              <LazyCodeEditor value={bodySql} onChange={setBodySql} language="sql" height={220} darkMode={darkMode} readOnly={!canWrite} />
            </div>
          </Form.Item>
        )}

        {objType === 'EVENT' && (
          <>
            <Row gutter={[10, 8]}>
              <Col xs={24} sm={6}>
                <Form.Item label={t('Schedule Type')} style={{ marginBottom: 10 }}>
                  <Segmented
                    block
                    value={evScheduleKind}
                    onChange={(v) => setEvScheduleKind(v as 'EVERY' | 'AT')}
                    options={[
                      { value: 'EVERY', label: t('Recurring') },
                      { value: 'AT', label: t('One-Time') },
                    ]}
                  />
                </Form.Item>
              </Col>
              {evScheduleKind === 'EVERY' ? (
                <>
                  <Col xs={12} sm={6}>
                    <Form.Item label={t('Every')} style={{ marginBottom: 10 }}>
                      <InputNumber min={1} value={evEveryVal} onChange={setEvEveryVal} style={{ width: '100%' }} />
                    </Form.Item>
                  </Col>
                  <Col xs={12} sm={6}>
                    <Form.Item label={t('Interval Unit')} style={{ marginBottom: 10 }}>
                      <Select
                        value={evEveryUnit}
                        onChange={setEvEveryUnit}
                        options={[
                          { value: 'SECOND', label: 'SECOND' },
                          { value: 'MINUTE', label: 'MINUTE' },
                          { value: 'HOUR', label: 'HOUR' },
                          { value: 'DAY', label: 'DAY' },
                          { value: 'WEEK', label: 'WEEK' },
                          { value: 'MONTH', label: 'MONTH' },
                        ]}
                      />
                    </Form.Item>
                  </Col>
                </>
              ) : (
                <Col xs={24} sm={12}>
                  <Form.Item label={t('Execute At')} required style={{ marginBottom: 10 }}>
                    <Input
                      value={evExecuteAt}
                      onChange={(e) => setEvExecuteAt(e.target.value)}
                      placeholder="2026-12-31 23:59:59"
                    />
                  </Form.Item>
                </Col>
              )}
              <Col xs={6} sm={3}>
                <Form.Item label={t('Preserve')} style={{ marginBottom: 10 }}>
                  <Switch checked={evPreserve} onChange={setEvPreserve} />
                </Form.Item>
              </Col>
              <Col xs={6} sm={3}>
                <Form.Item label={t('Enabled')} style={{ marginBottom: 10 }}>
                  <Switch checked={evEnabled} onChange={setEvEnabled} />
                </Form.Item>
              </Col>
            </Row>
            {evScheduleKind === 'EVERY' && (
              <Row gutter={[10, 8]}>
                <Col xs={24} sm={12}>
                  <Form.Item label={t('Starts At (Optional)')} style={{ marginBottom: 10 }}>
                    <Input
                      value={evStartsAt}
                      onChange={(e) => setEvStartsAt(e.target.value)}
                      placeholder="YYYY-MM-DD HH:mm:ss"
                    />
                  </Form.Item>
                </Col>
                <Col xs={24} sm={12}>
                  <Form.Item label={t('Ends At (Optional)')} style={{ marginBottom: 10 }}>
                    <Input
                      value={evEndsAt}
                      onChange={(e) => setEvEndsAt(e.target.value)}
                      placeholder="YYYY-MM-DD HH:mm:ss"
                    />
                  </Form.Item>
                </Col>
              </Row>
            )}
            <Form.Item label={t('Event SQL Body (DO)')} required style={{ marginBottom: 0 }}>
              <div style={{ border: '1px solid rgba(128,128,128,0.25)', borderRadius: 8, overflow: 'hidden' }}>
                <LazyCodeEditor value={bodySql} onChange={setBodySql} language="sql" height={170} darkMode={darkMode} readOnly={!canWrite} />
              </div>
            </Form.Item>
          </>
        )}

        {objType === 'EXTENSION' && (
          <>
            {extensionRestricted && (
              <Alert
                type="warning"
                showIcon
                style={{ marginBottom: 10 }}
                message={t('Installing extensions in Web Edition requires Administrator role')}
              />
            )}
            <Row gutter={[10, 8]}>
              {lowDb !== 'duckdb' && (
                <Col xs={16} sm={12}>
                  <Form.Item label={t('Extension Version (Optional)')} style={{ marginBottom: 10 }}>
                    <Input
                      value={extVersion}
                      onChange={(e) => setExtVersion(e.target.value)}
                      placeholder={t('Default latest version')}
                    />
                  </Form.Item>
                </Col>
              )}
              <Col xs={8} sm={12}>
                <Form.Item label={t('Cascade Dependencies')} style={{ marginBottom: 10 }}>
                  <Switch checked={extCascade} onChange={setExtCascade} />
                </Form.Item>
              </Col>
            </Row>
          </>
        )}

        {objType === 'TYPE' && (
          <>
            <Form.Item label={t('Type Category')} style={{ marginBottom: 10 }}>
              <Segmented
                value={typeKind}
                onChange={(v) => setTypeKind(v as 'ENUM' | 'DOMAIN')}
                options={[
                  { value: 'ENUM', label: t('ENUM Type') },
                  { value: 'DOMAIN', label: t('DOMAIN Type') },
                ]}
              />
            </Form.Item>
            {typeKind === 'ENUM' ? (
              <Form.Item label={t('Enum Labels')} required style={{ marginBottom: 10 }}>
                <Select
                  mode="tags"
                  value={enumValues}
                  onChange={setEnumValues}
                  tokenSeparators={[',']}
                  placeholder={t('Type a label and press Enter')}
                />
              </Form.Item>
            ) : (
              <Row gutter={[10, 8]}>
                <Col xs={24} sm={10}>
                  <Form.Item label={t('Base Data Type')} required style={{ marginBottom: 10 }}>
                    <Input
                      value={domainBaseType}
                      onChange={(e) => setDomainBaseType(e.target.value)}
                      placeholder="VARCHAR(255)"
                    />
                  </Form.Item>
                </Col>
                <Col xs={24} sm={14}>
                  <Form.Item label={t('CHECK Constraint Expression')} style={{ marginBottom: 10 }}>
                    <Input
                      value={domainCheckExpr}
                      onChange={(e) => setDomainCheckExpr(e.target.value)}
                      placeholder="VALUE <> ''"
                    />
                  </Form.Item>
                </Col>
              </Row>
            )}
          </>
        )}

        {(objType === 'PACKAGE' || objType === 'PACKAGE_BODY') && (
          <Form.Item label={t('PL/SQL Definition')} required style={{ marginBottom: 0 }}>
            <div style={{ border: '1px solid rgba(128,128,128,0.25)', borderRadius: 8, overflow: 'hidden' }}>
              <LazyCodeEditor value={bodySql} onChange={setBodySql} language="sql" height={250} darkMode={darkMode} readOnly={!canWrite} />
            </div>
          </Form.Item>
        )}

        {objType === 'SYNONYM' && (
          <Row gutter={[10, 8]}>
            <Col xs={24} sm={9}>
              <Form.Item label={t('Target Schema / Owner')} style={{ marginBottom: 10 }}>
                <Input
                  value={synTargetOwner}
                  onChange={(e) => setSynTargetOwner(e.target.value)}
                  placeholder={schema || t('Optional owner')}
                />
              </Form.Item>
            </Col>
            <Col xs={24} sm={10}>
              <Form.Item label={t('Target Object')} required style={{ marginBottom: 10 }}>
                <AutoComplete
                  value={synTargetObject}
                  onChange={setSynTargetObject}
                  options={tables.map((tbl) => ({ value: tbl }))}
                  placeholder={t('Target table, view, or sequence')}
                />
              </Form.Item>
            </Col>
            {lowDb !== 'sqlserver' && (
              <Col xs={12} sm={5}>
                <Form.Item label={t('Public Synonym')} style={{ marginBottom: 10 }}>
                  <Switch checked={isPublicObj} onChange={setIsPublicObj} />
                </Form.Item>
              </Col>
            )}
          </Row>
        )}

        {objType === 'DB_LINK' && (
          <Row gutter={[10, 8]}>
            <Col xs={24} sm={12}>
              <Form.Item label={t('Remote Host / Connect String')} required style={{ marginBottom: 10 }}>
                <Input
                  value={dbLinkHost}
                  onChange={(e) => setDbLinkHost(e.target.value)}
                  placeholder="10.0.0.10:1521/ORCL"
                />
              </Form.Item>
            </Col>
            <Col xs={12} sm={6}>
              <Form.Item label={t('Remote Username')} style={{ marginBottom: 10 }}>
                <Input value={dbLinkUser} onChange={(e) => setDbLinkUser(e.target.value)} placeholder="scott" />
              </Form.Item>
            </Col>
            <Col xs={12} sm={6}>
              <Form.Item label={t('Remote Password')} style={{ marginBottom: 10 }}>
                <Input.Password value={dbLinkPass} onChange={(e) => setDbLinkPass(e.target.value)} />
              </Form.Item>
            </Col>
            <Col xs={12} sm={6}>
              <Form.Item label={t('Public DB Link')} style={{ marginBottom: 10 }}>
                <Switch checked={isPublicObj} onChange={setIsPublicObj} />
              </Form.Item>
            </Col>
          </Row>
        )}

        {objType === 'DICTIONARY' && (
          <>
            {mode === 'create' && (
              <Form.Item label={t('Source Template')} style={{ marginBottom: 10 }}>
                {(() => {
                  const applyDictPreset = (next: 'clickhouse' | 'postgresql' | 'http') => {
                    setDictPreset(next);
                    const qName = qualifiedIdent(dbType, schema, objName || 'new_dictionary');
                    const srcTbl = tables[0] || 'source_table';
                    if (next === 'postgresql') {
                      setBodySql(
                        `CREATE OR REPLACE DICTIONARY ${qName}\n(\n  id UInt64,\n  name String\n)\nPRIMARY KEY id\nSOURCE(POSTGRESQL(host '127.0.0.1' port 5432 user 'postgres' password '' db 'postgres' table '${srcTbl}'))\nLIFETIME(MIN 60 MAX 300)\nLAYOUT(HASHED());`
                      );
                    } else if (next === 'http') {
                      setBodySql(
                        `CREATE OR REPLACE DICTIONARY ${qName}\n(\n  id UInt64,\n  name String\n)\nPRIMARY KEY id\nSOURCE(HTTP(url 'http://127.0.0.1:8080/dict' format 'JSONEachRow'))\nLIFETIME(300)\nLAYOUT(FLAT());`
                      );
                    } else {
                      setBodySql(
                        `CREATE OR REPLACE DICTIONARY ${qName}\n(\n  id UInt64,\n  name String\n)\nPRIMARY KEY id\nSOURCE(CLICKHOUSE(DB '${database || 'default'}' TABLE '${srcTbl}'))\nLIFETIME(MIN 60 MAX 300)\nLAYOUT(FLAT());`
                      );
                    }
                  };
                  const dictOptions = [
                    { value: 'clickhouse', label: t('ClickHouse Table') },
                    { value: 'postgresql', label: 'PostgreSQL' },
                    { value: 'http', label: t('HTTP Endpoint') },
                  ];
                  return isMobile ? (
                    <Select
                      style={{ width: '100%' }}
                      value={dictPreset}
                      onChange={(v) => applyDictPreset(v as 'clickhouse' | 'postgresql' | 'http')}
                      options={dictOptions}
                    />
                  ) : (
                    <Segmented
                      value={dictPreset}
                      onChange={(v) => applyDictPreset(v as 'clickhouse' | 'postgresql' | 'http')}
                      options={dictOptions}
                    />
                  );
                })()}
              </Form.Item>
            )}
            <Form.Item label={t('Dictionary DDL')} required style={{ marginBottom: 0 }}>
              <div style={{ border: '1px solid rgba(128,128,128,0.25)', borderRadius: 8, overflow: 'hidden' }}>
                <LazyCodeEditor value={bodySql} onChange={setBodySql} language="sql" height={240} darkMode={darkMode} readOnly={!canWrite} />
              </div>
            </Form.Item>
          </>
        )}

        {objType === 'MACRO' && (
          <>
            <Row gutter={[10, 8]}>
              <Col xs={24} sm={8}>
                <Form.Item label={t('Macro Type')} style={{ marginBottom: 10 }}>
                  <Segmented
                    block
                    value={isTableMacro ? 'table' : 'scalar'}
                    onChange={(v) => setIsTableMacro(v === 'table')}
                    options={[
                      { value: 'scalar', label: t('Scalar Macro') },
                      { value: 'table', label: t('Table Macro') },
                    ]}
                  />
                </Form.Item>
              </Col>
              <Col xs={24} sm={16}>
                <Form.Item label={t('Parameters')} style={{ marginBottom: 10 }}>
                  <Select
                    mode="tags"
                    value={macroParams}
                    onChange={setMacroParams}
                    tokenSeparators={[',', ' ']}
                    placeholder="a, b"
                  />
                </Form.Item>
              </Col>
            </Row>
            <Form.Item
              label={isTableMacro ? t('Table SELECT Query') : t('Scalar Expression')}
              required
              style={{ marginBottom: 0 }}
            >
              <div style={{ border: '1px solid rgba(128,128,128,0.25)', borderRadius: 8, overflow: 'hidden' }}>
                <LazyCodeEditor value={bodySql} onChange={setBodySql} language="sql" height={180} darkMode={darkMode} readOnly={!canWrite} />
              </div>
            </Form.Item>
          </>
        )}

        {objType === 'TRIGGER' && (
          <>
            <Row gutter={[10, 8]}>
              <Col xs={12}>
                <Form.Item label={t('Timing')} style={{ marginBottom: 10 }}>
                  <Select
                    value={trigTiming}
                    onChange={setTrigTiming}
                    options={[
                      { value: 'BEFORE', label: 'BEFORE' },
                      { value: 'AFTER', label: 'AFTER' },
                      { value: 'INSTEAD OF', label: 'INSTEAD OF' },
                    ]}
                  />
                </Form.Item>
              </Col>
              <Col xs={12}>
                <Form.Item label={t('Event')} style={{ marginBottom: 10 }}>
                  <Select
                    value={trigEvent}
                    onChange={setTrigEvent}
                    options={[
                      { value: 'INSERT', label: 'INSERT' },
                      { value: 'UPDATE', label: 'UPDATE' },
                      { value: 'DELETE', label: 'DELETE' },
                    ]}
                  />
                </Form.Item>
              </Col>
            </Row>
            <Form.Item label={t('Trigger SQL Body')} style={{ marginBottom: 0 }}>
              <div style={{ border: '1px solid rgba(128,128,128,0.25)', borderRadius: 8, overflow: 'hidden' }}>
                <LazyCodeEditor value={bodySql} onChange={setBodySql} language="sql" height={180} darkMode={darkMode} readOnly={!canWrite} />
              </div>
            </Form.Item>
          </>
        )}

        {objType === 'FUNCTION' && (
          <>
            {lowDb !== 'clickhouse' && (
              <Row gutter={[10, 8]}>
                <Col xs={24} sm={14}>
                  <Form.Item label={t('Parameters')} style={{ marginBottom: 10 }}>
                    <Input value={fnParams} onChange={(e) => setFnParams(e.target.value)} placeholder="p_id INT" />
                  </Form.Item>
                </Col>
                <Col xs={24} sm={10}>
                  <Form.Item label={t('Return Type')} style={{ marginBottom: 10 }}>
                    <Input
                      value={fnReturns}
                      onChange={(e) => setFnReturns(e.target.value)}
                      placeholder="VARCHAR(255)"
                    />
                  </Form.Item>
                </Col>
              </Row>
            )}
            <Form.Item
              label={lowDb === 'clickhouse' ? t('Lambda Expression') : t('Function Body')}
              style={{ marginBottom: 0 }}
            >
              <div style={{ border: '1px solid rgba(128,128,128,0.25)', borderRadius: 8, overflow: 'hidden' }}>
                <LazyCodeEditor value={bodySql} onChange={setBodySql} language="sql" height={190} darkMode={darkMode} readOnly={!canWrite} />
              </div>
            </Form.Item>
          </>
        )}

        {objType === 'PROCEDURE' && (
          <>
            <Form.Item label={t('Parameters')} style={{ marginBottom: 10 }}>
              <Input value={procParams} onChange={(e) => setProcParams(e.target.value)} placeholder="IN p_id INT" />
            </Form.Item>
            <Form.Item label={t('Procedure Body')} style={{ marginBottom: 0 }}>
              <div style={{ border: '1px solid rgba(128,128,128,0.25)', borderRadius: 8, overflow: 'hidden' }}>
                <LazyCodeEditor value={bodySql} onChange={setBodySql} language="sql" height={200} darkMode={darkMode} readOnly={!canWrite} />
              </div>
            </Form.Item>
          </>
        )}
      </Form>
    );
  };

  const renderSqlPreviewTab = () => {
    const effectiveSql = rawSql || buildLocalPreviewSQL();
    return (
      <Spin spinning={loadingDDL || previewLoading}>
        <div
          style={{
            marginBottom: 8,
            display: 'flex',
            justifyContent: 'space-between',
            alignItems: 'center',
            flexWrap: 'wrap',
            gap: 6,
          }}
        >
          <Space size={6} wrap>
            <Tag color={rawSqlDirty ? 'gold' : 'geekblue'} style={{ margin: 0 }}>
              {rawSqlDirty ? t('Custom Raw SQL Override') : t('Auto-Generated SQL Preview')}
            </Tag>
          </Space>
          <Space size={6} wrap>
            {mode === 'create' && (
              <Button
                size="small"
                icon={<ReloadOutlined />}
                onClick={() => {
                  setRawSqlDirty(false);
                  const fresh = buildLocalPreviewSQL();
                  setRawSql(fresh);
                }}
              >
                {t('Sync from Form')}
              </Button>
            )}
            <Button
              size="small"
              icon={<CopyOutlined />}
              onClick={async () => {
                await writeTextToClipboard(effectiveSql);
                message.success(t('Copied'));
              }}
            >
              {t('Copy SQL')}
            </Button>
          </Space>
        </div>
        <div style={{ border: '1px solid rgba(128,128,128,0.25)', borderRadius: 8, overflow: 'hidden' }}>
          <LazyCodeEditor
            value={effectiveSql}
            onChange={(val) => {
              setRawSql(val);
              setRawSqlDirty(true);
            }}
            language="sql"
            height={330}
            darkMode={darkMode}
            readOnly={!canWrite}
          />
        </div>
      </Spin>
    );
  };

  const submitBtn = (
    <Button
      type="primary"
      loading={saving}
      disabled={extensionRestricted || !canWrite}
      onClick={() => void handleSubmit()}
    >
      {mode === 'create'
        ? objType === 'EXTENSION' && lowDb === 'duckdb'
          ? t('Install Extension')
          : t('Create')
        : t('Apply Changes')}
    </Button>
  );

  return (
    <Modal
      open={open}
      onCancel={onClose}
      width="min(760px, calc(100vw - 16px))"
      style={isMobile ? { top: 10, maxWidth: 'calc(100vw - 16px)' } : { maxWidth: 'calc(100vw - 16px)' }}
      styles={{ body: { maxHeight: isMobile ? 'calc(100dvh - 140px)' : 'calc(100dvh - 180px)', overflowY: 'auto' } }}
      destroyOnClose
      title={
        <Space wrap size={6}>
          {canWrite ? <CodeOutlined /> : <EyeOutlined />}
          <span>
            {mode === 'create'
              ? `${t('New Object')} · ${typeLabel(objType)}`
              : `${canWrite ? t('Edit') : t('Inspect')} ${typeLabel(objType)}: ${editObject?.name || ''}`}
          </span>
          <Tag color="blue" style={{ margin: 0 }}>
            {dbType.toUpperCase()}
          </Tag>
        </Space>
      }
      footer={
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 8 }}>
          <div>
            {mode === 'edit' && canWrite && editObject && (
              <Popconfirm
                title={`${t('Drop')} ${editObject.name}?`}
                onConfirm={() => void handleDrop()}
                okButtonProps={{ danger: true }}
              >
                <Button danger icon={<DeleteOutlined />} loading={saving}>
                  {t('Drop')}
                </Button>
              </Popconfirm>
            )}
          </div>
          <Space wrap>
            <Button onClick={onClose}>{t('Cancel')}</Button>
            {canWrite &&
              (extensionRestricted ? (
                <Tooltip title={t('Installing extensions in Web Edition requires Administrator role')}>
                  <span>{submitBtn}</span>
                </Tooltip>
              ) : (
                submitBtn
              ))}
          </Space>
        </div>
      }
    >
      {renderLifecycleToolbar()}
      <Tabs
        size="small"
        activeKey={activeTab}
        onChange={handleTabChange}
        items={[
          {
            key: 'form',
            label: (
              <Space size={4}>
                <FormOutlined />
                <span>{t('Form Builder')}</span>
              </Space>
            ),
            children: renderFormFields(),
          },
          {
            key: 'sql',
            label: (
              <Space size={4}>
                <CodeOutlined />
                <span>{t('SQL / DDL Preview')}</span>
              </Space>
            ),
            children: renderSqlPreviewTab(),
          },
        ]}
      />
      {!canWrite && (
        <>
          <Divider style={{ margin: '10px 0' }} />
          <Text type="secondary" style={{ fontSize: 12 }}>
            {t('Read-Only Mode')}
          </Text>
        </>
      )}
    </Modal>
  );
};

export default SchemaObjectModal;
