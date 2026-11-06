export interface ClipboardColumnMeta {
  name: string;
  type?: string;
  nullable?: boolean;
}

export function formatRawCellText(val: any): string {
  if (val === null || val === undefined) return 'NULL';
  if (typeof val === 'object') {
    try {
      return JSON.stringify(val);
    } catch {
      return String(val);
    }
  }
  return String(val);
}

export function formatTsvCell(val: any): string {
  const raw = formatRawCellText(val);
  if (raw.includes('\t') || raw.includes('\r') || raw.includes('\n') || raw.includes('"')) {
    return `"${raw.replace(/"/g, '""')}"`;
  }
  return raw;
}

export function serializeRowsToTsv(
  columns: string[],
  rows: Record<string, any>[],
  includeHeader = false
): string {
  const lines: string[] = [];
  if (includeHeader && columns.length > 0) {
    lines.push(columns.map((c) => formatTsvCell(c)).join('\t'));
  }
  for (const row of rows) {
    lines.push(columns.map((col) => formatTsvCell(row[col])).join('\t'));
  }
  return lines.join('\r\n');
}

/**
 * Extracts multi-cell or full-row browser text selection inside an Ant Design Table as clean TSV
 * (`\t` between columns, `\r\n` between rows) so pasting into Notepad, Excel, or DBHarbor never splits cells vertically.
 */
export function extractSelectionAsTsv(container: HTMLElement | null): string | null {
  if (!container || typeof window === 'undefined') return null;
  const active = document.activeElement;
  if (
    active &&
    (active.tagName === 'INPUT' ||
      active.tagName === 'TEXTAREA' ||
      (active as HTMLElement).isContentEditable)
  ) {
    return null;
  }

  const sel = window.getSelection();
  if (!sel || sel.rangeCount === 0 || sel.isCollapsed) return null;

  const range = sel.getRangeAt(0);
  if (!container.contains(range.commonAncestorContainer)) return null;

  const trList = Array.from(container.querySelectorAll('tr'));
  const rowMatrix: string[][] = [];
  let totalSelectedCells = 0;
  let singleSelectedCell: HTMLElement | null = null;

  for (const tr of trList) {
    if (
      tr.classList.contains('ant-table-measure-row') ||
      tr.classList.contains('ant-table-placeholder') ||
      tr.getAttribute('data-copy-skip') === 'true' ||
      (tr as HTMLElement).offsetHeight === 0
    ) {
      continue;
    }
    if (!sel.containsNode(tr, true)) continue;

    const cells = Array.from(tr.children) as HTMLElement[];
    const rowValues: string[] = [];

    for (const cell of cells) {
      const tag = cell.tagName;
      if (tag !== 'TD' && tag !== 'TH') continue;
      if (
        cell.classList.contains('ant-table-selection-column') ||
        cell.getAttribute('data-copy-skip') === 'true' ||
        cell.querySelector('[data-copy-skip="true"]')
      ) {
        continue;
      }
      if (!sel.containsNode(cell, true)) continue;

      totalSelectedCells++;
      singleSelectedCell = cell;

      if (tag === 'TH') {
        const colHolder = cell.querySelector('[data-col-name]') as HTMLElement | null;
        const headerText =
          colHolder?.getAttribute('data-col-name') ??
          cell.getAttribute('data-col-name') ??
          cell.innerText.replace(/\s+/g, ' ').trim();
        rowValues.push(formatTsvCell(headerText));
      } else {
        const rawHolder = cell.querySelector('[data-raw-copy]') as HTMLElement | null;
        if (rawHolder && rawHolder.hasAttribute('data-raw-copy')) {
          rowValues.push(formatTsvCell(rawHolder.getAttribute('data-raw-copy') ?? ''));
        } else if (cell.hasAttribute('data-raw-copy')) {
          rowValues.push(formatTsvCell(cell.getAttribute('data-raw-copy') ?? ''));
        } else {
          const cleaned = cell.innerText.replace(/\r?\n+/g, ' ').trim();
          rowValues.push(formatTsvCell(cleaned));
        }
      }
    }

    if (rowValues.length > 0) {
      rowMatrix.push(rowValues);
    }
  }

  // Allow native substring copy when user selected partial characters strictly inside a single cell
  if (
    totalSelectedCells <= 1 &&
    singleSelectedCell &&
    singleSelectedCell.contains(range.startContainer) &&
    singleSelectedCell.contains(range.endContainer)
  ) {
    return null;
  }

  if (rowMatrix.length === 0) return null;
  return rowMatrix.map((r) => r.join('\t')).join('\r\n');
}

/**
 * Parses TSV text (including quoted multi-line cells from Excel/DBHarbor) into a 2D string matrix.
 */
export function parseTsvMatrix(text: string): string[][] {
  const rows: string[][] = [];
  let row: string[] = [];
  let field = '';
  let inQuotes = false;
  const src = text.replace(/\r\n/g, '\n').replace(/\r/g, '\n');

  for (let i = 0; i < src.length; i++) {
    const ch = src[i];
    if (inQuotes) {
      if (ch === '"') {
        if (src[i + 1] === '"') {
          field += '"';
          i++;
        } else {
          inQuotes = false;
        }
      } else {
        field += ch;
      }
    } else if (ch === '"' && field.length === 0) {
      inQuotes = true;
    } else if (ch === '\t') {
      row.push(field);
      field = '';
    } else if (ch === '\n') {
      row.push(field);
      rows.push(row);
      row = [];
      field = '';
    } else {
      field += ch;
    }
  }

  if (inQuotes || field.length > 0 || row.length > 0) {
    row.push(field);
    rows.push(row);
  }

  // Trim trailing completely empty newline row added by editors/spreadsheets on copy
  while (rows.length > 0 && rows[rows.length - 1].length === 1 && rows[rows.length - 1][0] === '') {
    rows.pop();
  }

  return rows;
}

function isNonStringColumnType(colType?: string): boolean {
  if (!colType) return false;
  const upper = colType.toUpperCase();
  return (
    upper.includes('INT') ||
    upper.includes('DEC') ||
    upper.includes('NUM') ||
    upper.includes('FLOAT') ||
    upper.includes('DOUBLE') ||
    upper.includes('REAL') ||
    upper.includes('BOOL') ||
    upper.includes('BIT') ||
    upper.includes('DATE') ||
    upper.includes('TIME') ||
    upper.includes('YEAR') ||
    upper.includes('UUID') ||
    upper.includes('SERIAL')
  );
}

export function coercePastedCellValue(raw: string, col?: ClipboardColumnMeta): any {
  const nullable = col?.nullable !== false;
  if (raw === 'NULL' || raw === '\\N') {
    return nullable ? null : raw;
  }
  if (raw === '' && nullable && isNonStringColumnType(col?.type)) {
    return null;
  }
  return raw;
}

export interface ParsedClipboardTabular {
  records: Record<string, any>[];
  matrix: string[][];
  matchedByHeader: boolean;
}

/**
 * Parses clipboard content (JSON array/object OR TSV from Notepad/Excel/DBHarbor) into structured row records and 2D matrix.
 */
export function parseClipboardTabular(
  rawText: string,
  columns: ClipboardColumnMeta[]
): ParsedClipboardTabular {
  const trimmed = rawText.trim();
  if (!trimmed) {
    return { records: [], matrix: [], matchedByHeader: false };
  }

  const colMapLower = new Map<string, ClipboardColumnMeta>();
  for (const c of columns) {
    colMapLower.set(c.name.toLowerCase(), c);
  }

  // 1. Check if clipboard is valid JSON object or array of objects
  if ((trimmed.startsWith('[') && trimmed.endsWith(']')) || (trimmed.startsWith('{') && trimmed.endsWith('}'))) {
    try {
      const parsed = JSON.parse(trimmed);
      const arr = Array.isArray(parsed) ? parsed : [parsed];
      if (arr.length > 0 && arr.every((item) => item && typeof item === 'object' && !Array.isArray(item))) {
        const records: Record<string, any>[] = arr.map((obj) => {
          const rec: Record<string, any> = {};
          for (const [k, v] of Object.entries(obj)) {
            const matched = colMapLower.get(k.toLowerCase());
            if (matched) {
              rec[matched.name] = v;
            } else {
              rec[k] = v;
            }
          }
          return rec;
        });
        const matrix = records.map((rec) => columns.map((c) => formatRawCellText(rec[c.name] ?? '')));
        return { records, matrix, matchedByHeader: true };
      }
    } catch {
      // Fall through to TSV parser
    }
  }

  // 2. Parse TSV matrix (Notepad / Excel / DBHarbor row copy)
  const matrix = parseTsvMatrix(rawText);
  if (matrix.length === 0) {
    return { records: [], matrix: [], matchedByHeader: false };
  }

  // Check if first row is a header row matching known column names
  let dataRows = matrix;
  let headerMapping: (ClipboardColumnMeta | undefined)[] | null = null;
  if (matrix.length >= 2 && matrix[0].length >= 1) {
    let matches = 0;
    const candidate = matrix[0].map((cell) => {
      const found = colMapLower.get(cell.trim().toLowerCase());
      if (found) matches++;
      return found;
    });
    if (matches >= Math.max(1, Math.ceil(matrix[0].length * 0.6))) {
      headerMapping = candidate;
      dataRows = matrix.slice(1);
    }
  }

  const records: Record<string, any>[] = dataRows.map((row) => {
    const rec: Record<string, any> = {};
    if (headerMapping) {
      row.forEach((cell, idx) => {
        const col = headerMapping![idx];
        if (col) {
          rec[col.name] = coercePastedCellValue(cell, col);
        }
      });
    } else {
      row.forEach((cell, idx) => {
        const col = columns[idx];
        if (col) {
          rec[col.name] = coercePastedCellValue(cell, col);
        }
      });
    }
    return rec;
  });

  return {
    records,
    matrix: dataRows,
    matchedByHeader: Boolean(headerMapping),
  };
}

export async function writeTextToClipboard(text: string): Promise<void> {
  if (navigator.clipboard && typeof navigator.clipboard.writeText === 'function') {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch {
      // Fallback below
    }
  }
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.style.position = 'fixed';
  ta.style.left = '-9999px';
  ta.style.top = '0';
  document.body.appendChild(ta);
  ta.focus();
  ta.select();
  document.execCommand('copy');
  document.body.removeChild(ta);
}
