package mongodb

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/sqlbase"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func init() {
	driver.Register(driver.DriverMeta{
		Type:        "mongodb",
		Name:        "MongoDB",
		Category:    driver.CategoryDocument,
		BadgeColor:  "#389e0d",
		DefaultPort: 27017,
		Description: "Modern distributed document database (supports form/URI connection modes, dual-view BSON document browsing/editing, index management, and aggregation)",
		Fields: []driver.ConnFieldSchema{
			{Key: "uri", Label: "Connection URI (optional, overrides host/port when set)", Type: "password", Target: "root", Placeholder: "mongodb://user:pass@127.0.0.1:27017/admin"},
			{Key: "host", Label: "Host", Type: "text", Target: "root", DefaultValue: "127.0.0.1"},
			{Key: "port", Label: "Port", Type: "number", Target: "root", DefaultValue: "27017"},
			{Key: "username", Label: "Username", Type: "text", Target: "root"},
			{Key: "password", Label: "Password", Type: "password", Target: "root"},
			{Key: "database", Label: "Default Database", Type: "text", Target: "root", DefaultValue: "admin"},
			{Key: "authSource", Label: "Auth Database (authSource)", Type: "text", Target: "options", DefaultValue: "admin"},
		},
	}, New)
}

type Driver struct {
	cfg    driver.ConnectionConfig
	client *mongo.Client
}

var (
	_ driver.DocumentDriver      = (*Driver)(nil)
	_ driver.DocExtendedDriver   = (*Driver)(nil)
	_ driver.DocGovernanceDriver = (*Driver)(nil)
)

type contextDialerFunc func(ctx context.Context, network, address string) (net.Conn, error)

func (f contextDialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return f(ctx, network, address)
}

func New(cfg driver.ConnectionConfig) (driver.Driver, error) {
	restrictIP := cfg.Option("__restrict_internal_ip", "") == "1"
	uri := strings.TrimSpace(cfg.URI)
	if uri == "" {
		host := strings.TrimSpace(cfg.Host)
		if host == "" && !restrictIP {
			host = "127.0.0.1"
		}
		if restrictIP {
			lowHost := strings.ToLower(host)
			if strings.Contains(lowHost, "/") || strings.Contains(lowHost, `\`) || strings.Contains(lowHost, ".sock") {
				return nil, errors.New("Unix domain socket hosts are restricted for non-administrator MongoDB connections")
			}
			if _, err := sqlbase.ValidateStrictHostname(host); err != nil {
				return nil, err
			}
		}
		port := cfg.Port
		if port == 0 {
			port = 27017
		}
		u := &url.URL{
			Scheme: "mongodb",
			Host:   net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port)),
		}
		if cfg.Username != "" {
			u.User = url.UserPassword(cfg.Username, cfg.Password)
			q := u.Query()
			q.Set("authSource", cfg.Option("authSource", "admin"))
			u.RawQuery = q.Encode()
		}
		uri = u.String()
	} else if restrictIP {
		if err := sqlbase.ValidateMongoURIForRestrictedWeb(uri); err != nil {
			return nil, err
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	clientOpts := options.Client().ApplyURI(uri).SetConnectTimeout(6 * time.Second).SetServerSelectionTimeout(6 * time.Second)
	if restrictIP {
		clientOpts.SetDialer(contextDialerFunc(sqlbase.NewRestrictedDialContext(8 * time.Second)))
	}
	client, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		return nil, err
	}
	return &Driver{cfg: cfg, client: client}, nil
}

func (d *Driver) Meta() driver.DriverMeta {
	m, _ := driver.GetMeta("mongodb")
	return m
}

func (d *Driver) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return d.client.Disconnect(ctx)
}

func (d *Driver) Ping(ctx context.Context) (driver.ServerInfo, error) {
	start := time.Now()
	if err := d.client.Ping(ctx, nil); err != nil {
		return driver.ServerInfo{}, err
	}
	var buildInfo bson.M
	ver := "MongoDB"
	if err := d.client.Database("admin").RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&buildInfo); err == nil {
		if v, ok := buildInfo["version"].(string); ok {
			ver = v
		}
	}
	return driver.ServerInfo{Version: ver, Product: "MongoDB", LatencyMs: time.Since(start).Milliseconds()}, nil
}

func (d *Driver) ListDatabases(ctx context.Context) ([]driver.DatabaseSummary, error) {
	res, err := d.client.ListDatabases(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	out := make([]driver.DatabaseSummary, 0, len(res.Databases))
	for _, db := range res.Databases {
		out = append(out, driver.DatabaseSummary{
			Name:      db.Name,
			SizeBytes: db.SizeOnDisk,
		})
	}
	return out, nil
}

func (d *Driver) CreateDatabase(ctx context.Context, name, _ string) error {
	return d.client.Database(name).CreateCollection(ctx, "_init")
}

func (d *Driver) DropDatabase(ctx context.Context, name string) error {
	return d.client.Database(name).Drop(ctx)
}

func (d *Driver) RenameCollection(ctx context.Context, database, oldName, newName string) error {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if database == "" || oldName == "" || newName == "" {
		return errors.New("database, old collection name, and new collection name are required")
	}
	if oldName == newName {
		return nil
	}
	return d.client.Database("admin").RunCommand(ctx, bson.D{
		{Key: "renameCollection", Value: database + "." + oldName},
		{Key: "to", Value: database + "." + newName},
		{Key: "dropTarget", Value: false},
	}).Err()
}

func (d *Driver) StreamExportCollection(ctx context.Context, database, collection, filterJSON, sortJSON, format string, maxRows int, w io.Writer) error {
	if database == "" || collection == "" {
		return errors.New("database and collection are required")
	}
	filter, err := parseExtJSONOrEmpty(filterJSON)
	if err != nil {
		return fmt.Errorf("invalid Filter JSON: %w", err)
	}
	sortDoc, err := parseExtJSONOrEmpty(sortJSON)
	if err != nil {
		return fmt.Errorf("invalid Sort JSON: %w", err)
	}
	findOpts := options.Find()
	if maxRows > 0 {
		findOpts.SetLimit(int64(maxRows))
	}
	if len(sortDoc) > 0 {
		findOpts.SetSort(sortDoc)
	}
	cur, err := d.client.Database(database).Collection(collection).Find(ctx, filter, findOpts)
	if err != nil {
		return err
	}
	defer cur.Close(ctx)

	bw := bufio.NewWriterSize(w, 64*1024)
	defer bw.Flush()

	normFmt := strings.ToLower(strings.TrimSpace(format))
	if normFmt == "" {
		normFmt = "json"
	}

	switch normFmt {
	case "jsonl":
		for cur.Next(ctx) {
			var m bson.M
			if err := cur.Decode(&m); err != nil {
				return err
			}
			b, err := json.Marshal(normalizeBSONMap(m))
			if err != nil {
				return err
			}
			if _, err := bw.Write(b); err != nil {
				return err
			}
			_ = bw.WriteByte('\n')
		}
		return cur.Err()

	case "csv", "csv_bom":
		if normFmt == "csv_bom" {
			_, _ = bw.Write([]byte{0xEF, 0xBB, 0xBF})
		}
		cw := csv.NewWriter(bw)
		defer cw.Flush()

		var sample []map[string]any
		keySet := map[string]struct{}{}
		for len(sample) < 100 && cur.Next(ctx) {
			var m bson.M
			if err := cur.Decode(&m); err != nil {
				return err
			}
			nm := normalizeBSONMap(m)
			for k := range nm {
				keySet[k] = struct{}{}
			}
			sample = append(sample, nm)
		}
		if len(sample) == 0 {
			return cur.Err()
		}
		headers := make([]string, 0, len(keySet))
		if _, hasID := keySet["_id"]; hasID {
			headers = append(headers, "_id")
			delete(keySet, "_id")
		}
		rest := make([]string, 0, len(keySet))
		for k := range keySet {
			rest = append(rest, k)
		}
		sort.Strings(rest)
		headers = append(headers, rest...)
		if err := cw.Write(headers); err != nil {
			return err
		}
		formatCell := func(v any) string {
			if v == nil {
				return ""
			}
			switch val := v.(type) {
			case string:
				return val
			case map[string]any, []any:
				b, _ := json.Marshal(val)
				return string(b)
			default:
				return fmt.Sprintf("%v", val)
			}
		}
		row := make([]string, len(headers))
		for _, doc := range sample {
			for i, h := range headers {
				row[i] = formatCell(doc[h])
			}
			if err := cw.Write(row); err != nil {
				return err
			}
		}
		for cur.Next(ctx) {
			var m bson.M
			if err := cur.Decode(&m); err != nil {
				return err
			}
			nm := normalizeBSONMap(m)
			for i, h := range headers {
				row[i] = formatCell(nm[h])
			}
			if err := cw.Write(row); err != nil {
				return err
			}
		}
		cw.Flush()
		return cur.Err()

	default: // json
		_, _ = bw.WriteString("[\n")
		first := true
		for cur.Next(ctx) {
			var m bson.M
			if err := cur.Decode(&m); err != nil {
				return err
			}
			b, err := json.MarshalIndent(normalizeBSONMap(m), "  ", "  ")
			if err != nil {
				return err
			}
			if !first {
				_, _ = bw.WriteString(",\n")
			}
			first = false
			_, _ = bw.WriteString("  ")
			if _, err := bw.Write(b); err != nil {
				return err
			}
		}
		_, _ = bw.WriteString("\n]\n")
		return cur.Err()
	}
}

var readOnlyMongoCommands = map[string]struct{}{
	"find": {}, "count": {}, "distinct": {}, "collstats": {}, "dbstats": {},
	"listcollections": {}, "listdatabases": {}, "listindexes": {},
	"buildinfo": {}, "serverstatus": {}, "ping": {}, "whatsmyuri": {},
	"connectionstatus": {}, "hostinfo": {}, "datasize": {},
}

var forbiddenMongoReadOnlyOperators = map[string]struct{}{
	"$out": {}, "$merge": {}, "$where": {}, "$function": {}, "$accumulator": {},
}

func containsForbiddenMongoOperator(v any) (bool, string) {
	switch doc := v.(type) {
	case bson.D:
		for _, elem := range doc {
			kl := strings.ToLower(strings.TrimSpace(elem.Key))
			if _, bad := forbiddenMongoReadOnlyOperators[kl]; bad {
				return true, elem.Key
			}
			if bad, op := containsForbiddenMongoOperator(elem.Value); bad {
				return true, op
			}
		}
	case bson.M:
		for k, val := range doc {
			kl := strings.ToLower(strings.TrimSpace(k))
			if _, bad := forbiddenMongoReadOnlyOperators[kl]; bad {
				return true, k
			}
			if bad, op := containsForbiddenMongoOperator(val); bad {
				return true, op
			}
		}
	case map[string]any:
		for k, val := range doc {
			kl := strings.ToLower(strings.TrimSpace(k))
			if _, bad := forbiddenMongoReadOnlyOperators[kl]; bad {
				return true, k
			}
			if bad, op := containsForbiddenMongoOperator(val); bad {
				return true, op
			}
		}
	case bson.A:
		for _, item := range doc {
			if bad, op := containsForbiddenMongoOperator(item); bad {
				return true, op
			}
		}
	case []any:
		for _, item := range doc {
			if bad, op := containsForbiddenMongoOperator(item); bad {
				return true, op
			}
		}
	}
	return false, ""
}

func validateMongoReadOnlyDoc(cmdDoc bson.D) (bool, string) {
	if len(cmdDoc) == 0 {
		return false, "empty MongoDB command"
	}
	if bad, op := containsForbiddenMongoOperator(cmdDoc); bad {
		return false, fmt.Sprintf("operator %q is not permitted in read-only mode", op)
	}
	cmdName := strings.ToLower(strings.TrimSpace(cmdDoc[0].Key))
	if _, ok := readOnlyMongoCommands[cmdName]; ok {
		return true, ""
	}
	switch cmdName {
	case "explain":
		switch inner := cmdDoc[0].Value.(type) {
		case bson.D:
			return validateMongoReadOnlyDoc(inner)
		case bson.M:
			var sub bson.D
			for k, v := range inner {
				sub = append(sub, bson.E{Key: k, Value: v})
			}
			return validateMongoReadOnlyDoc(sub)
		}
		return false, "explain target must be a read-only command object"
	case "aggregate":
		for _, elem := range cmdDoc {
			if strings.EqualFold(elem.Key, "pipeline") {
				if _, ok := elem.Value.(bson.A); !ok {
					return false, "aggregate pipeline must be an array"
				}
			}
		}
		return true, ""
	}
	return false, fmt.Sprintf("MongoDB command %q is not permitted in read-only mode", cmdDoc[0].Key)
}

// IsReadOnlyCommand validates that a raw MongoDB JSON command is strictly read-only.
func IsReadOnlyCommand(commandJSON string) (bool, string) {
	var cmdDoc bson.D
	if err := bson.UnmarshalExtJSON([]byte(commandJSON), false, &cmdDoc); err != nil {
		return false, "invalid command JSON"
	}
	return validateMongoReadOnlyDoc(cmdDoc)
}

func (d *Driver) resolveDBName(database string) string {
	dbName := strings.TrimSpace(database)
	if dbName == "" {
		dbName = strings.TrimSpace(d.cfg.Database)
	}
	if dbName == "" {
		dbName = "test"
	}
	return dbName
}

func parseCollectionSpec(spec *mongo.CollectionSpecification) driver.CollectionSummary {
	colType := strings.ToLower(strings.TrimSpace(spec.Type))
	if colType == "" {
		colType = "collection"
	}
	summary := driver.CollectionSummary{
		Name: spec.Name,
		Type: colType,
	}
	if len(spec.Options) > 0 {
		var m bson.M
		if bson.Unmarshal(spec.Options, &m) == nil {
			if capped, ok := m["capped"].(bool); ok && capped {
				summary.Capped = true
			}
			if sz := toInt64(m["size"]); sz > 0 {
				summary.MaxBytes = sz
			}
			if mx := toInt64(m["max"]); mx > 0 {
				summary.MaxDocs = mx
			}
			if viewOn, ok := m["viewOn"].(string); ok && viewOn != "" {
				summary.ViewOn = viewOn
			}
			if pipeRaw, exists := m["pipeline"]; exists && pipeRaw != nil {
				if b, err := json.Marshal(normalizeBSONVal(pipeRaw)); err == nil {
					summary.PipelineJSON = string(b)
				}
			}
			if valRaw, exists := m["validator"]; exists && valRaw != nil {
				norm := normalizeBSONVal(valRaw)
				if mp, ok := norm.(map[string]any); !ok || len(mp) > 0 {
					if b, err := json.Marshal(norm); err == nil {
						summary.ValidatorJSON = string(b)
					}
				}
			}
			if lvl, ok := m["validationLevel"].(string); ok && lvl != "" {
				summary.ValidationLevel = lvl
			}
			if act, ok := m["validationAction"].(string); ok && act != "" {
				summary.ValidationAction = act
			}
			if tsDoc, ok := m["timeseries"].(bson.M); ok {
				summary.Type = "timeseries"
				if tf, ok := tsDoc["timeField"].(string); ok {
					summary.TimeField = tf
				}
				if mf, ok := tsDoc["metaField"].(string); ok {
					summary.MetaField = mf
				}
				if gr, ok := tsDoc["granularity"].(string); ok {
					summary.Granularity = gr
				}
			}
			if expRaw, exists := m["expireAfterSeconds"]; exists && expRaw != nil {
				exp := toInt64(expRaw)
				summary.ExpireAfterSeconds = &exp
			}
		}
	}
	return summary
}

func parseExtJSONArray(raw string) (bson.A, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !strings.HasPrefix(trimmed, "[") {
		return nil, errors.New("pipeline JSON must be a valid JSON array, e.g. [{\"$match\": {}}]")
	}
	var arr bson.A
	if err := bson.UnmarshalExtJSON([]byte(trimmed), false, &arr); err != nil {
		return nil, fmt.Errorf("invalid pipeline JSON array: %w", err)
	}
	return arr, nil
}

func (d *Driver) ListCollections(ctx context.Context, database string) ([]driver.CollectionSummary, error) {
	db := d.client.Database(d.resolveDBName(database))
	specs, err := db.ListCollectionSpecifications(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	out := make([]driver.CollectionSummary, 0, len(specs))
	for _, spec := range specs {
		if spec == nil || spec.Name == "system.views" {
			continue
		}
		summary := parseCollectionSpec(spec)
		if summary.Type != "view" {
			cnt, _ := db.Collection(spec.Name).EstimatedDocumentCount(ctx)
			var stats bson.M
			var sizeBytes int64
			var idxCnt int
			if db.RunCommand(ctx, bson.D{{Key: "collStats", Value: spec.Name}}).Decode(&stats) == nil {
				sizeBytes = toInt64(stats["size"])
				idxCnt = int(toInt64(stats["nindexes"]))
			}
			summary.Count = cnt
			summary.SizeBytes = sizeBytes
			summary.IndexesCount = idxCnt
		}
		out = append(out, summary)
	}
	return out, nil
}

func (d *Driver) GetCollectionOptions(ctx context.Context, database, collection string) (driver.CollectionSummary, error) {
	colName := strings.TrimSpace(collection)
	if colName == "" {
		return driver.CollectionSummary{}, errors.New("collection name is required")
	}
	db := d.client.Database(d.resolveDBName(database))
	specs, err := db.ListCollectionSpecifications(ctx, bson.D{{Key: "name", Value: colName}})
	if err != nil {
		return driver.CollectionSummary{}, err
	}
	if len(specs) == 0 || specs[0] == nil {
		return driver.CollectionSummary{}, fmt.Errorf("collection %q not found", colName)
	}
	summary := parseCollectionSpec(specs[0])
	if summary.Type != "view" {
		summary.Count, _ = db.Collection(colName).EstimatedDocumentCount(ctx)
		var stats bson.M
		if db.RunCommand(ctx, bson.D{{Key: "collStats", Value: colName}}).Decode(&stats) == nil {
			summary.SizeBytes = toInt64(stats["size"])
			summary.IndexesCount = int(toInt64(stats["nindexes"]))
		}
	}
	return summary, nil
}

func (d *Driver) CreateCollection(ctx context.Context, database, collection string) error {
	return d.client.Database(d.resolveDBName(database)).CreateCollection(ctx, collection)
}

func (d *Driver) CreateCollectionAdvanced(ctx context.Context, req driver.DocCreateCollectionRequest) error {
	name := strings.TrimSpace(req.Collection)
	if name == "" {
		return errors.New("collection name is required")
	}
	db := d.client.Database(d.resolveDBName(req.Database))
	colType := strings.ToLower(strings.TrimSpace(req.Type))

	if colType == "view" {
		viewOn := strings.TrimSpace(req.ViewOn)
		if viewOn == "" {
			return errors.New("viewOn source collection is required when creating a MongoDB view")
		}
		pipeline, err := parseExtJSONArray(req.PipelineJSON)
		if err != nil {
			return err
		}
		return db.RunCommand(ctx, bson.D{
			{Key: "create", Value: name},
			{Key: "viewOn", Value: viewOn},
			{Key: "pipeline", Value: pipeline},
		}).Err()
	}

	cmd := bson.D{{Key: "create", Value: name}}
	switch {
	case colType == "timeseries":
		timeField := strings.TrimSpace(req.TimeField)
		if timeField == "" {
			return errors.New("timeField is required when creating a time-series collection")
		}
		tsDoc := bson.D{{Key: "timeField", Value: timeField}}
		if mf := strings.TrimSpace(req.MetaField); mf != "" {
			tsDoc = append(tsDoc, bson.E{Key: "metaField", Value: mf})
		}
		if gr := strings.TrimSpace(req.Granularity); gr != "" {
			tsDoc = append(tsDoc, bson.E{Key: "granularity", Value: gr})
		}
		cmd = append(cmd, bson.E{Key: "timeseries", Value: tsDoc})
		if req.ExpireAfterSeconds != nil {
			cmd = append(cmd, bson.E{Key: "expireAfterSeconds", Value: *req.ExpireAfterSeconds})
		}
	case req.Capped:
		maxBytes := req.MaxBytes
		if maxBytes <= 0 {
			maxBytes = 1048576
		}
		cmd = append(cmd,
			bson.E{Key: "capped", Value: true},
			bson.E{Key: "size", Value: maxBytes},
		)
		if req.MaxDocs > 0 {
			cmd = append(cmd, bson.E{Key: "max", Value: req.MaxDocs})
		}
	}

	if rawVal := strings.TrimSpace(req.ValidatorJSON); rawVal != "" {
		var valDoc bson.D
		if err := bson.UnmarshalExtJSON([]byte(rawVal), false, &valDoc); err != nil {
			return fmt.Errorf("invalid validator JSON: %w", err)
		}
		cmd = append(cmd, bson.E{Key: "validator", Value: valDoc})
	}
	if lvl := strings.TrimSpace(req.ValidationLevel); lvl != "" {
		cmd = append(cmd, bson.E{Key: "validationLevel", Value: lvl})
	}
	if act := strings.TrimSpace(req.ValidationAction); act != "" {
		cmd = append(cmd, bson.E{Key: "validationAction", Value: act})
	}

	return db.RunCommand(ctx, cmd).Err()
}

func (d *Driver) ModifyCollectionOptions(ctx context.Context, req driver.DocModCollectionRequest) error {
	name := strings.TrimSpace(req.Collection)
	if name == "" {
		return errors.New("collection name is required")
	}
	db := d.client.Database(d.resolveDBName(req.Database))
	cmd := bson.D{{Key: "collMod", Value: name}}

	viewOn := strings.TrimSpace(req.ViewOn)
	rawPipe := strings.TrimSpace(req.PipelineJSON)
	if viewOn != "" || rawPipe != "" {
		if viewOn == "" {
			return errors.New("viewOn is required when modifying a view")
		}
		pipeline, err := parseExtJSONArray(rawPipe)
		if err != nil {
			return err
		}
		cmd = append(cmd,
			bson.E{Key: "viewOn", Value: viewOn},
			bson.E{Key: "pipeline", Value: pipeline},
		)
	} else {
		if rawVal := strings.TrimSpace(req.ValidatorJSON); rawVal != "" {
			var valDoc bson.D
			if err := bson.UnmarshalExtJSON([]byte(rawVal), false, &valDoc); err != nil {
				return fmt.Errorf("invalid validator JSON: %w", err)
			}
			cmd = append(cmd, bson.E{Key: "validator", Value: valDoc})
		}
		if lvl := strings.TrimSpace(req.ValidationLevel); lvl != "" {
			cmd = append(cmd, bson.E{Key: "validationLevel", Value: lvl})
		}
		if act := strings.TrimSpace(req.ValidationAction); act != "" {
			cmd = append(cmd, bson.E{Key: "validationAction", Value: act})
		}
		if req.ExpireAfterSeconds != nil {
			cmd = append(cmd, bson.E{Key: "expireAfterSeconds", Value: *req.ExpireAfterSeconds})
		}
	}

	if len(cmd) <= 1 {
		return errors.New("no collection or view options provided for modification")
	}
	return db.RunCommand(ctx, cmd).Err()
}

func (d *Driver) DropCollection(ctx context.Context, database, collection string) error {
	return d.client.Database(database).Collection(collection).Drop(ctx)
}

func (d *Driver) FindDocuments(ctx context.Context, req driver.DocFindRequest) (driver.DocFindResult, error) {
	start := time.Now()
	coll := d.client.Database(req.Database).Collection(req.Collection)

	filter, err := parseExtJSONOrEmpty(req.FilterJSON)
	if err != nil {
		return driver.DocFindResult{}, fmt.Errorf("invalid Filter JSON: %w", err)
	}
	sortDoc, err := parseExtJSONOrEmpty(req.SortJSON)
	if err != nil {
		return driver.DocFindResult{}, fmt.Errorf("invalid Sort JSON: %w", err)
	}
	projDoc, err := parseExtJSONOrEmpty(req.ProjectionJSON)
	if err != nil {
		return driver.DocFindResult{}, fmt.Errorf("invalid Projection JSON: %w", err)
	}

	page := req.Page
	if page < 1 {
		page = 1
	}
	ps := req.PageSize
	if ps <= 0 || ps > 500 {
		ps = 50
	}

	total, _ := coll.CountDocuments(ctx, filter)
	findOpts := options.Find().SetSkip(int64((page - 1) * ps)).SetLimit(int64(ps))
	if len(sortDoc) > 0 {
		findOpts.SetSort(sortDoc)
	} else {
		findOpts.SetSort(bson.D{{Key: "_id", Value: -1}})
	}
	if len(projDoc) > 0 {
		findOpts.SetProjection(projDoc)
	}

	cur, err := coll.Find(ctx, filter, findOpts)
	if err != nil {
		return driver.DocFindResult{}, err
	}
	defer cur.Close(ctx)

	var docs []map[string]any
	for cur.Next(ctx) {
		var m bson.M
		if err := cur.Decode(&m); err != nil {
			return driver.DocFindResult{}, err
		}
		docs = append(docs, normalizeBSONMap(m))
	}
	if docs == nil {
		docs = []map[string]any{}
	}
	return driver.DocFindResult{
		Documents: docs,
		Total:     total,
		ElapsedMs: time.Since(start).Milliseconds(),
	}, nil
}

func (d *Driver) InsertDocument(ctx context.Context, database, collection, docJSON string) (string, error) {
	var doc any
	if err := bson.UnmarshalExtJSON([]byte(docJSON), false, &doc); err != nil {
		return "", fmt.Errorf("failed to parse document JSON: %w", err)
	}
	res, err := d.client.Database(database).Collection(collection).InsertOne(ctx, doc)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%v", res.InsertedID), nil
}

func (d *Driver) ImportDocuments(ctx context.Context, database, collection string, r io.Reader, format string, continueOnError bool) (driver.ImportResult, error) {
	start := time.Now()
	if strings.TrimSpace(database) == "" {
		database = d.cfg.Database
	}
	if strings.TrimSpace(database) == "" {
		database = "test"
	}
	if strings.TrimSpace(collection) == "" {
		return driver.ImportResult{}, errors.New("collection name is required")
	}

	br := bufio.NewReaderSize(io.LimitReader(r, 128<<20), 64*1024)
	// Skip leading UTF-8 BOM / whitespace to detect format without buffering 128 MiB in memory
	var firstByte byte
	hasFirst := false
	for {
		b, bErr := br.ReadByte()
		if bErr != nil {
			if errors.Is(bErr, io.EOF) {
				return driver.ImportResult{}, errors.New("import content is empty")
			}
			return driver.ImportResult{}, bErr
		}
		if b == 0xEF {
			peek, _ := br.Peek(2)
			if len(peek) == 2 && peek[0] == 0xBB && peek[1] == 0xBF {
				_, _ = br.Discard(2)
				continue
			}
		}
		if b == ' ' || b == '\t' || b == '\r' || b == '\n' {
			continue
		}
		firstByte = b
		hasFirst = true
		_ = br.UnreadByte()
		break
	}
	if !hasFirst {
		return driver.ImportResult{}, errors.New("import content is empty")
	}

	coll := d.client.Database(database).Collection(collection)
	res := driver.ImportResult{}
	normFmt := strings.ToLower(strings.TrimSpace(format))

	flushBatch := func(batch []any) error {
		if len(batch) == 0 {
			return nil
		}
		insOpts := options.InsertMany().SetOrdered(!continueOnError)
		insRes, iErr := coll.InsertMany(ctx, batch, insOpts)
		if insRes != nil {
			res.ImportedRows += len(insRes.InsertedIDs)
		}
		if iErr != nil {
			if continueOnError {
				inserted := 0
				if insRes != nil {
					inserted = len(insRes.InsertedIDs)
				}
				if diff := len(batch) - inserted; diff > 0 {
					res.FailedCount += diff
				}
				if len(res.Warnings) < 20 {
					res.Warnings = append(res.Warnings, iErr.Error())
				}
				return nil
			}
			return iErr
		}
		return nil
	}

	if firstByte == '[' && normFmt != "jsonl" && normFmt != "ndjson" {
		dec := json.NewDecoder(br)
		tok, tErr := dec.Token()
		if tErr != nil || tok != json.Delim('[') {
			return res, fmt.Errorf("invalid JSON document array: %v", tErr)
		}
		batch := make([]any, 0, 200)
		for dec.More() {
			var rawItem json.RawMessage
			if dErr := dec.Decode(&rawItem); dErr != nil {
				if continueOnError {
					res.FailedCount++
					if len(res.Warnings) < 20 {
						res.Warnings = append(res.Warnings, dErr.Error())
					}
					break
				}
				return res, fmt.Errorf("invalid document in JSON array: %w", dErr)
			}
			var doc bson.M
			if uErr := bson.UnmarshalExtJSON(rawItem, false, &doc); uErr != nil {
				if continueOnError {
					res.FailedCount++
					if len(res.Warnings) < 20 {
						res.Warnings = append(res.Warnings, uErr.Error())
					}
					continue
				}
				return res, fmt.Errorf("invalid document in JSON array: %w", uErr)
			}
			batch = append(batch, doc)
			if len(batch) >= 200 {
				if fErr := flushBatch(batch); fErr != nil {
					res.ElapsedMs = time.Since(start).Milliseconds()
					return res, fErr
				}
				batch = batch[:0]
			}
		}
		if fErr := flushBatch(batch); fErr != nil {
			res.ElapsedMs = time.Since(start).Milliseconds()
			return res, fErr
		}
		res.ElapsedMs = time.Since(start).Milliseconds()
		return res, nil
	}

	if normFmt == "jsonl" || normFmt == "ndjson" {
		sc := bufio.NewScanner(br)
		sc.Buffer(make([]byte, 64*1024), 16<<20)
		batch := make([]any, 0, 200)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var doc bson.M
			if uErr := bson.UnmarshalExtJSON([]byte(line), false, &doc); uErr != nil {
				if continueOnError {
					res.FailedCount++
					if len(res.Warnings) < 20 {
						res.Warnings = append(res.Warnings, uErr.Error())
					}
					continue
				}
				return res, fmt.Errorf("invalid JSONL document: %w", uErr)
			}
			batch = append(batch, doc)
			if len(batch) >= 200 {
				if fErr := flushBatch(batch); fErr != nil {
					res.ElapsedMs = time.Since(start).Milliseconds()
					return res, fErr
				}
				batch = batch[:0]
			}
		}
		if sErr := sc.Err(); sErr != nil {
			res.ElapsedMs = time.Since(start).Milliseconds()
			return res, sErr
		}
		if fErr := flushBatch(batch); fErr != nil {
			res.ElapsedMs = time.Since(start).Milliseconds()
			return res, fErr
		}
		res.ElapsedMs = time.Since(start).Milliseconds()
		return res, nil
	}

	// Stream one or more concatenated / newline-delimited JSON objects via json.Decoder
	dec := json.NewDecoder(br)
	batch := make([]any, 0, 200)
	for {
		var rawItem json.RawMessage
		if dErr := dec.Decode(&rawItem); dErr != nil {
			if errors.Is(dErr, io.EOF) {
				break
			}
			return res, fmt.Errorf("invalid JSON document: %w", dErr)
		}
		var doc bson.M
		if uErr := bson.UnmarshalExtJSON(rawItem, false, &doc); uErr != nil {
			if continueOnError {
				res.FailedCount++
				if len(res.Warnings) < 20 {
					res.Warnings = append(res.Warnings, uErr.Error())
				}
				continue
			}
			return res, fmt.Errorf("invalid JSON document: %w", uErr)
		}
		batch = append(batch, doc)
		if len(batch) >= 200 {
			if fErr := flushBatch(batch); fErr != nil {
				res.ElapsedMs = time.Since(start).Milliseconds()
				return res, fErr
			}
			batch = batch[:0]
		}
	}
	if fErr := flushBatch(batch); fErr != nil {
		res.ElapsedMs = time.Since(start).Milliseconds()
		return res, fErr
	}
	res.ElapsedMs = time.Since(start).Milliseconds()
	return res, nil
}

func (d *Driver) UpdateDocument(ctx context.Context, database, collection, idStr, docJSON string) error {
	var doc bson.M
	if err := bson.UnmarshalExtJSON([]byte(docJSON), false, &doc); err != nil {
		return fmt.Errorf("failed to parse document JSON: %w", err)
	}
	delete(doc, "_id")
	idVal := parseObjectIDOrValue(idStr)
	res, err := d.client.Database(database).Collection(collection).ReplaceOne(ctx, bson.M{"_id": idVal}, doc)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		// Retry raw string _id if hex string happened to not be ObjectID
		_, err = d.client.Database(database).Collection(collection).ReplaceOne(ctx, bson.M{"_id": idStr}, doc)
		return err
	}
	return nil
}

func (d *Driver) DeleteDocuments(ctx context.Context, database, collection string, ids []string) (int64, error) {
	var total int64
	coll := d.client.Database(database).Collection(collection)
	for _, rawID := range ids {
		res, err := coll.DeleteOne(ctx, bson.M{"_id": parseObjectIDOrValue(rawID)})
		if err != nil {
			return total, err
		}
		if res.DeletedCount == 0 {
			res, _ = coll.DeleteOne(ctx, bson.M{"_id": rawID})
		}
		if res != nil {
			total += res.DeletedCount
		}
	}
	return total, nil
}

func (d *Driver) ListIndexes(ctx context.Context, database, collection string) ([]driver.DocIndexInfo, error) {
	cur, err := d.client.Database(d.resolveDBName(database)).Collection(collection).Indexes().List(ctx)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	list := []driver.DocIndexInfo{}
	for cur.Next(ctx) {
		var m bson.M
		if cur.Decode(&m) == nil {
			name, _ := m["name"].(string)
			uniq, _ := m["unique"].(bool)
			sparse, _ := m["sparse"].(bool)
			keys := map[string]any{}
			if km, ok := m["key"].(bson.M); ok {
				keys = normalizeBSONMap(km)
			}
			info := driver.DocIndexInfo{
				Name:   name,
				Keys:   keys,
				Unique: uniq,
				Sparse: sparse,
			}
			if expRaw, exists := m["expireAfterSeconds"]; exists && expRaw != nil {
				sec := int32(toInt64(expRaw))
				info.ExpireAfterSeconds = &sec
			}
			if pfRaw, exists := m["partialFilterExpression"]; exists && pfRaw != nil {
				if b, mErr := json.Marshal(normalizeBSONVal(pfRaw)); mErr == nil {
					info.PartialFilterJSON = string(b)
				}
			}
			list = append(list, info)
		}
	}
	return list, nil
}

func (d *Driver) CreateIndex(ctx context.Context, database, collection, keysJSON string, unique bool) error {
	return d.CreateIndexAdvanced(ctx, driver.DocCreateIndexRequest{
		Database:   database,
		Collection: collection,
		KeysJSON:   keysJSON,
		Unique:     unique,
	})
}

func (d *Driver) CreateIndexAdvanced(ctx context.Context, req driver.DocCreateIndexRequest) error {
	if strings.TrimSpace(req.Collection) == "" {
		return errors.New("collection name is required")
	}
	keysDoc, err := parseExtJSONOrEmpty(req.KeysJSON)
	if err != nil || len(keysDoc) == 0 {
		return errors.New("index key definition must be a non-empty JSON object, e.g. {\"email\": 1}")
	}
	idxOpts := options.Index().SetUnique(req.Unique)
	if name := strings.TrimSpace(req.Name); name != "" {
		idxOpts.SetName(name)
	}
	if req.Sparse {
		idxOpts.SetSparse(true)
	}
	if req.ExpireAfterSeconds != nil {
		idxOpts.SetExpireAfterSeconds(*req.ExpireAfterSeconds)
	}
	if rawPF := strings.TrimSpace(req.PartialFilterJSON); rawPF != "" {
		pfDoc, pfErr := parseExtJSONOrEmpty(rawPF)
		if pfErr != nil || len(pfDoc) == 0 {
			return fmt.Errorf("invalid partialFilterExpression JSON: %v", pfErr)
		}
		idxOpts.SetPartialFilterExpression(pfDoc)
	}
	_, err = d.client.Database(d.resolveDBName(req.Database)).Collection(req.Collection).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    keysDoc,
		Options: idxOpts,
	})
	return err
}

func (d *Driver) DropIndex(ctx context.Context, database, collection, indexName string) error {
	_, err := d.client.Database(database).Collection(collection).Indexes().DropOne(ctx, indexName)
	return err
}

func (d *Driver) RunCommand(ctx context.Context, database, commandJSON string) (any, error) {
	if database == "" {
		database = "admin"
	}
	var cmdDoc bson.D
	if err := bson.UnmarshalExtJSON([]byte(commandJSON), false, &cmdDoc); err != nil {
		return nil, fmt.Errorf("invalid command JSON: %w", err)
	}
	var result bson.M
	if err := d.client.Database(database).RunCommand(ctx, cmdDoc).Decode(&result); err != nil {
		return nil, err
	}
	return normalizeBSONMap(result), nil
}

func (d *Driver) GetMonitorSnapshot(ctx context.Context) (driver.MonitorSnapshot, error) {
	snap := driver.MonitorSnapshot{
		Timestamp:      time.Now().Format(time.RFC3339),
		Metrics:        []driver.MonitorMetric{},
		Storage:        []driver.StorageItem{},
		Sessions:       []driver.SessionItem{},
		CanKillSession: true,
	}
	var status bson.M
	if d.client.Database("admin").RunCommand(ctx, bson.D{{Key: "serverStatus", Value: 1}}).Decode(&status) == nil {
		snap.Version = fmt.Sprintf("%v", status["version"])
		snap.UptimeSec = toInt64(status["uptime"])
		if conns, ok := status["connections"].(bson.M); ok {
			snap.Metrics = append(snap.Metrics,
				driver.MonitorMetric{Key: "current", Label: "Current Connections", Value: fmt.Sprintf("%d", toInt64(conns["current"]))},
				driver.MonitorMetric{Key: "available", Label: "Available Connections", Value: fmt.Sprintf("%d", toInt64(conns["available"]))},
			)
		}
		if mem, ok := status["mem"].(bson.M); ok {
			snap.Metrics = append(snap.Metrics,
				driver.MonitorMetric{Key: "resident", Label: "Resident Memory", Value: fmt.Sprintf("%d", toInt64(mem["resident"])), Unit: "MB"},
			)
		}
	}

	if dbs, err := d.ListDatabases(ctx); err == nil {
		for _, db := range dbs {
			snap.Storage = append(snap.Storage, driver.StorageItem{Name: db.Name, Category: "DATABASE", SizeBytes: db.SizeBytes})
		}
	}

	var curOp bson.M
	if d.client.Database("admin").RunCommand(ctx, bson.D{{Key: "currentOp", Value: 1}}).Decode(&curOp) == nil {
		if inprog, ok := curOp["inprog"].(bson.A); ok {
			for _, item := range inprog {
				m, ok := item.(bson.M)
				if !ok {
					continue
				}
				opid := fmt.Sprintf("%v", m["opid"])
				cmdBytes, _ := json.Marshal(normalizeBSONVal(m["command"]))
				snap.Sessions = append(snap.Sessions, driver.SessionItem{
					ID:          opid,
					Host:        fmt.Sprintf("%v", m["client"]),
					Database:    fmt.Sprintf("%v", m["ns"]),
					Command:     fmt.Sprintf("%v", m["op"]),
					State:       "ACTIVE",
					DurationSec: toInt64(m["secs_running"]),
					Query:       string(cmdBytes),
				})
			}
		}
	}
	return snap, nil
}

func (d *Driver) KillSession(ctx context.Context, sessionID string) error {
	var opid int64
	_, _ = fmt.Sscanf(sessionID, "%d", &opid)
	return d.client.Database("admin").RunCommand(ctx, bson.D{
		{Key: "killOp", Value: 1},
		{Key: "op", Value: opid},
	}).Err()
}

func parseExtJSONOrEmpty(s string) (bson.D, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "{}" {
		return bson.D{}, nil
	}
	var doc bson.D
	err := bson.UnmarshalExtJSON([]byte(s), false, &doc)
	return doc, err
}

func parseObjectIDOrValue(raw string) any {
	raw = strings.TrimSpace(raw)
	if oid, err := primitive.ObjectIDFromHex(raw); err == nil {
		return oid
	}
	return raw
}

func normalizeBSONMap(m bson.M) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = normalizeBSONVal(v)
	}
	return out
}

func normalizeBSONVal(v any) any {
	switch val := v.(type) {
	case primitive.ObjectID:
		return val.Hex()
	case primitive.DateTime:
		return val.Time().Format(time.RFC3339)
	case bson.M:
		return normalizeBSONMap(val)
	case bson.D:
		m := make(map[string]any, len(val))
		for _, e := range val {
			m[e.Key] = normalizeBSONVal(e.Value)
		}
		return m
	case bson.A:
		arr := make([]any, len(val))
		for i, item := range val {
			arr[i] = normalizeBSONVal(item)
		}
		return arr
	default:
		return val
	}
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	default:
		return 0
	}
}
