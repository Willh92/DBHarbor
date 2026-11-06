package driver

import (
	"context"
	"io"
)

type Category string

const (
	CategoryRelational Category = "relational"
	CategoryOLAP       Category = "olap"
	CategoryKeyValue   Category = "keyvalue"
	CategoryDocument   Category = "document"
)

type SelectOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type ConnFieldSchema struct {
	Key           string         `json:"key"`
	Label         string         `json:"label"`
	Type          string         `json:"type"`   // text, number, password, select, switch
	Target        string         `json:"target"` // root, options
	Placeholder   string         `json:"placeholder,omitempty"`
	DefaultValue  string         `json:"defaultValue,omitempty"`
	Required      bool           `json:"required,omitempty"`
	SelectOptions []SelectOption `json:"selectOptions,omitempty"`
}

type DriverMeta struct {
	Type               string            `json:"type"`
	Name               string            `json:"name"`
	Category           Category          `json:"category"`
	BadgeColor         string            `json:"badgeColor"`
	DefaultPort        int               `json:"defaultPort"`
	Description        string            `json:"description"`
	SupportsSchemas    bool              `json:"supportsSchemas"`
	SupportsPartitions bool              `json:"supportsPartitions"`
	SupportsExplain    bool              `json:"supportsExplain"`
	DefaultTypes       []string          `json:"defaultTypes,omitempty"`
	Fields             []ConnFieldSchema `json:"fields"`
}

type ConnectionConfig struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Type     string            `json:"type"`
	Color    string            `json:"color,omitempty"`
	Host     string            `json:"host,omitempty"`
	Port     int               `json:"port,omitempty"`
	Username string            `json:"username,omitempty"`
	Password string            `json:"password,omitempty"`
	Database string            `json:"database,omitempty"`
	FilePath string            `json:"filePath,omitempty"`
	URI      string            `json:"uri,omitempty"`
	Options  map[string]string `json:"options,omitempty"`
}

func (c ConnectionConfig) Option(key, fallback string) string {
	if c.Options != nil {
		if v, ok := c.Options[key]; ok && v != "" {
			return v
		}
	}
	return fallback
}

type ServerInfo struct {
	Version   string `json:"version"`
	Product   string `json:"product"`
	LatencyMs int64  `json:"latencyMs"`
}

type DatabaseSummary struct {
	Name        string `json:"name"`
	SizeBytes   int64  `json:"sizeBytes,omitempty"`
	TablesCount int    `json:"tablesCount,omitempty"`
	Charset     string `json:"charset,omitempty"`
}

type ObjectItem struct {
	Name       string `json:"name"`
	Type       string `json:"type"` // TABLE, VIEW, MATERIALIZED VIEW, FUNCTION, PROCEDURE
	Engine     string `json:"engine,omitempty"`
	Rows       int64  `json:"rows"`
	DataBytes  int64  `json:"dataBytes"`
	IndexBytes int64  `json:"indexBytes,omitempty"`
	Collation  string `json:"collation,omitempty"`
	Comment    string `json:"comment,omitempty"`
	CreatedAt  string `json:"createdAt,omitempty"`
}

type SchemaObjects struct {
	Database string       `json:"database"`
	Schema   string       `json:"schema"`
	Tables   []ObjectItem `json:"tables"`
	Views    []ObjectItem `json:"views"`
	Routines []ObjectItem `json:"routines"`
	Triggers []ObjectItem `json:"triggers,omitempty"`
}

type ColumnDef struct {
	Name            string `json:"name"`
	Type            string `json:"type"`
	Nullable        bool   `json:"nullable"`
	DefaultValue    string `json:"defaultValue,omitempty"`
	IsPrimaryKey    bool   `json:"isPrimaryKey"`
	IsAutoIncrement bool   `json:"isAutoIncrement"`
	Comment         string `json:"comment,omitempty"`
	Extra           string `json:"extra,omitempty"`
}

type IndexDef struct {
	Name      string   `json:"name"`
	Columns   []string `json:"columns"`
	Unique    bool     `json:"unique"`
	Primary   bool     `json:"primary"`
	IndexType string   `json:"indexType,omitempty"`
}

type ForeignKeyDef struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	RefTable   string   `json:"refTable"`
	RefColumns []string `json:"refColumns"`
	OnUpdate   string   `json:"onUpdate,omitempty"`
	OnDelete   string   `json:"onDelete,omitempty"`
}

type TableStructure struct {
	Database    string            `json:"database"`
	Schema      string            `json:"schema,omitempty"`
	Table       string            `json:"table"`
	Engine      string            `json:"engine,omitempty"`
	Charset     string            `json:"charset,omitempty"`
	Collation   string            `json:"collation,omitempty"`
	Comment     string            `json:"comment,omitempty"`
	DDL         string            `json:"ddl"`
	Columns     []ColumnDef       `json:"columns"`
	Indexes     []IndexDef        `json:"indexes"`
	ForeignKeys []ForeignKeyDef   `json:"foreignKeys"`
	Options     map[string]string `json:"options,omitempty"`
}

type SortSpec struct {
	Column    string `json:"column"`
	Direction string `json:"direction"` // ASC, DESC
}

type BrowseDataRequest struct {
	Database string     `json:"database"`
	Schema   string     `json:"schema"`
	Table    string     `json:"table"`
	Page     int        `json:"page"`
	PageSize int        `json:"pageSize"`
	Filter   string     `json:"filter"`
	Sorts    []SortSpec `json:"sorts"`
}

type QueryColumn struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable,omitempty"`
}

type QueryResult struct {
	Statement    string        `json:"statement"`
	Columns      []QueryColumn `json:"columns"`
	Rows         [][]any       `json:"rows"`
	Total        int64         `json:"total"`
	HasTotal     bool          `json:"hasTotal"`
	AffectedRows int64         `json:"affectedRows"`
	ElapsedMs    int64         `json:"elapsedMs"`
	IsSelect     bool          `json:"isSelect"`
	PrimaryKeys  []string      `json:"primaryKeys,omitempty"`
	Message      string        `json:"message,omitempty"`
}

type RowUpdateSpec struct {
	Locator map[string]any `json:"locator"`
	Changes map[string]any `json:"changes"`
}

type RowMutationRequest struct {
	Database    string           `json:"database"`
	Schema      string           `json:"schema"`
	Table       string           `json:"table"`
	PrimaryKeys []string         `json:"primaryKeys"`
	Inserts     []map[string]any `json:"inserts"`
	Updates     []RowUpdateSpec  `json:"updates"`
	Deletes     []map[string]any `json:"deletes"`
}

type MutationResult struct {
	Inserted    int      `json:"inserted"`
	Updated     int      `json:"updated"`
	Deleted     int      `json:"deleted"`
	ExecutedSQL []string `json:"executedSql"`
}

type CreateTableRequest struct {
	Database    string          `json:"database"`
	Schema      string          `json:"schema"`
	Table       string          `json:"table"`
	Columns     []ColumnDef     `json:"columns"`
	Indexes     []IndexDef      `json:"indexes"`
	ForeignKeys []ForeignKeyDef `json:"foreignKeys,omitempty"`
	Engine      string          `json:"engine"`
	OrderBy     string          `json:"orderBy"`
	PartitionBy string          `json:"partitionBy"`
	Charset     string          `json:"charset"`
	Comment     string          `json:"comment"`
	PreviewOnly bool            `json:"previewOnly"`
}

type AlterAction struct {
	Action      string        `json:"action"` // ADD_COLUMN, MODIFY_COLUMN, RENAME_COLUMN, DROP_COLUMN, ADD_INDEX, DROP_INDEX, ADD_FOREIGN_KEY, DROP_FOREIGN_KEY, RENAME_TABLE, COMMENT_TABLE
	Column      ColumnDef     `json:"column,omitempty"`
	OldName     string        `json:"oldName,omitempty"`
	NewName     string        `json:"newName,omitempty"`
	Comment     string        `json:"comment,omitempty"`
	AfterColumn string        `json:"afterColumn,omitempty"`
	Index       IndexDef      `json:"index,omitempty"`
	ForeignKey  ForeignKeyDef `json:"foreignKey,omitempty"`
}

type AlterTableRequest struct {
	Database    string        `json:"database"`
	Schema      string        `json:"schema"`
	Table       string        `json:"table"`
	Actions     []AlterAction `json:"actions"`
	PreviewOnly bool          `json:"previewOnly"`
}

type ObjectActionRequest struct {
	Database        string `json:"database"`
	Schema          string `json:"schema"`
	ObjectName      string `json:"objectName"`
	ObjectType      string `json:"objectType"` // TABLE, VIEW, FUNCTION, PROCEDURE, TRIGGER
	Action          string `json:"action"`     // DROP, TRUNCATE, OPTIMIZE, ANALYZE, RENAME, COPY_STRUCTURE, COPY_DATA, CREATE_VIEW, CREATE_ROUTINE, CREATE_TRIGGER, COMMENT
	TargetName      string `json:"targetName,omitempty"`
	ViewSQL         string `json:"viewSql,omitempty"`
	RoutineSQL      string `json:"routineSql,omitempty"`
	ReplaceExisting bool   `json:"replaceExisting,omitempty"`
	Comment         string `json:"comment,omitempty"`
}

type ExportRequest struct {
	Database     string   `json:"database"`
	Schema       string   `json:"schema"`
	Table        string   `json:"table"`
	Tables       []string `json:"tables,omitempty"`
	CustomSQL    string   `json:"customSql"`
	Format       string   `json:"format"` // csv, csv_bom, json, jsonl, markdown, sql
	IncludeDDL   bool     `json:"includeDdl"`
	DropIfExists bool     `json:"dropIfExists"`
	MaxRows      int      `json:"maxRows"`
}

type ImportRequest struct {
	Database        string    `json:"database"`
	Schema          string    `json:"schema"`
	Table           string    `json:"table"`
	Format          string    `json:"format"` // csv, json, jsonl, sql
	ContinueOnError bool      `json:"continueOnError"`
	Reader          io.Reader `json:"-"`
}

type ImportResult struct {
	ImportedRows int      `json:"importedRows"`
	FailedCount  int      `json:"failedCount,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
	ElapsedMs    int64    `json:"elapsedMs"`
}

type AutocompleteMeta struct {
	Databases []string            `json:"databases"`
	Tables    map[string][]string `json:"tables"` // table -> columns
}

type PartitionInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Rows        int64  `json:"rows"`
	BytesOnDisk int64  `json:"bytesOnDisk"`
	PartsCount  int64  `json:"partsCount"`
	Active      bool   `json:"active"`
}

// Redis Key-Value DTOs
type KVKeySummary struct {
	Key  string `json:"key"`
	Type string `json:"type"`
	TTL  int64  `json:"ttl"`
}

type KVScanResult struct {
	Cursor uint64          `json:"cursor"`
	Keys   []KVKeySummary  `json:"keys"`
	DBKeys map[int]int64   `json:"dbKeys"`
}

type KVCollectionItem struct {
	Field string  `json:"field,omitempty"`
	Value string  `json:"value"`
	Score float64 `json:"score,omitempty"`
	Index int     `json:"index,omitempty"`
	ID    string  `json:"id,omitempty"`
}

type KVEntryDetail struct {
	Key         string             `json:"key"`
	Type        string             `json:"type"`
	TTL         int64              `json:"ttl"`
	Length      int64              `json:"length"`
	StringValue string             `json:"stringValue,omitempty"`
	Items       []KVCollectionItem `json:"items,omitempty"`
}

type KVSetRequest struct {
	Key         string             `json:"key"`
	Type        string             `json:"type"`
	TTL         int64              `json:"ttl"`
	StringValue string             `json:"stringValue,omitempty"`
	Items       []KVCollectionItem `json:"items,omitempty"`
}

type KVItemMutationRequest struct {
	Key      string  `json:"key"`
	Type     string  `json:"type"`
	Action   string  `json:"action"` // ADD, UPDATE, DELETE
	Field    string  `json:"field,omitempty"`
	OldValue string  `json:"oldValue,omitempty"`
	Value    string  `json:"value,omitempty"`
	Score    float64 `json:"score,omitempty"`
	Index    int     `json:"index,omitempty"`
}

type KVCommandResult struct {
	Command   string `json:"command"`
	Output    string `json:"output"`
	ElapsedMs int64  `json:"elapsedMs"`
}

// MongoDB Document DTOs
type CollectionSummary struct {
	Name         string `json:"name"`
	Count        int64  `json:"count"`
	SizeBytes    int64  `json:"sizeBytes"`
	IndexesCount int    `json:"indexesCount"`
}

type DocFindRequest struct {
	Database       string `json:"database"`
	Collection     string `json:"collection"`
	FilterJSON     string `json:"filterJson"`
	SortJSON       string `json:"sortJson"`
	ProjectionJSON string `json:"projectionJson"`
	Page           int    `json:"page"`
	PageSize       int    `json:"pageSize"`
}

type DocFindResult struct {
	Documents []map[string]any `json:"documents"`
	Total     int64            `json:"total"`
	ElapsedMs int64            `json:"elapsedMs"`
}

type DocIndexInfo struct {
	Name   string         `json:"name"`
	Keys   map[string]any `json:"keys"`
	Unique bool           `json:"unique"`
}

// Monitor DTOs
type MonitorMetric struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value string `json:"value"`
	Unit  string `json:"unit,omitempty"`
}

type StorageItem struct {
	Name      string `json:"name"`
	Category  string `json:"category,omitempty"`
	Rows      int64  `json:"rows"`
	SizeBytes int64  `json:"sizeBytes"`
}

type SessionItem struct {
	ID          string `json:"id"`
	User        string `json:"user"`
	Host        string `json:"host"`
	Database    string `json:"database"`
	Command     string `json:"command"`
	State       string `json:"state"`
	DurationSec int64  `json:"durationSec"`
	Query       string `json:"query"`
}

type MonitorSnapshot struct {
	Timestamp      string          `json:"timestamp"`
	Version        string          `json:"version"`
	UptimeSec      int64           `json:"uptimeSec"`
	Metrics        []MonitorMetric `json:"metrics"`
	Storage        []StorageItem   `json:"storage"`
	Sessions       []SessionItem   `json:"sessions"`
	CanKillSession bool            `json:"canKillSession"`
}

// Core Pluggable Interfaces

type Driver interface {
	Meta() DriverMeta
	Ping(ctx context.Context) (ServerInfo, error)
	Close() error
	ListDatabases(ctx context.Context) ([]DatabaseSummary, error)
	CreateDatabase(ctx context.Context, name, charset string) error
}

type SQLDriver interface {
	Driver
	ListSchemas(ctx context.Context, database string) ([]string, error)
	ListObjects(ctx context.Context, database, schema string) (SchemaObjects, error)
	GetTableStructure(ctx context.Context, database, schema, table string) (TableStructure, error)
	BrowseData(ctx context.Context, req BrowseDataRequest) (QueryResult, error)
	BuildMutationPreviewSQL(req RowMutationRequest) ([]string, error)
	ApplyRowMutations(ctx context.Context, req RowMutationRequest) (MutationResult, error)
	CreateTable(ctx context.Context, req CreateTableRequest) (string, error)
	AlterTable(ctx context.Context, req AlterTableRequest) ([]string, error)
	ManageObject(ctx context.Context, req ObjectActionRequest) error
	ExecuteSQL(ctx context.Context, database, query string, maxRows int) ([]QueryResult, error)
	StreamExport(ctx context.Context, req ExportRequest, w io.Writer) error
	BatchImport(ctx context.Context, req ImportRequest) (ImportResult, error)
	GetAutocompleteMeta(ctx context.Context, database string) (AutocompleteMeta, error)
}

type PartitionManager interface {
	ListPartitions(ctx context.Context, database, table string) ([]PartitionInfo, error)
	ManagePartition(ctx context.Context, database, table, partitionID, action string) error
}

type KeyValueDriver interface {
	Driver
	ScanKeys(ctx context.Context, dbIndex int, pattern string, cursor uint64, count int64) (KVScanResult, error)
	GetKey(ctx context.Context, dbIndex int, key string) (KVEntryDetail, error)
	SetKey(ctx context.Context, dbIndex int, req KVSetRequest) error
	MutateCollectionItem(ctx context.Context, dbIndex int, req KVItemMutationRequest) error
	DeleteKeys(ctx context.Context, dbIndex int, keys []string) (int64, error)
	SetTTL(ctx context.Context, dbIndex int, key string, ttlSeconds int64) error
	ExecuteCommand(ctx context.Context, dbIndex int, cmdLine string) (KVCommandResult, error)
}

type DocumentDriver interface {
	Driver
	ListCollections(ctx context.Context, database string) ([]CollectionSummary, error)
	CreateCollection(ctx context.Context, database, collection string) error
	DropCollection(ctx context.Context, database, collection string) error
	FindDocuments(ctx context.Context, req DocFindRequest) (DocFindResult, error)
	InsertDocument(ctx context.Context, database, collection, docJSON string) (string, error)
	UpdateDocument(ctx context.Context, database, collection, idJSON, docJSON string) error
	DeleteDocuments(ctx context.Context, database, collection string, idsJSON []string) (int64, error)
	ListIndexes(ctx context.Context, database, collection string) ([]DocIndexInfo, error)
	CreateIndex(ctx context.Context, database, collection string, keysJSON string, unique bool) error
	DropIndex(ctx context.Context, database, collection, indexName string) error
	RunCommand(ctx context.Context, database, commandJSON string) (any, error)
}

type MonitorProvider interface {
	GetMonitorSnapshot(ctx context.Context) (MonitorSnapshot, error)
	KillSession(ctx context.Context, sessionID string) error
}

// DatabaseDropper is implemented by drivers that support dropping a logical database or catalog.
type DatabaseDropper interface {
	DropDatabase(ctx context.Context, name string) error
}

// SchemaManager is implemented by SQL drivers that support multi-schema namespaces (e.g. PostgreSQL, KingbaseES, SQL Server, Dameng).
type SchemaManager interface {
	CreateSchema(ctx context.Context, database, schema string) error
	DropSchema(ctx context.Context, database, schema string, cascade bool) error
}

// KVExtendedDriver adds key rename, key duplication, and logical database flush capabilities for Key-Value engines.
type KVExtendedDriver interface {
	RenameKey(ctx context.Context, dbIndex int, oldKey, newKey string, nxOnly bool) error
	DuplicateKey(ctx context.Context, dbIndex int, srcKey, dstKey string, replace bool) error
	FlushDB(ctx context.Context, dbIndex int) error
}

// DocExtendedDriver adds collection rename, streaming collection export, and batch document import capabilities for Document engines.
type DocExtendedDriver interface {
	RenameCollection(ctx context.Context, database, oldName, newName string) error
	StreamExportCollection(ctx context.Context, database, collection, filterJSON, sortJSON, format string, maxRows int, w io.Writer) error
	ImportDocuments(ctx context.Context, database, collection string, r io.Reader, format string, continueOnError bool) (ImportResult, error)
}
