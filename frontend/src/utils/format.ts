export function formatBytes(bytes?: number): string {
  if (!bytes || bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let idx = 0;
  let val = bytes;
  while (val >= 1024 && idx < units.length - 1) {
    val /= 1024;
    idx++;
  }
  return `${val >= 100 || idx === 0 ? val.toFixed(0) : val.toFixed(2)} ${units[idx]}`;
}

export function formatDuration(seconds?: number): string {
  if (!seconds || seconds <= 0) return '0s';
  if (seconds < 60) return `${seconds}s`;
  const mins = Math.floor(seconds / 60);
  if (mins < 60) return `${mins}m ${seconds % 60}s`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ${mins % 60}m`;
  const days = Math.floor(hours / 24);
  return `${days}d ${hours % 24}h`;
}

export const DB_SHORT_LABELS: Record<string, { code: string; color: string }> = {
  clickhouse: { code: 'CH', color: '#faad14' },
  mysql: { code: 'MY', color: '#1677ff' },
  mariadb: { code: 'MA', color: '#2f54eb' },
  tidb: { code: 'Ti', color: '#eb2f96' },
  oceanbase: { code: 'OB', color: '#0958d9' },
  postgres: { code: 'PG', color: '#1d39c4' },
  kingbase: { code: 'KB', color: '#c41d7f' },
  sqlite: { code: 'SQ', color: '#13c2c2' },
  duckdb: { code: 'DK', color: '#d4b106' },
  redis: { code: 'RD', color: '#dc2626' },
  mongodb: { code: 'MG', color: '#389e0d' },
  sqlserver: { code: 'MS', color: '#cf1322' },
  oracle: { code: 'OR', color: '#f5222d' },
  dameng: { code: 'DM', color: '#fa541c' },
};
