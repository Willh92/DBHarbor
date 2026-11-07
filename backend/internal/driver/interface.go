package driver

import (
	"context"
	"io"
	"strings"
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

type DriverCapabilities struct {
	ObjectTypes      []string `json:"objectTypes,omitempty"`
	TableFeatures    []string `json:"tableFeatures,omitempty"`
	PartitionActions []string `json:"partitionActions,omitempty"`
	MatViewActions   []string `json:"matViewActions,omitempty"`
}

func (c DriverCapabilities) IsEmpty() bool {
	return len(c.ObjectTypes) == 0 && len(c.TableFeatures) == 0 && len(c.PartitionActions) == 0 && len(c.MatViewActions) == 0
}

// DefaultCapabilities returns the canonical capability descriptor for each built-in database engine.
func DefaultCapabilities(dbType string) DriverCapabilities {
	switch strings.ToLower(strings.TrimSpace(dbType)) {
	case "mysql", "oceanbase":
		return DriverCapabilities{
			ObjectTypes:      []string{"TABLE", "VIEW", "FUNCTION", "PROCEDURE", "TRIGGER", "EVENT"},
			TableFeatures:    []string{"INDEXES", "FOREIGN_KEYS", "CHECKS", "PARTITIONS", "GENERATED_COLS"},
			PartitionActions: []string{"TRUNCATE", "DROP", "ADD", "OPTIMIZE", "ANALYZE"},
		}
	case "mariadb":
		return DriverCapabilities{
			ObjectTypes:      []string{"TABLE", "VIEW", "SEQUENCE", "FUNCTION", "PROCEDURE", "TRIGGER", "EVENT"},
			TableFeatures:    []string{"INDEXES", "FOREIGN_KEYS", "CHECKS", "PARTITIONS", "GENERATED_COLS"},
			PartitionActions: []string{"TRUNCATE", "DROP", "ADD", "OPTIMIZE", "ANALYZE"},
		}
	case "tidb":
		return DriverCapabilities{
			ObjectTypes:      []string{"TABLE", "VIEW", "SEQUENCE", "FUNCTION", "PROCEDURE", "TRIGGER"},
			TableFeatures:    []string{"INDEXES", "FOREIGN_KEYS", "CHECKS", "PARTITIONS", "GENERATED_COLS"},
			PartitionActions: []string{"TRUNCATE", "DROP", "ADD", "ANALYZE"},
		}
	case "clickhouse":
		return DriverCapabilities{
			ObjectTypes:      []string{"TABLE", "VIEW", "MATERIALIZED_VIEW", "DICTIONARY", "FUNCTION"},
			TableFeatures:    []string{"SKIPPING_INDEXES", "PROJECTIONS", "PARTITIONS", "TTL"},
			PartitionActions: []string{"OPTIMIZE", "DETACH", "ATTACH", "DROP"},
			MatViewActions:   []string{"REFRESH"},
		}
	case "postgres", "kingbase":
		return DriverCapabilities{
			ObjectTypes:      []string{"TABLE", "VIEW", "MATERIALIZED_VIEW", "SEQUENCE", "FUNCTION", "PROCEDURE", "TRIGGER", "TYPE", "EXTENSION"},
			TableFeatures:    []string{"INDEXES", "FOREIGN_KEYS", "CHECKS", "PARTITIONS", "GENERATED_COLS"},
			PartitionActions: []string{"DETACH", "ATTACH", "DROP"},
			MatViewActions:   []string{"REFRESH", "REFRESH_CONCURRENTLY"},
		}
	case "oracle", "dameng", "dm8":
		return DriverCapabilities{
			ObjectTypes:      []string{"TABLE", "VIEW", "MATERIALIZED_VIEW", "SEQUENCE", "PACKAGE", "FUNCTION", "PROCEDURE", "TRIGGER", "SYNONYM", "DB_LINK"},
			TableFeatures:    []string{"INDEXES", "FOREIGN_KEYS", "CHECKS", "PARTITIONS"},
			PartitionActions: []string{"TRUNCATE", "DROP"},
			MatViewActions:   []string{"REFRESH"},
		}
	case "sqlserver":
		return DriverCapabilities{
			ObjectTypes:      []string{"TABLE", "VIEW", "SEQUENCE", "FUNCTION", "PROCEDURE", "TRIGGER", "SYNONYM"},
			TableFeatures:    []string{"INDEXES", "FOREIGN_KEYS", "CHECKS", "PARTITIONS", "GENERATED_COLS"},
			PartitionActions: []string{"TRUNCATE"},
		}
	case "duckdb":
		return DriverCapabilities{
			ObjectTypes:   []string{"TABLE", "VIEW", "SEQUENCE", "MACRO", "EXTENSION"},
			TableFeatures: []string{"INDEXES", "FOREIGN_KEYS", "CHECKS"},
		}
	case "sqlite":
		return DriverCapabilities{
			ObjectTypes:   []string{"TABLE", "VIEW", "TRIGGER"},
			TableFeatures: []string{"INDEXES", "FOREIGN_KEYS", "CHECKS", "GENERATED_COLS", "VIRTUAL_TABLES", "PRAGMAS"},
		}
	case "mongodb":
		return DriverCapabilities{
			ObjectTypes:   []string{"COLLECTION", "VIEW", "TIMESERIES"},
			TableFeatures: []string{"INDEXES", "VALIDATOR"},
		}
	case "redis":
		return DriverCapabilities{
			ObjectTypes:   []string{"STRING", "HASH", "LIST", "SET", "ZSET", "STREAM"},
			TableFeatures: []string{"STREAM_GROUPS", "MEMORY_ENCODING", "SLOWLOG", "SERVER_CONFIG", "PUBSUB"},
		}
	default:
		return DriverCapabilities{
			ObjectTypes:   []string{"TABLE", "VIEW"},
			TableFeatures: []string{"INDEXES", "FOREIGN_KEYS"},
		}
	}
}

type DriverMeta struct {
	Type               string             `json:"type"`
	Name               string             `json:"name"`
	Category           Category           `json:"category"`
	BadgeColor         string             `json:"badgeColor"`
	DefaultPort        int                `json:"defaultPort"`
	Description        string             `json:"description"`
	SupportsSchemas    bool               `json:"supportsSchemas"`
	SupportsPartitions bool               `json:"supportsPartitions"`
	SupportsExplain    bool               `json:"supportsExplain"`
	DefaultTypes       []string           `json:"defaultTypes,omitempty"`
	Fields             []ConnFieldSchema  `json:"fields"`
	Capabilities       DriverCapabilities `json:"capabilities"`
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
	Name       string            `json:"name"`
	Type       string            `json:"type"` // TABLE, VIEW, MATERIALIZED_VIEW, SEQUENCE, FUNCTION, PROCEDURE, TRIGGER, EVENT, TYPE, EXTENSION, SYNONYM, PACKAGE, PACKAGE_BODY, DB_LINK, DICTIONARY, MACRO, TABLE_MACRO
	Engine     string            `json:"engine,omitempty"`
	Rows       int64             `json:"rows"`
	DataBytes  int64             `json:"dataBytes"`
	IndexBytes int64             `json:"indexBytes,omitempty"`
	Collation  string            `json:"collation,omitempty"`
	Comment    string            `json:"comment,omitempty"`
	CreatedAt  string            `json:"createdAt,omitempty"`
	TargetName string            `json:"targetName,omitempty"`
	Status     string            `json:"status,omitempty"`
	Extra      map[string]string `json:"extra,omitempty"`
}

type SchemaObjects struct {
	Database          string       `json:"database"`
	Schema            string       `json:"schema"`
	Tables            []ObjectItem `json:"tables"`
	Views             []ObjectItem `json:"views"`
	MaterializedViews []ObjectItem `json:"materializedViews,omitempty"`
	Routines          []ObjectItem `json:"routines"`
	Triggers          []ObjectItem `json:"triggers,omitempty"`
	Sequences         []ObjectItem `json:"sequences,omitempty"`
	Events            []ObjectItem `json:"events,omitempty"`
	Packages          []ObjectItem `json:"packages,omitempty"`
	Types             []ObjectItem `json:"types,omitempty"`
	Synonyms          []ObjectItem `json:"synonyms,omitempty"`
	Extensions        []ObjectItem `json:"extensions,omitempty"`
	Dictionaries      []ObjectItem `json:"dictionaries,omitempty"`
	DbLinks           []ObjectItem `json:"dbLinks,omitempty"`
	Macros            []ObjectItem `json:"macros,omitempty"`
}

type ColumnDef struct {
	Name             string `json:"name"`
	Type             string `json:"type"`
	Nullable         bool   `json:"nullable"`
	DefaultValue     string `json:"defaultValue,omitempty"`
	IsPrimaryKey     bool   `json:"isPrimaryKey"`
	IsAutoIncrement  bool   `json:"isAutoIncrement"`
	Comment          string `json:"comment,omitempty"`
	Extra            string `json:"extra,omitempty"`
	GeneratedExpr    string `json:"generatedExpr,omitempty"`
	GeneratedStorage string `json:"generatedStorage,omitempty"` // VIRTUAL, STORED, COMPUTED
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

type CheckConstraintDef struct {
	Name       string `json:"name"`
	Expression string `json:"expression"`
	Enforced   bool   `json:"enforced"`
}

type ProjectionDef struct {
	Name        string `json:"name"`
	Query       string `json:"query"`
	Rows        int64  `json:"rows,omitempty"`
	BytesOnDisk int64  `json:"bytesOnDisk,omitempty"`
}

type SkippingIndexDef struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Expression  string `json:"expression"`
	Granularity int64  `json:"granularity"`
	BytesOnDisk int64  `json:"bytesOnDisk,omitempty"`
}

type TableStructure struct {
	Database         string               `json:"database"`
	Schema           string               `json:"schema,omitempty"`
	Table            string               `json:"table"`
	Engine           string               `json:"engine,omitempty"`
	Charset          string               `json:"charset,omitempty"`
	Collation        string               `json:"collation,omitempty"`
	Comment          string               `json:"comment,omitempty"`
	DDL              string               `json:"ddl"`
	Columns          []ColumnDef          `json:"columns"`
	Indexes          []IndexDef           `json:"indexes"`
	ForeignKeys      []ForeignKeyDef      `json:"foreignKeys"`
	CheckConstraints []CheckConstraintDef `json:"checkConstraints,omitempty"`
	Projections      []ProjectionDef      `json:"projections,omitempty"`
	SkippingIndexes  []SkippingIndexDef   `json:"skippingIndexes,omitempty"`
	TableTTL         string               `json:"tableTtl,omitempty"`
	Diagnostics      map[string]string    `json:"diagnostics,omitempty"`
	Options          map[string]string    `json:"options,omitempty"`
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
	Truncated    bool          `json:"truncated,omitempty"`
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
	Database         string               `json:"database"`
	Schema           string               `json:"schema"`
	Table            string               `json:"table"`
	Columns          []ColumnDef          `json:"columns"`
	Indexes          []IndexDef           `json:"indexes"`
	ForeignKeys      []ForeignKeyDef      `json:"foreignKeys,omitempty"`
	CheckConstraints []CheckConstraintDef `json:"checkConstraints,omitempty"`
	Engine           string               `json:"engine"`
	OrderBy          string               `json:"orderBy"`
	PartitionBy      string               `json:"partitionBy"`
	Charset          string               `json:"charset"`
	Comment          string               `json:"comment"`
	Strict           bool                 `json:"strict,omitempty"`
	WithoutRowID     bool                 `json:"withoutRowId,omitempty"`
	VirtualModule    string               `json:"virtualModule,omitempty"`
	VirtualArgs      string               `json:"virtualArgs,omitempty"`
	PreviewOnly      bool                 `json:"previewOnly"`
}

type AlterAction struct {
	Action        string             `json:"action"` // ADD_COLUMN, MODIFY_COLUMN, RENAME_COLUMN, DROP_COLUMN, ADD_INDEX, DROP_INDEX, ADD_FOREIGN_KEY, DROP_FOREIGN_KEY, RENAME_TABLE, COMMENT_TABLE, ADD_CHECK, DROP_CHECK, ADD_PROJECTION, DROP_PROJECTION, MATERIALIZE_PROJECTION, CLEAR_PROJECTION, ADD_SKIPPING_INDEX, DROP_SKIPPING_INDEX, MATERIALIZE_SKIPPING_INDEX, CLEAR_SKIPPING_INDEX, MODIFY_TTL, REMOVE_TTL, SET_PRAGMA
	Column        ColumnDef          `json:"column,omitempty"`
	OldName       string             `json:"oldName,omitempty"`
	NewName       string             `json:"newName,omitempty"`
	Comment       string             `json:"comment,omitempty"`
	AfterColumn   string             `json:"afterColumn,omitempty"`
	Index         IndexDef           `json:"index,omitempty"`
	ForeignKey    ForeignKeyDef      `json:"foreignKey,omitempty"`
	Check         CheckConstraintDef `json:"check,omitempty"`
	Projection    ProjectionDef      `json:"projection,omitempty"`
	SkippingIndex SkippingIndexDef   `json:"skippingIndex,omitempty"`
	TTLExpr       string             `json:"ttlExpr,omitempty"`
	PragmaKey     string             `json:"pragmaKey,omitempty"`
	PragmaValue   string             `json:"pragmaValue,omitempty"`
}

type AlterTableRequest struct {
	Database    string        `json:"database"`
	Schema      string        `json:"schema"`
	Table       string        `json:"table"`
	Actions     []AlterAction `json:"actions"`
	PreviewOnly bool          `json:"previewOnly"`
}

type ObjectSpec struct {
	// Sequence options
	DataType    string `json:"dataType,omitempty"`
	StartWith   *int64 `json:"startWith,omitempty"`
	IncrementBy *int64 `json:"incrementBy,omitempty"`
	MinValue    *int64 `json:"minValue,omitempty"`
	MaxValue    *int64 `json:"maxValue,omitempty"`
	Cache       *int64 `json:"cache,omitempty"`
	Cycle       bool   `json:"cycle,omitempty"`
	RestartWith *int64 `json:"restartWith,omitempty"`

	// MySQL/MariaDB Scheduled Event options
	ScheduleKind string `json:"scheduleKind,omitempty"` // EVERY, AT
	EveryValue   int    `json:"everyValue,omitempty"`
	EveryUnit    string `json:"everyUnit,omitempty"` // SECOND, MINUTE, HOUR, DAY, WEEK, MONTH
	ExecuteAt    string `json:"executeAt,omitempty"`
	StartsAt     string `json:"startsAt,omitempty"`
	EndsAt       string `json:"endsAt,omitempty"`
	Preserve     bool   `json:"preserve,omitempty"`
	Enabled      *bool  `json:"enabled,omitempty"`
	BodySQL      string `json:"bodySql,omitempty"`

	// Materialized View options
	SelectSQL    string `json:"selectSql,omitempty"`
	Populate     bool   `json:"populate,omitempty"`
	TargetTable  string `json:"targetTable,omitempty"`
	EngineClause string `json:"engineClause,omitempty"`
	RefreshMode  string `json:"refreshMode,omitempty"` // COMPLETE, FAST, FORCE, CONCURRENTLY

	// PostgreSQL Type / Domain / Extension options
	TypeKind     string   `json:"typeKind,omitempty"` // ENUM, DOMAIN, COMPOSITE
	EnumValues   []string `json:"enumValues,omitempty"`
	NewEnumValue string   `json:"newEnumValue,omitempty"`
	BaseType     string   `json:"baseType,omitempty"`
	CheckExpr    string   `json:"checkExpr,omitempty"`
	ExtensionVer string   `json:"extensionVersion,omitempty"`
	Cascade      bool     `json:"cascade,omitempty"`

	// Synonym / DB Link options
	TargetOwner  string `json:"targetOwner,omitempty"`
	TargetObject string `json:"targetObject,omitempty"`
	DbLinkHost   string `json:"dbLinkHost,omitempty"`
	DbLinkUser   string `json:"dbLinkUser,omitempty"`
	DbLinkPass   string `json:"dbLinkPass,omitempty"`
	Public       bool   `json:"public,omitempty"`

	// DuckDB Macro / ClickHouse SQL UDF options
	Parameters   []string `json:"parameters,omitempty"`
	IsTableMacro bool     `json:"isTableMacro,omitempty"`
	MacroExpr    string   `json:"macroExpr,omitempty"`
}

type ObjectActionRequest struct {
	Database        string      `json:"database"`
	Schema          string      `json:"schema"`
	ObjectName      string      `json:"objectName"`
	ObjectType      string      `json:"objectType"` // TABLE, VIEW, MATERIALIZED_VIEW, SEQUENCE, FUNCTION, PROCEDURE, TRIGGER, EVENT, TYPE, EXTENSION, SYNONYM, PACKAGE, PACKAGE_BODY, DB_LINK, DICTIONARY, MACRO, TABLE_MACRO
	Action          string      `json:"action"`     // DROP, TRUNCATE, OPTIMIZE, ANALYZE, RENAME, COPY_STRUCTURE, COPY_DATA, CREATE_VIEW, CREATE_ROUTINE, CREATE_TRIGGER, COMMENT, CREATE_OR_REPLACE, ALTER, REFRESH, REFRESH_CONCURRENTLY, RESTART, ENABLE, DISABLE, COMPILE, COMPILE_BODY, RELOAD, ADD_ENUM_VALUE, INSTALL, LOAD, HEALTH_CHECK
	TargetName      string      `json:"targetName,omitempty"`
	ViewSQL         string      `json:"viewSql,omitempty"`
	RoutineSQL      string      `json:"routineSql,omitempty"`
	ReplaceExisting bool        `json:"replaceExisting,omitempty"`
	Comment         string      `json:"comment,omitempty"`
	PreviewOnly     bool        `json:"previewOnly,omitempty"`
	Spec            *ObjectSpec `json:"spec,omitempty"`

	// Server-populated security context (ignored during JSON unmarshaling)
	IsAdmin     bool `json:"-"`
	WebMode     bool `json:"-"`
	AllowPrivIP bool `json:"-"`
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
	ID               string `json:"id"`
	Name             string `json:"name"`
	Rows             int64  `json:"rows"`
	BytesOnDisk      int64  `json:"bytesOnDisk"`
	PartsCount       int64  `json:"partsCount"`
	Active           bool   `json:"active"`
	Detached         bool   `json:"detached,omitempty"`
	Strategy         string `json:"strategy,omitempty"`
	Expression       string `json:"expression,omitempty"`
	BoundDescription string `json:"boundDescription,omitempty"`
}

type PartitionActionRequest struct {
	Database    string `json:"database"`
	Schema      string `json:"schema,omitempty"`
	Table       string `json:"table"`
	PartitionID string `json:"partitionId"`
	Action      string `json:"action"` // TRUNCATE, DROP, DETACH, ATTACH, OPTIMIZE, ANALYZE, ADD
	Expression  string `json:"expression,omitempty"`
	PreviewOnly bool   `json:"previewOnly,omitempty"`
}

// Redis Key-Value DTOs
type KVKeySummary struct {
	Key  string `json:"key"`
	Type string `json:"type"`
	TTL  int64  `json:"ttl"`
}

type KVScanResult struct {
	Cursor uint64         `json:"cursor"`
	Keys   []KVKeySummary `json:"keys"`
	DBKeys map[int]int64  `json:"dbKeys"`
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
	MemoryBytes int64              `json:"memoryBytes,omitempty"`
	Encoding    string             `json:"encoding,omitempty"`
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
	Key      string            `json:"key"`
	Type     string            `json:"type"`
	Action   string            `json:"action"` // ADD, UPDATE, DELETE, TRIM
	Field    string            `json:"field,omitempty"`
	OldValue string            `json:"oldValue,omitempty"`
	Value    string            `json:"value,omitempty"`
	Score    float64           `json:"score,omitempty"`
	Index    int               `json:"index,omitempty"`
	ID       string            `json:"id,omitempty"`
	Fields   map[string]string `json:"fields,omitempty"`
	MaxLen   int64             `json:"maxLen,omitempty"`
}

type KVCommandResult struct {
	Command   string `json:"command"`
	Output    string `json:"output"`
	ElapsedMs int64  `json:"elapsedMs"`
}

type KVStreamConsumer struct {
	Name    string `json:"name"`
	Pending int64  `json:"pending"`
	IdleMs  int64  `json:"idleMs"`
}

type KVStreamGroup struct {
	Name            string             `json:"name"`
	Consumers       int64              `json:"consumers"`
	Pending         int64              `json:"pending"`
	LastDeliveredID string             `json:"lastDeliveredId"`
	EntriesRead     int64              `json:"entriesRead,omitempty"`
	Lag             int64              `json:"lag,omitempty"`
	ConsumerList    []KVStreamConsumer `json:"consumerList,omitempty"`
}

type KVStreamInfo struct {
	Length      int64           `json:"length"`
	RadixKeys   int64           `json:"radixKeys,omitempty"`
	FirstID     string          `json:"firstId,omitempty"`
	LastID      string          `json:"lastId,omitempty"`
	GroupsCount int64           `json:"groupsCount"`
	Groups      []KVStreamGroup `json:"groups"`
}

type KVStreamGroupActionRequest struct {
	DB       int    `json:"db"`
	Key      string `json:"key"`
	Action   string `json:"action"` // CREATE, SETID, DESTROY, DELCONSUMER
	Group    string `json:"group"`
	StartID  string `json:"startId,omitempty"`
	Consumer string `json:"consumer,omitempty"`
}

type KVSlowlogEntry struct {
	ID         int64  `json:"id"`
	Timestamp  string `json:"timestamp"`
	DurationUs int64  `json:"durationUs"`
	Command    string `json:"command"`
	ClientAddr string `json:"clientAddr,omitempty"`
	ClientName string `json:"clientName,omitempty"`
}

type KVConfigItem struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// MongoDB Document DTOs
type CollectionSummary struct {
	Name               string `json:"name"`
	Type               string `json:"type,omitempty"` // collection, view, timeseries
	Count              int64  `json:"count"`
	SizeBytes          int64  `json:"sizeBytes"`
	IndexesCount       int    `json:"indexesCount"`
	Capped             bool   `json:"capped,omitempty"`
	MaxBytes           int64  `json:"maxBytes,omitempty"`
	MaxDocs            int64  `json:"maxDocs,omitempty"`
	ViewOn             string `json:"viewOn,omitempty"`
	PipelineJSON       string `json:"pipelineJson,omitempty"`
	ValidatorJSON      string `json:"validatorJson,omitempty"`
	ValidationLevel    string `json:"validationLevel,omitempty"`
	ValidationAction   string `json:"validationAction,omitempty"`
	TimeField          string `json:"timeField,omitempty"`
	MetaField          string `json:"metaField,omitempty"`
	Granularity        string `json:"granularity,omitempty"`
	ExpireAfterSeconds *int64 `json:"expireAfterSeconds,omitempty"`
}

type DocCreateCollectionRequest struct {
	Database           string `json:"database"`
	Collection         string `json:"collection"`
	Type               string `json:"type,omitempty"` // collection, view, timeseries
	Capped             bool   `json:"capped,omitempty"`
	MaxBytes           int64  `json:"maxBytes,omitempty"`
	MaxDocs            int64  `json:"maxDocs,omitempty"`
	ViewOn             string `json:"viewOn,omitempty"`
	PipelineJSON       string `json:"pipelineJson,omitempty"`
	TimeField          string `json:"timeField,omitempty"`
	MetaField          string `json:"metaField,omitempty"`
	Granularity        string `json:"granularity,omitempty"`
	ExpireAfterSeconds *int64 `json:"expireAfterSeconds,omitempty"`
	ValidatorJSON      string `json:"validatorJson,omitempty"`
	ValidationLevel    string `json:"validationLevel,omitempty"`
	ValidationAction   string `json:"validationAction,omitempty"`
}

type DocModCollectionRequest struct {
	Database           string `json:"database"`
	Collection         string `json:"collection"`
	ViewOn             string `json:"viewOn,omitempty"`
	PipelineJSON       string `json:"pipelineJson,omitempty"`
	ValidatorJSON      string `json:"validatorJson,omitempty"`
	ValidationLevel    string `json:"validationLevel,omitempty"`
	ValidationAction   string `json:"validationAction,omitempty"`
	ExpireAfterSeconds *int64 `json:"expireAfterSeconds,omitempty"`
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
	Name               string         `json:"name"`
	Keys               map[string]any `json:"keys"`
	Unique             bool           `json:"unique"`
	Sparse             bool           `json:"sparse,omitempty"`
	ExpireAfterSeconds *int32         `json:"expireAfterSeconds,omitempty"`
	PartialFilterJSON  string         `json:"partialFilterJson,omitempty"`
}

type DocCreateIndexRequest struct {
	Database           string `json:"database"`
	Collection         string `json:"collection"`
	Name               string `json:"name,omitempty"`
	KeysJSON           string `json:"keysJson"`
	Unique             bool   `json:"unique,omitempty"`
	Sparse             bool   `json:"sparse,omitempty"`
	ExpireAfterSeconds *int32 `json:"expireAfterSeconds,omitempty"`
	PartialFilterJSON  string `json:"partialFilterJson,omitempty"`
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
	ListPartitions(ctx context.Context, database, schema, table string) ([]PartitionInfo, error)
	ManagePartition(ctx context.Context, req PartitionActionRequest) (string, error)
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

// SchemaManager is implemented by SQL drivers that support multi-schema namespaces (e.g. PostgreSQL, KingbaseES, SQL Server, Dameng, DuckDB).
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

// KVDiagnosticsDriver adds Stream Consumer Groups, Slowlog, Server Config, and Pub/Sub publish capabilities for Redis.
type KVDiagnosticsDriver interface {
	GetStreamInfo(ctx context.Context, dbIndex int, key string) (KVStreamInfo, error)
	ManageStreamGroup(ctx context.Context, req KVStreamGroupActionRequest) error
	GetSlowlog(ctx context.Context, count int64) ([]KVSlowlogEntry, error)
	ResetSlowlog(ctx context.Context) error
	GetConfig(ctx context.Context, pattern string) ([]KVConfigItem, error)
	SetConfig(ctx context.Context, key, value string) error
	PublishMessage(ctx context.Context, channel, message string) (int64, error)
}

// DocExtendedDriver adds collection rename, streaming collection export, and batch document import capabilities for Document engines.
type DocExtendedDriver interface {
	RenameCollection(ctx context.Context, database, oldName, newName string) error
	StreamExportCollection(ctx context.Context, database, collection, filterJSON, sortJSON, format string, maxRows int, w io.Writer) error
	ImportDocuments(ctx context.Context, database, collection string, r io.Reader, format string, continueOnError bool) (ImportResult, error)
}

// DocGovernanceDriver adds Capped/TimeSeries/View creation, JSON Schema Validator updates (collMod), and TTL/Partial/Sparse index creation for MongoDB.
type DocGovernanceDriver interface {
	GetCollectionOptions(ctx context.Context, database, collection string) (CollectionSummary, error)
	CreateCollectionAdvanced(ctx context.Context, req DocCreateCollectionRequest) error
	ModifyCollectionOptions(ctx context.Context, req DocModCollectionRequest) error
	CreateIndexAdvanced(ctx context.Context, req DocCreateIndexRequest) error
}

// FileExporter is implemented by single-file database drivers (SQLite 3, DuckDB)
// to produce a transactionally consistent standalone binary snapshot of the database.
type FileExporter interface {
	ExportDatabaseFile(ctx context.Context, destPath string) error
}
