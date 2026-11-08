import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Badge,
  Button,
  Card,
  Checkbox,
  Drawer,
  Dropdown,
  Form,
  Input,
  Modal,
  Pagination,
  Popconfirm,
  Popover,
  Radio,
  Select,
  Space,
  Tag,
  Tooltip,
  Typography,
  message,
} from 'antd';
import type { MenuProps } from 'antd';
import {
  CheckOutlined,
  CloseOutlined,
  CopyOutlined,
  DeleteOutlined,
  DownloadOutlined,
  EditOutlined,
  ExpandOutlined,
  EyeOutlined,
  FilterOutlined,
  FormOutlined,
  MoreOutlined,
  PlusOutlined,
  ReloadOutlined,
  SearchOutlined,
  SettingOutlined,
  SnippetsOutlined,
} from '@ant-design/icons';
import { api, isAuthPromptCancelledError } from '../../api/client';
import { useResponsive } from '../../hooks/useResponsive';
import { useI18n } from '../../i18n/useI18n';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import type { QueryColumn, WorkspaceTab } from '../../types';
import {
  coercePastedCellValue,
  formatRawCellText,
  parseClipboardTabular,
  serializeRowsToTsv,
  writeTextToClipboard,
} from '../../utils/tableClipboard';
import { ResizableTable, calcSmartColumnWidth } from '../common/ResizableTable';
import { ContextNavBar, type TransferModalMode, type TransferOpenOptions } from './ContextNavBar';

interface Props {
  tab: WorkspaceTab;
  onOpenTransfer: (mode: TransferModalMode, tableName?: string, opts?: TransferOpenOptions) => void;
}

interface SearchCond {
  id: string;
  column: string;
  op: string;
  value: string;
}

const SEARCH_OPS = [
  { value: '=', label: '= Equals' },
  { value: '!=', label: '!= Not Equals' },
  { value: '>', label: '> Greater Than' },
  { value: '>=', label: '>= Greater or Equal' },
  { value: '<', label: '< Less Than' },
  { value: '<=', label: '<= Less or Equal' },
  { value: 'LIKE', label: 'LIKE Pattern' },
  { value: 'NOT LIKE', label: 'NOT LIKE Exclude' },
  { value: 'IN', label: 'IN (comma-separated)' },
  { value: 'IS NULL', label: 'IS NULL' },
  { value: 'IS NOT NULL', label: 'IS NOT NULL' },
];

function escapeSQLVal(raw: string): string {
  const trimmed = raw.trim();
  if (/^-?\d+(\.\d+)?$/.test(trimmed)) {
    return trimmed;
  }
  return `'${raw.replace(/'/g, "''")}'`;
}

function quoteIdentForEngine(dbType: string, ident: string): string {
  if (['mysql', 'mariadb', 'tidb', 'oceanbase', 'clickhouse'].includes(dbType)) {
    return `\`${ident.replace(/`/g, '``')}\``;
  }
  if (dbType === 'sqlserver') {
    return `[${ident.replace(/\]/g, ']]')}]`;
  }
  return `"${ident.replace(/"/g, '""')}"`;
}

function formatSelectedRows(
  selectedRows: Record<string, any>[],
  cols: string[],
  mode: 'tsv' | 'tsv_header' | 'csv' | 'json' | 'sql',
  tableName: string
): string {
  if (mode === 'tsv') {
    return serializeRowsToTsv(cols, selectedRows, false);
  }
  if (mode === 'tsv_header') {
    return serializeRowsToTsv(cols, selectedRows, true);
  }
  if (mode === 'json') {
    const clean = selectedRows.map((r) => {
      const o: Record<string, any> = {};
      cols.forEach((c) => {
        o[c] = r[c] ?? null;
      });
      return o;
    });
    return JSON.stringify(clean, null, 2);
  }
  if (mode === 'sql') {
    const colList = cols.map((c) => `"${c}"`).join(', ');
    return selectedRows
      .map((r) => {
        const vals = cols
          .map((c) => {
            const v = r[c];
            if (v === null || v === undefined) return 'NULL';
            if (typeof v === 'number' || typeof v === 'boolean') return String(v);
            return `'${String(v).replace(/'/g, "''")}'`;
          })
          .join(', ');
        return `INSERT INTO "${tableName}" (${colList}) VALUES (${vals});`;
      })
      .join('\r\n');
  }
  const esc = (v: any) => {
    if (v === null || v === undefined) return '';
    const s = String(v);
    return /[",\n\r]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
  };
  return [cols.map(esc).join(','), ...selectedRows.map((r) => cols.map((c) => esc(r[c])).join(','))].join('\r\n');
}

interface CellStateHub {
  subscribe: (rk: string, colName: string, fn: () => void) => () => void;
  isEditing: (rk: string, colName: string) => boolean;
  isFocused: (rk: string, colName: string) => boolean;
  isDirty: (rk: string, colName: string) => boolean;
  setEditing: (next: { rowKey: string; col: string } | null) => void;
  setFocused: (next: { rowKey: string; col: string } | null) => void;
  getDisplayedColumns: () => QueryColumn[];
  setBackup: (b: { rowKey: string; col: string; prevVal: any } | null) => void;
  getBackup: () => { rowKey: string; col: string; prevVal: any } | null;
  onCellChange: (rk: string, colName: string, val: any) => void;
  onPasteCells: (clip: string, rk: string, colName: string) => void;
  onCheckDissolve: (rk: string, relatedTarget?: EventTarget | null) => void;
  onRemoveNewRow: (rk: string) => void;
}

const GridDataCell = React.memo<{
  rk: string;
  col: QueryColumn;
  colIdx: number;
  isPK: boolean;
  val: any;
  record: Record<string, any>;
  canWrite: boolean;
  isMobile: boolean;
  hub: CellStateHub;
  t: (key: string, params?: Record<string, string | number>) => string;
}>(({ rk, col, colIdx, isPK, val, record, canWrite, isMobile, hub, t }) => {
  const [, setTick] = useState(0);

  useEffect(() => {
    return hub.subscribe(rk, col.name, () => setTick((n) => n + 1));
  }, [hub, rk, col.name]);

  const isEditing = canWrite && hub.isEditing(rk, col.name);
  const isDirty = hub.isDirty(rk, col.name);
  const isNew = canWrite && rk.startsWith('new_');
  const isFocused = hub.isFocused(rk, col.name);

  if (isEditing || isNew) {
    const strVal = val === null ? '' : String(val ?? '');
    const isMultiline = isEditing && (strVal.includes('\n') || strVal.length > 80);

    if (isMultiline) {
      return (
        <Input.TextArea
          size="small"
          autoFocus
          autoSize={{ minRows: 2, maxRows: 6 }}
          value={strVal}
          style={{ fontFamily: 'monospace', fontSize: 12 }}
          onChange={(e) => hub.onCellChange(rk, col.name, e.target.value)}
          onBlur={(e) => {
            hub.setEditing(null);
            hub.onCheckDissolve(rk, e.relatedTarget);
          }}
          onKeyDown={(e) => {
            if (e.key === 'Escape') {
              e.preventDefault();
              e.stopPropagation();
              const bk = hub.getBackup();
              if (bk && bk.rowKey === rk && bk.col === col.name) {
                hub.onCellChange(rk, col.name, bk.prevVal);
              }
              hub.setEditing(null);
              hub.onCheckDissolve(rk, null);
            } else if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
              e.preventDefault();
              hub.setEditing(null);
            }
          }}
        />
      );
    }

    return (
      <Input
        size="small"
        autoFocus={isEditing}
        value={strVal}
        placeholder={val === null ? 'NULL' : isNew && isPK ? t('(Auto / PK)') : ''}
        onChange={(e) => hub.onCellChange(rk, col.name, e.target.value)}
        onPaste={(e) => {
          const clip = e.clipboardData?.getData('text/plain') || '';
          if (clip.includes('\t') || clip.includes('\n')) {
            e.preventDefault();
            hub.setEditing(null);
            hub.onPasteCells(clip, rk, col.name);
          }
        }}
        onBlur={(e) => {
          if (!isNew) hub.setEditing(null);
          hub.onCheckDissolve(rk, e.relatedTarget);
        }}
        onPressEnter={() => hub.setEditing(null)}
        onKeyDown={(e) => {
          const dispCols = hub.getDisplayedColumns();
          if (e.key === 'Tab' && dispCols.length > 1) {
            const nextIdx = e.shiftKey ? colIdx - 1 : colIdx + 1;
            if (nextIdx >= 0 && nextIdx < dispCols.length) {
              e.preventDefault();
              const nextCol = dispCols[nextIdx].name;
              hub.setBackup({ rowKey: rk, col: nextCol, prevVal: record[nextCol] });
              hub.setEditing({ rowKey: rk, col: nextCol });
              hub.setFocused({ rowKey: rk, col: nextCol });
            }
          } else if (e.altKey && !e.shiftKey && e.key.toLowerCase() === 'n' && col.nullable) {
            e.preventDefault();
            e.stopPropagation();
            hub.onCellChange(rk, col.name, null);
            hub.setEditing(null);
          } else if (e.key === 'Escape') {
            e.preventDefault();
            e.stopPropagation();
            if (!isNew) {
              const bk = hub.getBackup();
              if (bk && bk.rowKey === rk && bk.col === col.name) {
                hub.onCellChange(rk, col.name, bk.prevVal);
              }
              hub.setEditing(null);
            } else if (record.__fromFiller && !record.__touched) {
              hub.setEditing(null);
              hub.onRemoveNewRow(rk);
            } else {
              hub.setEditing(null);
            }
          }
        }}
        suffix={
          col.nullable && (!isMobile || isEditing || isFocused) ? (
            <Tag
              style={{ cursor: 'pointer', margin: 0, fontSize: 10 }}
              onPointerDown={(e) => {
                e.preventDefault();
                e.stopPropagation();
                hub.onCellChange(rk, col.name, null);
                hub.setEditing(null);
              }}
              onMouseDown={(e) => {
                e.preventDefault();
                e.stopPropagation();
                hub.onCellChange(rk, col.name, null);
                hub.setEditing(null);
              }}
            >
              {isMobile ? 'NULL' : t('Set NULL')}
            </Tag>
          ) : undefined
        }
      />
    );
  }

  return (
    <span
      data-raw-copy={formatRawCellText(val)}
      onDoubleClick={() => {
        if (!canWrite) return;
        hub.setBackup({ rowKey: rk, col: col.name, prevVal: val });
        hub.setEditing({ rowKey: rk, col: col.name });
      }}
      style={{
        display: 'block',
        minHeight: 22,
        lineHeight: '22px',
        cursor: canWrite ? 'pointer' : 'default',
        padding: '0 4px',
        borderRadius: 4,
        background: isDirty
          ? 'rgba(250, 173, 20, 0.22)'
          : isFocused
            ? 'rgba(22, 119, 255, 0.08)'
            : 'transparent',
        border: isDirty
          ? '1px solid #faad14'
          : isFocused
            ? '1px solid rgba(22, 119, 255, 0.35)'
            : '1px solid transparent',
        overflow: 'hidden',
        textOverflow: 'ellipsis',
        whiteSpace: 'nowrap',
      }}
      title={canWrite ? t('Double-click to edit cell') : formatRawCellText(val)}
    >
      {val === null ? (
        <Tag style={{ fontSize: 10, opacity: 0.65, margin: 0, lineHeight: '16px' }}>NULL</Tag>
      ) : (
        String(val)
      )}
    </span>
  );
});

export const TableDataTab: React.FC<Props> = React.memo(({ tab, onOpenTransfer }) => {
  const { t } = useI18n();
  const { isMobile } = useResponsive();
  const isActiveTab = useWorkbenchStore((s) => s.activeTabKey === tab.key);
  const isDesktop = useWorkbenchStore((s) => s.isDesktop);
  const role = useWorkbenchStore((s) => s.role);
  const connUnlocked = useWorkbenchStore(
    (s) => s.connections.find((c) => c.id === tab.connectionId)?.unlocked
  );
  const treeRefreshTick = useWorkbenchStore((s) => s.treeRefreshTick);
  const canWrite = isDesktop || role !== 'readonly';
  const needsUnlockReloadRef = useRef(false);

  const [loading, setLoading] = useState(false);
  const [columns, setColumns] = useState<QueryColumn[]>([]);
  const [rows, setRows] = useState<Record<string, any>[]>([]);
  const [originalRows, setOriginalRows] = useState<Record<string, any>[]>([]);
  const [primaryKeys, setPrimaryKeys] = useState<string[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(100);
  const [whereInput, setWhereInput] = useState('');
  const [appliedFilter, setAppliedFilter] = useState('');
  const [sortSpec, setSortSpec] = useState<{ column: string; direction: string }[]>([]);

  // Column visibility filter for wide tables
  const [hiddenCols, setHiddenCols] = useState<string[]>([]);
  const [colFilterSearch, setColFilterSearch] = useState('');

  // Visual Search Builder & Mobile Filter Drawer toggle
  const [showSearchBuilder, setShowSearchBuilder] = useState(false);
  const [mobileFilterOpen, setMobileFilterOpen] = useState(false);
  const [searchLogic, setSearchLogic] = useState<'AND' | 'OR'>('AND');
  const [searchConds, setSearchConds] = useState<SearchCond[]>([]);

  // Form-based Row Insert Modal
  const [insertModalOpen, setInsertModalOpen] = useState(false);
  const [insertForm] = Form.useForm();
  const [nullFlags, setNullFlags] = useState<Record<string, boolean>>({});

  // Paste Rows Modal (fallback for toolbar Paste button or manual multi-row paste review)
  const [pasteModalOpen, setPasteModalOpen] = useState(false);
  const [pasteText, setPasteText] = useState('');
  const [pasteMode, setPasteMode] = useState<'new_rows' | 'overwrite_cells'>('new_rows');

  // Batch Column Update Modal for selected rows
  const [batchModalOpen, setBatchModalOpen] = useState(false);
  const [batchCol, setBatchCol] = useState<string>('');
  const [batchVal, setBatchVal] = useState<string>('');
  const [batchSetNull, setBatchSetNull] = useState<boolean>(false);

  // Inline editing, active cell focus & pending mutations (per-cell pub/sub avoids full-table re-renders)
  const editingCellRef = useRef<{ rowKey: string; col: string } | null>(null);
  const focusedCellRef = useRef<{ rowKey: string; col: string } | null>(null);
  const dirtyUpdatesRef = useRef<Record<string, Record<string, any>>>({});
  const cellListenersRef = useRef<Map<string, Set<() => void>>>(new Map());
  const [hasFocusedCell, setHasFocusedCell] = useState(false);
  const [dirtyRowCount, setDirtyRowCount] = useState(0);
  const [newRows, setNewRows] = useState<Record<string, any>[]>([]);
  const [selectedRowKeys, setSelectedRowKeys] = useState<React.Key[]>([]);
  const [previewSQLs, setPreviewSQLs] = useState<string[]>([]);
  const [previewModalOpen, setPreviewModalOpen] = useState(false);
  const [saving, setSaving] = useState(false);

  const notifyCell = useCallback((rk: string, colName: string) => {
    const subs = cellListenersRef.current.get(`${rk}\0${colName}`);
    if (subs) {
      subs.forEach((fn) => fn());
    }
  }, []);

  const updateEditingCell = useCallback(
    (next: { rowKey: string; col: string } | null) => {
      const prev = editingCellRef.current;
      if (prev?.rowKey === next?.rowKey && prev?.col === next?.col) return;
      editingCellRef.current = next;
      if (prev) notifyCell(prev.rowKey, prev.col);
      if (next) notifyCell(next.rowKey, next.col);
    },
    [notifyCell]
  );

  const updateFocusedCell = useCallback(
    (next: { rowKey: string; col: string } | null) => {
      const prev = focusedCellRef.current;
      if (prev?.rowKey === next?.rowKey && prev?.col === next?.col) return;
      focusedCellRef.current = next;
      setHasFocusedCell(Boolean(next));
      if (prev) notifyCell(prev.rowKey, prev.col);
      if (next) notifyCell(next.rowKey, next.col);
    },
    [notifyCell]
  );

  // Single virtual-anchor context menu state (zero extra DOM nodes per cell)
  const [ctxMenu, setCtxMenu] = useState<{
    open: boolean;
    x: number;
    y: number;
    record: Record<string, any> | null;
    col: QueryColumn | null;
  }>({ open: false, x: 0, y: 0, record: null, col: null });

  // Row inspector / bottom-sheet editor drawer
  const [inspectRow, setInspectRow] = useState<Record<string, any> | null>(null);
  const [inspectSearch, setInspectSearch] = useState<string>('');

  const browseAbortRef = useRef<AbortController | null>(null);
  const editBackupRef = useRef<{ rowKey: string; col: string; prevVal: any } | null>(null);
  const rowsRef = useRef<Record<string, any>[]>([]);
  const originalRowsRef = useRef<Record<string, any>[]>([]);
  const newRowsRef = useRef<Record<string, any>[]>([]);
  rowsRef.current = rows;
  originalRowsRef.current = originalRows;
  newRowsRef.current = newRows;

  useEffect(() => {
    return () => {
      browseAbortRef.current?.abort();
    };
  }, []);

  const loadData = useCallback(
    async (targetPage = page, targetSize = pageSize, filter = appliedFilter, sorts = sortSpec) => {
      if (!tab.table) return;
      browseAbortRef.current?.abort();
      const ctrl = new AbortController();
      browseAbortRef.current = ctrl;

      setLoading(true);
      try {
        const res = await api.browseData(
          tab.connectionId,
          {
            database: tab.database,
            schema: tab.schema,
            table: tab.table,
            page: targetPage,
            pageSize: targetSize,
            filter,
            sorts,
          },
          ctrl.signal
        );
        if (ctrl.signal.aborted) return;
        needsUnlockReloadRef.current = false;

        setColumns(res.columns || []);
        setPrimaryKeys(res.primaryKeys || []);
        setTotal(res.total || 0);

        const mapped = (res.rows || []).map((arr, rIdx) => {
          const obj: Record<string, any> = { __rowKey: `r_${rIdx}` };
          (res.columns || []).forEach((col, cIdx) => {
            obj[col.name] = arr[cIdx];
          });
          return obj;
        });
        setRows(mapped);
        setOriginalRows(mapped);
        dirtyUpdatesRef.current = {};
        setDirtyRowCount(0);
        setNewRows([]);
        setSelectedRowKeys([]);
        setInspectRow(null);
        editingCellRef.current = null;
        focusedCellRef.current = null;
        setHasFocusedCell(false);
      } catch (err: any) {
        if (isAuthPromptCancelledError(err)) {
          needsUnlockReloadRef.current = true;
        } else if (err?.name !== 'AbortError') {
          message.error(err.message);
        }
      } finally {
        if (!ctrl.signal.aborted) {
          setLoading(false);
        }
      }
    },
    [tab.connectionId, tab.database, tab.schema, tab.table, page, pageSize, appliedFilter, sortSpec]
  );

  useEffect(() => {
    setHiddenCols([]);
    setColFilterSearch('');
    setSearchConds([]);
    setShowSearchBuilder(false);
    setMobileFilterOpen(false);
    setInspectRow(null);
    setInspectSearch('');
    setPage(1);
    setWhereInput('');
    setAppliedFilter('');
    setSortSpec([]);
    needsUnlockReloadRef.current = false;
    loadData(1, pageSize, '', []);
  }, [tab.connectionId, tab.database, tab.schema, tab.table]);

  useEffect(() => {
    if (needsUnlockReloadRef.current && connUnlocked) {
      needsUnlockReloadRef.current = false;
      void loadData();
    }
  }, [connUnlocked, treeRefreshTick, loadData]);

  const visibleColumns = useMemo(
    () => columns.filter((c) => c.name !== '__dbharbor_rowid__'),
    [columns]
  );

  const displayedColumns = useMemo(() => {
    if (hiddenCols.length === 0) return visibleColumns;
    const hiddenSet = new Set(hiddenCols);
    return visibleColumns.filter((c) => !hiddenSet.has(c.name));
  }, [visibleColumns, hiddenCols]);

  const topNewRows = useMemo(() => newRows.filter((r) => !r.__atBottom), [newRows]);
  const bottomNewRows = useMemo(() => newRows.filter((r) => Boolean(r.__atBottom)), [newRows]);
  const combinedDataSource = useMemo(
    () => [...topNewRows, ...rows, ...bottomNewRows],
    [topNewRows, rows, bottomNewRows]
  );

  const activeNewRows = useMemo(
    () =>
      newRows.filter(
        (r) =>
          !r.__fromFiller ||
          Boolean(r.__touched) ||
          visibleColumns.some((c) => r[c.name] !== '' && r[c.name] !== undefined)
      ),
    [newRows, visibleColumns]
  );

  const buildLocator = (origRow: Record<string, any>): Record<string, any> => {
    const loc: Record<string, any> = {};
    if (primaryKeys.length > 0) {
      primaryKeys.forEach((pk) => {
        loc[pk] = origRow[pk];
      });
      return loc;
    }
    visibleColumns.forEach((c) => {
      loc[c.name] = origRow[c.name];
    });
    return loc;
  };

  const buildMutationPayload = () => {
    const inserts = activeNewRows.map((nr) => {
      const clean: Record<string, any> = {};
      Object.entries(nr).forEach(([k, v]) => {
        if (!k.startsWith('__') && v !== undefined && v !== '') {
          clean[k] = v;
        }
      });
      return clean;
    });

    const updates = Object.entries(dirtyUpdatesRef.current).map(([rowKey, changes]) => {
      const idx = rows.findIndex((r) => r.__rowKey === rowKey);
      const orig = originalRows[idx] || rows[idx];
      return {
        locator: buildLocator(orig),
        changes,
      };
    });

    const deletes: Record<string, any>[] = [];
    selectedRowKeys.forEach((rk) => {
      const keyStr = String(rk);
      if (keyStr.startsWith('new_') || keyStr.startsWith('__filler_')) return;
      const idx = rows.findIndex((r) => r.__rowKey === keyStr);
      if (idx >= 0) {
        deletes.push(buildLocator(originalRows[idx]));
      }
    });

    return {
      database: tab.database,
      schema: tab.schema,
      table: tab.table || '',
      primaryKeys,
      inserts,
      updates,
      deletes,
    };
  };

  const pendingChangeCount = dirtyRowCount + activeNewRows.length;

  const handleCellChange = useCallback(
    (rowKey: string, colName: string, value: any) => {
      if (!canWrite) return;
      if (rowKey.startsWith('new_')) {
        setNewRows((prev) =>
          prev.map((r) => (r.__rowKey === rowKey ? { ...r, __touched: true, [colName]: value } : r))
        );
        return;
      }
      setRows((prev) => prev.map((r) => (r.__rowKey === rowKey ? { ...r, [colName]: value } : r)));
      const idx = rowsRef.current.findIndex((r) => r.__rowKey === rowKey);
      const origVal = originalRowsRef.current[idx]?.[colName];

      const prevMap = dirtyUpdatesRef.current;
      const wasDirty = Boolean(prevMap[rowKey] && colName in prevMap[rowKey]);
      const curRowPatch = { ...(prevMap[rowKey] || {}) };
      if (origVal === value || (origVal !== null && value !== null && String(origVal) === String(value))) {
        delete curRowPatch[colName];
      } else {
        curRowPatch[colName] = value;
      }
      const next = { ...prevMap };
      if (Object.keys(curRowPatch).length === 0) {
        delete next[rowKey];
      } else {
        next[rowKey] = curRowPatch;
      }
      dirtyUpdatesRef.current = next;
      const nowDirty = Boolean(next[rowKey] && colName in next[rowKey]);
      if (wasDirty !== nowDirty) {
        notifyCell(rowKey, colName);
      }
      setDirtyRowCount(Object.keys(next).length);
    },
    [canWrite, notifyCell]
  );

  const handleAddRowInline = () => {
    if (!canWrite) return;
    const atBottom = rows.length <= 15;
    const newKey = `new_${Date.now()}_${newRows.length}`;
    const rowObj: Record<string, any> = {
      __rowKey: newKey,
      __atBottom: atBottom,
      __touched: true,
    };
    visibleColumns.forEach((c) => {
      rowObj[c.name] = '';
    });
    setNewRows((prev) => (atBottom ? [...prev, rowObj] : [rowObj, ...prev]));
    const firstEditable = displayedColumns.find((c) => !primaryKeys.includes(c.name)) || displayedColumns[0];
    if (firstEditable) {
      updateEditingCell({ rowKey: newKey, col: firstEditable.name });
      updateFocusedCell({ rowKey: newKey, col: firstEditable.name });
    }
  };

  const materializeFillerRow = useCallback(
    (preferredCol?: string) => {
      if (!canWrite || displayedColumns.length === 0) return;
      const targetCol =
        preferredCol ||
        displayedColumns.find((c) => !primaryKeys.includes(c.name))?.name ||
        displayedColumns[0].name;

      const existingUntouched = newRowsRef.current.find(
        (r) =>
          r.__fromFiller &&
          !r.__touched &&
          visibleColumns.every((c) => r[c.name] === '' || r[c.name] === undefined)
      );
      if (existingUntouched) {
        updateEditingCell({ rowKey: existingUntouched.__rowKey, col: targetCol });
        updateFocusedCell({ rowKey: existingUntouched.__rowKey, col: targetCol });
        return;
      }

      const newKey = `new_${Date.now()}_${Math.random().toString(36).slice(2, 6)}`;
      const rowObj: Record<string, any> = {
        __rowKey: newKey,
        __fromFiller: true,
        __atBottom: true,
        __touched: false,
      };
      visibleColumns.forEach((c) => {
        rowObj[c.name] = '';
      });
      setNewRows((prev) => [...prev, rowObj]);
      updateEditingCell({ rowKey: newKey, col: targetCol });
      updateFocusedCell({ rowKey: newKey, col: targetCol });
    },
    [canWrite, displayedColumns, primaryKeys, visibleColumns, updateEditingCell, updateFocusedCell]
  );

  const checkDissolveFillerRow = useCallback(
    (rowKey: string, relatedTarget?: EventTarget | null) => {
      if (!rowKey.startsWith('new_')) return;
      const checkSameRow = (el: Element | null) => {
        if (!el) return false;
        const tr = el.closest('tr');
        return Boolean(tr && tr.getAttribute('data-row-key') === rowKey);
      };
      if (relatedTarget instanceof Element && checkSameRow(relatedTarget)) {
        return;
      }
      window.setTimeout(() => {
        if (checkSameRow(document.activeElement)) return;
        setNewRows((prev) =>
          prev.filter((r) => {
            if (r.__rowKey !== rowKey) return true;
            if (!r.__fromFiller || r.__touched) return true;
            return visibleColumns.some((c) => r[c.name] !== '' && r[c.name] !== undefined);
          })
        );
      }, 0);
    },
    [visibleColumns]
  );

  const openBatchEditModal = useCallback(() => {
    if (!canWrite || selectedRowKeys.length === 0 || visibleColumns.length === 0) return;
    const fc = focusedCellRef.current;
    const defaultCol =
      (fc && visibleColumns.some((c) => c.name === fc.col) ? fc.col : '') ||
      visibleColumns.find((c) => !primaryKeys.includes(c.name))?.name ||
      visibleColumns[0].name;
    setBatchCol(defaultCol);
    setBatchVal('');
    setBatchSetNull(false);
    setBatchModalOpen(true);
  }, [canWrite, selectedRowKeys.length, visibleColumns, primaryKeys]);

  const handleApplyBatchEdit = useCallback(() => {
    if (!canWrite || !batchCol || selectedRowKeys.length === 0) return;
    const nextValue = batchSetNull ? null : batchVal;
    selectedRowKeys.forEach((rk) => {
      const keyStr = String(rk);
      if (!keyStr.startsWith('__filler_')) {
        handleCellChange(keyStr, batchCol, nextValue);
      }
    });
    setBatchModalOpen(false);
    message.success(
      t('Staged batch update on "{col}" for {count} row(s)', {
        col: batchCol,
        count: selectedRowKeys.length,
      })
    );
  }, [canWrite, batchCol, batchSetNull, batchVal, selectedRowKeys, handleCellChange, t]);

  const handleExportFiltered = useCallback(() => {
    if (!tab.table) return;
    const q = (ident: string) => quoteIdentForEngine(tab.dbType, ident);
    const useSchema =
      Boolean(tab.schema) &&
      ['postgres', 'kingbase', 'sqlserver', 'oracle', 'dameng'].includes(tab.dbType);
    const qualified = useSchema ? `${q(tab.schema!)}.${q(tab.table)}` : q(tab.table);
    let sql = `SELECT * FROM ${qualified}`;
    if (appliedFilter.trim()) {
      sql += ` WHERE ${appliedFilter.trim()}`;
    }
    if (sortSpec.length > 0) {
      const orderClause = sortSpec
        .map((s) => `${q(s.column)} ${s.direction === 'DESC' ? 'DESC' : 'ASC'}`)
        .join(', ');
      sql += ` ORDER BY ${orderClause}`;
    }
    onOpenTransfer('export', undefined, { customSQL: sql });
  }, [tab.table, tab.dbType, tab.schema, appliedFilter, sortSpec, onOpenTransfer]);

  const handleDuplicateRows = useCallback(
    (sourceRows: Record<string, any>[]) => {
      if (!canWrite || sourceRows.length === 0) return;
      const singlePK = primaryKeys.length === 1 ? primaryKeys[0] : '';
      const pkColMeta = singlePK ? visibleColumns.find((c) => c.name === singlePK) : undefined;
      const autoLikePK =
        Boolean(singlePK) &&
        (pkColMeta?.type?.toUpperCase().includes('INT') ||
          pkColMeta?.type?.toUpperCase().includes('SERIAL') ||
          singlePK.toLowerCase().endsWith('id'));

      const cloned = sourceRows.map((src, idx) => {
        const rowObj: Record<string, any> = { __rowKey: `new_${Date.now()}_${idx}_${Math.random().toString(36).slice(2, 6)}` };
        visibleColumns.forEach((c) => {
          if (autoLikePK && c.name === singlePK) {
            rowObj[c.name] = '';
          } else {
            rowObj[c.name] = src[c.name] ?? null;
          }
        });
        return rowObj;
      });
      setNewRows((prev) => [...cloned, ...prev]);
      message.success(t('Duplicated {count} row(s) into staging', { count: cloned.length }));
    },
    [canWrite, primaryKeys, visibleColumns, t]
  );

  /**
   * Applies single-row or multi-row clipboard text (TSV from Notepad/Excel/DBHarbor or JSON) into staging.
   */
  const applyClipboardPaste = useCallback(
    (
      rawText: string,
      mode: 'auto' | 'new_rows' | 'overwrite_cells' = 'auto',
      anchor?: { rowKey: string; col: string } | null
    ) => {
      if (!canWrite) return false;
      const parsed = parseClipboardTabular(rawText, displayedColumns);
      if (parsed.records.length === 0 && parsed.matrix.length === 0) {
        message.warning(t('Clipboard does not contain valid tabular or JSON row data'));
        return false;
      }

      const targetAnchor = anchor ?? focusedCellRef.current;
      const maxMatrixCols = parsed.matrix.reduce((m, r) => Math.max(m, r.length), 0);
      const shouldOverwrite =
        mode === 'overwrite_cells' ||
        (mode === 'auto' &&
          Boolean(targetAnchor) &&
          !parsed.matchedByHeader &&
          displayedColumns.length > 1 &&
          maxMatrixCols > 0 &&
          maxMatrixCols < displayedColumns.length);

      if (shouldOverwrite && targetAnchor) {
        const startRowIdx = combinedDataSource.findIndex((r) => r.__rowKey === targetAnchor.rowKey);
        const startColIdx = Math.max(
          0,
          displayedColumns.findIndex((c) => c.name === targetAnchor.col)
        );
        if (startRowIdx >= 0) {
          let updatedCells = 0;
          const overflowNewRows: Record<string, any>[] = [];

          parsed.matrix.forEach((matrixRow, rOffset) => {
            const targetRow = combinedDataSource[startRowIdx + rOffset];
            if (targetRow) {
              matrixRow.forEach((cellStr, cOffset) => {
                const colMeta = displayedColumns[startColIdx + cOffset];
                if (!colMeta) return;
                const val = coercePastedCellValue(cellStr, colMeta);
                handleCellChange(targetRow.__rowKey, colMeta.name, val);
                updatedCells++;
              });
            } else {
              const nr: Record<string, any> = {
                __rowKey: `new_${Date.now()}_${rOffset}_${Math.random().toString(36).slice(2, 6)}`,
              };
              visibleColumns.forEach((c) => {
                nr[c.name] = '';
              });
              matrixRow.forEach((cellStr, cOffset) => {
                const colMeta = displayedColumns[startColIdx + cOffset];
                if (colMeta) {
                  nr[colMeta.name] = coercePastedCellValue(cellStr, colMeta);
                }
              });
              overflowNewRows.push(nr);
            }
          });

          if (overflowNewRows.length > 0) {
            setNewRows((prev) => [...prev, ...overflowNewRows]);
          }
          message.success(
            t('Pasted {rows} row(s) ({cells} cell(s) updated)', {
              rows: parsed.matrix.length,
              cells: updatedCells,
            })
          );
          return true;
        }
      }

      // Default: Insert as staged new row(s), clearing colliding auto-increment PKs to avoid duplicate key errors
      const singlePK = primaryKeys.length === 1 ? primaryKeys[0] : '';
      const pkColMeta = singlePK ? visibleColumns.find((c) => c.name === singlePK) : undefined;
      const autoLikePK =
        Boolean(singlePK) &&
        (pkColMeta?.type?.toUpperCase().includes('INT') ||
          pkColMeta?.type?.toUpperCase().includes('SERIAL') ||
          singlePK.toLowerCase().endsWith('id'));
      const existingPKValues = new Set(
        singlePK ? combinedDataSource.map((r) => String(r[singlePK] ?? '')) : []
      );

      const staged: Record<string, any>[] = parsed.records.map((rec, idx) => {
        const rowObj: Record<string, any> = {
          __rowKey: `new_${Date.now()}_${idx}_${Math.random().toString(36).slice(2, 6)}`,
        };
        visibleColumns.forEach((c) => {
          if (Object.prototype.hasOwnProperty.call(rec, c.name)) {
            rowObj[c.name] = rec[c.name];
          } else {
            rowObj[c.name] = '';
          }
        });
        if (
          autoLikePK &&
          rowObj[singlePK] !== '' &&
          rowObj[singlePK] !== null &&
          existingPKValues.has(String(rowObj[singlePK]))
        ) {
          rowObj[singlePK] = '';
        } else if (singlePK && rowObj[singlePK]) {
          existingPKValues.add(String(rowObj[singlePK]));
        }
        return rowObj;
      });

      setNewRows((prev) => [...staged, ...prev]);
      message.success(
        t('Pasted {count} row(s) into staging — review & click Save Changes', { count: staged.length })
      );
      return true;
    },
    [canWrite, displayedColumns, visibleColumns, combinedDataSource, handleCellChange, primaryKeys, t]
  );

  const openPasteDialog = async (defaultMode: 'new_rows' | 'overwrite_cells') => {
    if (!canWrite) return;
    let initialClip = '';
    if (navigator.clipboard && typeof navigator.clipboard.readText === 'function') {
      try {
        initialClip = (await navigator.clipboard.readText()) || '';
      } catch {
        // Ignore clipboard read permission errors; user can paste manually into textarea
      }
    }
    setPasteMode(defaultMode);
    setPasteText(initialClip);
    setPasteModalOpen(true);
  };

  const triggerToolbarPaste = async (preferredMode: 'new_rows' | 'overwrite_cells' = 'new_rows') => {
    if (!canWrite) return;
    if (navigator.clipboard && typeof navigator.clipboard.readText === 'function') {
      try {
        const txt = await navigator.clipboard.readText();
        if (txt && txt.trim()) {
          applyClipboardPaste(txt, preferredMode, focusedCellRef.current);
          return;
        }
      } catch {
        // Browser requires manual paste dialog (e.g. non-HTTPS context)
      }
    }
    setPasteMode(preferredMode);
    setPasteText('');
    setPasteModalOpen(true);
  };

  const openFormInsertModal = () => {
    insertForm.resetFields();
    setNullFlags({});
    setInsertModalOpen(true);
  };

  const handleSubmitFormInsert = async () => {
    const vals = insertForm.getFieldsValue();
    const record: Record<string, any> = {};
    visibleColumns.forEach((c) => {
      if (nullFlags[c.name]) {
        record[c.name] = null;
      } else if (vals[c.name] !== undefined && vals[c.name] !== '') {
        record[c.name] = vals[c.name];
      }
    });
    if (Object.keys(record).length === 0) {
      message.warning(t('Please fill in at least one column value'));
      return;
    }
    setSaving(true);
    try {
      await api.applyMutations(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        table: tab.table || '',
        primaryKeys,
        inserts: [record],
        updates: [],
        deletes: [],
      });
      message.success(t('Row inserted successfully'));
      setInsertModalOpen(false);
      await loadData(page, pageSize, appliedFilter, sortSpec);
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setSaving(false);
    }
  };

  const applyVisualSearch = () => {
    const clauses = searchConds
      .filter((c) => c.column)
      .map((c) => {
        const qCol = quoteIdentForEngine(tab.dbType, c.column);
        if (c.op === 'IS NULL' || c.op === 'IS NOT NULL') {
          return `${qCol} ${c.op}`;
        }
        if (c.op === 'IN') {
          const items = c.value
            .split(',')
            .map((s) => s.trim())
            .filter(Boolean)
            .map(escapeSQLVal);
          return items.length > 0 ? `${qCol} IN (${items.join(', ')})` : '';
        }
        return `${qCol} ${c.op} ${escapeSQLVal(c.value)}`;
      })
      .filter(Boolean);

    const composed = clauses.join(` ${searchLogic} `);
    setWhereInput(composed);
    setAppliedFilter(composed);
    setPage(1);
    loadData(1, pageSize, composed, sortSpec);
  };

  const applyQuickWhereClause = (clause: string) => {
    const next = appliedFilter.trim() ? `${appliedFilter.trim()} AND ${clause}` : clause;
    setWhereInput(next);
    setAppliedFilter(next);
    setPage(1);
    loadData(1, pageSize, next, sortSpec);
  };

  const handlePreviewSQL = async () => {
    try {
      const payload = buildMutationPayload();
      const sqls = await api.previewMutations(tab.connectionId, payload);
      setPreviewSQLs(sqls || []);
      setPreviewModalOpen(true);
    } catch (err: any) {
      message.error(err.message);
    }
  };

  const handleCommitChanges = async (includeDeletes = false) => {
    setSaving(true);
    try {
      const payload = buildMutationPayload();
      if (!includeDeletes) {
        payload.deletes = [];
      }
      const res = await api.applyMutations(tab.connectionId, payload);
      message.success(
        t('Changes committed: {ins} inserted, {upd} updated, {del} deleted', {
          ins: res.inserted,
          upd: res.updated,
          del: res.deleted,
        })
      );
      setPreviewModalOpen(false);
      await loadData(page, pageSize, appliedFilter, sortSpec);
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setSaving(false);
    }
  };

  const handleDeleteSingleRow = async (rec: Record<string, any>) => {
    if (!canWrite) return;
    const rk = String(rec.__rowKey);
    if (rk.startsWith('new_')) {
      setNewRows((prev) => prev.filter((r) => r.__rowKey !== rk));
      return;
    }
    const idx = rows.findIndex((r) => r.__rowKey === rk);
    if (idx < 0) return;
    setSaving(true);
    try {
      await api.applyMutations(tab.connectionId, {
        database: tab.database,
        schema: tab.schema,
        table: tab.table || '',
        primaryKeys,
        inserts: [],
        updates: [],
        deletes: [buildLocator(originalRows[idx] || rec)],
      });
      message.success(t('Row deleted'));
      await loadData(page, pageSize, appliedFilter, sortSpec);
    } catch (err: any) {
      message.error(err.message);
    } finally {
      setSaving(false);
    }
  };

  const getRowsForCopy = useCallback((): Record<string, any>[] => {
    if (selectedRowKeys.length > 0) {
      const keySet = new Set(selectedRowKeys.map(String));
      return combinedDataSource.filter((r) => keySet.has(String(r.__rowKey)));
    }
    const fc = focusedCellRef.current;
    if (fc) {
      const found = combinedDataSource.find((r) => r.__rowKey === fc.rowKey);
      if (found) return [found];
    }
    return combinedDataSource;
  }, [selectedRowKeys, combinedDataSource]);

  const handleCopyRows = useCallback(
    (mode: 'tsv' | 'tsv_header' | 'csv' | 'json' | 'sql', explicitRows?: Record<string, any>[]) => {
      const targetRows = explicitRows ?? getRowsForCopy();
      if (targetRows.length === 0) return;
      const colNames = displayedColumns.map((c) => c.name);
      const out = formatSelectedRows(targetRows, colNames, mode, tab.table || 'table_name');
      writeTextToClipboard(out);
      const label = mode === 'tsv' || mode === 'tsv_header' ? 'TSV' : mode.toUpperCase();
      message.success(t('Copied {count} rows as {mode}', { count: targetRows.length, mode: label }));
    },
    [getRowsForCopy, displayedColumns, tab.table, t]
  );

  // Fallback copy handler when no DOM range is highlighted (e.g. checkbox-selected rows + Ctrl+C)
  const handleFallbackCopy = useCallback(
    (e: React.ClipboardEvent<HTMLDivElement>) => {
      const active = document.activeElement;
      if (active && (active.tagName === 'INPUT' || active.tagName === 'TEXTAREA')) return;
      const fc = focusedCellRef.current;
      const targetRows =
        selectedRowKeys.length > 0
          ? combinedDataSource.filter((r) => selectedRowKeys.includes(r.__rowKey))
          : fc
            ? combinedDataSource.filter((r) => r.__rowKey === fc.rowKey)
            : [];
      if (targetRows.length === 0) return;
      const colNames = displayedColumns.map((c) => c.name);
      const tsv = serializeRowsToTsv(colNames, targetRows, false);
      e.clipboardData.setData('text/plain', tsv);
      e.preventDefault();
      message.success(t('Copied {count} rows as {mode}', { count: targetRows.length, mode: 'TSV' }));
    },
    [selectedRowKeys, combinedDataSource, displayedColumns, t]
  );

  const commitRef = useRef(handleCommitChanges);
  commitRef.current = handleCommitChanges;
  const reloadRef = useRef(loadData);
  reloadRef.current = loadData;
  const copyRowsRef = useRef(handleCopyRows);
  copyRowsRef.current = handleCopyRows;
  const pasteRef = useRef(applyClipboardPaste);
  pasteRef.current = applyClipboardPaste;
  const addInlineRowRef = useRef(handleAddRowInline);
  addInlineRowRef.current = handleAddRowInline;

  // Keyboard shortcuts when this TableDataTab is active:
  // - Ctrl/Cmd+S: commit staged mutations
  // - Alt+N (when not in input): insert new inline blank row
  // - Ctrl/Cmd+C (when no text selection & not in input): copy checked rows or focused row as TSV
  // - Ctrl/Cmd+V (paste event when not in input): paste single/multiple TSV or JSON rows
  // - F5: refresh data
  useEffect(() => {
    if (!isActiveTab) return;
    const hasOpenOverlay = () =>
      pasteModalOpen ||
      insertModalOpen ||
      batchModalOpen ||
      previewModalOpen ||
      mobileFilterOpen ||
      Boolean(inspectRow) ||
      Boolean(document.querySelector('.ant-modal-wrap:not([style*="display: none"]), .ant-drawer-open'));

    const onKeyDown = (e: KeyboardEvent) => {
      if (hasOpenOverlay()) return;
      const active = document.activeElement;
      const inInput = Boolean(
        active && (active.tagName === 'INPUT' || active.tagName === 'TEXTAREA' || (active as HTMLElement).isContentEditable)
      );

      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
        if (pendingChangeCount > 0 && !saving) {
          e.preventDefault();
          commitRef.current(false);
        }
      } else if (e.altKey && !e.shiftKey && !e.ctrlKey && !e.metaKey && e.key.toLowerCase() === 'n' && !inInput && canWrite) {
        e.preventDefault();
        addInlineRowRef.current();
      } else if ((e.ctrlKey || e.metaKey) && !e.shiftKey && e.key.toLowerCase() === 'c' && !inInput) {
        const sel = window.getSelection();
        if (!sel || sel.isCollapsed) {
          if (selectedRowKeys.length > 0) {
            e.preventDefault();
            copyRowsRef.current('tsv');
          } else if (focusedCellRef.current) {
            const fcKey = focusedCellRef.current.rowKey;
            const row = combinedDataSource.find((r) => r.__rowKey === fcKey);
            if (row) {
              e.preventDefault();
              copyRowsRef.current('tsv', [row]);
            }
          }
        }
      } else if (e.key === 'F5') {
        e.preventDefault();
        reloadRef.current();
      }
    };

    const onPaste = (e: ClipboardEvent) => {
      if (!canWrite || hasOpenOverlay()) return;
      const active = document.activeElement;
      const inInput = Boolean(
        active && (active.tagName === 'INPUT' || active.tagName === 'TEXTAREA' || (active as HTMLElement).isContentEditable)
      );
      if (inInput) return;
      const txt = e.clipboardData?.getData('text/plain') || '';
      if (!txt.trim()) return;
      e.preventDefault();
      pasteRef.current(txt, 'auto');
    };

    window.addEventListener('keydown', onKeyDown);
    window.addEventListener('paste', onPaste);
    return () => {
      window.removeEventListener('keydown', onKeyDown);
      window.removeEventListener('paste', onPaste);
    };
  }, [
    isActiveTab,
    pendingChangeCount,
    saving,
    selectedRowKeys,
    combinedDataSource,
    canWrite,
    pasteModalOpen,
    insertModalOpen,
    batchModalOpen,
    previewModalOpen,
    mobileFilterOpen,
    inspectRow,
  ]);

  const gridWrapRef = useRef<HTMLDivElement | null>(null);
  const [gridBodyHeight, setGridBodyHeight] = useState<number>(360);
  const cellLongPressTimerRef = useRef<number | null>(null);
  const cellTouchStartRef = useRef<{ x: number; y: number } | null>(null);
  const cellLongPressFiredRef = useRef<boolean>(false);

  useEffect(() => {
    return () => {
      if (cellLongPressTimerRef.current !== null) {
        window.clearTimeout(cellLongPressTimerRef.current);
      }
    };
  }, []);

  useEffect(() => {
    const el = gridWrapRef.current;
    if (!el) return;
    const heightThreshold = isMobile ? 6 : 2;
    const measure = () => {
      if ((window as any).__DBHARBOR_LAYOUT_DRAGGING__) return;
      if (el.clientHeight <= 0) return;
      el.style.setProperty('--dbharbor-grid-total-h', `${Math.max(180, el.clientHeight - 2)}px`);
      const thead = el.querySelector('.ant-table-thead') as HTMLElement | null;
      const headH = thead && thead.offsetHeight > 0 ? thead.offsetHeight : 38;
      const nextH = Math.max(56, Math.floor(el.clientHeight - headH - 2));
      setGridBodyHeight((prev) => (Math.abs(prev - nextH) >= heightThreshold ? nextH : prev));
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    window.addEventListener('dbharbor-layout-settled', measure);
    return () => {
      ro.disconnect();
      window.removeEventListener('dbharbor-layout-settled', measure);
    };
  }, [displayedColumns.length, showSearchBuilder, mobileFilterOpen, isMobile]);

  const fillerRows = useMemo(() => {
    if (loading || displayedColumns.length === 0) return [];
    const targetVisible = Math.max(1, Math.floor((gridBodyHeight - 8) / 31));
    const count = Math.max(canWrite ? 1 : 0, targetVisible - combinedDataSource.length);
    if (count <= 0) return [];
    const list: Record<string, any>[] = [];
    for (let i = 0; i < count; i++) {
      list.push({
        __rowKey: `__filler_${i}`,
        __isFillerRow: true,
        __isFirstFiller: i === 0,
      });
    }
    return list;
  }, [loading, displayedColumns.length, gridBodyHeight, canWrite, combinedDataSource.length]);

  const gridDataSource = useMemo(
    () => (fillerRows.length > 0 ? [...combinedDataSource, ...fillerRows] : combinedDataSource),
    [combinedDataSource, fillerRows]
  );

  const maxRowNum = Math.max(1, (page - 1) * pageSize + rows.length);
  const rowIndexDigits = Math.max(2, String(maxRowNum).length);
  const rowIndexColWidth = isMobile
    ? (newRows.length > 0 ? 92 : 66) + Math.max(0, rowIndexDigits - 2) * 7
    : 76 + Math.max(0, rowIndexDigits - 4) * 7;

  const displayedColsRef = useRef(displayedColumns);
  displayedColsRef.current = displayedColumns;
  const checkDissolveRef = useRef(checkDissolveFillerRow);
  checkDissolveRef.current = checkDissolveFillerRow;
  const handleCellChangeRef = useRef(handleCellChange);
  handleCellChangeRef.current = handleCellChange;

  const cellHub = useMemo<CellStateHub>(
    () => ({
      subscribe: (rk, colName, fn) => {
        const key = `${rk}\0${colName}`;
        let set = cellListenersRef.current.get(key);
        if (!set) {
          set = new Set();
          cellListenersRef.current.set(key, set);
        }
        set.add(fn);
        return () => {
          const cur = cellListenersRef.current.get(key);
          if (cur) {
            cur.delete(fn);
            if (cur.size === 0) cellListenersRef.current.delete(key);
          }
        };
      },
      isEditing: (rk, colName) =>
        editingCellRef.current?.rowKey === rk && editingCellRef.current?.col === colName,
      isFocused: (rk, colName) =>
        focusedCellRef.current?.rowKey === rk && focusedCellRef.current?.col === colName,
      isDirty: (rk, colName) =>
        Boolean(dirtyUpdatesRef.current[rk] && colName in dirtyUpdatesRef.current[rk]),
      setEditing: (next) => updateEditingCell(next),
      setFocused: (next) => updateFocusedCell(next),
      getDisplayedColumns: () => displayedColsRef.current,
      setBackup: (b) => {
        editBackupRef.current = b;
      },
      getBackup: () => editBackupRef.current,
      onCellChange: (rk, colName, val) => handleCellChangeRef.current(rk, colName, val),
      onPasteCells: (clip, rk, colName) =>
        pasteRef.current(clip, 'overwrite_cells', { rowKey: rk, col: colName }),
      onCheckDissolve: (rk, rel) => checkDissolveRef.current(rk, rel),
      onRemoveNewRow: (rk) => setNewRows((prev) => prev.filter((r) => r.__rowKey !== rk)),
    }),
    [updateEditingCell, updateFocusedCell]
  );

  const tableColumns = useMemo(
    () => [
      {
        key: '__rowIndex',
        title: '#',
        width: rowIndexColWidth,
        minWidth: rowIndexColWidth,
        resizable: false,
        fixed: 'left' as const,
        align: 'center' as const,
        onHeaderCell: () =>
          ({
            'data-copy-skip': 'true',
            className: 'dbharbor-row-index-cell',
            style: { paddingInline: 5, overflow: 'hidden' },
          } as any),
        onCell: (record: Record<string, any>) =>
          record.__isFillerRow
            ? {
                'data-copy-skip': 'true',
                className: 'dbharbor-row-index-cell',
                style: { paddingInline: 5, overflow: 'hidden' },
                onClick: () => {
                  if (canWrite) materializeFillerRow();
                },
              }
            : ({
                'data-copy-skip': 'true',
                className: 'dbharbor-row-index-cell',
                style: { paddingInline: 5, overflow: 'hidden' },
              } as any),
        render: (_: any, record: Record<string, any>, idx: number) => {
          if (record.__isFillerRow) {
            if (canWrite && record.__isFirstFiller) {
              return (
                <span
                  data-copy-skip="true"
                  title={t('Click blank row to add new record')}
                  style={{
                    display: 'inline-flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                    opacity: 0.45,
                    fontSize: isMobile ? 13 : 11,
                  }}
                >
                  <PlusOutlined />
                </span>
              );
            }
            return null;
          }

          const rk = String(record.__rowKey);
          const isNew = rk.startsWith('new_');
          const rowNumber = (page - 1) * pageSize + (idx - topNewRows.length) + 1;

          if (isNew) {
            const btnDim = isMobile ? 28 : 20;
            return (
              <div
                data-copy-skip="true"
                style={{
                  display: 'inline-flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  gap: isMobile ? 4 : 3,
                  minHeight: btnDim,
                  whiteSpace: 'nowrap',
                }}
              >
                {!isMobile && (
                  <Tag color="green" style={{ margin: 0, fontSize: 10, lineHeight: '16px', padding: '0 3px' }}>
                    +
                  </Tag>
                )}
                <Tooltip title={t('Edit Row in Form / Inspector')}>
                  <span
                    role="button"
                    tabIndex={-1}
                    onClick={(e) => {
                      e.stopPropagation();
                      setInspectSearch('');
                      setInspectRow(record);
                    }}
                    style={{
                      width: btnDim,
                      height: btnDim,
                      borderRadius: 6,
                      display: 'inline-flex',
                      alignItems: 'center',
                      justifyContent: 'center',
                      cursor: 'pointer',
                      color: '#1677ff',
                      background: 'rgba(22, 119, 255, 0.12)',
                      flexShrink: 0,
                    }}
                  >
                    {isMobile ? (
                      <FormOutlined style={{ fontSize: 13 }} />
                    ) : (
                      <ExpandOutlined style={{ fontSize: 11 }} />
                    )}
                  </span>
                </Tooltip>
                <Tooltip title={t('Remove staged row')}>
                  <span
                    role="button"
                    tabIndex={-1}
                    onClick={(e) => {
                      e.stopPropagation();
                      setNewRows((prev) => prev.filter((r) => r.__rowKey !== rk));
                      setSelectedRowKeys((prev) => prev.filter((k) => String(k) !== rk));
                    }}
                    style={{
                      width: btnDim,
                      height: btnDim,
                      borderRadius: 6,
                      display: 'inline-flex',
                      alignItems: 'center',
                      justifyContent: 'center',
                      cursor: 'pointer',
                      color: '#ff4d4f',
                      background: 'rgba(255, 77, 79, 0.12)',
                      flexShrink: 0,
                    }}
                  >
                    <CloseOutlined style={{ fontSize: isMobile ? 12 : 10 }} />
                  </span>
                </Tooltip>
              </div>
            );
          }

          return (
            <div
              data-copy-skip="true"
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                justifyContent: 'center',
                gap: isMobile ? 4 : 4,
                minHeight: isMobile ? 28 : 20,
                whiteSpace: 'nowrap',
              }}
            >
              <span
                style={{
                  minWidth: isMobile ? 18 : 22,
                  textAlign: 'right',
                  fontSize: isMobile ? 11.5 : 11,
                  color: 'rgba(140, 140, 140, 0.95)',
                  fontVariantNumeric: 'tabular-nums',
                  display: 'inline-flex',
                  alignItems: 'center',
                  justifyContent: 'flex-end',
                }}
              >
                {rowNumber}
              </span>
              <Tooltip title={t('Inspect Row & JSON')}>
                <span
                  role="button"
                  tabIndex={-1}
                  onClick={(e) => {
                    e.stopPropagation();
                    setInspectSearch('');
                    setInspectRow(record);
                  }}
                  style={{
                    width: isMobile ? 26 : 18,
                    height: isMobile ? 26 : 18,
                    borderRadius: isMobile ? 6 : 4,
                    display: 'inline-flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                    cursor: 'pointer',
                    opacity: isMobile ? 0.78 : 0.68,
                    lineHeight: 1,
                    flexShrink: 0,
                  }}
                  className="dbharbor-row-inspect-btn"
                >
                  <ExpandOutlined style={{ fontSize: isMobile ? 12 : 11, display: 'block' }} />
                </span>
              </Tooltip>
            </div>
          );
        },
      },
      ...displayedColumns.map((col, colIdx) => {
        const isPK = primaryKeys.includes(col.name);
        const upperType = (col.type || '').toUpperCase();
        const minBodyWidth =
          upperType.includes('TEXT') || upperType.includes('JSON') || col.name.toLowerCase().includes('password')
            ? isMobile ? 150 : 200
            : upperType.includes('TIME') || upperType.includes('DATE')
              ? isMobile ? 135 : 160
              : isMobile ? 96 : 115;
        const smartWidth = calcSmartColumnWidth(col.name, {
          type: col.type,
          isPK,
          hasSorter: true,
          minBodyWidth,
        });

        return {
          key: col.name,
          title: (
            <span data-col-name={col.name} style={{ display: 'inline-flex', alignItems: 'center', gap: 4, whiteSpace: 'nowrap' }}>
              <span>{col.name}</span>
              {isPK && (
                <Tag color="gold" style={{ margin: 0, fontSize: 10, lineHeight: '14px', padding: '0 4px' }}>
                  PK
                </Tag>
              )}
              {col.type && (
                <Typography.Text type="secondary" style={{ fontSize: 10, fontWeight: 400 }}>
                  {col.type}
                </Typography.Text>
              )}
            </span>
          ),
          dataIndex: col.name,
          sorter: true,
          width: smartWidth,
          minWidth: 68,
          ellipsis: true,
          onCell: (record: Record<string, any>) => {
            if (record.__isFillerRow) {
              return {
                'data-copy-skip': 'true',
                onClick: () => {
                  if (canWrite) {
                    materializeFillerRow(col.name);
                  }
                },
                onDoubleClick: () => {
                  if (canWrite) {
                    materializeFillerRow(col.name);
                  }
                },
              } as any;
            }
            return {
              onTouchStart: isMobile
                ? (e: React.TouchEvent) => {
                    if (cellLongPressTimerRef.current !== null) {
                      window.clearTimeout(cellLongPressTimerRef.current);
                      cellLongPressTimerRef.current = null;
                    }
                    cellLongPressFiredRef.current = false;
                    const t0 = e.touches[0];
                    if (!t0) return;
                    const cx = t0.clientX;
                    const cy = t0.clientY;
                    cellTouchStartRef.current = { x: cx, y: cy };
                    cellLongPressTimerRef.current = window.setTimeout(() => {
                      cellLongPressTimerRef.current = null;
                      cellLongPressFiredRef.current = true;
                      updateFocusedCell({ rowKey: record.__rowKey, col: col.name });
                      setCtxMenu({
                        open: true,
                        x: cx,
                        y: cy,
                        record,
                        col,
                      });
                    }, 480);
                  }
                : undefined,
              onTouchMove: isMobile
                ? (e: React.TouchEvent) => {
                    if (cellLongPressTimerRef.current === null || !cellTouchStartRef.current) return;
                    const t0 = e.touches[0];
                    if (!t0) return;
                    if (Math.hypot(t0.clientX - cellTouchStartRef.current.x, t0.clientY - cellTouchStartRef.current.y) > 8) {
                      window.clearTimeout(cellLongPressTimerRef.current);
                      cellLongPressTimerRef.current = null;
                    }
                  }
                : undefined,
              onTouchEnd: isMobile
                ? () => {
                    if (cellLongPressTimerRef.current !== null) {
                      window.clearTimeout(cellLongPressTimerRef.current);
                      cellLongPressTimerRef.current = null;
                    }
                  }
                : undefined,
              onTouchCancel: isMobile
                ? () => {
                    if (cellLongPressTimerRef.current !== null) {
                      window.clearTimeout(cellLongPressTimerRef.current);
                      cellLongPressTimerRef.current = null;
                    }
                  }
                : undefined,
              onClick: (e: React.MouseEvent) => {
                if (cellLongPressFiredRef.current) {
                  cellLongPressFiredRef.current = false;
                  e.preventDefault();
                  e.stopPropagation();
                  return;
                }
                const rk = record.__rowKey;
                const fc = focusedCellRef.current;
                const ec = editingCellRef.current;
                if (
                  isMobile &&
                  canWrite &&
                  fc?.rowKey === rk &&
                  fc?.col === col.name &&
                  !(ec?.rowKey === rk && ec?.col === col.name) &&
                  !String(rk).startsWith('new_')
                ) {
                  editBackupRef.current = { rowKey: rk, col: col.name, prevVal: record[col.name] };
                  updateEditingCell({ rowKey: rk, col: col.name });
                  return;
                }
                updateFocusedCell({ rowKey: rk, col: col.name });
              },
              onContextMenu: (e: React.MouseEvent) => {
                e.preventDefault();
                if (isMobile && cellLongPressFiredRef.current) return;
                updateFocusedCell({ rowKey: record.__rowKey, col: col.name });
                setCtxMenu({
                  open: true,
                  x: e.clientX,
                  y: e.clientY,
                  record,
                  col,
                });
              },
            } as any;
          },
          render: (val: any, record: Record<string, any>) => {
            if (record.__isFillerRow) {
              if (record.__isFirstFiller && colIdx === 0) {
                if (canWrite) {
                  return (
                    <span
                      data-copy-skip="true"
                      style={{
                        fontSize: 11,
                        opacity: 0.42,
                        fontStyle: 'italic',
                        whiteSpace: 'nowrap',
                        pointerEvents: 'none',
                      }}
                    >
                      {isMobile ? `+ ${t('Tap to add row')}` : `+ ${t('Click blank row to add new record')}`}
                    </span>
                  );
                }
                if (combinedDataSource.length === 0) {
                  return (
                    <span data-copy-skip="true" style={{ fontSize: 11, opacity: 0.42 }}>
                      ({t('No rows')})
                    </span>
                  );
                }
              }
              return null;
            }

            return (
              <GridDataCell
                rk={String(record.__rowKey)}
                col={col}
                colIdx={colIdx}
                isPK={isPK}
                val={val}
                record={record}
                canWrite={canWrite}
                isMobile={isMobile}
                hub={cellHub}
                t={t}
              />
            );
          },
        };
      }),
    ],
    [
      displayedColumns,
      primaryKeys,
      page,
      pageSize,
      rowIndexColWidth,
      topNewRows.length,
      newRows.length,
      canWrite,
      isMobile,
      combinedDataSource.length,
      t,
      cellHub,
      updateEditingCell,
      updateFocusedCell,
      materializeFillerRow,
    ]
  );

  // Build Context Menu items for right-clicked cell & row
  const ctxMenuItems = useMemo<MenuProps['items']>(() => {
    const rec = ctxMenu.record;
    const col = ctxMenu.col;
    if (!rec || !col) return [];

    const cellVal = rec[col.name];
    const isNull = cellVal === null || cellVal === undefined;
    const multiSelected =
      selectedRowKeys.length > 1 && selectedRowKeys.includes(rec.__rowKey)
        ? combinedDataSource.filter((r) => selectedRowKeys.includes(r.__rowKey))
        : [rec];

    const items: MenuProps['items'] = [
      {
        key: 'copy-cell',
        icon: <CopyOutlined />,
        label: t('Copy Cell Value'),
        onClick: () => {
          writeTextToClipboard(formatRawCellText(cellVal));
          message.success(t('Copied to clipboard'));
        },
      },
      {
        key: 'copy-row-tsv',
        icon: <CopyOutlined />,
        label:
          multiSelected.length > 1
            ? t('Copy Selected ({count}) Rows (TSV)', { count: multiSelected.length })
            : t('Copy Full Row (TSV - Notepad / Excel)'),
        onClick: () => handleCopyRows('tsv', multiSelected),
      },
      {
        key: 'copy-row-header',
        icon: <CopyOutlined />,
        label: t('Copy with Column Headers (TSV)'),
        onClick: () => handleCopyRows('tsv_header', multiSelected),
      },
      isMobile
        ? {
            type: 'group' as const,
            key: 'copy-more',
            label: t('Copy Row As...'),
            children: [
              {
                key: 'copy-json',
                icon: <CopyOutlined />,
                label: t('Copy as JSON Array'),
                onClick: () => handleCopyRows('json', multiSelected),
              },
              {
                key: 'copy-sql',
                icon: <CopyOutlined />,
                label: t('Copy as INSERT SQL'),
                onClick: () => handleCopyRows('sql', multiSelected),
              },
              {
                key: 'copy-csv',
                icon: <CopyOutlined />,
                label: t('Copy as CSV'),
                onClick: () => handleCopyRows('csv', multiSelected),
              },
            ],
          }
        : {
            key: 'copy-more',
            icon: <CopyOutlined />,
            label: t('Copy Row As...'),
            children: [
              {
                key: 'copy-json',
                label: t('Copy as JSON Array'),
                onClick: () => handleCopyRows('json', multiSelected),
              },
              {
                key: 'copy-sql',
                label: t('Copy as INSERT SQL'),
                onClick: () => handleCopyRows('sql', multiSelected),
              },
              {
                key: 'copy-csv',
                label: t('Copy as CSV'),
                onClick: () => handleCopyRows('csv', multiSelected),
              },
            ],
          },
      { type: 'divider' },
      {
        key: 'filter-eq',
        icon: <FilterOutlined />,
        label: isNull
          ? `${t('Filter')}: ${col.name} IS NULL`
          : `${t('Filter')}: ${col.name} = ${String(cellVal).slice(0, 24)}`,
        onClick: () => {
          const qCol = quoteIdentForEngine(tab.dbType, col.name);
          applyQuickWhereClause(isNull ? `${qCol} IS NULL` : `${qCol} = ${escapeSQLVal(String(cellVal))}`);
        },
      },
      {
        key: 'filter-neq',
        icon: <FilterOutlined />,
        label: isNull
          ? `${t('Filter')}: ${col.name} IS NOT NULL`
          : `${t('Filter')}: ${col.name} != ${String(cellVal).slice(0, 24)}`,
        onClick: () => {
          const qCol = quoteIdentForEngine(tab.dbType, col.name);
          applyQuickWhereClause(
            isNull ? `${qCol} IS NOT NULL` : `${qCol} != ${escapeSQLVal(String(cellVal))}`
          );
        },
      },
      {
        key: 'inspect-row',
        icon: <ExpandOutlined />,
        label: t('Inspect Row & JSON'),
        onClick: () => setInspectRow(rec),
      },
    ];

    if (canWrite) {
      items.push(
        { type: 'divider' },
        {
          key: 'edit-cell',
          icon: <EditOutlined />,
          label: t('Edit Cell'),
          onClick: () => {
            editBackupRef.current = { rowKey: rec.__rowKey, col: col.name, prevVal: cellVal };
            updateEditingCell({ rowKey: rec.__rowKey, col: col.name });
          },
        },
        ...(col.nullable
          ? [
              {
                key: 'set-null',
                label: t('Set NULL'),
                onClick: () => handleCellChange(rec.__rowKey, col.name, null),
              },
            ]
          : []),
        ...(selectedRowKeys.length > 0
          ? [
              {
                key: 'batch-edit',
                icon: <EditOutlined />,
                label: t('Batch Edit Selected ({count})', { count: selectedRowKeys.length }),
                onClick: openBatchEditModal,
              },
            ]
          : []),
        {
          key: 'dup-row',
          icon: <PlusOutlined />,
          label:
            multiSelected.length > 1
              ? t('Duplicate Selected ({count}) Rows', { count: multiSelected.length })
              : t('Duplicate Row'),
          onClick: () => handleDuplicateRows(multiSelected),
        },
        {
          key: 'paste-new',
          icon: <SnippetsOutlined />,
          label: t('Paste as New Row(s) (Ctrl+V)'),
          onClick: () => triggerToolbarPaste('new_rows'),
        },
        {
          key: 'paste-cells',
          icon: <SnippetsOutlined />,
          label: t('Paste into Cells Here'),
          onClick: () => triggerToolbarPaste('overwrite_cells'),
        },
        { type: 'divider' },
        {
          key: 'del-row',
          danger: true,
          icon: <DeleteOutlined />,
          label: t('Delete Row'),
          onClick: () => {
            Modal.confirm({
              title: t('Delete this row permanently?'),
              okButtonProps: { danger: true },
              onOk: () => handleDeleteSingleRow(rec),
            });
          },
        }
      );
    }

    return items;
  }, [
    ctxMenu,
    selectedRowKeys,
    combinedDataSource,
    canWrite,
    isMobile,
    t,
    handleCopyRows,
    handleCellChange,
    handleDuplicateRows,
    openBatchEditModal,
  ]);

  const filteredColumnPickerList = useMemo(() => {
    const q = colFilterSearch.trim().toLowerCase();
    return q ? visibleColumns.filter((c) => c.name.toLowerCase().includes(q)) : visibleColumns;
  }, [visibleColumns, colFilterSearch]);

  const columnVisibilityPopoverContent = (
    <div style={{ width: 'min(230px, calc(100vw - 48px))' }}>
      <div style={{ display: 'flex', gap: 6, marginBottom: 6 }}>
        <Input
          size="small"
          allowClear
          prefix={<SearchOutlined />}
          placeholder={t('Filter columns...')}
          value={colFilterSearch}
          onChange={(e) => setColFilterSearch(e.target.value)}
        />
        {hiddenCols.length > 0 && (
          <Button size="small" type="link" style={{ padding: '0 4px' }} onClick={() => setHiddenCols([])}>
            {t('Reset')}
          </Button>
        )}
      </div>
      <div style={{ maxHeight: 240, overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 4 }}>
        {filteredColumnPickerList.map((c) => {
          const checked = !hiddenCols.includes(c.name);
          return (
            <Checkbox
              key={c.name}
              checked={checked}
              onChange={(e) => {
                if (e.target.checked) {
                  setHiddenCols((prev) => prev.filter((x) => x !== c.name));
                } else if (displayedColumns.length > 1) {
                  setHiddenCols((prev) => [...prev, c.name]);
                }
              }}
            >
              <Typography.Text style={{ fontSize: 12 }}>{c.name}</Typography.Text>
            </Checkbox>
          );
        })}
      </div>
    </div>
  );

  const copyMenuItems: MenuProps['items'] = [
    {
      key: 'tsv',
      label: t('Copy Rows (TSV - Notepad / Excel)'),
      onClick: () => handleCopyRows('tsv'),
    },
    {
      key: 'tsv_header',
      label: t('Copy with Column Headers (TSV)'),
      onClick: () => handleCopyRows('tsv_header'),
    },
    { key: 'csv', label: t('Copy as CSV'), onClick: () => handleCopyRows('csv') },
    { key: 'json', label: t('Copy as JSON Array'), onClick: () => handleCopyRows('json') },
    { key: 'sql', label: t('Copy as INSERT SQL'), onClick: () => handleCopyRows('sql') },
  ];

  const hasActiveFilterOrSort = Boolean(appliedFilter.trim()) || sortSpec.length > 0;
  const selectedBatchColMeta = visibleColumns.find((c) => c.name === batchCol);
  const filteredInspectColumns = useMemo(() => {
    const q = inspectSearch.trim().toLowerCase();
    return q ? visibleColumns.filter((c) => c.name.toLowerCase().includes(q)) : visibleColumns;
  }, [visibleColumns, inspectSearch]);

  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        height: '100%',
        minWidth: 0,
        minHeight: 0,
        padding: isMobile ? '4px 8px' : '8px 14px',
        overflow: 'hidden',
      }}
    >
      {/* Single Floating Anchor for Cell/Row Right-Click Context Menu */}
      {ctxMenu.open && (
        <Dropdown
          open={ctxMenu.open}
          onOpenChange={(open) => {
            if (!open) setCtxMenu((prev) => ({ ...prev, open: false }));
          }}
          menu={{
            items: ctxMenuItems,
            onClick: () => setCtxMenu((prev) => ({ ...prev, open: false })),
          }}
          trigger={['click']}
        >
          <span
            style={{
              position: 'fixed',
              left: ctxMenu.x,
              top: ctxMenu.y,
              width: 1,
              height: 1,
              pointerEvents: 'none',
              zIndex: 1000,
            }}
          />
        </Dropdown>
      )}

      <div style={{ flexShrink: 0 }}>
        <ContextNavBar tab={tab} activeSection="data" onOpenTransfer={onOpenTransfer} />
      </div>

      {/* Top Action & WHERE Filter Toolbar */}
      {isMobile ? (
        <div
          className="dbharbor-no-scrollbar"
          style={{
            flexShrink: 0,
            display: 'flex',
            justifyContent: 'space-between',
            alignItems: 'center',
            gap: 6,
            marginBottom: 6,
            minWidth: 0,
            overflowX: 'auto',
          }}
        >
          <Space size={6} style={{ flexShrink: 0 }}>
            {canWrite && (
              <Space.Compact size="middle">
                <Button
                  type="primary"
                  style={{ height: 34, padding: '0 10px' }}
                  icon={<PlusOutlined />}
                  onClick={openFormInsertModal}
                >
                  {t('Add')}
                </Button>
                <Dropdown
                  trigger={['click']}
                  menu={{
                    items: [
                      {
                        key: 'form',
                        icon: <FormOutlined />,
                        label: t('Insert Form'),
                        onClick: openFormInsertModal,
                      },
                      {
                        key: 'inline',
                        icon: <PlusOutlined />,
                        label: t('Add Row Inline'),
                        onClick: handleAddRowInline,
                      },
                       {
                         key: 'paste',
                         icon: <SnippetsOutlined />,
                         label: t('Paste Rows'),
                         onClick: () =>
                           hasFocusedCell ? openPasteDialog('overwrite_cells') : triggerToolbarPaste('new_rows'),
                       },
                       ...(hasFocusedCell
                         ? [
                             {
                               key: 'paste_cells',
                               icon: <SnippetsOutlined />,
                               label: t('Paste into Cells Here'),
                               onClick: () => triggerToolbarPaste('overwrite_cells'),
                             },
                           ]
                         : []),
                       {
                         key: 'paste_modal',
                         icon: <SnippetsOutlined />,
                         label: t('Paste...'),
                         onClick: () => openPasteDialog(hasFocusedCell ? 'overwrite_cells' : 'new_rows'),
                       },
                    ],
                  }}
                >
                  <Button type="primary" style={{ height: 34, width: 28, padding: 0 }} icon={<MoreOutlined />} />
                </Dropdown>
              </Space.Compact>
            )}
            <Badge dot={Boolean(appliedFilter.trim())} offset={[-2, 2]}>
              <Button
                style={{ height: 34, padding: '0 10px' }}
                type={mobileFilterOpen || Boolean(appliedFilter.trim()) ? 'primary' : 'default'}
                ghost={mobileFilterOpen || Boolean(appliedFilter.trim())}
                icon={<FilterOutlined />}
                onClick={() => setMobileFilterOpen((prev) => !prev)}
              >
                {t('Filter')}
              </Button>
            </Badge>
            {visibleColumns.length > 0 && (
              <Popover trigger="click" placement="bottom" content={columnVisibilityPopoverContent}>
                <Button
                  style={{
                    height: 34,
                    width: hiddenCols.length > 0 ? undefined : 34,
                    padding: hiddenCols.length > 0 ? '0 8px' : 0,
                  }}
                  icon={<SettingOutlined />}
                  title={t('Columns')}
                >
                  {hiddenCols.length > 0 ? `${displayedColumns.length}/${visibleColumns.length}` : null}
                </Button>
              </Popover>
            )}
          </Space>

          <Space size={6} style={{ flexShrink: 0 }}>
            {hasActiveFilterOrSort && (
              <Button
                style={{ width: 34, height: 34, padding: 0 }}
                icon={<DownloadOutlined />}
                onClick={handleExportFiltered}
                title={t('Export Filtered')}
              />
            )}
            {combinedDataSource.length > 0 && (
              <Dropdown trigger={['click']} menu={{ items: copyMenuItems }} placement="bottomRight">
                <Button style={{ width: 34, height: 34, padding: 0 }} icon={<CopyOutlined />} title={t('Copy')} />
              </Dropdown>
            )}
            <Button
              style={{ width: 34, height: 34, padding: 0 }}
              icon={<ReloadOutlined />}
              onClick={() => loadData()}
              title={t('Refresh Data')}
            />
          </Space>
        </div>
      ) : (
        <div
          style={{
            flexShrink: 0,
            display: 'flex',
            justifyContent: 'space-between',
            alignItems: 'center',
            gap: 8,
            marginBottom: 8,
            flexWrap: 'wrap',
            minWidth: 0,
          }}
        >
          <Space wrap size={6} style={{ minWidth: 0, maxWidth: '100%' }}>
            {canWrite && (
              <>
                <Button size="small" type="primary" icon={<PlusOutlined />} onClick={handleAddRowInline}>
                  {t('Add Row Inline')}
                </Button>
                <Button size="small" icon={<FormOutlined />} onClick={openFormInsertModal}>
                  {t('Insert Form')}
                </Button>
                <Tooltip title={t('Paste single or multiple rows from Notepad / Excel / JSON (Ctrl+V)')}>
                  <Button size="small" icon={<SnippetsOutlined />} onClick={() => triggerToolbarPaste('new_rows')}>
                    {t('Paste Rows')}
                  </Button>
                </Tooltip>
              </>
            )}

            {combinedDataSource.length > 0 && (
              <Dropdown.Button
                trigger={['click']}
                size="small"
                icon={<CopyOutlined />}
                onClick={() => handleCopyRows('tsv')}
                menu={{ items: copyMenuItems }}
              >
                {selectedRowKeys.length > 0
                  ? t('Copy Selected ({count})', { count: selectedRowKeys.length })
                  : t('Copy')}
              </Dropdown.Button>
            )}

            <Button
              size="small"
              type={showSearchBuilder ? 'primary' : 'default'}
              ghost={showSearchBuilder}
              icon={<SearchOutlined />}
              onClick={() => {
                const next = !showSearchBuilder;
                setShowSearchBuilder(next);
                if (next && searchConds.length === 0 && visibleColumns[0]) {
                  setSearchConds([{ id: String(Date.now()), column: visibleColumns[0].name, op: '=', value: '' }]);
                }
              }}
            >
              {t('Visual Search')}
            </Button>

            {visibleColumns.length > 0 && (
              <Popover trigger="click" placement="bottom" content={columnVisibilityPopoverContent}>
                <Button size="small" icon={<SettingOutlined />}>
                  {hiddenCols.length > 0
                    ? `${t('Visible Columns')} (${displayedColumns.length}/${visibleColumns.length})`
                    : t('Visible Columns')}
                </Button>
              </Popover>
            )}

            {hasActiveFilterOrSort && (
              <Button size="small" icon={<DownloadOutlined />} onClick={handleExportFiltered}>
                {t('Export Filtered')}
              </Button>
            )}

            {canWrite && selectedRowKeys.length > 0 && (
              <>
                <Button size="small" icon={<EditOutlined />} onClick={openBatchEditModal}>
                  {t('Batch Edit ({count})', { count: selectedRowKeys.length })}
                </Button>
                <Button
                  size="small"
                  icon={<PlusOutlined />}
                  onClick={() => {
                    const chosen = combinedDataSource.filter((r) => selectedRowKeys.includes(r.__rowKey));
                    handleDuplicateRows(chosen);
                  }}
                >
                  {t('Duplicate Selected ({count})', { count: selectedRowKeys.length })}
                </Button>

                <Popconfirm
                  title={t('Delete {count} selected row(s)?', { count: selectedRowKeys.length })}
                  onConfirm={() => {
                    const onlyNew = selectedRowKeys.every((k) => String(k).startsWith('new_'));
                    if (onlyNew) {
                      setNewRows((prev) => prev.filter((r) => !selectedRowKeys.includes(r.__rowKey)));
                      setSelectedRowKeys([]);
                    } else {
                      handleCommitChanges(true);
                    }
                  }}
                >
                  <Button size="small" danger icon={<DeleteOutlined />}>
                    {t('Delete Selected ({count})', { count: selectedRowKeys.length })}
                  </Button>
                </Popconfirm>
              </>
            )}

            {canWrite && pendingChangeCount > 0 && (
              <>
                <Badge count={pendingChangeCount} size="small">
                  <Button
                    size="small"
                    type="primary"
                    icon={<CheckOutlined />}
                    loading={saving}
                    onClick={() => handleCommitChanges(false)}
                  >
                    {t('Save Changes')}
                  </Button>
                </Badge>
                <Button size="small" icon={<EyeOutlined />} onClick={handlePreviewSQL}>
                  {t('Preview SQL')}
                </Button>
                <Button
                  size="small"
                  icon={<CloseOutlined />}
                  onClick={() => loadData(page, pageSize, appliedFilter, sortSpec)}
                >
                  {t('Discard')}
                </Button>
              </>
            )}

            <Button size="small" icon={<ReloadOutlined />} onClick={() => loadData()} title={t('Refresh Data')} />
          </Space>

          <Space.Compact style={{ minWidth: 0, flex: '1 1 240px', maxWidth: 'min(440px, 100%)' }}>
            <Input
              size="small"
              style={{ minWidth: 0 }}
              addonBefore={
                <Space size={4}>
                  <FilterOutlined />
                  <span>WHERE</span>
                </Space>
              }
              placeholder={t("e.g. id > 10 AND status = 'PAID' (Enter to filter)")}
              value={whereInput}
              allowClear
              onChange={(e) => setWhereInput(e.target.value)}
              onPressEnter={() => {
                setAppliedFilter(whereInput);
                setPage(1);
                loadData(1, pageSize, whereInput, sortSpec);
              }}
            />
            <Button
              size="small"
              type="primary"
              style={{ flexShrink: 0 }}
              onClick={() => {
                setAppliedFilter(whereInput);
                setPage(1);
                loadData(1, pageSize, whereInput, sortSpec);
              }}
            >
              {t('Filter')}
            </Button>
          </Space.Compact>
        </div>
      )}

      {/* Mobile Collapsible WHERE & Visual Search Panel */}
      {isMobile && mobileFilterOpen && (
        <Card size="small" style={{ marginBottom: 6, flexShrink: 0 }} styles={{ body: { padding: '6px 8px' } }}>
          <Space direction="vertical" size={6} style={{ width: '100%' }}>
            <Space.Compact style={{ width: '100%' }}>
              <Input
                size="small"
                addonBefore="WHERE"
                placeholder="id > 10"
                value={whereInput}
                allowClear
                onChange={(e) => setWhereInput(e.target.value)}
                onPressEnter={() => {
                  setAppliedFilter(whereInput);
                  setPage(1);
                  loadData(1, pageSize, whereInput, sortSpec);
                }}
              />
              <Button
                size="small"
                type="primary"
                onClick={() => {
                  setAppliedFilter(whereInput);
                  setPage(1);
                  loadData(1, pageSize, whereInput, sortSpec);
                }}
              >
                {t('Filter')}
              </Button>
            </Space.Compact>
            <Space size={6} wrap>
              <Button
                size="small"
                type={showSearchBuilder ? 'primary' : 'default'}
                ghost={showSearchBuilder}
                icon={<SearchOutlined />}
                onClick={() => {
                  const next = !showSearchBuilder;
                  setShowSearchBuilder(next);
                  if (next && searchConds.length === 0 && visibleColumns[0]) {
                    setSearchConds([{ id: String(Date.now()), column: visibleColumns[0].name, op: '=', value: '' }]);
                  }
                }}
              >
                {t('Visual Search')}
              </Button>
              {appliedFilter && (
                <Button
                  size="small"
                  onClick={() => {
                    setWhereInput('');
                    setAppliedFilter('');
                    setSearchConds([]);
                    setPage(1);
                    loadData(1, pageSize, '', sortSpec);
                  }}
                >
                  {t('Reset')}
                </Button>
              )}
            </Space>
          </Space>
        </Card>
      )}

      {/* Visual Multi-Condition Search Builder */}
      {showSearchBuilder && (
        <Card size="small" style={{ marginBottom: 6, flexShrink: 0 }} styles={{ body: { padding: '6px 10px' } }}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            <div
              style={{
                maxHeight: 'min(128px, 18vh)',
                overflowY: 'auto',
                display: 'flex',
                flexDirection: 'column',
                gap: 6,
              }}
            >
              {searchConds.map((cond, idx) => (
                <Space key={cond.id} wrap size={6}>
                  {idx === 0 ? (
                    <Select
                      size="small"
                      value={searchLogic}
                      onChange={setSearchLogic}
                      style={{ width: isMobile ? 105 : 120 }}
                      options={[
                        { value: 'AND', label: t('Match ALL') },
                        { value: 'OR', label: t('Match ANY') },
                      ]}
                    />
                  ) : (
                    <Tag style={{ width: 64, textAlign: 'center', margin: 0 }}>{searchLogic}</Tag>
                  )}
                  <Select
                    size="small"
                    style={{ width: isMobile ? 130 : 165 }}
                    value={cond.column}
                    onChange={(v) =>
                      setSearchConds((prev) => prev.map((x) => (x.id === cond.id ? { ...x, column: v } : x)))
                    }
                    options={visibleColumns.map((c) => ({ value: c.name, label: c.name }))}
                  />
                  <Select
                    size="small"
                    style={{ width: isMobile ? 140 : 175 }}
                    value={cond.op}
                    onChange={(v) =>
                      setSearchConds((prev) => prev.map((x) => (x.id === cond.id ? { ...x, op: v } : x)))
                    }
                    options={SEARCH_OPS.map((op) => ({ value: op.value, label: t(op.label) }))}
                  />
                  {cond.op !== 'IS NULL' && cond.op !== 'IS NOT NULL' && (
                    <Input
                      size="small"
                      style={{ width: isMobile ? 140 : 200 }}
                      placeholder={cond.op === 'LIKE' ? '%keyword%' : 'value'}
                      value={cond.value}
                      onChange={(e) =>
                        setSearchConds((prev) =>
                          prev.map((x) => (x.id === cond.id ? { ...x, value: e.target.value } : x))
                        )
                      }
                      onPressEnter={applyVisualSearch}
                    />
                  )}
                  <Button
                    size="small"
                    type="text"
                    danger
                    icon={<DeleteOutlined />}
                    onClick={() => setSearchConds((prev) => prev.filter((x) => x.id !== cond.id))}
                  />
                </Space>
              ))}
            </div>
            <Space size={8} wrap>
              <Button
                size="small"
                type="dashed"
                icon={<PlusOutlined />}
                onClick={() =>
                  setSearchConds((prev) => [
                    ...prev,
                    { id: String(Date.now() + Math.random()), column: visibleColumns[0]?.name || '', op: '=', value: '' },
                  ])
                }
              >
                {t('Add Condition')}
              </Button>
              <Button size="small" type="primary" icon={<SearchOutlined />} onClick={applyVisualSearch}>
                {t('Run Search')}
              </Button>
              <Button
                size="small"
                onClick={() => {
                  setSearchConds([]);
                  setWhereInput('');
                  setAppliedFilter('');
                  loadData(1, pageSize, '', sortSpec);
                }}
              >
                {t('Reset')}
              </Button>
            </Space>
          </div>
        </Card>
      )}

      {/* Data Grid */}
      <div ref={gridWrapRef} style={{ flex: 1, minHeight: 0, minWidth: 0, overflow: 'hidden' }}>
        <ResizableTable<Record<string, any>>
          rowKey="__rowKey"
          size="small"
          bordered
          loading={loading}
          fullHeightBody={gridBodyHeight}
          wrapClassName={isMobile ? 'dbharbor-mobile-table' : undefined}
          dataSource={gridDataSource}
          pagination={false}
          onFallbackCopy={handleFallbackCopy}
          scroll={{ x: 'max-content', y: gridBodyHeight }}
          onRow={(record) =>
            record.__isFillerRow
              ? ({
                  className: canWrite
                    ? 'dbharbor-filler-row dbharbor-filler-row-writable'
                    : 'dbharbor-filler-row',
                  'data-copy-skip': 'true',
                } as any)
              : ({
                  className: String(record.__rowKey || '').startsWith('new_')
                    ? 'dbharbor-staged-new-row'
                    : undefined,
                  'data-row-key': record.__rowKey,
                } as any)
          }
          rowSelection={{
            selectedRowKeys,
            onChange: (keys) =>
              setSelectedRowKeys(keys.filter((k) => !String(k).startsWith('__filler_'))),
            getCheckboxProps: (record) =>
              record.__isFillerRow ? { disabled: true, style: { display: 'none' } } : {},
            columnWidth: isMobile ? 38 : 40,
            fixed: true,
          }}
          onChange={(_, __, sorter: any) => {
            const nextSorts: { column: string; direction: string }[] = [];
            if (sorter?.field && sorter?.order) {
              nextSorts.push({
                column: String(sorter.field),
                direction: sorter.order === 'descend' ? 'DESC' : 'ASC',
              });
            }
            setSortSpec(nextSorts);
            loadData(page, pageSize, appliedFilter, nextSorts);
          }}
          columns={tableColumns}
        />
      </div>

      {/* Mobile Sticky Bottom Bar for Staged Mutations & Row Selection (36px touch targets + anti-mis-tap isolation) */}
      {isMobile && ((canWrite && pendingChangeCount > 0) || selectedRowKeys.length > 0) && (
        <div
          className="dbharbor-no-scrollbar"
          style={{
            flexShrink: 0,
            marginTop: 6,
            padding: '6px 8px',
            borderRadius: 8,
            background: 'rgba(22, 119, 255, 0.08)',
            border: '1px solid rgba(22, 119, 255, 0.24)',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            gap: 8,
            overflowX: 'auto',
          }}
        >
          <Space size={6} style={{ flexShrink: 0 }}>
            {canWrite && pendingChangeCount > 0 && (
              <>
                <Button
                  type="primary"
                  style={{ height: 36, padding: '0 12px', fontWeight: 600 }}
                  icon={<CheckOutlined />}
                  loading={saving}
                  onClick={() => handleCommitChanges(false)}
                >
                  {`${t('Save Changes')} (${pendingChangeCount})`}
                </Button>
                <Button style={{ height: 36, padding: '0 10px' }} icon={<EyeOutlined />} onClick={handlePreviewSQL}>
                  SQL
                </Button>
                <Popconfirm
                  title={t('Discard all unsaved row changes?')}
                  onConfirm={() => loadData(page, pageSize, appliedFilter, sortSpec)}
                >
                  <Button style={{ height: 36, padding: '0 10px' }} icon={<CloseOutlined />}>
                    {t('Discard')}
                  </Button>
                </Popconfirm>
              </>
            )}
            {selectedRowKeys.length > 0 && (
              <>
                <Dropdown trigger={['click']} placement="top" menu={{ items: copyMenuItems }}>
                  <Button style={{ height: 36, padding: '0 10px' }} icon={<CopyOutlined />}>
                    {`${t('Copy')} (${selectedRowKeys.length})`}
                  </Button>
                </Dropdown>
                {canWrite && (
                  <>
                    <Button style={{ height: 36, padding: '0 10px' }} icon={<EditOutlined />} onClick={openBatchEditModal}>
                      {t('Batch Edit')}
                    </Button>
                    <Button
                      style={{ height: 36, padding: '0 10px' }}
                      icon={<PlusOutlined />}
                      onClick={() =>
                        handleDuplicateRows(combinedDataSource.filter((r) => selectedRowKeys.includes(r.__rowKey)))
                      }
                    >
                      {t('Duplicate')}
                    </Button>
                  </>
                )}
              </>
            )}
          </Space>

          {canWrite && selectedRowKeys.length > 0 && (
            <div style={{ flexShrink: 0, marginLeft: 'auto' }}>
              <Popconfirm
                placement="topRight"
                title={t('Delete {count} selected row(s)?', { count: selectedRowKeys.length })}
                onConfirm={() => {
                  const onlyNew = selectedRowKeys.every((k) => String(k).startsWith('new_'));
                  if (onlyNew) {
                    setNewRows((prev) => prev.filter((r) => !selectedRowKeys.includes(r.__rowKey)));
                    setSelectedRowKeys([]);
                  } else {
                    handleCommitChanges(true);
                  }
                }}
              >
                <Button danger style={{ height: 36, minWidth: 40, padding: '0 10px' }} icon={<DeleteOutlined />} />
              </Popconfirm>
            </div>
          )}
        </div>
      )}

      {/* Footer Status & Pagination */}
      <div
        style={{
          flexShrink: 0,
          paddingTop: isMobile ? 4 : 6,
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          flexWrap: 'nowrap',
          gap: 8,
          minWidth: 0,
          borderTop: '1px solid rgba(128,128,128,0.15)',
        }}
      >
        {isMobile ? (
          <Typography.Text type="secondary" style={{ fontSize: 11, whiteSpace: 'nowrap' }}>
            {t('Total {count} rows', { count: total.toLocaleString() })}
          </Typography.Text>
        ) : (
          <Typography.Text
            type="secondary"
            ellipsis
            className="table-footer-tip"
            style={{ fontSize: 12, flex: 1, minWidth: 160 }}
          >
            {t('Tip: Ctrl+C copies clean TSV rows for Notepad/Excel; Ctrl+V pastes single or multiple rows; right-click any cell for quick actions.')}
          </Typography.Text>
        )}
        <div style={{ flexShrink: 0, marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 6 }}>
          <Pagination
            size="small"
            simple={isMobile}
            current={page}
            pageSize={pageSize}
            total={total}
            showSizeChanger={!isMobile}
            pageSizeOptions={[50, 100, 200, 500]}
            showTotal={isMobile ? undefined : (cnt) => t('Total {count} rows', { count: cnt.toLocaleString() })}
            onChange={(p, ps) => {
              setPage(p);
              setPageSize(ps);
              loadData(p, ps, appliedFilter, sortSpec);
            }}
          />
          {isMobile && (
            <Select
              size="small"
              popupMatchSelectWidth={false}
              value={pageSize}
              style={{ height: 28 }}
              options={[50, 100, 200, 500].map((n) => ({
                value: n,
                label: `${n} / ${t('page')}`,
              }))}
              onChange={(ps) => {
                setPage(1);
                setPageSize(ps);
                loadData(1, ps, appliedFilter, sortSpec);
              }}
            />
          )}
        </div>
      </div>

      {/* Batch Edit Selected Rows Modal */}
      <Modal
        open={batchModalOpen}
        title={t('Batch Edit Selected ({count})', { count: selectedRowKeys.length })}
        width="min(480px, 95vw)"
        onCancel={() => setBatchModalOpen(false)}
        onOk={handleApplyBatchEdit}
        okText={t('Apply to Selected Rows')}
      >
        <Form layout="vertical" autoComplete="off">
          <Form.Item label={t('Target Column')} style={{ marginBottom: 12 }}>
            <Select
              showSearch
              value={batchCol}
              onChange={(val) => {
                setBatchCol(val);
                const colMeta = visibleColumns.find((c) => c.name === val);
                if (!colMeta?.nullable) setBatchSetNull(false);
              }}
              options={visibleColumns.map((c) => ({
                value: c.name,
                label: `${c.name}${c.type ? ` (${c.type})` : ''}`,
              }))}
            />
          </Form.Item>
          {selectedBatchColMeta?.nullable && (
            <Form.Item style={{ marginBottom: 10 }}>
              <Checkbox checked={batchSetNull} onChange={(e) => setBatchSetNull(e.target.checked)}>
                {t('Set NULL')}
              </Checkbox>
            </Form.Item>
          )}
          <Form.Item label={t('New Column Value')} style={{ marginBottom: 4 }}>
            <Input.TextArea
              autoSize={{ minRows: 2, maxRows: 6 }}
              disabled={batchSetNull}
              value={batchSetNull ? 'NULL' : batchVal}
              onChange={(e) => setBatchVal(e.target.value)}
              placeholder={batchSetNull ? 'NULL' : t('Enter new value to stage across selected rows')}
              style={{ fontFamily: 'monospace', fontSize: 12 }}
            />
          </Form.Item>
        </Form>
      </Modal>

      {/* Paste Single / Multiple Rows Modal */}
      <Modal
        open={pasteModalOpen}
        title={t('Paste Single or Multiple Rows (TSV / Excel / JSON)')}
        width="min(680px, 95vw)"
        onCancel={() => setPasteModalOpen(false)}
        onOk={() => {
          if (applyClipboardPaste(pasteText, pasteMode, focusedCellRef.current)) {
            setPasteModalOpen(false);
            setPasteText('');
          }
        }}
        okText={t('Apply Paste')}
      >
        <Space direction="vertical" size={10} style={{ width: '100%' }}>
          <Radio.Group
            value={pasteMode}
            onChange={(e) => setPasteMode(e.target.value)}
            style={{ display: 'flex', flexDirection: isMobile ? 'column' : 'row', gap: 6 }}
          >
            <Radio value="new_rows">{t('Insert as New Staged Row(s)')}</Radio>
            <Radio value="overwrite_cells" disabled={!hasFocusedCell}>
              {t('Overwrite Starting from Active Cell')}
            </Radio>
          </Radio.Group>
          <Input.TextArea
            rows={10}
            autoFocus={!isMobile}
            value={pasteText}
            onChange={(e) => setPasteText(e.target.value)}
            placeholder={t('Paste tab-separated rows from Notepad / Excel or a JSON array here...')}
            style={{ fontFamily: 'JetBrains Mono, Consolas, monospace', fontSize: 12 }}
          />
        </Space>
      </Modal>

      {/* Form-Based Insert Row Modal */}
      <Modal
        open={insertModalOpen}
        title={`${t('Insert Form')} - ${tab.table}`}
        width="min(620px, 95vw)"
        onCancel={() => setInsertModalOpen(false)}
        onOk={handleSubmitFormInsert}
        okText={t('Insert Row')}
        confirmLoading={saving}
      >
        <Form form={insertForm} layout="vertical" autoComplete="off">
          {visibleColumns.map((col) => (
            <Form.Item
              key={col.name}
              label={
                <Space wrap>
                  <Typography.Text strong>{col.name}</Typography.Text>
                  <Tag color="blue" style={{ fontSize: 11 }}>
                    {col.type || 'TEXT'}
                  </Tag>
                  {primaryKeys.includes(col.name) && <Tag color="gold">PK</Tag>}
                  {col.nullable && (
                    <Checkbox
                      checked={Boolean(nullFlags[col.name])}
                      onChange={(e) => setNullFlags((prev) => ({ ...prev, [col.name]: e.target.checked }))}
                    >
                      {t('Set NULL')}
                    </Checkbox>
                  )}
                </Space>
              }
              name={col.name}
              style={{ marginBottom: 10 }}
            >
              <Input
                disabled={Boolean(nullFlags[col.name])}
                placeholder={
                  nullFlags[col.name]
                    ? 'NULL'
                    : primaryKeys.includes(col.name)
                      ? t('Leave blank for auto-increment PK')
                      : t('Leave blank to use column default')
                }
              />
            </Form.Item>
          ))}
        </Form>
      </Modal>

      {/* SQL Mutation Preview Modal */}
      <Modal
        open={previewModalOpen}
        title={t('Pending Data Mutation SQL Preview')}
        width="min(700px, 95vw)"
        onCancel={() => setPreviewModalOpen(false)}
        onOk={() => handleCommitChanges(selectedRowKeys.length > 0)}
        okText={t('Confirm & Execute SQL')}
        confirmLoading={saving}
      >
        <pre
          style={{
            maxHeight: 360,
            overflow: 'auto',
            padding: 12,
            borderRadius: 8,
            background: 'rgba(128,128,128,0.1)',
            fontFamily: 'monospace',
            fontSize: 12,
          }}
        >
          {previewSQLs.length > 0 ? previewSQLs.join(';\n\n') + ';' : t('-- No pending SQL changes')}
        </pre>
      </Modal>

      {/* Row Detail Inspector / Mobile Bottom-Sheet Editor Drawer */}
      <Drawer
        open={Boolean(inspectRow)}
        destroyOnClose
        placement={isMobile ? 'bottom' : 'right'}
        height={isMobile ? '85dvh' : undefined}
        width={isMobile ? '100%' : 'min(480px, 95vw)'}
        styles={{ body: { padding: isMobile ? 12 : 24 } }}
        title={`${t('Row Inspector')} - ${tab.table}`}
        onClose={() => setInspectRow(null)}
        extra={
          inspectRow && (
            <Space size={6}>
              <Button
                size="small"
                icon={<CopyOutlined />}
                onClick={() => handleCopyRows('tsv', [inspectRow])}
              >
                TSV
              </Button>
              <Button
                size="small"
                icon={<CopyOutlined />}
                onClick={() => {
                  const copyObj: Record<string, any> = {};
                  Object.entries(inspectRow).forEach(([k, v]) => {
                    if (!k.startsWith('__')) copyObj[k] = v;
                  });
                  writeTextToClipboard(JSON.stringify(copyObj, null, 2));
                  message.success(t('Row JSON copied'));
                }}
              >
                JSON
              </Button>
            </Space>
          )
        }
        footer={
          inspectRow && canWrite ? (
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
              <Space size={8}>
                {!String(inspectRow.__rowKey || '').startsWith('new_') && (
                  <Button
                    size={isMobile ? 'middle' : 'small'}
                    icon={<PlusOutlined />}
                    onClick={() => {
                      handleDuplicateRows([inspectRow]);
                      setInspectRow(null);
                    }}
                  >
                    {t('Duplicate Row')}
                  </Button>
                )}
                {String(inspectRow.__rowKey || '').startsWith('new_') ? (
                  <Button
                    size={isMobile ? 'middle' : 'small'}
                    danger
                    icon={<DeleteOutlined />}
                    onClick={() => {
                      const targetKey = inspectRow.__rowKey;
                      setInspectRow(null);
                      setNewRows((prev) => prev.filter((r) => r.__rowKey !== targetKey));
                    }}
                  >
                    {t('Discard')}
                  </Button>
                ) : (
                  <Popconfirm
                    title={t('Delete this row permanently?')}
                    onConfirm={() => {
                      const target = inspectRow;
                      setInspectRow(null);
                      handleDeleteSingleRow(target);
                    }}
                  >
                    <Button size={isMobile ? 'middle' : 'small'} danger icon={<DeleteOutlined />}>
                      {t('Delete Row')}
                    </Button>
                  </Popconfirm>
                )}
              </Space>
              {pendingChangeCount > 0 && (
                <Button
                  size={isMobile ? 'middle' : 'small'}
                  type="primary"
                  icon={<CheckOutlined />}
                  loading={saving}
                  onClick={async () => {
                    await handleCommitChanges(false);
                    setInspectRow(null);
                  }}
                >
                  {t('Save Changes')}
                </Button>
              )}
            </div>
          ) : null
        }
      >
        {inspectRow && (
          <>
            {visibleColumns.length > 6 && (
              <Input
                size="small"
                allowClear
                prefix={<SearchOutlined />}
                placeholder={t('Filter columns...')}
                value={inspectSearch}
                onChange={(e) => setInspectSearch(e.target.value)}
                style={{ marginBottom: 10 }}
              />
            )}
            {filteredInspectColumns.map((c) => {
              const rawVal = inspectRow[c.name];
              const isNullVal = rawVal === null;
              const strVal = isNullVal ? '' : String(rawVal ?? '');
              return (
                <div key={c.name} style={{ marginBottom: 12 }}>
                  <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 6 }}>
                    <Typography.Text strong style={{ fontSize: 12 }}>
                      {c.name}{' '}
                      <Typography.Text type="secondary" style={{ fontWeight: 400 }}>
                        ({c.type})
                      </Typography.Text>
                    </Typography.Text>
                    <Space size={4} style={{ flexShrink: 0 }}>
                      {!String(inspectRow.__rowKey || '').startsWith('new_') && (
                        <>
                          <Tag
                            style={{ cursor: 'pointer', margin: 0, fontSize: 10 }}
                            title={
                              isNullVal
                                ? `${t('Filter')}: ${c.name} IS NULL`
                                : `${t('Filter')}: ${c.name} = ${strVal.slice(0, 24)}`
                            }
                            onClick={() => {
                              const qCol = quoteIdentForEngine(tab.dbType, c.name);
                              applyQuickWhereClause(
                                isNullVal ? `${qCol} IS NULL` : `${qCol} = ${escapeSQLVal(strVal)}`
                              );
                              setInspectRow(null);
                            }}
                          >
                            <FilterOutlined style={{ fontSize: 9, marginRight: 2 }} />=
                          </Tag>
                          <Tag
                            style={{ cursor: 'pointer', margin: 0, fontSize: 10 }}
                            title={
                              isNullVal
                                ? `${t('Filter')}: ${c.name} IS NOT NULL`
                                : `${t('Filter')}: ${c.name} != ${strVal.slice(0, 24)}`
                            }
                            onClick={() => {
                              const qCol = quoteIdentForEngine(tab.dbType, c.name);
                              applyQuickWhereClause(
                                isNullVal ? `${qCol} IS NOT NULL` : `${qCol} != ${escapeSQLVal(strVal)}`
                              );
                              setInspectRow(null);
                            }}
                          >
                            <FilterOutlined style={{ fontSize: 9, marginRight: 2 }} />!=
                          </Tag>
                        </>
                      )}
                      {canWrite && c.nullable && (
                        <Tag
                          color={isNullVal ? 'orange' : undefined}
                          style={{ cursor: 'pointer', margin: 0, fontSize: 10 }}
                          onClick={() => {
                            const nextVal = isNullVal ? '' : null;
                            setInspectRow({ ...inspectRow, [c.name]: nextVal });
                            handleCellChange(inspectRow.__rowKey, c.name, nextVal);
                          }}
                        >
                          {isNullVal ? 'NULL' : t('Set NULL')}
                        </Tag>
                      )}
                      <Button
                        type="text"
                        size="small"
                        icon={<CopyOutlined style={{ fontSize: 11 }} />}
                        onClick={() => {
                          writeTextToClipboard(isNullVal ? 'NULL' : strVal);
                          message.success(t('Copied {name}', { name: c.name }));
                        }}
                      />
                    </Space>
                  </div>
                  <Input.TextArea
                    autoSize={{ minRows: 1, maxRows: 8 }}
                    readOnly={!canWrite}
                    placeholder={isNullVal ? 'NULL' : ''}
                    value={strVal}
                    onChange={(e) => {
                      if (!canWrite) return;
                      const nextVal = e.target.value;
                      setInspectRow({ ...inspectRow, [c.name]: nextVal });
                      handleCellChange(inspectRow.__rowKey, c.name, nextVal);
                    }}
                    style={{ marginTop: 4, fontFamily: 'monospace', fontSize: 12 }}
                  />
                </div>
              );
            })}
          </>
        )}
      </Drawer>
    </div>
  );
});
