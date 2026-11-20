const BACKTICK_ENGINES = new Set(['mysql', 'mariadb', 'tidb', 'oceanbase', 'clickhouse']);
const BRACKET_ENGINES = new Set(['sqlserver', 'mssql']);
const BACKSLASH_ESCAPE_ENGINES = new Set(['mysql', 'mariadb', 'tidb', 'oceanbase', 'clickhouse']);
const SCHEMA_QUALIFIED_ENGINES = new Set(['postgres', 'kingbase', 'sqlserver', 'mssql', 'oracle', 'dameng', 'dm8']);
const BOOLEAN_KEYWORD_ENGINES = new Set(['postgres', 'kingbase', 'clickhouse', 'sqlite', 'duckdb']);

export function normalizeSqlEngine(dbType?: string): string {
  return (dbType || '').trim().toLowerCase();
}

export function quoteIdentForEngine(dbType: string | undefined, ident: string): string {
  const clean = ident.replace(/\0/g, '');
  if (!clean) return '';
  const eng = normalizeSqlEngine(dbType);
  if (BACKTICK_ENGINES.has(eng)) return `\`${clean.replace(/`/g, '``')}\``;
  if (BRACKET_ENGINES.has(eng)) return `[${clean.replace(/]/g, ']]')}]`;
  return `"${clean.replace(/"/g, '""')}"`;
}

export function qualifyTableForEngine(
  dbType: string | undefined,
  table: string,
  schema?: string
): string {
  const qTbl = quoteIdentForEngine(dbType, table);
  const cleanSchema = (schema || '').trim();
  const qSchema =
    cleanSchema && SCHEMA_QUALIFIED_ENGINES.has(normalizeSqlEngine(dbType))
      ? quoteIdentForEngine(dbType, cleanSchema)
      : '';
  if (qSchema && qTbl) {
    return `${qSchema}.${qTbl}`;
  }
  return qTbl;
}

export function escapeSqlStringBodyForEngine(dbType: string | undefined, raw: string): string {
  const eng = normalizeSqlEngine(dbType);
  if (BACKSLASH_ESCAPE_ENGINES.has(eng)) {
    return raw
      .replace(/\\/g, '\\\\')
      .replace(/'/g, "''")
      .replace(/\0/g, '\\0')
      .replace(/\r/g, '\\r')
      .replace(/\n/g, '\\n');
  }
  return raw.replace(/\0/g, '').replace(/'/g, "''");
}

export function formatSqlLiteralForEngine(dbType: string | undefined, val: unknown): string {
  if (val === null || val === undefined) return 'NULL';
  if (typeof val === 'number') {
    return Number.isFinite(val) ? String(val) : `'${escapeSqlStringBodyForEngine(dbType, String(val))}'`;
  }
  if (typeof val === 'boolean') {
    if (BOOLEAN_KEYWORD_ENGINES.has(normalizeSqlEngine(dbType))) {
      return val ? 'TRUE' : 'FALSE';
    }
    return val ? '1' : '0';
  }
  return `'${escapeSqlStringBodyForEngine(dbType, String(val))}'`;
}

export function escapeSqlFilterValForEngine(dbType: string | undefined, raw: string): string {
  const trimmed = raw.trim();
  if (/^(?:-?0\.\d+|0|-?[1-9]\d*(?:\.\d+)?)$/.test(trimmed)) {
    return trimmed;
  }
  return `'${escapeSqlStringBodyForEngine(dbType, raw)}'`;
}
