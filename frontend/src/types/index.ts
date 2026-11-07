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

export interface DriverCapabilities {
  objectTypes?: string[];
  tableFeatures?: string[];
  partitionActions?: string[];
  matViewActions?: string[];
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
  capabilities?: DriverCapabilities;
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
  ephemeral?: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface EphemeralOpenResponse {
  connection: SavedConnection;
  database: string;
  firstTable?: string;
  tablesCount: number;
  sizeBytes: number;
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
  createdAt?: string;
  targetName?: string;
  status?: string;
  extra?: Record<string, string>;
}

export interface SchemaObjects {
  database: string;
  schema: string;
  tables: ObjectItem[];
  views: ObjectItem[];
  materializedViews?: ObjectItem[];
  routines: ObjectItem[];
  triggers?: ObjectItem[];
  sequences?: ObjectItem[];
  events?: ObjectItem[];
  packages?: ObjectItem[];
  types?: ObjectItem[];
  synonyms?: ObjectItem[];
  extensions?: ObjectItem[];
  dictionaries?: ObjectItem[];
  dbLinks?: ObjectItem[];
  macros?: ObjectItem[];
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
  generatedExpr?: string;
  generatedStorage?: string;
}

export interface CheckConstraintDef {
  name: string;
  expression: string;
  enforced: boolean;
}

export interface ProjectionDef {
  name: string;
  query: string;
  rows?: number;
  bytesOnDisk?: number;
}

export interface SkippingIndexDef {
  name: string;
  type: string;
  expression: string;
  granularity: number;
  bytesOnDisk?: number;
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
    | 'COMMENT_TABLE'
    | 'ADD_CHECK'
    | 'DROP_CHECK'
    | 'ADD_PROJECTION'
    | 'DROP_PROJECTION'
    | 'MATERIALIZE_PROJECTION'
    | 'CLEAR_PROJECTION'
    | 'ADD_SKIPPING_INDEX'
    | 'DROP_SKIPPING_INDEX'
    | 'MATERIALIZE_SKIPPING_INDEX'
    | 'CLEAR_SKIPPING_INDEX'
    | 'MODIFY_TTL'
    | 'REMOVE_TTL'
    | 'SET_PRAGMA';
  column?: ColumnDef;
  oldName?: string;
  newName?: string;
  comment?: string;
  afterColumn?: string;
  index?: IndexDef;
  foreignKey?: ForeignKeyDef;
  check?: CheckConstraintDef;
  projection?: ProjectionDef;
  skippingIndex?: SkippingIndexDef;
  ttlExpr?: string;
  pragmaKey?: string;
  pragmaValue?: string;
}

export interface ObjectSpec {
  dataType?: string;
  startWith?: number;
  incrementBy?: number;
  minValue?: number;
  maxValue?: number;
  cache?: number;
  cycle?: boolean;
  restartWith?: number;

  scheduleKind?: 'EVERY' | 'AT';
  everyValue?: number;
  everyUnit?: string;
  executeAt?: string;
  startsAt?: string;
  endsAt?: string;
  preserve?: boolean;
  enabled?: boolean;
  bodySql?: string;

  selectSql?: string;
  populate?: boolean;
  targetTable?: string;
  engineClause?: string;
  refreshMode?: string;

  typeKind?: 'ENUM' | 'DOMAIN' | 'COMPOSITE';
  enumValues?: string[];
  newEnumValue?: string;
  baseType?: string;
  checkExpr?: string;
  extensionVersion?: string;
  cascade?: boolean;

  targetOwner?: string;
  targetObject?: string;
  dbLinkHost?: string;
  dbLinkUser?: string;
  dbLinkPass?: string;
  public?: boolean;

  parameters?: string[];
  isTableMacro?: boolean;
  macroExpr?: string;
}

export interface ObjectActionPayload {
  database?: string;
  schema?: string;
  objectName: string;
  objectType: string;
  action: string;
  targetName?: string;
  viewSql?: string;
  routineSql?: string;
  replaceExisting?: boolean;
  comment?: string;
  previewOnly?: boolean;
  spec?: ObjectSpec;
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
  checkConstraints?: CheckConstraintDef[];
  projections?: ProjectionDef[];
  skippingIndexes?: SkippingIndexDef[];
  tableTtl?: string;
  diagnostics?: Record<string, string>;
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
  truncated?: boolean;
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
  detached?: boolean;
  strategy?: string;
  expression?: string;
  boundDescription?: string;
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
  memoryBytes?: number;
  encoding?: string;
  stringValue?: string;
  items: KVCollectionItem[];
}

export interface KVStreamConsumer {
  name: string;
  pending: number;
  idleMs: number;
}

export interface KVStreamGroup {
  name: string;
  consumers: number;
  pending: number;
  lastDeliveredId: string;
  entriesRead?: number;
  lag?: number;
  consumerList?: KVStreamConsumer[];
}

export interface KVStreamInfo {
  length: number;
  radixKeys?: number;
  firstId?: string;
  lastId?: string;
  groupsCount: number;
  groups: KVStreamGroup[];
}

export interface KVSlowlogEntry {
  id: number;
  timestamp: string;
  durationUs: number;
  command: string;
  clientAddr?: string;
  clientName?: string;
}

export interface KVConfigItem {
  key: string;
  value: string;
}

export interface CollectionSummary {
  name: string;
  type?: 'collection' | 'view' | 'timeseries' | string;
  count: number;
  sizeBytes: number;
  indexesCount: number;
  capped?: boolean;
  maxBytes?: number;
  maxDocs?: number;
  viewOn?: string;
  pipelineJson?: string;
  validatorJson?: string;
  validationLevel?: string;
  validationAction?: string;
  timeField?: string;
  metaField?: string;
  granularity?: string;
  expireAfterSeconds?: number;
}

export interface DocIndexInfo {
  name: string;
  keys: Record<string, any>;
  unique: boolean;
  sparse?: boolean;
  expireAfterSeconds?: number;
  partialFilterJson?: string;
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
