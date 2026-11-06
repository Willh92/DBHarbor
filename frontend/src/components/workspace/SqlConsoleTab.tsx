import React, { memo, useDeferredValue, useEffect, useMemo, useRef, useState } from 'react';
import {
  Alert,
  Button,
  Checkbox,
  Drawer,
  Dropdown,
  Empty,
  Input,
  Modal,
  Popconfirm,
  Select,
  Space,
  Tabs,
  Tag,
  Typography,
  message,
} from 'antd';
import {
  AlignLeftOutlined,
  BookOutlined,
  BulbOutlined,
  ClearOutlined,
  CopyOutlined,
  DeleteOutlined,
  DownloadOutlined,
  FileTextOutlined,
  HistoryOutlined,
  MoreOutlined,
  PlayCircleOutlined,
  PlusOutlined,
  SaveOutlined,
  SearchOutlined,
  StopOutlined,
} from '@ant-design/icons';
import { clearSQLModelAutocomplete, monaco, setSQLModelAutocomplete } from '../../utils/monacoSetup';
import { api } from '../../api/client';
import { useResponsive } from '../../hooks/useResponsive';
import { MonacoEditorSurface } from '../common/MonacoEditorSurface';
import { ResizableTable, calcSmartColumnWidth } from '../common/ResizableTable';
import { useI18n } from '../../i18n/useI18n';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import type { DatabaseSummary, HistoryEntry, QueryResult, SavedSnippet, WorkspaceTab } from '../../types';
import { formatRawCellText, formatTsvCell, writeTextToClipboard } from '../../utils/tableClipboard';

interface Props {
  tab: WorkspaceTab;
  onOpenExportSQL: (database: string, sql: string) => void;
}

function simpleFormatSQL(sql: string): string {
  const majorClauses = [
    'SELECT',
    'FROM',
    'LEFT JOIN',
    'RIGHT JOIN',
    'INNER JOIN',
    'JOIN',
    'WHERE',
    'GROUP BY',
    'HAVING',
    'ORDER BY',
    'LIMIT',
    'OFFSET',
    'VALUES',
    'SET',
  ];
  let out = sql.trim().replace(/[ \t]+/g, ' ');
  for (const kw of majorClauses) {
    const re = new RegExp(`\\s+\\b(${kw.replace(' ', '\\s+')})\\b`, 'gi');
    out = out.replace(re, (_, m) => `\n${m.toUpperCase()}`);
  }
  return out
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)
    .join('\n');
}

function formatResultForClipboard(
  r: QueryResult,
  mode: 'tsv' | 'csv' | 'csv_bom' | 'json' | 'jsonl' | 'md' | 'sql',
  tableHint = 'export_table'
): string {
  const cols = r.columns.map((c) => c.name);
  if (mode === 'tsv') {
    const header = cols.map((c) => formatTsvCell(c)).join('\t');
    const body = r.rows.map((row) => row.map((v) => formatTsvCell(v)).join('\t'));
    return [header, ...body].join('\r\n');
  }
  if (mode === 'json' || mode === 'jsonl') {
    const objs = r.rows.map((row) => {
      const o: Record<string, any> = {};
      cols.forEach((c, idx) => {
        o[c] = row[idx];
      });
      return o;
    });
    return mode === 'jsonl' ? objs.map((o) => JSON.stringify(o)).join('\n') : JSON.stringify(objs, null, 2);
  }
  if (mode === 'md') {
    const header = `| ${cols.join(' | ')} |`;
    const sep = `| ${cols.map(() => '---').join(' | ')} |`;
    const body = r.rows.map((row) => `| ${row.map((v) => (v === null ? 'NULL' : String(v).replace(/\|/g, '\\|'))).join(' | ')} |`);
    return [header, sep, ...body].join('\n');
  }
  if (mode === 'sql') {
    const colList = cols.map((c) => `"${c}"`).join(', ');
    return r.rows
      .map((row) => {
        const vals = row
          .map((v) => {
            if (v === null || v === undefined) return 'NULL';
            if (typeof v === 'number' || typeof v === 'boolean') return String(v);
            return `'${String(v).replace(/'/g, "''")}'`;
          })
          .join(', ');
        return `INSERT INTO "${tableHint}" (${colList}) VALUES (${vals});`;
      })
      .join('\n');
  }
  // CSV / CSV UTF-8 BOM
  const esc = (v: any) => {
    if (v === null || v === undefined) return '';
    const s = String(v);
    return /[",\n\r]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
  };
  const csvBody = [cols.map(esc).join(','), ...r.rows.map((row) => row.map(esc).join(','))].join('\n');
  return mode === 'csv_bom' ? `\uFEFF${csvBody}` : csvBody;
}

function prettyFormatCellValue(raw: any): string {
  if (raw === null || raw === undefined) return 'NULL';
  const s = String(raw);
  const trimmed = s.trim();
  if ((trimmed.startsWith('{') && trimmed.endsWith('}')) || (trimmed.startsWith('[') && trimmed.endsWith(']'))) {
    try {
      return JSON.stringify(JSON.parse(trimmed), null, 2);
    } catch {
      return s;
    }
  }
  return s;
}

const ResultGrid = memo<{ result: QueryResult; tableHint?: string }>(({ result, tableHint }) => {
  const { t } = useI18n();
  const { isMobile } = useResponsive();
  const [rowSearch, setRowSearch] = useState('');
  const deferredRowSearch = useDeferredValue(rowSearch);
  const [inspectCell, setInspectCell] = useState<{ column: string; value: string } | null>(null);

  const filteredRawRows = useMemo(() => {
    const q = deferredRowSearch.trim().toLowerCase();
    if (!q) return result.rows;
    return result.rows.filter((arr) =>
      arr.some((v) => v !== null && v !== undefined && String(v).toLowerCase().includes(q))
    );
  }, [result.rows, deferredRowSearch]);

  const dataSource = useMemo(
    () =>
      filteredRawRows.map((arr, rIdx) => {
        const obj: Record<string, any> = { __rk: String(rIdx) };
        result.columns.forEach((_, cIdx) => {
          obj[`c_${cIdx}`] = arr[cIdx];
        });
        return obj;
      }),
    [filteredRawRows, result.columns]
  );

  const columns = useMemo(
    () =>
      result.columns.map((c, cIdx) => ({
        title: (
          <span data-col-name={c.name} style={{ display: 'inline-flex', alignItems: 'center', gap: 4, whiteSpace: 'nowrap' }}>
            <span>{c.name}</span>
            {c.type && (
              <Typography.Text type="secondary" style={{ fontSize: 10 }}>
                {c.type}
              </Typography.Text>
            )}
          </span>
        ),
        dataIndex: `c_${cIdx}`,
        width: calcSmartColumnWidth(c.name, { type: c.type, minBodyWidth: 130 }),
        ellipsis: true,
        render: (v: any) => (
          <span
            data-raw-copy={formatRawCellText(v)}
            onDoubleClick={() => setInspectCell({ column: c.name, value: prettyFormatCellValue(v) })}
            style={{ display: 'block', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', cursor: 'pointer' }}
            title={v === null ? 'NULL' : String(v)}
          >
            {v === null ? <Tag style={{ fontSize: 10, opacity: 0.6, margin: 0 }}>NULL</Tag> : String(v)}
          </span>
        ),
      })),
    [result.columns]
  );

  const copyAs = (mode: 'tsv' | 'csv' | 'json' | 'md' | 'sql') => {
    const exportTarget: QueryResult =
      filteredRawRows.length === result.rows.length ? result : { ...result, rows: filteredRawRows };
    const text = formatResultForClipboard(exportTarget, mode, tableHint || 'table_name');
    writeTextToClipboard(text);
    message.success(t('Copied {count} rows as {mode}', { count: exportTarget.rows.length, mode: mode.toUpperCase() }));
  };

  const downloadAsFile = (mode: 'csv_bom' | 'csv' | 'json' | 'jsonl' | 'md' | 'sql') => {
    const exportTarget: QueryResult =
      filteredRawRows.length === result.rows.length ? result : { ...result, rows: filteredRawRows };
    const content = formatResultForClipboard(exportTarget, mode, tableHint || 'query_result');
    const extMap: Record<string, { ext: string; mime: string }> = {
      csv_bom: { ext: 'csv', mime: 'text/csv;charset=utf-8' },
      csv: { ext: 'csv', mime: 'text/csv;charset=utf-8' },
      json: { ext: 'json', mime: 'application/json;charset=utf-8' },
      jsonl: { ext: 'jsonl', mime: 'application/x-ndjson;charset=utf-8' },
      md: { ext: 'md', mime: 'text/markdown;charset=utf-8' },
      sql: { ext: 'sql', mime: 'application/sql;charset=utf-8' },
    };
    const meta = extMap[mode] || extMap.csv_bom;
    const blob = new Blob([content], { type: meta.mime });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${tableHint || 'query_result'}_${new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-')}.${meta.ext}`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    setTimeout(() => URL.revokeObjectURL(url), 1500);
  };

  const gridWrapRef = useRef<HTMLDivElement | null>(null);
  const [gridBodyHeight, setGridBodyHeight] = useState<number>(220);

  useEffect(() => {
    const el = gridWrapRef.current;
    if (!el) return;
    const measure = () => {
      if ((window as any).__DBHARBOR_LAYOUT_DRAGGING__) return;
      if (el.clientHeight <= 0) return;
      const thead = el.querySelector('.ant-table-thead') as HTMLElement | null;
      const pag = el.querySelector('.ant-table-pagination') as HTMLElement | null;
      const headH = thead && thead.offsetHeight > 0 ? thead.offsetHeight : 38;
      const pagH = pag && pag.offsetHeight > 0 ? pag.offsetHeight + 16 : 42;
      const nextH = Math.max(90, Math.floor(el.clientHeight - headH - pagH - 4));
      setGridBodyHeight((prev) => (prev === nextH ? prev : nextH));
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    window.addEventListener('dbharbor-layout-settled', measure);
    return () => {
      ro.disconnect();
      window.removeEventListener('dbharbor-layout-settled', measure);
    };
  }, [result.columns.length, dataSource.length]);

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', minWidth: 0, minHeight: 0, overflow: 'hidden' }}>
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          marginBottom: 6,
          flexShrink: 0,
          gap: 6,
          flexWrap: 'wrap',
        }}
      >
        {!isMobile && (
          <Typography.Text type="secondary" style={{ fontSize: 12 }} ellipsis>
            {t('Elapsed {ms} ms · Returned {count} rows', { ms: result.elapsedMs, count: result.rows.length })}
            {filteredRawRows.length !== result.rows.length ? ` (${filteredRawRows.length})` : ''}
          </Typography.Text>
        )}
        <div style={{ display: 'flex', alignItems: 'center', gap: 6, flex: isMobile ? 1 : undefined, minWidth: 0 }}>
          <Input
            size="small"
            allowClear
            prefix={<SearchOutlined />}
            placeholder={t('Filter result rows...')}
            value={rowSearch}
            onChange={(e) => setRowSearch(e.target.value)}
            style={isMobile ? { flex: 1, minWidth: 90 } : { width: 180, maxWidth: '48vw' }}
          />
          <Dropdown.Button
            size="small"
            icon={<CopyOutlined />}
            onClick={() => copyAs('tsv')}
            menu={{
              items: [
                { key: 'tsv', label: t('Copy Rows (TSV - Notepad / Excel)'), onClick: () => copyAs('tsv') },
                { key: 'csv', label: t('Copy as CSV'), onClick: () => copyAs('csv') },
                { key: 'json', label: t('Copy as JSON Array'), onClick: () => copyAs('json') },
                { key: 'md', label: t('Copy as Markdown Table'), onClick: () => copyAs('md') },
                { key: 'sql', label: t('Copy as INSERT SQL'), onClick: () => copyAs('sql') },
              ],
            }}
          >
            {isMobile ? t('Copy') : t('Copy Result')}
          </Dropdown.Button>
          <Dropdown
            menu={{
              items: [
                { key: 'csv_bom', label: 'CSV (Excel UTF-8 BOM)', onClick: () => downloadAsFile('csv_bom') },
                { key: 'csv', label: 'CSV (.csv)', onClick: () => downloadAsFile('csv') },
                { key: 'json', label: 'JSON (.json)', onClick: () => downloadAsFile('json') },
                { key: 'jsonl', label: 'JSON Lines (.jsonl)', onClick: () => downloadAsFile('jsonl') },
                { key: 'md', label: 'Markdown (.md)', onClick: () => downloadAsFile('md') },
                { key: 'sql', label: 'INSERT SQL (.sql)', onClick: () => downloadAsFile('sql') },
              ],
            }}
          >
            <Button size="small" icon={<DownloadOutlined />} title={t('Save File')}>
              {isMobile ? '' : t('Save File')}
            </Button>
          </Dropdown>
        </div>
      </div>
      <div ref={gridWrapRef} style={{ flex: 1, minHeight: 0, minWidth: 0, overflow: 'hidden' }}>
        <ResizableTable
          size="small"
          bordered
          rowKey="__rk"
          pagination={{ pageSize: 50, size: 'small', simple: isMobile, showSizeChanger: !isMobile, responsive: true }}
          scroll={{ x: 'max-content', y: gridBodyHeight }}
          dataSource={dataSource}
          columns={columns}
        />
      </div>

      <Modal
        open={Boolean(inspectCell)}
        title={inspectCell ? `${t('Cell Value Inspector')} — ${inspectCell.column}` : t('Cell Value Inspector')}
        width="min(620px, calc(100vw - 24px))"
        onCancel={() => setInspectCell(null)}
        footer={
          <Space>
            <Button
              icon={<CopyOutlined />}
              type="primary"
              onClick={() => {
                if (inspectCell) {
                  navigator.clipboard.writeText(inspectCell.value);
                  message.success(t('Copied {name}', { name: inspectCell.column }));
                }
              }}
            >
              {t('Copy')}
            </Button>
            <Button onClick={() => setInspectCell(null)}>{t('Close')}</Button>
          </Space>
        }
      >
        <Input.TextArea
          readOnly
          autoSize={{ minRows: 4, maxRows: 16 }}
          value={inspectCell?.value || ''}
          style={{ fontFamily: "'JetBrains Mono', Consolas, Menlo, monospace", fontSize: 12 }}
        />
      </Modal>
    </div>
  );
});

const DEFAULT_PLACEHOLDER_PREFIX = '-- Connection: ';

export const SqlConsoleTab: React.FC<Props> = ({ tab, onOpenExportSQL }) => {
  const { darkMode, bumpTreeRefresh, openTab, isDesktop } = useWorkbenchStore();
  const { isMobile } = useResponsive();
  const { t } = useI18n();
  const defaultHeader = `${DEFAULT_PLACEHOLDER_PREFIX}${tab.connName} (${tab.dbType})\n-- Shortcut: Ctrl+Enter / Cmd+Enter to execute selection or full script (semicolon-separated)\n`;

  const [sqlText, setSqlText] = useState(
    tab.initialSQL || (tab.table ? `SELECT * FROM ${tab.table} LIMIT 100;` : `${defaultHeader}SELECT 1 AS status;`)
  );
  const [currentDB, setCurrentDB] = useState(tab.database || '');
  const [databases, setDatabases] = useState<DatabaseSummary[]>([]);
  const [firstTableHint, setFirstTableHint] = useState<string>(tab.table || 'users');
  const [running, setRunning] = useState(false);
  const [results, setResults] = useState<QueryResult[]>([]);
  const [totalElapsed, setTotalElapsed] = useState<number>(0);
  const [execError, setExecError] = useState<string>('');

  // History & Snippets drawer
  const [drawerMode, setDrawerMode] = useState<'history' | 'snippets' | null>(null);
  const [drawerSearch, setDrawerSearch] = useState('');
  const deferredDrawerSearch = useDeferredValue(drawerSearch);
  const [historyList, setHistoryList] = useState<HistoryEntry[]>([]);
  const [snippets, setSnippets] = useState<SavedSnippet[]>([]);
  const [saveModalOpen, setSaveModalOpen] = useState(false);
  const [snippetTitle, setSnippetTitle] = useState('');
  const [snippetShared, setSnippetShared] = useState(false);
  const [hasSelection, setHasSelection] = useState(false);
  const [editorHeight, setEditorHeight] = useState<number>(() => {
    const saved = Number(localStorage.getItem('dbharbor_sql_editor_height'));
    return saved >= 110 && saved <= 620 ? saved : 210;
  });

  const editorPaneRef = useRef<HTMLDivElement | null>(null);
  const rowSplitterRef = useRef<HTMLDivElement | null>(null);
  const editorRef = useRef<monaco.editor.IStandaloneCodeEditor | null>(null);
  const selectionDispRef = useRef<monaco.IDisposable | null>(null);
  const modelUriRef = useRef<string>('');
  const latestMetaRef = useRef<{ tables: Record<string, string[]>; databases: string[] }>({ tables: {}, databases: [] });
  const abortRef = useRef<AbortController | null>(null);
  const executeRef = useRef<(overridePrefix?: string) => void>(() => {});
  const formatRef = useRef<() => void>(() => {});

  useEffect(() => {
    return () => {
      abortRef.current?.abort();
      selectionDispRef.current?.dispose();
      if (modelUriRef.current) {
        clearSQLModelAutocomplete(modelUriRef.current);
      }
    };
  }, []);

  // Sync external initialSQL updates when opened via ContextNavBar
  useEffect(() => {
    if (tab.initialSQL) {
      setSqlText(tab.initialSQL);
    }
  }, [tab.initialSQL]);

  useEffect(() => {
    api
      .listDatabases(tab.connectionId)
      .then((dbs) => {
        setDatabases(dbs);
        if (!currentDB && dbs[0]?.name) {
          setCurrentDB(dbs[0].name);
        }
      })
      .catch(() => {});
  }, [tab.connectionId]);

  useEffect(() => {
    let active = true;
    api
      .getAutocomplete(tab.connectionId, currentDB)
      .then((meta) => {
        if (!active) return;
        const tables = meta.tables || {};
        const dbs = meta.databases || [];
        latestMetaRef.current = { tables, databases: dbs };
        const tableNames = Object.keys(tables);
        if (tableNames.length > 0) {
          setFirstTableHint(tableNames[0]);
          // If still on generic SELECT 1 placeholder, populate a real table query for instant convenience
          setSqlText((prev) =>
            prev.endsWith('SELECT 1 AS status;')
              ? `${defaultHeader}SELECT * FROM ${tableNames[0]} LIMIT 50;`
              : prev
          );
        }

        const uri = editorRef.current?.getModel()?.uri.toString() || modelUriRef.current;
        if (uri) {
          modelUriRef.current = uri;
          setSQLModelAutocomplete(uri, tables, dbs);
        }
      })
      .catch(() => {});

    return () => {
      active = false;
    };
  }, [tab.connectionId, currentDB]);

  const executeSQL = async (overridePrefix?: string) => {
    const ed = editorRef.current;
    let targetQuery = ed ? ed.getValue() : sqlText;
    if (ed) {
      const sel = ed.getSelection();
      if (sel && !sel.isEmpty()) {
        const selected = ed.getModel()?.getValueInRange(sel);
        if (selected?.trim()) {
          targetQuery = selected;
        }
      }
    }
    targetQuery = targetQuery.trim();
    if (!targetQuery) {
      message.warning(t('Please enter a SQL statement to execute'));
      return;
    }
    if (overridePrefix) {
      targetQuery = `${overridePrefix} ${targetQuery}`;
    }

    abortRef.current?.abort();
    const ctrl = new AbortController();
    abortRef.current = ctrl;

    setRunning(true);
    setExecError('');
    try {
      const res = await api.executeQuery(tab.connectionId, currentDB, targetQuery, 1000, ctrl.signal);
      setResults(res.results || []);
      setTotalElapsed(res.totalElapsedMs || 0);
      if ((res.results || []).some((r) => !r.isSelect)) {
        bumpTreeRefresh();
      }
      message.success(t('SQL executed ({ms} ms)', { ms: res.totalElapsedMs }));
    } catch (err: any) {
      if (err?.name === 'AbortError') {
        message.info(t('SQL query aborted'));
      } else {
        setExecError(err.message);
        if (err?.data?.partialResults?.length) {
          setResults(err.data.partialResults);
        }
      }
    } finally {
      setRunning(false);
    }
  };

  // Keep latest closure in ref so Monaco Ctrl/Cmd+Enter always reads fresh state & DB!
  executeRef.current = executeSQL;

  const handleExplain = () => {
    const prefix = tab.dbType === 'sqlite' ? 'EXPLAIN QUERY PLAN' : 'EXPLAIN';
    executeSQL(prefix);
  };

  const handleFormat = () => {
    const cur = editorRef.current ? editorRef.current.getValue() : sqlText;
    const formatted = simpleFormatSQL(cur);
    setSqlText(formatted);
    editorRef.current?.setValue(formatted);
  };
  formatRef.current = handleFormat;

  const insertTemplate = (tpl: string) => {
    setSqlText(tpl);
    editorRef.current?.setValue(tpl);
    editorRef.current?.focus();
  };

  const openHistory = async () => {
    setDrawerSearch('');
    setDrawerMode('history');
    const list = await api.listHistory(tab.connectionId);
    setHistoryList(list);
  };

  const openSnippets = async () => {
    setDrawerSearch('');
    setDrawerMode('snippets');
    const list = await api.listSnippets();
    setSnippets(list);
  };

  const handleSaveSnippet = async () => {
    if (!snippetTitle.trim()) return;
    const cur = editorRef.current ? editorRef.current.getValue() : sqlText;
    await api.saveSnippet({
      title: snippetTitle.trim(),
      dbType: tab.dbType,
      connectionId: tab.connectionId,
      database: currentDB,
      sql: cur,
      isShared: !isDesktop && snippetShared,
    });
    message.success(t('Saved to SQL snippets'));
    setSaveModalOpen(false);
    setSnippetTitle('');
    setSnippetShared(false);
  };

  const tbl = tab.table || firstTableHint || 'table_name';

  const handleEditorSplitterPointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return;
    e.preventDefault();
    const pointerId = e.pointerId;
    const splitterEl = e.currentTarget;
    try {
      splitterEl.setPointerCapture(pointerId);
    } catch {
      // ignore synthetic events
    }

    const startY = e.clientY;
    const startH = editorPaneRef.current ? editorPaneRef.current.getBoundingClientRect().height : editorHeight;
    (window as any).__DBHARBOR_LAYOUT_DRAGGING__ = true;
    splitterEl.classList.add('is-dragging');
    document.body.classList.add('dbharbor-is-dragging');
    document.body.style.cursor = 'row-resize';

    let rafId = 0;
    let latestH = Math.round(startH);

    const applyHeight = () => {
      rafId = 0;
      if (editorPaneRef.current) {
        editorPaneRef.current.style.height = `${latestH}px`;
      }
      editorRef.current?.layout();
    };

    const onMove = (ev: PointerEvent) => {
      latestH = Math.max(92, Math.min(620, Math.round(startH + (ev.clientY - startY))));
      if (!rafId) {
        rafId = window.requestAnimationFrame(applyHeight);
      }
    };
    const onUp = () => {
      if (rafId) {
        window.cancelAnimationFrame(rafId);
        rafId = 0;
      }
      if (editorPaneRef.current) {
        editorPaneRef.current.style.height = `${latestH}px`;
      }
      try {
        if (splitterEl.hasPointerCapture(pointerId)) {
          splitterEl.releasePointerCapture(pointerId);
        }
      } catch {
        // ignore
      }
      (window as any).__DBHARBOR_LAYOUT_DRAGGING__ = false;
      splitterEl.classList.remove('is-dragging');
      document.body.classList.remove('dbharbor-is-dragging');
      document.body.style.cursor = '';
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
      window.removeEventListener('pointercancel', onUp);
      setEditorHeight(latestH);
      localStorage.setItem('dbharbor_sql_editor_height', String(latestH));
      window.requestAnimationFrame(() => {
        editorRef.current?.layout();
        window.dispatchEvent(new Event('dbharbor-layout-settled'));
      });
    };
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
    window.addEventListener('pointercancel', onUp);
  };

  const templateMenuItems = [
    {
      key: 'select',
      label: 'SELECT ... LIMIT 100',
      onClick: () => insertTemplate(`SELECT *\nFROM ${tbl}\nWHERE 1 = 1\nLIMIT 100;`),
    },
    {
      key: 'insert',
      label: 'INSERT INTO ...',
      onClick: () => insertTemplate(`INSERT INTO ${tbl} (col1, col2)\nVALUES ('val1', 'val2');`),
    },
    {
      key: 'update',
      label: 'UPDATE ... SET ...',
      onClick: () => insertTemplate(`UPDATE ${tbl}\nSET col1 = 'new_value'\nWHERE id = 1;`),
    },
    {
      key: 'alter',
      label: 'ALTER TABLE ... ADD COLUMN',
      onClick: () => insertTemplate(`ALTER TABLE ${tbl}\nADD COLUMN new_col VARCHAR(128);`),
    },
    {
      key: 'create',
      label: 'CREATE TABLE ...',
      onClick: () =>
        insertTemplate(
          `CREATE TABLE IF NOT EXISTS demo_items (\n  id INTEGER PRIMARY KEY,\n  title VARCHAR(255) NOT NULL,\n  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP\n);`
        ),
    },
  ];

  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        height: '100%',
        minWidth: 0,
        minHeight: 0,
        padding: isMobile ? '6px 4px' : '10px 14px',
        gap: 6,
        overflow: 'hidden',
      }}
    >
      {/* Top Toolbar */}
      {isMobile ? (
        <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexShrink: 0 }}>
          <Button size="small" type="primary" icon={<PlayCircleOutlined />} loading={running} onClick={() => executeSQL()}>
            {t('Run')}
          </Button>
          {running && (
            <Button danger size="small" icon={<StopOutlined />} onClick={() => abortRef.current?.abort()} />
          )}
          <Select
            size="small"
            style={{ flex: 1, minWidth: 90 }}
            value={currentDB || undefined}
            onChange={setCurrentDB}
            placeholder={t('Database')}
            options={databases.map((d) => ({ value: d.name, label: d.name }))}
          />
          <Button size="small" icon={<HistoryOutlined />} onClick={openHistory} title={t('History')} />
          <Dropdown
            trigger={['click']}
            menu={{
              items: [
                { key: 'explain', icon: <BulbOutlined />, label: t('EXPLAIN Plan'), onClick: handleExplain },
                { key: 'format', icon: <AlignLeftOutlined />, label: t('Format'), onClick: handleFormat },
                { key: 'templates', icon: <FileTextOutlined />, label: t('Templates'), children: templateMenuItems },
                { key: 'save', icon: <SaveOutlined />, label: t('Save Snippet'), onClick: () => setSaveModalOpen(true) },
                { key: 'snippets', icon: <BookOutlined />, label: t('Snippets'), onClick: openSnippets },
                {
                  key: 'export',
                  icon: <DownloadOutlined />,
                  label: t('Export Result'),
                  onClick: () => onOpenExportSQL(currentDB, editorRef.current ? editorRef.current.getValue() : sqlText),
                },
                {
                  key: 'new',
                  icon: <PlusOutlined />,
                  label: t('New Console'),
                  onClick: () =>
                    openTab({
                      key: `sql:${tab.connectionId}:${Date.now()}`,
                      title: `SQL - ${currentDB || tab.connName}`,
                      kind: 'sql_console',
                      connectionId: tab.connectionId,
                      connName: tab.connName,
                      dbType: tab.dbType,
                      database: currentDB,
                      schema: tab.schema,
                    }),
                },
              ],
            }}
          >
            <Button size="small" icon={<MoreOutlined />} />
          </Dropdown>
        </div>
      ) : (
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 8, flexShrink: 0 }}>
          <Space wrap size={6}>
            <Button type="primary" icon={<PlayCircleOutlined />} loading={running} onClick={() => executeSQL()}>
              {hasSelection ? t('Run Selection (Ctrl+Enter)') : t('Run SQL (Ctrl+Enter)')}
            </Button>
            {running && (
              <Button danger size="small" icon={<StopOutlined />} onClick={() => abortRef.current?.abort()}>
                {t('Stop')}
              </Button>
            )}
            <Select
              size="small"
              style={{ minWidth: 160 }}
              value={currentDB || undefined}
              onChange={setCurrentDB}
              placeholder={t('Database')}
              options={databases.map((d) => ({ value: d.name, label: `DB: ${d.name}` }))}
            />
            <Button size="small" icon={<BulbOutlined />} onClick={handleExplain}>
              {t('EXPLAIN Plan')}
            </Button>
            <Button size="small" icon={<AlignLeftOutlined />} onClick={handleFormat}>
              {t('Format')}
            </Button>
            <Dropdown menu={{ items: templateMenuItems }}>
              <Button size="small" icon={<FileTextOutlined />}>
                {t('Templates')}
              </Button>
            </Dropdown>
            <Button size="small" icon={<SaveOutlined />} onClick={() => setSaveModalOpen(true)}>
              {t('Save Snippet')}
            </Button>
            <Button
              size="small"
              icon={<DownloadOutlined />}
              onClick={() => onOpenExportSQL(currentDB, editorRef.current ? editorRef.current.getValue() : sqlText)}
            >
              {t('Export Result')}
            </Button>
          </Space>

          <Space size={6}>
            <Button
              size="small"
              icon={<PlusOutlined />}
              onClick={() =>
                openTab({
                  key: `sql:${tab.connectionId}:${Date.now()}`,
                  title: `SQL - ${currentDB || tab.connName}`,
                  kind: 'sql_console',
                  connectionId: tab.connectionId,
                  connName: tab.connName,
                  dbType: tab.dbType,
                  database: currentDB,
                  schema: tab.schema,
                })
              }
            >
              {t('New Console')}
            </Button>
            <Button size="small" icon={<HistoryOutlined />} onClick={openHistory}>
              {t('History')}
            </Button>
            <Button size="small" icon={<BookOutlined />} onClick={openSnippets}>
              {t('Snippets')}
            </Button>
          </Space>
        </div>
      )}

      {/* Monaco Editor Pane */}
      <div
        ref={editorPaneRef}
        style={{
          height: isMobile ? Math.min(editorHeight, 155) : editorHeight,
          minHeight: 92,
          maxHeight: '68%',
          flexShrink: 0,
          border: '1px solid rgba(128,128,128,0.22)',
          borderRadius: 8,
          overflow: 'hidden',
        }}
      >
        <MonacoEditorSurface
          language="sql"
          darkMode={darkMode}
          compactGutter={isMobile}
          value={sqlText}
          onChange={(v) => setSqlText(v || '')}
          onMount={(editor) => {
            editorRef.current = editor;
            const uri = editor.getModel()?.uri.toString() || '';
            if (uri) {
              modelUriRef.current = uri;
              setSQLModelAutocomplete(uri, latestMetaRef.current.tables, latestMetaRef.current.databases);
            }
            selectionDispRef.current?.dispose();
            selectionDispRef.current = editor.onDidChangeCursorSelection((ev) => {
              const nonEmpty = !ev.selection.isEmpty();
              setHasSelection((prev) => (prev === nonEmpty ? prev : nonEmpty));
            });
            editor.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.Enter, () => {
              executeRef.current();
            });
            editor.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyMod.Shift | monaco.KeyCode.KeyF, () => {
              formatRef.current();
            });
            editor.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => {
              setSaveModalOpen(true);
            });
          }}
        />
      </div>

      {/* Horizontal Drag Splitter */}
      <div
        ref={rowSplitterRef}
        className="dbharbor-row-splitter"
        onPointerDown={handleEditorSplitterPointerDown}
        onDoubleClick={() => {
          setEditorHeight(210);
          if (editorPaneRef.current) {
            editorPaneRef.current.style.height = '210px';
          }
          localStorage.setItem('dbharbor_sql_editor_height', '210');
          window.requestAnimationFrame(() => {
            editorRef.current?.layout();
            window.dispatchEvent(new Event('dbharbor-layout-settled'));
          });
        }}
      />

      {/* Results Pane */}
      <div style={{ flex: 1, minHeight: 0, minWidth: 0, overflow: 'hidden', display: 'flex', flexDirection: 'column' }}>
        {execError && (
          <Alert
            type="error"
            showIcon
            closable
            onClose={() => setExecError('')}
            message={t('SQL Execution Error')}
            description={execError}
            style={{ marginBottom: 8, flexShrink: 0 }}
          />
        )}

        {results.length === 0 && !execError ? (
          <div style={{ flex: 1, minHeight: 0, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
            <Empty
              description={t(
                'Enter any custom SQL (SELECT / INSERT / UPDATE / ALTER / CREATE) and press Ctrl+Enter to execute'
              )}
              image={Empty.PRESENTED_IMAGE_SIMPLE}
            />
          </div>
        ) : (
          <Tabs
            size="small"
            className="sql-results-tabs"
            tabBarExtraContent={
              totalElapsed >= 0 && results.length > 0 ? (
                <Space size={6}>
                  <Tag color="processing" style={{ margin: 0 }}>
                    {t('{count} result(s) · Total {ms} ms', { count: results.length, ms: totalElapsed })}
                  </Tag>
                  <Button
                    size="small"
                    type="text"
                    icon={<ClearOutlined />}
                    title={t('Clear Results')}
                    onClick={() => {
                      setResults([]);
                      setExecError('');
                    }}
                  />
                </Space>
              ) : undefined
            }
            items={results.map((r, idx) => ({
              key: String(idx),
              label: r.isSelect
                ? t('Result #{idx} ({count} rows)', { idx: idx + 1, count: r.rows.length })
                : t('Statement #{idx}', { idx: idx + 1 }),
              children: r.isSelect ? (
                <ResultGrid result={r} tableHint={tbl} />
              ) : (
                <Alert
                  type="success"
                  showIcon
                  message={
                    r.message ||
                    t('Statement executed. Affected rows: {rows} · Elapsed {ms} ms', {
                      rows: r.affectedRows,
                      ms: r.elapsedMs,
                    })
                  }
                  description={<pre style={{ margin: '6px 0 0', fontSize: 12, whiteSpace: 'pre-wrap' }}>{r.statement}</pre>}
                />
              ),
            }))}
          />
        )}
      </div>

      {/* History & Snippets Drawer */}
      <Drawer
        open={Boolean(drawerMode)}
        destroyOnClose
        title={drawerMode === 'history' ? t('SQL Execution History') : t('Saved SQL Snippets')}
        width={isMobile ? '92vw' : 520}
        onClose={() => setDrawerMode(null)}
        extra={
          drawerMode === 'history' && historyList.length > 0 ? (
            <Popconfirm
              title={t('Clear execution history for this connection?')}
              onConfirm={async () => {
                await api.clearHistory(tab.connectionId);
                setHistoryList([]);
                message.success(t('Execution history cleared'));
              }}
            >
              <Button size="small" danger icon={<ClearOutlined />}>
                {t('Clear History')}
              </Button>
            </Popconfirm>
          ) : null
        }
      >
        <Input
          size="small"
          allowClear
          prefix={<SearchOutlined />}
          placeholder={t('Filter history or snippets...')}
          value={drawerSearch}
          onChange={(e) => setDrawerSearch(e.target.value)}
          style={{ marginBottom: 10 }}
        />
        {drawerMode === 'history'
          ? historyList
              .filter((h) => {
                const q = deferredDrawerSearch.trim().toLowerCase();
                return !q || h.query.toLowerCase().includes(q) || (h.database || '').toLowerCase().includes(q);
              })
              .map((h) => (
              <div
                key={h.id}
                style={{
                  padding: 10,
                  marginBottom: 8,
                  borderRadius: 8,
                  border: '1px solid rgba(128,128,128,0.2)',
                }}
              >
                <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 4 }}>
                  <Space size={4}>
                    <Tag color={h.status === 'ok' ? 'success' : 'error'}>{h.status.toUpperCase()}</Tag>
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      {h.elapsedMs} ms · {h.database}
                    </Typography.Text>
                  </Space>
                  <Button
                    size="small"
                    type="link"
                    onClick={() => {
                      setSqlText(h.query);
                      editorRef.current?.setValue(h.query);
                      setDrawerMode(null);
                    }}
                  >
                    {t('Load into Editor')}
                  </Button>
                </div>
                <pre style={{ margin: 0, fontSize: 12, whiteSpace: 'pre-wrap', maxHeight: 100, overflow: 'auto' }}>
                  {h.query}
                </pre>
              </div>
              ))
          : snippets
              .filter((sn) => {
                const q = deferredDrawerSearch.trim().toLowerCase();
                return !q || sn.title.toLowerCase().includes(q) || sn.sql.toLowerCase().includes(q);
              })
              .map((sn) => (
              <div
                key={sn.id}
                style={{
                  padding: 10,
                  marginBottom: 8,
                  borderRadius: 8,
                  border: '1px solid rgba(128,128,128,0.2)',
                }}
              >
                <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 4 }}>
                  <Space size={6}>
                    <Typography.Text strong>{sn.title}</Typography.Text>
                    {!isDesktop && sn.isShared && (
                      <Tag color="geekblue" style={{ margin: 0, fontSize: 10 }}>
                        {t('Team Shared')}
                      </Tag>
                    )}
                  </Space>
                  <Space>
                    <Button
                      size="small"
                      type="link"
                      onClick={() => {
                        setSqlText(sn.sql);
                        editorRef.current?.setValue(sn.sql);
                        setDrawerMode(null);
                      }}
                    >
                      {t('Insert')}
                    </Button>
                    {(isDesktop || sn.editable !== false) && (
                      <Button
                        size="small"
                        type="text"
                        danger
                        icon={<DeleteOutlined />}
                        onClick={async () => {
                          if (sn.id) {
                            await api.deleteSnippet(sn.id);
                            openSnippets();
                          }
                        }}
                      />
                    )}
                  </Space>
                </div>
                <pre style={{ margin: 0, fontSize: 12, whiteSpace: 'pre-wrap' }}>{sn.sql}</pre>
              </div>
            ))}
      </Drawer>

      <Modal
        open={saveModalOpen}
        title={t('Save SQL Snippet')}
        width="min(460px, calc(100vw - 24px))"
        onCancel={() => setSaveModalOpen(false)}
        onOk={handleSaveSnippet}
      >
        <Space direction="vertical" size={10} style={{ width: '100%' }}>
          <Input
            placeholder={t('Snippet title, e.g. Daily Revenue Summary')}
            value={snippetTitle}
            onChange={(e) => setSnippetTitle(e.target.value)}
            onPressEnter={handleSaveSnippet}
          />
          {!isDesktop && (
            <Checkbox checked={snippetShared} onChange={(e) => setSnippetShared(e.target.checked)}>
              {t('Share snippet with team')}
            </Checkbox>
          )}
        </Space>
      </Modal>
    </div>
  );
};
