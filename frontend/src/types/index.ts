export type DriverCategory = 'relational' | 'olap' | 'keyvalue' | 'document';

export type UserRole = 'admin' | 'readwrite' | 'readonly';

export type ConnectionVisibility = 'shared' | 'private';

export interface SelectOption {
  label: string;
  value: string;
}

export interface ConnFieldSchema {
  key: string;
  label: string;
  type: 'text' | 'number' | 'password' | 'select' | 'switch';
  target: 'root' | 'options';
  placeholder?: string;
  defaultValue?: string;
  required?: boolean;
  selectOptions?: SelectOption[];
}

export interface DriverMeta {
  type: string;
  name: string;
  category: DriverCategory;
  badgeColor: string;
  defaultPort: number;
  description: string;
  supportsSchemas: boolean;
  supportsPartitions: boolean;
  supportsExplain: boolean;
  defaultTypes?: string[];
  fields: ConnFieldSchema[];
}

export interface ConnectionFormPayload {
  id?: string;
  name: string;
  type: string;
  color?: string;
  host?: string;
  port?: number;
  username?: string;
  password?: string;
  database?: string;
  filePath?: string;
  uri?: string;
  options?: Record<string, string>;
  keepExistingSecret?: boolean;
  visibility?: ConnectionVisibility;
}

export interface SavedConnection {
  id: string;
  name: string;
  type: string;
  color: string;
  host: string;
  port: number;
  username: string;
  hasPassword: boolean;
  database: string;
  filePath: string;
  hasUri: boolean;
  options: Record<string, string>;
  ownerId?: string;
  visibility?: ConnectionVisibility;
  editable?: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface ExportedConnectionProfile {
  name: string;
  type: string;
  color?: string;
  host?: string;
  port?: number;
  username?: string;
  database?: string;
  filePath?: string;
  options?: Record<string, string>;
  visibility?: ConnectionVisibility;
}

export interface ManagedUser {
  id: string;
  username: string;
  displayName: string;
  role: UserRole;
  disabled: boolean;
  canCreateConn: boolean;
  connScope: 'all' | 'selected';
  allowedConnIds: string[];
  totpEnabled: boolean;
  passwordChanged: boolean;
  activeSessionsCount: number;
  createdAt: string;
}

export interface ServerInfo {
  version: string;
  product: string;
  latencyMs: number;
}

export interface DatabaseSummary {
  name: string;
  sizeBytes?: number;
  tablesCount?: number;
  charset?: string;
}

export interface ObjectItem {
  name: string;
  type: string;
  engine?: string;
  rows: number;
  dataBytes: number;
  indexBytes?: number;
  collation?: string;
  comment?: string;
}

export interface SchemaObjects {
  database: string;
  schema: string;
  tables: ObjectItem[];
  views: ObjectItem[];
  routines: ObjectItem[];
  triggers?: ObjectItem[];
}

export interface ColumnDef {
  name: string;
  type: string;
  nullable: boolean;
  defaultValue?: string;
  isPrimaryKey: boolean;
  isAutoIncrement: boolean;
  comment?: string;
  extra?: string;
}

export interface AlterAction {
  action:
    | 'ADD_COLUMN'
    | 'MODIFY_COLUMN'
    | 'RENAME_COLUMN'
    | 'DROP_COLUMN'
    | 'ADD_INDEX'
    | 'DROP_INDEX'
    | 'ADD_FOREIGN_KEY'
    | 'DROP_FOREIGN_KEY'
    | 'RENAME_TABLE'
    | 'COMMENT_TABLE';
  column?: ColumnDef;
  oldName?: string;
  newName?: string;
  comment?: string;
  afterColumn?: string;
  index?: IndexDef;
  foreignKey?: ForeignKeyDef;
}

export interface IndexDef {
  name: string;
  columns: string[];
  unique: boolean;
  primary: boolean;
  indexType?: string;
}

export interface ForeignKeyDef {
  name: string;
  columns: string[];
  refTable: string;
  refColumns: string[];
  onUpdate?: string;
  onDelete?: string;
}

export interface TableStructure {
  database: string;
  schema?: string;
  table: string;
  engine?: string;
  charset?: string;
  collation?: string;
  comment?: string;
  ddl: string;
  columns: ColumnDef[];
  indexes: IndexDef[];
  foreignKeys: ForeignKeyDef[];
  options?: Record<string, string>;
}

export interface QueryColumn {
  name: string;
  type: string;
  nullable?: boolean;
}

export interface QueryResult {
  statement: string;
  columns: QueryColumn[];
  rows: any[][];
  total: number;
  hasTotal: boolean;
  affectedRows: number;
  elapsedMs: number;
  isSelect: boolean;
  primaryKeys?: string[];
  message?: string;
}

export interface PartitionInfo {
  id: string;
  name: string;
  rows: number;
  bytesOnDisk: number;
  partsCount: number;
  active: boolean;
}

export interface KVKeySummary {
  key: string;
  type: string;
  ttl: number;
}

export interface KVCollectionItem {
  field?: string;
  value: string;
  score?: number;
  index?: number;
  id?: string;
}

export interface KVEntryDetail {
  key: string;
  type: string;
  ttl: number;
  length: number;
  stringValue?: string;
  items: KVCollectionItem[];
}

export interface CollectionSummary {
  name: string;
  count: number;
  sizeBytes: number;
  indexesCount: number;
}

export interface DocIndexInfo {
  name: string;
  keys: Record<string, any>;
  unique: boolean;
}

export interface MonitorMetric {
  key: string;
  label: string;
  value: string;
  unit?: string;
}

export interface StorageItem {
  name: string;
  category?: string;
  rows: number;
  sizeBytes: number;
}

export interface SessionItem {
  id: string;
  user: string;
  host: string;
  database: string;
  command: string;
  state: string;
  durationSec: number;
  query: string;
}

export interface MonitorSnapshot {
  timestamp: string;
  version: string;
  uptimeSec: number;
  metrics: MonitorMetric[];
  storage: StorageItem[];
  sessions: SessionItem[];
  canKillSession: boolean;
}

export interface HistoryEntry {
  id: string;
  connectionId: string;
  connName: string;
  dbType: string;
  database: string;
  query: string;
  elapsedMs: number;
  status: string;
  errorMsg?: string;
  createdAt: string;
}

export interface SavedSnippet {
  id?: string;
  title: string;
  dbType: string;
  connectionId?: string;
  database?: string;
  sql: string;
  ownerId?: string;
  ownerName?: string;
  isShared?: boolean;
  editable?: boolean;
  createdAt?: string;
}

export interface SecurityStatus {
  userId: string;
  username: string;
  displayName?: string;
  role?: UserRole;
  canCreateConn?: boolean;
  totpEnabled: boolean;
  passwordChanged: boolean;
  recoveryCodesRemaining: number;
}

export interface AuthSession {
  id: string;
  userId: string;
  ip: string;
  userAgent: string;
  createdAt: string;
  lastSeenAt: string;
  expiresAt: number | string;
  current: boolean;
}

export interface AuditLogEntry {
  id: string;
  eventType: string;
  username: string;
  ip: string;
  userAgent: string;
  detail: string;
  createdAt: string;
}

export interface SystemStats {
  version?: string;
  allocMB: number;
  sysMB: number;
  heapObjects: number;
  goroutines: number;
  uptimeSec: number;
  goVersion: string;
  os: string;
  arch: string;
  activePools: number;
  maxPools: number;
}

export type UpdateChannel = 'release' | 'preview';

export interface BuildVersionInfo {
  version: string;
  displayVersion: string;
  edition?: 'server' | 'desktop';
  channel?: UpdateChannel;
  authEnabled?: boolean;
  gitCommit: string;
  buildTime: string;
  goVersion: string;
  os: string;
  arch: string;
  repoUrl: string;
  gitUrl: string;
  releasesUrl: string;
  license?: string;
  embeddedChangelog: string;
  embeddedChangelogs?: Record<string, string>;
}

export interface DesktopExportResult {
  canceled: boolean;
  savedPath?: string;
  bytes?: number;
}

export interface LocalFSBrowseEntry {
  name: string;
  path: string;
  isDir: boolean;
  sizeBytes: number;
  updatedAt: string;
}

export interface LocalFSBrowseResponse {
  currentDir: string;
  parentDir: string;
  dataDir: string;
  roots: { label: string; path: string }[];
  entries: LocalFSBrowseEntry[];
}

export interface KVBackupPayload {
  service: string;
  exportedAt: string;
  db: number;
  entries: KVEntryDetail[];
}

export interface ReleaseAsset {
  name: string;
  size: number;
  downloadUrl: string;
}

export interface ReleaseItem {
  tagName: string;
  name: string;
  body: string;
  htmlUrl: string;
  publishedAt: string;
  prerelease: boolean;
  assets?: ReleaseAsset[];
}

export interface UpdateCheckResult {
  channel?: UpdateChannel | string;
  currentVersion: string;
  latestVersion: string;
  latestPrerelease?: boolean;
  hasUpdate: boolean;
  releaseUrl: string;
  matchedAssetUrl?: string;
  matchedAssetName?: string;
  publishedAt?: string;
  repoUrl: string;
  gitUrl: string;
  changelogLang?: string;
  changelogMarkdown: string;
  changelogSource: 'github_changelog' | 'github_releases' | 'github_commits' | 'embedded' | string;
  releases: ReleaseItem[];
  checkedAt: string;
  fromCache: boolean;
  onlineCheckSuccess: boolean;
  notice?: string;
}

export interface DbAccountInfo {
  username: string;
  host?: string;
  identity: string;
  authPlugin?: string;
  locked?: boolean;
  isSuper?: boolean;
  canLogin?: boolean;
  roles?: string[];
  extra?: string;
}

export interface DbGrantEntry {
  statement: string;
  scope?: string;
  privileges?: string;
  grantable?: boolean;
}

export interface DbPrivilegeCapabilities {
  supported: boolean;
  engine: string;
  supportsHost: boolean;
  supportsLock: boolean;
  supportsRoles: boolean;
  supportsTableScope: boolean;
  supportsGlobalScope: boolean;
  availablePrivileges: string[];
  availableRoles?: string[];
  reason?: string;
}

export interface DbPrivilegeActionPayload {
  action: 'CREATE_USER' | 'ALTER_USER' | 'DROP_USER' | 'LOCK_USER' | 'UNLOCK_USER' | 'GRANT' | 'REVOKE';
  username: string;
  host?: string;
  database?: string;
  schema?: string;
  table?: string;
  scopeType?: 'global' | 'database' | 'schema' | 'table';
  password?: string;
  privileges?: string[];
  roles?: string[];
  withGrantOption?: boolean;
  isSuper?: boolean;
  canCreateDB?: boolean;
  canCreateRole?: boolean;
  aclPreset?: 'readonly' | 'readwrite' | 'admin' | 'custom';
  aclRules?: string;
}

export type LoginIconMode = 'shield' | 'database' | 'brand' | 'custom' | 'none';

export interface LoginBrandingConfig {
  pageTitle: string;
  iconMode: LoginIconMode;
  customIconDataUrl: string;
  heading: string;
  subtitle: string;
}

export interface AuthStatusResponse {
  authEnabled: boolean;
  initialized: boolean;
  needsSetup: boolean;
  branding: LoginBrandingConfig;
}

export interface LoginResponse {
  token?: string;
  username?: string;
  displayName?: string;
  userId?: string;
  role?: UserRole;
  canCreateConn?: boolean;
  requires2FA?: boolean;
  challengeToken?: string;
  totpEnabled?: boolean;
  passwordChanged?: boolean;
  usedRecoveryCode?: boolean;
  recoveryCodesRemaining?: number;
  branding?: LoginBrandingConfig;
}

export interface ImportResult {
  importedRows: number;
  failedCount?: number;
  warnings?: string[];
  elapsedMs: number;
}

export type TabKind =
  | 'overview'
  | 'table_data'
  | 'table_structure'
  | 'sql_console'
  | 'redis_browser'
  | 'mongo_collection'
  | 'monitor'
  | 'db_users';

export interface WorkspaceTab {
  key: string;
  title: string;
  kind: TabKind;
  connectionId: string;
  connName: string;
  dbType: string;
  database: string;
  schema?: string;
  table?: string;
  initialSQL?: string;
}
