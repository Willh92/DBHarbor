import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Table } from 'antd';
import type { TableProps } from 'antd';
import type { ColumnType } from 'antd/es/table';
import { useResponsive } from '../../hooks/useResponsive';
import { extractSelectionAsTsv } from '../../utils/tableClipboard';

export interface ResizableColumnType<RecordType = any> extends ColumnType<RecordType> {
  resizable?: boolean;
  minWidth?: number;
}

export interface ResizableTableProps<RecordType = any> extends Omit<TableProps<RecordType>, 'columns'> {
  columns?: ResizableColumnType<RecordType>[];
  defaultColumnWidth?: number;
  minColumnWidth?: number;
  fullHeightBody?: number;
  wrapClassName?: string;
  onFallbackCopy?: (e: React.ClipboardEvent<HTMLDivElement>) => void;
}

/**
 * Estimates pixel width required to display a header ReactNode on a single line without truncation.
 */
export function estimateHeaderContentWidth(node: React.ReactNode): number {
  if (node === null || node === undefined || typeof node === 'boolean') return 0;
  if (typeof node === 'string' || typeof node === 'number') {
    const str = String(node);
    let w = 0;
    for (let i = 0; i < str.length; i++) {
      w += str.charCodeAt(i) > 0x7f ? 14 : 8.4;
    }
    return w;
  }
  if (Array.isArray(node)) {
    let total = 0;
    let count = 0;
    for (const child of node) {
      const cw = estimateHeaderContentWidth(child);
      if (cw > 0) {
        total += cw;
        count++;
      }
    }
    return total + Math.max(0, count - 1) * 5;
  }
  if (React.isValidElement(node)) {
    const props: any = node.props || {};
    const inner = estimateHeaderContentWidth(props.children);
    // Account for Ant Design Tag / Badge pill padding & borders inside column headers (e.g. PK tag)
    if (props.color || (typeof props.className === 'string' && props.className.includes('tag'))) {
      return inner + 14;
    }
    return inner;
  }
  return 0;
}

export function calcSmartColumnWidth(
  name: string,
  opts?: { type?: string; isPK?: boolean; hasSorter?: boolean; minBodyWidth?: number }
): number {
  let nameW = 0;
  for (let i = 0; i < name.length; i++) {
    nameW += name.charCodeAt(i) > 0x7f ? 14 : 8.5;
  }
  const pkW = opts?.isPK ? 34 : 0;
  const typeStr = opts?.type || '';
  const typeW = typeStr ? Math.min(typeStr.length, 24) * 6.8 + 8 : 0;
  const sorterW = opts?.hasSorter ? 26 : 0;
  const paddingW = 34;
  const headerNeed = Math.ceil(nameW + pkW + typeW + sorterW + paddingW);
  return Math.max(headerNeed, opts?.minBodyWidth ?? 110);
}

interface HeaderCellProps extends React.ThHTMLAttributes<HTMLTableCellElement> {
  width?: number;
  minWidth?: number;
  resizable?: boolean;
  onResizeWidth?: (nextWidth: number) => void;
  onAutoExpandWidth?: () => void;
}

const ResizableHeaderCell: React.FC<HeaderCellProps> = ({
  width,
  minWidth = 56,
  resizable,
  onResizeWidth,
  onAutoExpandWidth,
  children,
  style,
  ...restProps
}) => {
  const thRef = useRef<HTMLTableCellElement | null>(null);
  const resizerRef = useRef<HTMLSpanElement | null>(null);
  const isDraggingRef = useRef(false);
  const lastTapTimeRef = useRef<number>(0);

  if (!resizable || !onResizeWidth) {
    return (
      <th ref={thRef} style={style} {...restProps}>
        {children}
      </th>
    );
  }

  const handlePointerDown = (e: React.PointerEvent<HTMLSpanElement>) => {
    if (e.pointerType === 'mouse' && e.button !== 0) return;
    e.preventDefault();
    e.stopPropagation();

    const pointerId = e.pointerId;
    const pointerType = e.pointerType;
    const resizerEl = e.currentTarget;
    try {
      resizerEl.setPointerCapture(pointerId);
    } catch {
      // ignore synthetic events
    }

    const thEl = thRef.current;
    const startX = e.clientX;
    const measured = thEl ? thEl.getBoundingClientRect().width : 140;
    const startWidth = typeof width === 'number' && width > 0 ? width : Math.round(measured);

    // Resolve matching <colgroup><col> and <table> DOM nodes once on pointerdown
    // so pointermove mutates native DOM once per VSync frame with zero React fiber reconciliation.
    const wrapEl = thEl?.closest('.dbharbor-resizable-table-wrap') as HTMLElement | null;
    const trEl = thEl?.parentElement;
    const colIndex = trEl && thEl ? Array.from(trEl.children).indexOf(thEl) : -1;

    const targetCols: HTMLElement[] = [];
    const targetTables: { el: HTMLTableElement; startW: number }[] = [];

    if (wrapEl && colIndex >= 0) {
      const tables = wrapEl.querySelectorAll<HTMLTableElement>(
        '.ant-table-header table, .ant-table-body table, .ant-table-content table'
      );
      tables.forEach((tbl) => {
        const colgroup = tbl.querySelector('colgroup');
        const colNode = colgroup?.children[colIndex] as HTMLElement | undefined;
        if (colNode) {
          targetCols.push(colNode);
          targetTables.push({
            el: tbl,
            startW: tbl.getBoundingClientRect().width,
          });
        }
      });
    }

    isDraggingRef.current = true;
    (window as any).__DBHARBOR_LAYOUT_DRAGGING__ = true;
    resizerEl.classList.add('is-dragging');
    document.body.classList.add('dbharbor-is-dragging');
    const prevCursor = document.body.style.cursor;
    document.body.style.cursor = 'col-resize';

    let latestWidth = startWidth;
    let hasMoved = false;
    let rafId = 0;

    const preventTouchScroll = (touchEvent: TouchEvent) => {
      if (touchEvent.cancelable) {
        touchEvent.preventDefault();
      }
    };

    const applyColWidth = () => {
      rafId = 0;
      const appliedDelta = latestWidth - startWidth;
      if (targetCols.length > 0) {
        const wPx = `${latestWidth}px`;
        for (let i = 0; i < targetCols.length; i++) {
          targetCols[i].style.width = wPx;
          targetCols[i].style.minWidth = wPx;
        }
        for (let i = 0; i < targetTables.length; i++) {
          const nextTblW = Math.max(100, Math.round(targetTables[i].startW + appliedDelta));
          targetTables[i].el.style.width = `${nextTblW}px`;
          targetTables[i].el.style.minWidth = '100%';
        }
      }
    };

    const onPointerMove = (moveEvent: PointerEvent) => {
      if (moveEvent.pointerId !== pointerId) return;
      const delta = moveEvent.clientX - startX;
      if (Math.abs(delta) >= 3) {
        hasMoved = true;
      }
      latestWidth = Math.max(minWidth, Math.min(1600, Math.round(startWidth + delta)));
      if (!rafId) {
        rafId = window.requestAnimationFrame(applyColWidth);
      }
    };

    const onPointerUp = (upEvent: PointerEvent) => {
      if (upEvent.pointerId !== pointerId) return;
      if (rafId) {
        window.cancelAnimationFrame(rafId);
        rafId = 0;
      }
      if (hasMoved) {
        applyColWidth();
      }
      try {
        if (resizerEl.hasPointerCapture(pointerId)) {
          resizerEl.releasePointerCapture(pointerId);
        }
      } catch {
        // ignore
      }
      isDraggingRef.current = false;
      (window as any).__DBHARBOR_LAYOUT_DRAGGING__ = false;
      resizerEl.classList.remove('is-dragging');
      document.body.classList.remove('dbharbor-is-dragging');
      document.body.style.cursor = prevCursor;
      window.removeEventListener('pointermove', onPointerMove);
      window.removeEventListener('pointerup', onPointerUp);
      window.removeEventListener('pointercancel', onPointerUp);
      window.removeEventListener('touchmove', preventTouchScroll);

      if (hasMoved) {
        // Block any trailing click on parent sortable <th> right after finishing a drag
        const suppressClick = (clickEvent: MouseEvent) => {
          clickEvent.stopPropagation();
          clickEvent.preventDefault();
          window.removeEventListener('click', suppressClick, true);
        };
        window.addEventListener('click', suppressClick, true);
        window.setTimeout(() => {
          window.removeEventListener('click', suppressClick, true);
        }, 80);
        onResizeWidth(latestWidth);
      } else if (pointerType !== 'mouse' && upEvent.type === 'pointerup') {
        const now = Date.now();
        if (now - lastTapTimeRef.current < 320) {
          lastTapTimeRef.current = 0;
          onAutoExpandWidth?.();
        } else {
          lastTapTimeRef.current = now;
        }
      }
    };

    window.addEventListener('pointermove', onPointerMove);
    window.addEventListener('pointerup', onPointerUp);
    window.addEventListener('pointercancel', onPointerUp);
    if (pointerType !== 'mouse') {
      window.addEventListener('touchmove', preventTouchScroll, { passive: false });
    }
  };

  // Preserve CSS `position: sticky` on fixed-left / fixed-right columns so `left` / `right` offsets never shift
  const isStickyCell =
    typeof restProps.className === 'string' && restProps.className.includes('ant-table-cell-fix-');
  const cellStyle: React.CSSProperties = isStickyCell
    ? { ...style }
    : { ...style, position: style?.position || 'relative' };
  const mergedThClassName = [restProps.className, 'dbharbor-resizable-th'].filter(Boolean).join(' ');

  return (
    <th ref={thRef} {...restProps} className={mergedThClassName} style={cellStyle}>
      {children}
      <span
        ref={resizerRef}
        className={`dbharbor-col-resizer${isDraggingRef.current ? ' is-dragging' : ''}`}
        onPointerDown={handlePointerDown}
        onClick={(e) => e.stopPropagation()}
        onDoubleClick={(e) => {
          e.preventDefault();
          e.stopPropagation();
          onAutoExpandWidth?.();
        }}
      />
    </th>
  );
};

/**
 * Ant Design Table wrapper with header-complete smart column sizing, unused space distribution,
 * and zero-React-re-render real-time DOM column resizing.
 */
export function ResizableTable<RecordType extends object = any>({
  columns = [],
  defaultColumnWidth = 160,
  minColumnWidth = 56,
  fullHeightBody,
  wrapClassName,
  components,
  scroll,
  onFallbackCopy,
  ...restProps
}: ResizableTableProps<RecordType>) {
  const { isMobile } = useResponsive();
  const [isCoarsePointer, setIsCoarsePointer] = useState<boolean>(() =>
    typeof window !== 'undefined' && typeof window.matchMedia === 'function'
      ? window.matchMedia('(pointer: coarse)').matches
      : false
  );
  const [hasFinePointer, setHasFinePointer] = useState<boolean>(() =>
    typeof window !== 'undefined' && typeof window.matchMedia === 'function'
      ? window.matchMedia('(any-pointer: fine)').matches
      : true
  );
  const wrapRef = useRef<HTMLDivElement | null>(null);
  const [containerWidth, setContainerWidth] = useState<number>(0);
  const [customWidths, setCustomWidths] = useState<Record<string, number>>({});
  const currentWidthsRef = useRef<Record<string, number>>({});

  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return;
    const coarseMq = window.matchMedia('(pointer: coarse)');
    const fineMq = window.matchMedia('(any-pointer: fine)');
    const onChange = () => {
      setIsCoarsePointer(coarseMq.matches);
      setHasFinePointer(fineMq.matches);
    };
    onChange();
    coarseMq.addEventListener?.('change', onChange);
    fineMq.addEventListener?.('change', onChange);
    return () => {
      coarseMq.removeEventListener?.('change', onChange);
      fineMq.removeEventListener?.('change', onChange);
    };
  }, []);

  const isTouchDevice = isMobile || (isCoarsePointer && !hasFinePointer);
  const hasVerticalScroll = Boolean(scroll?.y || (fullHeightBody && fullHeightBody > 0));
  const useTouchStickyHeader = isTouchDevice && hasVerticalScroll;

  // Measure available container width so extra horizontal space expands columns cleanly,
  // pausing React state updates while splitters or columns are actively dragging.
  useEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const widthThreshold = isTouchDevice ? 6 : 2;
    const measure = () => {
      if ((window as any).__DBHARBOR_LAYOUT_DRAGGING__) return;
      const w = Math.floor(el.clientWidth || 0);
      if (w <= 0) return;
      setContainerWidth((prev) => (Math.abs(prev - w) >= widthThreshold ? w : prev));
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    window.addEventListener('dbharbor-layout-settled', measure);
    return () => {
      ro.disconnect();
      window.removeEventListener('dbharbor-layout-settled', measure);
    };
  }, [isTouchDevice]);

  const colSignature = useMemo(
    () =>
      columns
        .map((c, idx) => String(c.key ?? (Array.isArray(c.dataIndex) ? c.dataIndex.join('.') : c.dataIndex) ?? idx))
        .join('|'),
    [columns]
  );

  useEffect(() => {
    setCustomWidths({});
  }, [colSignature]);

  const handleResize = useCallback((colKey: string, nextWidth: number) => {
    setCustomWidths((prev) => {
      if (prev[colKey] === nextWidth && Object.keys(prev).length > 0) return prev;
      return {
        ...currentWidthsRef.current,
        ...prev,
        [colKey]: nextWidth,
      };
    });
  }, []);

  const defaultSelectionColWidth = isMobile ? 38 : 40;
  const normalizedRowSelection = useMemo(() => {
    if (!restProps.rowSelection) return undefined;
    const explicitWidth = Number((restProps.rowSelection as any)?.columnWidth);
    return {
      ...restProps.rowSelection,
      columnWidth: explicitWidth > 0 ? explicitWidth : defaultSelectionColWidth,
    };
  }, [restProps.rowSelection, defaultSelectionColWidth]);

  const selectionColWidth = normalizedRowSelection
    ? Number(normalizedRowSelection.columnWidth) || defaultSelectionColWidth
    : 0;

  const { mergedColumns, totalWidth } = useMemo(() => {
    // Pass 1: compute smart base widths guaranteeing full column header visibility unless manually resized
    const prelim = columns.map((col, index) => {
      const rawKey = col.key ?? (Array.isArray(col.dataIndex) ? col.dataIndex.join('.') : col.dataIndex) ?? index;
      const colKey = String(rawKey);
      const rawBase = typeof col.width === 'number' ? col.width : defaultColumnWidth;
      const titleNode = typeof col.title === 'function' ? '' : col.title;
      const headerContentW = estimateHeaderContentWidth(titleNode);
      const canResize = col.resizable !== false && !col.fixed;
      const sorterExtra = col.sorter ? 26 : 0;
      const touchResizerExtra = isTouchDevice && canResize ? 10 : 0;
      const headerFloor =
        headerContentW > 0 ? Math.ceil(headerContentW + sorterExtra + touchResizerExtra + 28) : 0;
      const smartBase = col.fixed ? rawBase : Math.max(rawBase, headerFloor);
      const hasCustom = customWidths[colKey] !== undefined;
      const width = hasCustom ? customWidths[colKey] : smartBase;
      const isFlexCandidate = canResize && !hasCustom;
      return {
        col,
        colKey,
        smartBase,
        width,
        canResize,
        isFlexCandidate,
        colMin: col.minWidth ?? minColumnWidth,
      };
    });

    // Pass 2: if container has unused blank width on the right, distribute it proportionally across flexible columns
    const baseTotal = prelim.reduce((acc, item) => acc + item.width, 0);
    const scrollbarReserve = scroll?.y ? 12 : 2;
    const availForCols = containerWidth > 0 ? containerWidth - selectionColWidth - scrollbarReserve : 0;

    if (availForCols > baseTotal) {
      const flexItems = prelim.filter((x) => x.isFlexCandidate);
      const flexBaseSum = flexItems.reduce((acc, x) => acc + x.width, 0);
      if (flexItems.length > 0 && flexBaseSum > 0) {
        const extra = availForCols - baseTotal;
        let distributed = 0;
        flexItems.forEach((item, i) => {
          const add =
            i === flexItems.length - 1
              ? Math.max(0, extra - distributed)
              : Math.floor((extra * item.width) / flexBaseSum);
          item.width += add;
          distributed += add;
        });
      }
    }

    let finalSum = 0;
    const renderedSnapshot: Record<string, number> = {};
    const cols = prelim.map(({ col, colKey, smartBase, width, canResize, colMin }) => {
      finalSum += width;
      if (canResize) {
        renderedSnapshot[colKey] = width;
      }
      const origOnHeaderCell = col.onHeaderCell;
      return {
        ...col,
        width,
        onHeaderCell: (column: any) => {
          const prevProps = origOnHeaderCell ? origOnHeaderCell(column) : {};
          return {
            ...prevProps,
            width,
            minWidth: colMin,
            resizable: canResize,
            onResizeWidth: canResize ? (w: number) => handleResize(colKey, w) : undefined,
            onAutoExpandWidth: canResize
              ? () => {
                  const target = width < 340 ? Math.max(360, smartBase + 120) : smartBase;
                  handleResize(colKey, target);
                }
              : undefined,
          };
        },
      };
    });
    currentWidthsRef.current = renderedSnapshot;

    return { mergedColumns: cols, totalWidth: finalSum };
  }, [
    columns,
    customWidths,
    defaultColumnWidth,
    handleResize,
    isTouchDevice,
    minColumnWidth,
    containerWidth,
    selectionColWidth,
    scroll?.y,
  ]);

  const mergedComponents = useMemo(
    () => ({
      ...components,
      header: {
        ...(components?.header || {}),
        cell: ResizableHeaderCell,
      },
    }),
    [components]
  );

  const mergedScroll = useMemo(() => {
    const fullW = totalWidth + selectionColWidth;
    const minX = typeof scroll?.x === 'number' ? Math.max(scroll.x, fullW) : Math.max(fullW, 1);
    const resolvedX = scroll?.x === 'max-content' ? Math.max(fullW, 1) : minX;
    if (useTouchStickyHeader) {
      // Omit scroll.y so rc-table renders a single unified <table> with native GPU CSS sticky <thead>
      return { x: resolvedX };
    }
    return {
      ...scroll,
      x: resolvedX,
    };
  }, [scroll, totalWidth, selectionColWidth, useTouchStickyHeader]);

  const handleTableCopy = useCallback(
    (e: React.ClipboardEvent<HTMLDivElement>) => {
      const tsv = extractSelectionAsTsv(wrapRef.current);
      if (tsv !== null) {
        e.clipboardData.setData('text/plain', tsv);
        e.preventDefault();
        return;
      }
      onFallbackCopy?.(e);
    },
    [onFallbackCopy]
  );

  const wrapperClasses = [
    'dbharbor-resizable-table-wrap',
    fullHeightBody && fullHeightBody > 0 ? 'dbharbor-full-height-grid' : '',
    useTouchStickyHeader ? 'dbharbor-touch-sticky-grid' : '',
    isMobile ? 'dbharbor-mobile-table' : '',
    wrapClassName || '',
  ]
    .filter(Boolean)
    .join(' ');

  const stickyMaxHeightCss =
    useTouchStickyHeader && !fullHeightBody && scroll?.y !== undefined
      ? typeof scroll.y === 'number'
        ? `${scroll.y + 38}px`
        : String(scroll.y)
      : undefined;

  const wrapperStyle: React.CSSProperties = {
    width: '100%',
    minWidth: 0,
    ...(fullHeightBody && fullHeightBody > 0
      ? ({ '--dbharbor-grid-body-h': `${fullHeightBody}px` } as React.CSSProperties)
      : {}),
    ...(stickyMaxHeightCss
      ? ({ '--dbharbor-sticky-max-h': stickyMaxHeightCss } as React.CSSProperties)
      : {}),
  };

  return (
    <div
      ref={wrapRef}
      className={wrapperClasses}
      onCopy={handleTableCopy}
      style={wrapperStyle}
    >
      <Table<RecordType>
        {...restProps}
        rowSelection={normalizedRowSelection}
        tableLayout="fixed"
        components={mergedComponents}
        columns={mergedColumns}
        scroll={mergedScroll}
      />
    </div>
  );
}
