import type {
  AlterAction,
  AuditLogEntry,
  AuthSession,
  AuthStatusResponse,
  BuildVersionInfo,
  CheckConstraintDef,
  CollectionSummary,
  ColumnDef,
  ConnectionFormPayload,
  DatabaseSummary,
  DbAccountInfo,
  DbGrantEntry,
  DbPrivilegeActionPayload,
  DbPrivilegeCapabilities,
  DesktopExportResult,
  DocIndexInfo,
  DriverMeta,
  EphemeralOpenResponse,
  ExportedConnectionProfile,
  ForeignKeyDef,
  HistoryEntry,
  ImportResult,
  IndexDef,
  KVBackupPayload,
  KVConfigItem,
  KVEntryDetail,
  KVKeySummary,
  KVSlowlogEntry,
  KVStreamInfo,
  LocalFSBrowseResponse,
  LoginBrandingConfig,
  LoginResponse,
  ManagedUser,
  MonitorSnapshot,
  ObjectActionPayload,
  PartitionInfo,
  QueryResult,
  SavedConnection,
  SavedSnippet,
  SchemaObjects,
  SecurityStatus,
  ServerInfo,
  SystemStats,
  TableStructure,
  UpdateChannel,
  UpdateCheckResult,
  UserRole,
} from '../types';
import { detectInitialLocale } from '../i18n';
import { getRuntimeBootMeta, isDesktopRuntime } from '../utils/runtimeMode';

const TOKEN_KEY = 'dbharbor_jwt_token';
const UPDATE_CHANNEL_KEY = 'dbharbor_update_channel';
const AUTO_UPDATE_REMIND_KEY = 'dbharbor_auto_update_remind';
const SKIPPED_UPDATE_VERSION_KEY = 'dbharbor_skipped_update_version';

export function getSavedUpdateChannel(): UpdateChannel | undefined {
  const raw = localStorage.getItem(UPDATE_CHANNEL_KEY);
  return raw === 'preview' || raw === 'release' ? raw : undefined;
}

export function setSavedUpdateChannel(channel: UpdateChannel) {
  localStorage.setItem(UPDATE_CHANNEL_KEY, channel);
}

export function getAutoUpdateRemindEnabled(): boolean {
  return localStorage.getItem(AUTO_UPDATE_REMIND_KEY) !== 'false';
}

export function setAutoUpdateRemindEnabled(enabled: boolean) {
  localStorage.setItem(AUTO_UPDATE_REMIND_KEY, enabled ? 'true' : 'false');
}

export function getSkippedUpdateVersion(): string {
  return localStorage.getItem(SKIPPED_UPDATE_VERSION_KEY) || '';
}

export function setSkippedUpdateVersion(version: string) {
  if (version) {
    localStorage.setItem(SKIPPED_UPDATE_VERSION_KEY, version);
  } else {
    localStorage.removeItem(SKIPPED_UPDATE_VERSION_KEY);
  }
}

export function getAuthToken(): string {
  return localStorage.getItem(TOKEN_KEY) || '';
}

export function setAuthToken(token: string) {
  if (token) {
    localStorage.setItem(TOKEN_KEY, token);
  } else {
    localStorage.removeItem(TOKEN_KEY);
  }
}

export class ApiError extends Error {
  status: number;
  data: Record<string, any>;

  constructor(message: string, status: number, data: Record<string, any> = {}) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.data = data;
  }
}

interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'DELETE';
  params?: Record<string, string | number | boolean | undefined>;
  body?: unknown;
  signal?: AbortSignal;
  skipAuthRedirect?: boolean;
}

async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const { method = 'GET', params, body, signal, skipAuthRedirect = false } = opts;

  let url = `/api${path}`;
  if (params) {
    const qs = new URLSearchParams();
    Object.entries(params).forEach(([k, v]) => {
      if (v !== undefined && v !== null && v !== '') {
        qs.set(k, String(v));
      }
    });
    const qStr = qs.toString();
    if (qStr) url += `${url.includes('?') ? '&' : '?'}${qStr}`;
  }

  const locale = detectInitialLocale();
  const headers: Record<string, string> = {
    'Accept-Language': locale,
  };
  const desktopToken = getRuntimeBootMeta().desktopToken;
  if (desktopToken) {
    headers['X-DBHarbor-Desktop'] = desktopToken;
  }
  const token = getAuthToken();
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }

  let fetchBody: BodyInit | undefined;
  if (body instanceof FormData) {
    fetchBody = body;
  } else if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
    fetchBody = JSON.stringify(body);
  }

  const res = await fetch(url, {
    method,
    headers,
    body: fetchBody,
    credentials: 'same-origin',
    signal,
  });

  const text = await res.text();
  let data: any = {};
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = { error: text };
    }
  }

  if (!res.ok) {
    if (
      res.status === 401 &&
      !skipAuthRedirect &&
      !isDesktopRuntime() &&
      !path.startsWith('/auth/login') &&
      !path.startsWith('/auth/setup') &&
      !path.startsWith('/auth/status')
    ) {
      setAuthToken('');
      window.dispatchEvent(new Event('dbharbor:unauthorized'));
    }
    const msg = data?.error || `Request failed (HTTP ${res.status})`;
    throw new ApiError(msg, res.status, data || {});
  }

  return data as T;
}

export const api = {
  // Authentication, Initial Setup, Branding, Security & 2FA
  getAuthStatus: () => request<AuthStatusResponse>('/auth/status', { skipAuthRedirect: true }),

  setupInitialAdmin: (payload: {
    username: string;
    displayName?: string;
    password: string;
    rememberMe?: boolean;
    branding?: Partial<LoginBrandingConfig>;
  }) =>
    request<LoginResponse>('/auth/setup', {
      method: 'POST',
      body: payload,
      skipAuthRedirect: true,
    }),

  getCaptcha: () =>
    request<{ captchaToken: string; captchaImage: string }>('/auth/captcha', { skipAuthRedirect: true }),

  login: (payload: {
    username: string;
    password: string;
    captchaToken?: string;
    captchaAnswer?: string;
    rememberMe?: boolean;
  }) =>
    request<LoginResponse>('/auth/login', {
      method: 'POST',
      body: payload,
      skipAuthRedirect: true,
    }),

  verify2FALogin: (payload: { challengeToken: string; code: string; rememberMe?: boolean }) =>
    request<LoginResponse>('/auth/login/2fa', {
      method: 'POST',
      body: payload,
      skipAuthRedirect: true,
    }),

  logout: () => request<{ message: string }>('/auth/logout', { method: 'POST', skipAuthRedirect: true }),

  me: () =>
    request<{
      userId: string;
      username: string;
      displayName?: string;
      role?: UserRole;
      canCreateConn?: boolean;
      authEnabled?: boolean;
      totpEnabled?: boolean;
      passwordChanged?: boolean;
      recoveryCodesRemaining?: number;
    }>('/auth/me'),

  updateProfile: (displayName: string) =>
    request<{ message: string; displayName: string }>('/auth/profile', {
      method: 'PUT',
      body: { displayName },
    }),

  getSecurityStatus: () => request<SecurityStatus>('/auth/security'),

  changePassword: (oldPassword: string, newPassword: string, revokeOtherSessions = true) =>
    request<{ message: string; revokedSessions: number }>('/auth/password', {
      method: 'PUT',
      body: { oldPassword, newPassword, revokeOtherSessions },
    }),

  begin2FASetup: () =>
    request<{ secret: string; otpauthUri: string; qrCodeDataUrl: string }>('/auth/2fa/setup', {
      method: 'POST',
    }),

  enable2FA: (code: string) =>
    request<{ message: string; recoveryCodes: string[] }>('/auth/2fa/enable', {
      method: 'POST',
      body: { code },
    }),

  disable2FA: (password: string, code: string) =>
    request<{ message: string }>('/auth/2fa/disable', {
      method: 'POST',
      body: { password, code },
    }),

  regenerateRecoveryCodes: (password: string, code: string) =>
    request<{ recoveryCodes: string[] }>('/auth/2fa/recovery/regenerate', {
      method: 'POST',
      body: { password, code },
    }),

  listSessions: async () => {
    const data = await request<{ sessions: AuthSession[] }>('/auth/sessions');
    return data.sessions || [];
  },

  revokeSession: (sid: string) =>
    request<{ message: string }>(`/auth/sessions/${encodeURIComponent(sid)}`, { method: 'DELETE' }),

  revokeOtherSessions: () =>
    request<{ revoked: number; message: string }>('/auth/sessions/revoke-others', { method: 'POST' }),

  listAuditLogs: async (
    opts: number | { limit?: number; eventType?: string; username?: string; keyword?: string } = 80
  ) => {
    const params = typeof opts === 'number' ? { limit: opts } : { limit: opts.limit ?? 80, ...opts };
    const data = await request<{ logs: AuditLogEntry[] }>('/auth/audit-logs', { params });
    return data.logs || [];
  },

  // Administrator Team & RBAC User Governance
  adminListUsers: async () => {
    const data = await request<{ users: ManagedUser[] }>('/admin/users');
    return data.users || [];
  },
  adminCreateUser: (payload: {
    username: string;
    displayName?: string;
    password: string;
    role: UserRole;
    canCreateConn?: boolean;
    connScope?: 'all' | 'selected';
    allowedConnIds?: string[];
    forcePasswordChange?: boolean;
  }) => request<ManagedUser>('/admin/users', { method: 'POST', body: payload }),
  adminUpdateUser: (
    uid: string,
    payload: {
      displayName?: string;
      role?: UserRole;
      disabled?: boolean;
      canCreateConn?: boolean;
      connScope?: 'all' | 'selected';
      allowedConnIds?: string[];
    }
  ) => request<ManagedUser>(`/admin/users/${encodeURIComponent(uid)}`, { method: 'PUT', body: payload }),
  adminDeleteUser: (uid: string) =>
    request<{ message: string }>(`/admin/users/${encodeURIComponent(uid)}`, { method: 'DELETE' }),
  adminResetPassword: (uid: string, newPassword: string, forcePasswordChange = true) =>
    request<{ message: string; revokedSessions: number }>(`/admin/users/${encodeURIComponent(uid)}/password`, {
      method: 'PUT',
      body: { newPassword, forcePasswordChange },
    }),
  adminReset2FA: (uid: string) =>
    request<{ message: string }>(`/admin/users/${encodeURIComponent(uid)}/reset-2fa`, { method: 'POST' }),
  adminRevokeUserSessions: (uid: string) =>
    request<{ message: string; revoked: number }>(`/admin/users/${encodeURIComponent(uid)}/revoke-sessions`, {
      method: 'POST',
    }),
  adminGetLoginBranding: () => request<{ branding: LoginBrandingConfig }>('/admin/login-branding'),
  adminSaveLoginBranding: (payload: LoginBrandingConfig) =>
    request<{ message: string; branding: LoginBrandingConfig }>('/admin/login-branding', {
      method: 'PUT',
      body: payload,
    }),

  // System Runtime Stats, Version & GitHub Update Check
  getVersion: (lang?: string) =>
    request<BuildVersionInfo>('/version', {
      params: lang ? { lang } : undefined,
      skipAuthRedirect: true,
    }),
  getChangelog: (lang?: string) =>
    request<{ lang: string; markdown: string }>('/version/changelog', {
      params: { lang: lang || detectInitialLocale() },
      skipAuthRedirect: true,
    }),
  checkUpdate: (force = false, lang?: string, channel?: UpdateChannel) => {
    const locale = lang || detectInitialLocale();
    const resolvedChannel = channel || getSavedUpdateChannel();
    return request<UpdateCheckResult>(`/system/update-check?lang=${encodeURIComponent(locale)}`, {
      params: {
        force: force ? 'true' : undefined,
        channel: resolvedChannel,
      },
    });
  },
  getSystemStats: () => request<SystemStats>('/system/stats'),
  releaseIdlePools: () =>
    request<{ released: number; message: string }>('/system/pools/release-idle', { method: 'POST' }),
  setDesktopWindowTheme: (darkMode: boolean) =>
    request<{ darkMode: boolean }>('/desktop/window-theme', {
      method: 'POST',
      body: { darkMode },
      skipAuthRedirect: true,
    }),
  pickDesktopFile: (kind: string, currentPath = '') =>
    request<{ canceled: boolean; unsupported?: boolean; filePath?: string }>('/desktop/pick-file', {
      method: 'POST',
      body: { kind, currentPath },
    }),
  browseLocalFS: (path = '', all = false) =>
    request<LocalFSBrowseResponse>('/fs/browse', {
      params: { path: path || undefined, all: all ? '1' : undefined },
    }),
  uploadLocalDBFile: (file: File, overwrite = false) => {
    const fd = new FormData();
    fd.append('file', file);
    if (overwrite) fd.append('overwrite', 'true');
    return request<{ filePath: string; fileName: string; sizeBytes: number }>('/fs/upload-db', {
      method: 'POST',
      body: fd,
    });
  },
  openEphemeralDBFile: (file: File, driverType: 'auto' | 'sqlite' | 'duckdb' = 'auto') => {
    const fd = new FormData();
    fd.append('file', file);
    if (driverType && driverType !== 'auto') {
      fd.append('driverType', driverType);
    }
    return request<EphemeralOpenResponse>('/fs/ephemeral-open', {
      method: 'POST',
      body: fd,
    });
  },
  fetchEphemeralDBFileBlob: async (connId: string): Promise<Blob> => {
    const locale = detectInitialLocale();
    const headers: Record<string, string> = {
      'Accept-Language': locale,
    };
    const desktopToken = getRuntimeBootMeta().desktopToken;
    if (desktopToken) {
      headers['X-DBHarbor-Desktop'] = desktopToken;
    }
    const token = getAuthToken();
    if (token) {
      headers.Authorization = `Bearer ${token}`;
    }
    const res = await fetch(`/api/conn/${encodeURIComponent(connId)}/file/export`, {
      method: 'GET',
      headers,
      credentials: 'same-origin',
    });
    if (!res.ok) {
      if (res.status === 401 && !isDesktopRuntime()) {
        setAuthToken('');
        window.dispatchEvent(new CustomEvent('dbharbor:unauthorized'));
      }
      const text = await res.text();
      let errMsg = `Export failed (HTTP ${res.status})`;
      try {
        const parsed = JSON.parse(text);
        if (parsed?.error) errMsg = parsed.error;
      } catch {
        if (text) errMsg = text;
      }
      throw new ApiError(errMsg, res.status);
    }
    return res.blob();
  },
  triggerEphemeralDBFileDownload: async (connId: string): Promise<void> => {
    const { ticket } = await request<{ ticket: string }>('/auth/ticket', { method: 'POST' });
    const a = document.createElement('a');
    a.href = `/api/conn/${encodeURIComponent(connId)}/file/export?ticket=${encodeURIComponent(ticket)}`;
    a.rel = 'noopener';
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
  },

  listDrivers: async () => {
    const data = await request<{ drivers: DriverMeta[] }>('/drivers');
    return data.drivers || [];
  },

  // Connections
  listConnections: async () => {
    const data = await request<{ connections: SavedConnection[] }>('/connections');
    return data.connections || [];
  },
  testConnection: (payload: ConnectionFormPayload) =>
    request<ServerInfo>('/connections/test', { method: 'POST', body: payload }),
  pingConnection: (id: string) =>
    request<ServerInfo>(`/connections/${id}/ping`, { method: 'POST' }),
  disconnectConnection: (id: string) =>
    request<{ message: string }>(`/connections/${id}/disconnect`, { method: 'POST' }),
  createConnection: (payload: ConnectionFormPayload) =>
    request<SavedConnection>('/connections', { method: 'POST', body: payload }),
  updateConnection: (id: string, payload: ConnectionFormPayload) =>
    request<SavedConnection>(`/connections/${id}`, { method: 'PUT', body: payload }),
  deleteConnection: (id: string) =>
    request<{ message: string }>(`/connections/${id}`, { method: 'DELETE' }),
  duplicateConnection: (id: string) =>
    request<SavedConnection>(`/connections/${id}/duplicate`, { method: 'POST' }),
  exportConnections: () =>
    request<{ service: string; exportedAt: string; connections: ExportedConnectionProfile[] }>('/connections/export'),
  importConnections: (connections: ExportedConnectionProfile[]) =>
    request<{ imported: number; message: string }>('/connections/import', {
      method: 'POST',
      body: { connections },
    }),

  // Databases & Schema
  listDatabases: async (connId: string) => {
    const data = await request<{ databases: DatabaseSummary[] }>(`/conn/${connId}/databases`);
    return data.databases || [];
  },
  createDatabase: (connId: string, name: string, charset?: string) =>
    request<{ message: string }>(`/conn/${connId}/databases`, { method: 'POST', body: { name, charset } }),
  dropDatabase: (connId: string, name: string) =>
    request<{ message: string }>(`/conn/${connId}/databases`, { method: 'DELETE', params: { name } }),
  listSchemas: async (connId: string, database: string) => {
    const data = await request<{ schemas: string[] }>(`/conn/${connId}/schemas`, { params: { database } });
    return data.schemas || [];
  },
  createSchema: (connId: string, database: string, schema: string) =>
    request<{ message: string }>(`/conn/${connId}/schemas`, { method: 'POST', body: { database, schema } }),
  dropSchema: (connId: string, database: string, schema: string, cascade = false) =>
    request<{ message: string }>(`/conn/${connId}/schemas`, {
      method: 'DELETE',
      params: { database, schema, cascade: cascade ? 'true' : undefined },
    }),
  listObjects: (connId: string, database: string, schema = '') =>
    request<SchemaObjects>(`/conn/${connId}/objects`, { params: { database, schema } }),
  getTableStructure: (connId: string, database: string, schema: string, table: string) =>
    request<TableStructure>(`/conn/${connId}/table/structure`, { params: { database, schema, table } }),
  getObjectDDL: (connId: string, database: string, schema: string, objectType: string, objectName: string) =>
    request<{ ddl: string }>(`/conn/${connId}/object/ddl`, {
      params: { database, schema, objectType, objectName },
    }),

  // Database Engine Users & Privilege Governance
  dbPrivCapabilities: (connId: string) =>
    request<DbPrivilegeCapabilities>(`/conn/${connId}/privileges/capabilities`),
  dbListUsers: async (connId: string, database = '') => {
    const data = await request<{ users: DbAccountInfo[] }>(`/conn/${connId}/privileges/users`, {
      params: { database },
    });
    return data.users || [];
  },
  dbGetUserGrants: async (connId: string, username: string, host = '', database = '') => {
    const data = await request<{ grants: DbGrantEntry[] }>(`/conn/${connId}/privileges/grants`, {
      params: { username, host, database },
    });
    return data.grants || [];
  },
  dbPreviewPrivilegeAction: (connId: string, payload: DbPrivilegeActionPayload) =>
    request<{ statements: string[] }>(`/conn/${connId}/privileges/preview`, {
      method: 'POST',
      body: payload,
    }),
  dbExecutePrivilegeAction: (connId: string, payload: DbPrivilegeActionPayload) =>
    request<{ message: string; statements: string[] }>(`/conn/${connId}/privileges/execute`, {
      method: 'POST',
      body: payload,
    }),

  createTable: async (
    connId: string,
    payload: {
      database: string;
      schema?: string;
      table: string;
      columns: ColumnDef[];
      indexes?: IndexDef[];
      foreignKeys?: ForeignKeyDef[];
      checkConstraints?: CheckConstraintDef[];
      engine?: string;
      orderBy?: string;
      partitionBy?: string;
      charset?: string;
      comment?: string;
      strict?: boolean;
      withoutRowId?: boolean;
      virtualModule?: string;
      virtualArgs?: string;
      previewOnly?: boolean;
    }
  ) => {
    const data = await request<{ ddl: string }>(`/conn/${connId}/table/create`, {
      method: 'POST',
      body: payload,
    });
    return data.ddl;
  },

  alterTable: async (
    connId: string,
    payload: {
      database: string;
      schema?: string;
      table: string;
      actions: AlterAction[];
      previewOnly?: boolean;
    }
  ) => {
    const data = await request<{ sqls: string[] }>(`/conn/${connId}/table/alter`, {
      method: 'POST',
      body: payload,
    });
    return data.sqls || [];
  },

  objectAction: (
    connId: string,
    payload: ObjectActionPayload & { viewSQL?: string }
  ) => request<{ message: string; sqls?: string[] }>(`/conn/${connId}/object/action`, { method: 'POST', body: payload }),

  // Data Browse & Mutations
  browseData: (
    connId: string,
    payload: {
      database: string;
      schema?: string;
      table: string;
      page: number;
      pageSize: number;
      filter?: string;
      sorts?: { column: string; direction: string }[];
    },
    signal?: AbortSignal
  ) => request<QueryResult>(`/conn/${connId}/table/browse`, { method: 'POST', body: payload, signal }),

  previewMutations: async (
    connId: string,
    payload: {
      database: string;
      schema?: string;
      table: string;
      primaryKeys?: string[];
      inserts: Record<string, any>[];
      updates: { locator: Record<string, any>; changes: Record<string, any> }[];
      deletes: Record<string, any>[];
    }
  ) => {
    const data = await request<{ sqls: string[] }>(`/conn/${connId}/table/mutate/preview`, {
      method: 'POST',
      body: payload,
    });
    return data.sqls || [];
  },

  applyMutations: (
    connId: string,
    payload: {
      database: string;
      schema?: string;
      table: string;
      primaryKeys?: string[];
      inserts: Record<string, any>[];
      updates: { locator: Record<string, any>; changes: Record<string, any> }[];
      deletes: Record<string, any>[];
    }
  ) =>
    request<{ inserted: number; updated: number; deleted: number; executedSql: string[] }>(
      `/conn/${connId}/table/mutate`,
      { method: 'POST', body: payload }
    ),

  // Cross-Engine Table Partitions
  listPartitions: async (connId: string, database: string, table: string, schema = '') => {
    const data = await request<{ partitions: PartitionInfo[] }>(`/conn/${connId}/table/partitions`, {
      params: { database, schema: schema || undefined, table },
    });
    return data.partitions || [];
  },
  managePartition: (
    connId: string,
    databaseOrPayload:
      | string
      | {
          database: string;
          schema?: string;
          table: string;
          partitionId: string;
          action: string;
          expression?: string;
          previewOnly?: boolean;
        },
    table?: string,
    partitionId?: string,
    action?: string
  ) => {
    const body =
      typeof databaseOrPayload === 'string'
        ? { database: databaseOrPayload, table: table || '', partitionId: partitionId || '', action: action || '' }
        : databaseOrPayload;
    return request<{ message: string; sql?: string }>(`/conn/${connId}/table/partitions/action`, {
      method: 'POST',
      body,
    });
  },

  // SQL Console & Autocomplete
  executeQuery: (connId: string, database: string, query: string, maxRows = 1000, signal?: AbortSignal) =>
    request<{ results: QueryResult[]; totalElapsedMs: number }>(`/conn/${connId}/query`, {
      method: 'POST',
      body: { database, query, maxRows },
      signal,
    }),

  getAutocomplete: (connId: string, database: string) =>
    request<{ databases: string[]; tables: Record<string, string[]> }>(`/conn/${connId}/autocomplete`, {
      params: { database },
    }),

  listHistory: async (connectionId = '') => {
    const data = await request<{ history: HistoryEntry[] }>('/history', { params: { connectionId } });
    return data.history || [];
  },
  clearHistory: (connectionId = '') =>
    request<{ message: string }>('/history', { method: 'DELETE', params: { connectionId } }),

  listSnippets: async () => {
    const data = await request<{ snippets: SavedSnippet[] }>('/snippets');
    return data.snippets || [];
  },
  saveSnippet: (snippet: SavedSnippet) =>
    request<SavedSnippet>('/snippets', { method: 'POST', body: snippet }),
  deleteSnippet: (id: string) =>
    request<{ message: string }>(`/snippets/${id}`, { method: 'DELETE' }),

  // Export & Import (Single-Use Ticket Auth + Direct SQL Execution)
  triggerExportDownload: async (
    connId: string,
    params: {
      database: string;
      schema?: string;
      table?: string;
      tables?: string[];
      sql?: string;
      format: string;
      includeDdl?: boolean;
      dropIfExists?: boolean;
      maxRows?: number;
    }
  ): Promise<DesktopExportResult> => {
    if (isDesktopRuntime()) {
      return request<DesktopExportResult>(`/conn/${connId}/export/desktop-save`, {
        method: 'POST',
        body: params,
      });
    }

    const { ticket } = await request<{ ticket: string }>('/auth/ticket', { method: 'POST' });
    const qs = new URLSearchParams();
    qs.set('database', params.database || '');
    if (params.schema) qs.set('schema', params.schema);
    if (params.table) qs.set('table', params.table);
    if (params.tables && params.tables.length > 0) qs.set('tables', params.tables.join(','));
    if (params.sql) qs.set('sql', params.sql);
    qs.set('format', params.format);
    if (params.includeDdl) qs.set('includeDdl', 'true');
    if (params.dropIfExists) qs.set('dropIfExists', 'true');
    if (params.maxRows !== undefined) qs.set('maxRows', String(params.maxRows));
    qs.set('ticket', ticket);

    const a = document.createElement('a');
    a.href = `/api/conn/${connId}/export?${qs.toString()}`;
    a.rel = 'noopener';
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    return { canceled: false };
  },

  importFile: (
    connId: string,
    payload: {
      database: string;
      schema?: string;
      table?: string;
      format: string;
      continueOnError?: boolean;
      file: File;
    }
  ) => {
    const fd = new FormData();
    fd.append('database', payload.database);
    if (payload.schema) fd.append('schema', payload.schema);
    if (payload.table) fd.append('table', payload.table);
    fd.append('format', payload.format);
    if (payload.continueOnError) fd.append('continueOnError', 'true');
    fd.append('file', payload.file);
    return request<ImportResult>(`/conn/${connId}/import`, { method: 'POST', body: fd });
  },

  importSQLText: (
    connId: string,
    payload: { database: string; schema?: string; sql: string; continueOnError?: boolean }
  ) => request<ImportResult>(`/conn/${connId}/import/sql`, { method: 'POST', body: payload }),

  // Redis KV
  kvScan: (connId: string, db: number, pattern = '*', cursor = 0) =>
    request<{ cursor: number; keys: KVKeySummary[]; dbKeys: Record<string, number> }>(`/conn/${connId}/kv/scan`, {
      params: { db, pattern, cursor, count: 250 },
    }),
  kvGetKey: (connId: string, db: number, key: string) =>
    request<KVEntryDetail>(`/conn/${connId}/kv/key`, { params: { db, key } }),
  kvSetKey: (connId: string, payload: any) =>
    request<{ message: string }>(`/conn/${connId}/kv/key`, { method: 'POST', body: payload }),
  kvMutateItem: (connId: string, payload: any) =>
    request<{ message: string }>(`/conn/${connId}/kv/item`, { method: 'PUT', body: payload }),
  kvDeleteKeys: (connId: string, db: number, keys: string[]) =>
    request<{ deleted: number }>(`/conn/${connId}/kv/delete`, { method: 'POST', body: { db, keys } }),
  kvSetTTL: (connId: string, db: number, key: string, ttl: number) =>
    request<{ message: string }>(`/conn/${connId}/kv/ttl`, { method: 'PUT', body: { db, key, ttl } }),
  kvRenameKey: (connId: string, db: number, oldKey: string, newKey: string, nxOnly = true) =>
    request<{ message: string }>(`/conn/${connId}/kv/rename`, {
      method: 'PUT',
      body: { db, oldKey, newKey, nxOnly },
    }),
  kvDuplicateKey: (connId: string, db: number, srcKey: string, dstKey: string, replace = false) =>
    request<{ message: string }>(`/conn/${connId}/kv/duplicate`, {
      method: 'POST',
      body: { db, srcKey, dstKey, replace },
    }),
  kvFlushDB: (connId: string, db: number) =>
    request<{ message: string }>(`/conn/${connId}/kv/flush`, { method: 'POST', body: { db } }),
  kvCommand: (connId: string, db: number, command: string) =>
    request<{ command: string; output: string; elapsedMs: number }>(`/conn/${connId}/kv/cmd`, {
      method: 'POST',
      body: { db, command },
    }),
  kvExportKeys: (connId: string, payload: { db: number; pattern?: string; keys?: string[]; maxKeys?: number }) =>
    request<KVBackupPayload>(`/conn/${connId}/kv/export`, {
      method: 'POST',
      body: payload,
    }),
  kvImportKeys: (connId: string, payload: { db: number; overwrite?: boolean; entries: any[] }) =>
    request<{ imported: number; skipped: number }>(`/conn/${connId}/kv/import`, {
      method: 'POST',
      body: payload,
    }),
  kvStreamInfo: (connId: string, db: number, key: string) =>
    request<KVStreamInfo>(`/conn/${connId}/kv/stream-info`, { params: { db, key } }),
  kvStreamGroup: (
    connId: string,
    payload: {
      db: number;
      key: string;
      action: 'CREATE' | 'SETID' | 'DESTROY' | 'DELCONSUMER';
      group: string;
      startId?: string;
      consumer?: string;
    }
  ) =>
    request<{ message: string }>(`/conn/${connId}/kv/stream-group`, {
      method: 'POST',
      body: payload,
    }),
  kvGetDiagnostics: (connId: string, params: { kind: 'slowlog' | 'config'; count?: number; pattern?: string }) =>
    request<{ slowlog?: KVSlowlogEntry[]; config?: KVConfigItem[] }>(`/conn/${connId}/kv/diagnostics`, { params }),
  kvDiagnosticsAction: (
    connId: string,
    payload: {
      action: 'RESET_SLOWLOG' | 'SET_CONFIG' | 'PUBLISH';
      key?: string;
      value?: string;
      channel?: string;
      message?: string;
    }
  ) =>
    request<{ message: string; receivers?: number }>(`/conn/${connId}/kv/diagnostics`, {
      method: 'POST',
      body: payload,
    }),

  // MongoDB Document
  docListCollections: async (connId: string, database: string) => {
    const data = await request<{ collections: CollectionSummary[] }>(`/conn/${connId}/doc/collections`, {
      params: { database },
    });
    return data.collections || [];
  },
  docGetCollectionOptions: (connId: string, database: string, collection: string) =>
    request<CollectionSummary>(`/conn/${connId}/doc/collections/options`, {
      params: { database, collection },
    }),
  docCreateCollection: (
    connId: string,
    databaseOrPayload:
      | string
      | {
          database: string;
          collection: string;
          type?: string;
          capped?: boolean;
          maxBytes?: number;
          maxDocs?: number;
          viewOn?: string;
          pipelineJson?: string;
          timeField?: string;
          metaField?: string;
          granularity?: string;
          expireAfterSeconds?: number;
          validatorJson?: string;
          validationLevel?: string;
          validationAction?: string;
        },
    collection?: string
  ) => {
    const body =
      typeof databaseOrPayload === 'string'
        ? { database: databaseOrPayload, collection: collection || '' }
        : databaseOrPayload;
    return request<{ message: string }>(`/conn/${connId}/doc/collections`, {
      method: 'POST',
      body,
    });
  },
  docUpdateCollectionOptions: (
    connId: string,
    payload: {
      database: string;
      collection: string;
      viewOn?: string;
      pipelineJson?: string;
      validatorJson?: string;
      validationLevel?: string;
      validationAction?: string;
      expireAfterSeconds?: number;
    }
  ) =>
    request<{ message: string }>(`/conn/${connId}/doc/collections/options`, {
      method: 'PUT',
      body: payload,
    }),
  docDropCollection: (connId: string, database: string, collection: string) =>
    request<{ message: string }>(`/conn/${connId}/doc/collections`, {
      method: 'DELETE',
      params: { database, collection },
    }),
  docRenameCollection: (connId: string, database: string, oldName: string, newName: string) =>
    request<{ message: string }>(`/conn/${connId}/doc/collections/rename`, {
      method: 'PUT',
      body: { database, oldName, newName },
    }),
  triggerDocExportDownload: async (
    connId: string,
    params: {
      database: string;
      collection: string;
      filterJson?: string;
      sortJson?: string;
      format: 'json' | 'jsonl' | 'csv' | 'csv_bom';
      maxRows?: number;
    }
  ): Promise<DesktopExportResult> => {
    if (isDesktopRuntime()) {
      return request<DesktopExportResult>(`/conn/${connId}/doc/export/desktop-save`, {
        method: 'POST',
        body: params,
      });
    }
    const { ticket } = await request<{ ticket: string }>('/auth/ticket', { method: 'POST' });
    const qs = new URLSearchParams();
    qs.set('database', params.database);
    qs.set('collection', params.collection);
    if (params.filterJson) qs.set('filterJson', params.filterJson);
    if (params.sortJson) qs.set('sortJson', params.sortJson);
    qs.set('format', params.format);
    if (params.maxRows !== undefined) qs.set('maxRows', String(params.maxRows));
    qs.set('ticket', ticket);

    const a = document.createElement('a');
    a.href = `/api/conn/${connId}/doc/export?${qs.toString()}`;
    a.rel = 'noopener';
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    return { canceled: false };
  },
  docFind: (
    connId: string,
    payload: {
      database: string;
      collection: string;
      filterJson: string;
      sortJson: string;
      projectionJson: string;
      page: number;
      pageSize: number;
    }
  ) =>
    request<{ documents: Record<string, any>[]; total: number; elapsedMs: number }>(`/conn/${connId}/doc/find`, {
      method: 'POST',
      body: payload,
    }),
  docInsert: (connId: string, database: string, collection: string, document: string) =>
    request<{ message: string }>(`/conn/${connId}/doc/insert`, {
      method: 'POST',
      body: { database, collection, document },
    }),
  docImportFile: (
    connId: string,
    payload: { database: string; collection: string; format?: string; continueOnError?: boolean; file: File }
  ) => {
    const fd = new FormData();
    fd.append('database', payload.database);
    fd.append('collection', payload.collection);
    if (payload.format) fd.append('format', payload.format);
    if (payload.continueOnError) fd.append('continueOnError', 'true');
    fd.append('file', payload.file);
    return request<ImportResult>(`/conn/${connId}/doc/import`, {
      method: 'POST',
      body: fd,
    });
  },
  docImportText: (
    connId: string,
    payload: { database: string; collection: string; format?: string; content: string; continueOnError?: boolean }
  ) =>
    request<ImportResult>(`/conn/${connId}/doc/import`, {
      method: 'POST',
      body: payload,
    }),
  docUpdate: (connId: string, database: string, collection: string, id: string, document: string) =>
    request<{ message: string }>(`/conn/${connId}/doc/update`, {
      method: 'PUT',
      body: { database, collection, id, document },
    }),
  docDelete: (connId: string, database: string, collection: string, ids: string[]) =>
    request<{ message: string }>(`/conn/${connId}/doc/delete`, {
      method: 'POST',
      body: { database, collection, ids },
    }),
  docListIndexes: async (connId: string, database: string, collection: string) => {
    const data = await request<{ indexes: DocIndexInfo[] }>(`/conn/${connId}/doc/indexes`, {
      params: { database, collection },
    });
    return data.indexes || [];
  },
  docCreateIndex: (
    connId: string,
    databaseOrPayload:
      | string
      | {
          database: string;
          collection: string;
          name?: string;
          keysJson: string;
          unique?: boolean;
          sparse?: boolean;
          expireAfterSeconds?: number;
          partialFilterJson?: string;
        },
    collection?: string,
    keysJson?: string,
    unique?: boolean
  ) => {
    const body =
      typeof databaseOrPayload === 'string'
        ? { database: databaseOrPayload, collection: collection || '', keysJson: keysJson || '{}', unique: !!unique }
        : databaseOrPayload;
    return request<{ message: string }>(`/conn/${connId}/doc/indexes`, {
      method: 'POST',
      body,
    });
  },
  docDropIndex: (connId: string, database: string, collection: string, name: string) =>
    request<{ message: string }>(`/conn/${connId}/doc/indexes`, {
      method: 'DELETE',
      params: { database, collection, name },
    }),
  docRunCommand: (connId: string, database: string, commandJson: string) =>
    request<{ result: any; elapsedMs: number }>(`/conn/${connId}/doc/command`, {
      method: 'POST',
      body: { database, commandJson },
    }),

  // Live Monitor
  getMonitor: (connId: string) => request<MonitorSnapshot>(`/conn/${connId}/monitor`),
  killSession: (connId: string, sessionId: string) =>
    request<{ message: string }>(`/conn/${connId}/monitor/kill`, { method: 'POST', body: { sessionId } }),
};
