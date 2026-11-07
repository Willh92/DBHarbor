package sqlite

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/sqlbase"
)

func init() {
	driver.Register(driver.DriverMeta{
		Type:            "duckdb",
		Name:            "DuckDB",
		Category:        driver.CategoryOLAP,
		BadgeColor:      "#d4b106",
		DefaultPort:     0,
		Description:     "Cross-platform in-process OLAP analytical database (supports .duckdb files and direct SQL queries over Parquet/CSV)",
		SupportsSchemas: false,
		SupportsExplain: true,
		DefaultTypes:    []string{"BIGINT", "INTEGER", "VARCHAR", "DOUBLE", "DECIMAL(18,2)", "BOOLEAN", "TIMESTAMP", "DATE", "JSON"},
		Fields: []driver.ConnFieldSchema{
			{Key: "filePath", Label: "Database File Path", Type: "text", Target: "root", Placeholder: "./data/analytics.duckdb", Required: true},
			{Key: "cliPath", Label: "DuckDB CLI Path (optional, auto-detected from PATH if empty)", Type: "text", Target: "options", Placeholder: "duckdb"},
		},
	}, NewDuckDB)
}

type DuckDBDriver struct {
	cfg      driver.ConnectionConfig
	filePath string
	cliBin   string
	webMode  bool
}

const maxDuckDBJSONBytes = 32 << 20 // 32 MiB stdout safety ceiling for interactive JSON queries

var verifiedDuckDBBinaries sync.Map // map[string]bool

func resolveDuckDBBinary(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return explicit, nil
	}
	binName := "duckdb"
	if runtime.GOOS == "windows" {
		binName = "duckdb.exe"
	}
	if p, err := exec.LookPath(binName); err == nil {
		return p, nil
	}
	localCandidate := filepath.Join("drivers", binName)
	if _, err := os.Stat(localCandidate); err == nil {
		return localCandidate, nil
	}
	return binName, nil
}

func verifyDuckDBExecutable(binPath string) error {
	if _, ok := verifiedDuckDBBinaries.Load(binPath); ok {
		return nil
	}
	resolved := binPath
	if p, err := exec.LookPath(binPath); err == nil {
		resolved = p
	} else if _, statErr := os.Stat(binPath); statErr != nil {
		return fmt.Errorf("duckdb executable (%s) not found; please install DuckDB CLI into system PATH or configure security.duckdbCliPath in config.json", binPath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, resolved, "--version").CombinedOutput()
	if err != nil || !strings.Contains(strings.ToLower(string(out)), "duckdb") {
		return fmt.Errorf("configured binary %q failed DuckDB CLI verification (--version)", binPath)
	}
	verifiedDuckDBBinaries.Store(binPath, true)
	return nil
}

func NewDuckDB(cfg driver.ConnectionConfig) (driver.Driver, error) {
	fp := strings.TrimSpace(cfg.FilePath)
	if fp == "" {
		return nil, errors.New("DuckDB database file path cannot be empty")
	}
	webMode := cfg.Option("__web_mode", "") == "1"
	if webMode && strings.Contains(fp, ":") {
		if !(runtime.GOOS == "windows" && len(fp) >= 3 && ((fp[0] >= 'a' && fp[0] <= 'z') || (fp[0] >= 'A' && fp[0] <= 'Z')) && fp[1] == ':' && (fp[2] == '\\' || fp[2] == '/') && !strings.Contains(fp[2:], ":")) && fp != ":memory:" {
			return nil, errors.New("invalid DuckDB file path syntax")
		}
	}
	if fp != ":memory:" {
		fp = filepath.Clean(fp)
		if dir := filepath.Dir(fp); dir != "" && dir != "." {
			_ = os.MkdirAll(dir, 0o755)
		}
	}
	cliOption := cfg.Option("cliPath", "")
	if webMode {
		// In Web Server Edition, never execute client-supplied cliPath; use server-configured duckdbCliPath or system PATH only
		cliOption = cfg.Option("__server_duckdb_cli", "")
	}
	bin, _ := resolveDuckDBBinary(cliOption)
	return &DuckDBDriver{cfg: cfg, filePath: fp, cliBin: bin, webMode: webMode}, nil
}

func (d *DuckDBDriver) buildDuckDBArgs(sqlStmt string) []string {
	if d.webMode {
		return []string{
			"-cmd", "SET enable_external_access=false; SET autoinstall_known_extensions=false; SET autoload_known_extensions=false;",
			"-json",
			d.filePath,
			sqlStmt,
		}
	}
	return []string{"-json", d.filePath, sqlStmt}
}

func (d *DuckDBDriver) runJSON(ctx context.Context, sqlStmt string) ([]map[string]any, error) {
	if d.webMode {
		if err := verifyDuckDBExecutable(d.cliBin); err != nil {
			return nil, err
		}
	} else {
		if _, err := exec.LookPath(d.cliBin); err != nil {
			if _, statErr := os.Stat(d.cliBin); statErr != nil {
				return nil, fmt.Errorf("duckdb executable (%s) not found; please install DuckDB CLI into system PATH or specify cliPath in connection options", d.cliBin)
			}
		}
	}
	cmd := exec.CommandContext(ctx, d.cliBin, d.buildDuckDBArgs(sqlStmt)...)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(stdoutPipe, maxDuckDBJSONBytes+1))
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if len(raw) > maxDuckDBJSONBytes {
		return nil, fmt.Errorf("DuckDB query output exceeded %d MB memory safety limit; please add a LIMIT clause or use streaming export", maxDuckDBJSONBytes>>20)
	}
	if waitErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = waitErr.Error()
		}
		return nil, errors.New(msg)
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return []map[string]any{}, nil
	}
	var records []map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&records); err != nil {
		return nil, err
	}
	return records, nil
}

func (d *DuckDBDriver) Meta() driver.DriverMeta {
	m, _ := driver.GetMeta("duckdb")
	return m
}

func (d *DuckDBDriver) Close() error { return nil }

func (d *DuckDBDriver) Ping(ctx context.Context) (driver.ServerInfo, error) {
	start := time.Now()
	recs, err := d.runJSON(ctx, "SELECT version() AS ver")
	if err != nil {
		return driver.ServerInfo{}, err
	}
	ver := "DuckDB"
	if len(recs) > 0 {
		ver = fmt.Sprintf("%v", recs[0]["ver"])
	}
	return driver.ServerInfo{Version: ver, Product: "DuckDB", LatencyMs: time.Since(start).Milliseconds()}, nil
}

func (d *DuckDBDriver) ListDatabases(_ context.Context) ([]driver.DatabaseSummary, error) {
	var sz int64
	if st, err := os.Stat(d.filePath); err == nil {
		sz = st.Size()
	}
	return []driver.DatabaseSummary{{Name: "main", SizeBytes: sz}}, nil
}

func (d *DuckDBDriver) CreateDatabase(_ context.Context, _, _ string) error {
	return errors.New("DuckDB single-file connections do not require CREATE DATABASE")
}

func (d *DuckDBDriver) ListSchemas(_ context.Context, _ string) ([]string, error) {
	return []string{}, nil
}

func (d *DuckDBDriver) ListObjects(ctx context.Context, database, _ string) (driver.SchemaObjects, error) {
	res := driver.SchemaObjects{Database: "main", Tables: []driver.ObjectItem{}, Views: []driver.ObjectItem{}, Routines: []driver.ObjectItem{}}
	tRecs, err := d.runJSON(ctx, "SELECT table_name, estimated_size FROM duckdb_tables() WHERE internal = false ORDER BY table_name")
	if err != nil {
		return res, err
	}
	for _, r := range tRecs {
		var rows int64
		if num, ok := r["estimated_size"].(json.Number); ok {
			rows, _ = num.Int64()
		}
		res.Tables = append(res.Tables, driver.ObjectItem{
			Name: fmt.Sprintf("%v", r["table_name"]), Type: "TABLE", Engine: "DuckDB Columnar", Rows: rows,
		})
	}
	vRecs, _ := d.runJSON(ctx, "SELECT view_name FROM duckdb_views() WHERE internal = false ORDER BY view_name")
	for _, r := range vRecs {
		res.Views = append(res.Views, driver.ObjectItem{Name: fmt.Sprintf("%v", r["view_name"]), Type: "VIEW"})
	}
	return res, nil
}

func (d *DuckDBDriver) GetTableStructure(ctx context.Context, database, _, table string) (driver.TableStructure, error) {
	st := driver.TableStructure{Database: "main", Table: table, Engine: "DuckDB Columnar", Columns: []driver.ColumnDef{}, Indexes: []driver.IndexDef{}, ForeignKeys: []driver.ForeignKeyDef{}}
	recs, err := d.runJSON(ctx, fmt.Sprintf("DESCRIBE %s", sqlbase.QuoteIdent(sqlbase.DialectPostgres, table)))
	if err != nil {
		return st, err
	}
	for _, r := range recs {
		defVal := ""
		if r["default"] != nil {
			defVal = fmt.Sprintf("%v", r["default"])
		}
		st.Columns = append(st.Columns, driver.ColumnDef{
			Name:         fmt.Sprintf("%v", r["column_name"]),
			Type:         fmt.Sprintf("%v", r["column_type"]),
			Nullable:     fmt.Sprintf("%v", r["null"]) == "YES",
			DefaultValue: defVal,
			IsPrimaryKey: fmt.Sprintf("%v", r["key"]) == "PRI",
		})
	}
	st.DDL, _ = sqlbase.BuildCreateTableDDL(sqlbase.DialectPostgres, driver.CreateTableRequest{Table: table, Columns: st.Columns})
	return st, nil
}

func (d *DuckDBDriver) BrowseData(ctx context.Context, req driver.BrowseDataRequest) (driver.QueryResult, error) {
	dataSQL, countSQL, err := sqlbase.BuildBrowseQuery(sqlbase.DialectPostgres, req)
	if err != nil {
		return driver.QueryResult{}, err
	}
	resList, err := d.ExecuteSQL(ctx, req.Database, dataSQL, req.PageSize)
	if err != nil || len(resList) == 0 {
		return driver.QueryResult{}, err
	}
	res := resList[0]
	if cntRecs, cErr := d.runJSON(ctx, countSQL); cErr == nil && len(cntRecs) > 0 {
		for _, v := range cntRecs[0] {
			if n, ok := v.(json.Number); ok {
				res.Total, _ = n.Int64()
				res.HasTotal = true
			}
		}
	}
	if st, sErr := d.GetTableStructure(ctx, req.Database, "", req.Table); sErr == nil {
		for _, c := range st.Columns {
			if c.IsPrimaryKey {
				res.PrimaryKeys = append(res.PrimaryKeys, c.Name)
			}
		}
	}
	return res, nil
}

func (d *DuckDBDriver) BuildMutationPreviewSQL(req driver.RowMutationRequest) ([]string, error) {
	return sqlbase.BuildMutationSQLs(sqlbase.DialectPostgres, req)
}

func (d *DuckDBDriver) ApplyRowMutations(ctx context.Context, req driver.RowMutationRequest) (driver.MutationResult, error) {
	sqls, err := d.BuildMutationPreviewSQL(req)
	if err != nil {
		return driver.MutationResult{}, err
	}
	for _, s := range sqls {
		if _, err := d.runJSON(ctx, s); err != nil {
			return driver.MutationResult{ExecutedSQL: sqls}, err
		}
	}
	return driver.MutationResult{Inserted: len(req.Inserts), Updated: len(req.Updates), Deleted: len(req.Deletes), ExecutedSQL: sqls}, nil
}

func (d *DuckDBDriver) CreateTable(ctx context.Context, req driver.CreateTableRequest) (string, error) {
	ddl, err := sqlbase.BuildCreateTableDDL(sqlbase.DialectPostgres, req)
	if err != nil || req.PreviewOnly {
		return ddl, err
	}
	_, err = d.runJSON(ctx, ddl)
	return ddl, err
}

func (d *DuckDBDriver) AlterTable(ctx context.Context, req driver.AlterTableRequest) ([]string, error) {
	stmts, err := sqlbase.BuildAlterTableSQLs(sqlbase.DialectPostgres, req)
	if err != nil || req.PreviewOnly {
		return stmts, err
	}
	for _, s := range stmts {
		if _, err := d.runJSON(ctx, s); err != nil {
			return stmts, err
		}
	}
	return stmts, nil
}

func (d *DuckDBDriver) ManageObject(ctx context.Context, req driver.ObjectActionRequest) error {
	if strings.EqualFold(req.Action, "TRUNCATE") {
		_, err := d.runJSON(ctx, "DELETE FROM "+sqlbase.QuoteIdent(sqlbase.DialectPostgres, req.ObjectName))
		return err
	}
	sqls, err := sqlbase.BuildObjectActionSQLs(sqlbase.DialectPostgres, req)
	if err != nil {
		return err
	}
	for _, s := range sqls {
		if _, err := d.runJSON(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func (d *DuckDBDriver) ExecuteSQL(ctx context.Context, _, query string, maxRows int) ([]driver.QueryResult, error) {
	stmts := sqlbase.SplitStatements(query)
	var out []driver.QueryResult
	for _, stmt := range stmts {
		start := time.Now()
		recs, err := d.runJSON(ctx, stmt)
		if err != nil {
			return out, err
		}
		if maxRows > 0 && len(recs) > maxRows {
			recs = recs[:maxRows]
		}
		var colNames []string
		if len(recs) > 0 {
			for k := range recs[0] {
				colNames = append(colNames, k)
			}
			sort.Strings(colNames)
		}
		cols := make([]driver.QueryColumn, len(colNames))
		for i, c := range colNames {
			cols[i] = driver.QueryColumn{Name: c}
		}
		rows := make([][]any, len(recs))
		for rIdx, rec := range recs {
			row := make([]any, len(colNames))
			for cIdx, c := range colNames {
				row[cIdx] = rec[c]
			}
			rows[rIdx] = row
		}
		out = append(out, driver.QueryResult{
			Statement: stmt, Columns: cols, Rows: rows, Total: int64(len(rows)),
			ElapsedMs: time.Since(start).Milliseconds(), IsSelect: sqlbase.IsQueryStatement(stmt),
		})
	}
	return out, nil
}

func (d *DuckDBDriver) StreamExport(ctx context.Context, req driver.ExportRequest, w io.Writer) error {
	query := strings.TrimSpace(req.CustomSQL)
	if query == "" {
		if strings.TrimSpace(req.Table) == "" {
			// Full DuckDB SQL export
			objs, err := d.ListObjects(ctx, "main", "")
			if err != nil {
				return err
			}
			_, _ = io.WriteString(w, "-- DBHarbor DuckDB SQL Dump\n\n")
			for _, t := range objs.Tables {
				if req.IncludeDDL {
					if st, sErr := d.GetTableStructure(ctx, "main", "", t.Name); sErr == nil && st.DDL != "" {
						_, _ = io.WriteString(w, st.DDL+";\n\n")
					}
				}
				subReq := req
				subReq.Table = t.Name
				subReq.Format = "sql"
				subReq.IncludeDDL = false
				if err := d.StreamExport(ctx, subReq, w); err != nil {
					return err
				}
			}
			return nil
		}
		query = "SELECT * FROM " + sqlbase.QuoteIdent(sqlbase.DialectPostgres, req.Table)
	}
	resList, err := d.ExecuteSQL(ctx, req.Database, query, req.MaxRows)
	if err != nil || len(resList) == 0 {
		return err
	}
	res := resList[0]
	format := strings.ToLower(req.Format)
	switch format {
	case "csv", "csv_bom":
		if format == "csv_bom" {
			_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
		}
		cw := csv.NewWriter(w)
		headers := make([]string, len(res.Columns))
		for i, c := range res.Columns {
			headers[i] = c.Name
		}
		_ = cw.Write(headers)
		for _, r := range res.Rows {
			rec := make([]string, len(r))
			for i, v := range r {
				if v != nil {
					rec[i] = fmt.Sprintf("%v", v)
				}
			}
			_ = cw.Write(rec)
		}
		cw.Flush()
		return cw.Error()
	case "sql":
		if req.IncludeDDL && req.Table != "" {
			if st, sErr := d.GetTableStructure(ctx, "main", "", req.Table); sErr == nil && st.DDL != "" {
				_, _ = io.WriteString(w, st.DDL+";\n\n")
			}
		}
		targetTbl := sqlbase.QuoteIdent(sqlbase.DialectPostgres, req.Table)
		if req.Table == "" {
			targetTbl = `"exported_table"`
		}
		qCols := make([]string, len(res.Columns))
		for i, c := range res.Columns {
			qCols[i] = sqlbase.QuoteIdent(sqlbase.DialectPostgres, c.Name)
		}
		for _, r := range res.Rows {
			vals := make([]string, len(r))
			for i, v := range r {
				vals[i] = sqlbase.FormatLiteralForDialect(sqlbase.DialectPostgres, v)
			}
			_, _ = fmt.Fprintf(w, "INSERT INTO %s (%s) VALUES (%s);\n", targetTbl, strings.Join(qCols, ", "), strings.Join(vals, ", "))
		}
		return nil
	default:
		out := make([]map[string]any, 0, len(res.Rows))
		for _, r := range res.Rows {
			m := make(map[string]any, len(res.Columns))
			for i, c := range res.Columns {
				m[c.Name] = r[i]
			}
			out = append(out, m)
		}
		return json.NewEncoder(w).Encode(out)
	}
}

func (d *DuckDBDriver) BatchImport(ctx context.Context, req driver.ImportRequest) (driver.ImportResult, error) {
	start := time.Now()
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "sql" || format == "" {
		res := driver.ImportResult{}
		err := sqlbase.StreamStatements(req.Reader, func(stmt string) error {
			if _, execErr := d.runJSON(ctx, stmt); execErr != nil {
				if req.ContinueOnError {
					res.FailedCount++
					if len(res.Warnings) < 20 {
						res.Warnings = append(res.Warnings, execErr.Error())
					}
					return nil
				}
				return execErr
			}
			res.ImportedRows++
			return nil
		})
		res.ElapsedMs = time.Since(start).Milliseconds()
		return res, err
	}
	if format == "csv" && strings.TrimSpace(req.Table) != "" {
		cr := csv.NewReader(req.Reader)
		headers, err := cr.Read()
		if err != nil {
			return driver.ImportResult{}, err
		}
		tbl := sqlbase.QuoteIdent(sqlbase.DialectPostgres, req.Table)
		qCols := make([]string, len(headers))
		for i, h := range headers {
			qCols[i] = sqlbase.QuoteIdent(sqlbase.DialectPostgres, strings.TrimPrefix(h, "\uFEFF"))
		}
		imported := 0
		for {
			rec, rErr := cr.Read()
			if errors.Is(rErr, io.EOF) {
				break
			}
			if rErr != nil {
				return driver.ImportResult{ImportedRows: imported}, rErr
			}
			vals := make([]string, len(headers))
			for i := range headers {
				if i >= len(rec) || rec[i] == "" {
					vals[i] = "NULL"
				} else {
					vals[i] = sqlbase.FormatLiteralForDialect(sqlbase.DialectPostgres, rec[i])
				}
			}
			q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", tbl, strings.Join(qCols, ", "), strings.Join(vals, ", "))
			if _, err := d.runJSON(ctx, q); err != nil {
				return driver.ImportResult{ImportedRows: imported}, err
			}
			imported++
		}
		return driver.ImportResult{ImportedRows: imported, ElapsedMs: time.Since(start).Milliseconds()}, nil
	}
	return driver.ImportResult{}, errors.New("DuckDB import supports SQL or CSV format only")
}

func (d *DuckDBDriver) GetAutocompleteMeta(ctx context.Context, _ string) (driver.AutocompleteMeta, error) {
	meta := driver.AutocompleteMeta{Databases: []string{"main"}, Tables: map[string][]string{}}
	recs, err := d.runJSON(ctx, "SELECT table_name, column_name FROM duckdb_columns() WHERE internal = false")
	if err != nil {
		return meta, nil
	}
	for _, r := range recs {
		t := fmt.Sprintf("%v", r["table_name"])
		c := fmt.Sprintf("%v", r["column_name"])
		meta.Tables[t] = append(meta.Tables[t], c)
	}
	return meta, nil
}

func (d *DuckDBDriver) GetMonitorSnapshot(ctx context.Context) (driver.MonitorSnapshot, error) {
	snap := driver.MonitorSnapshot{Timestamp: time.Now().Format(time.RFC3339), Metrics: []driver.MonitorMetric{}, Storage: []driver.StorageItem{}, Sessions: []driver.SessionItem{}}
	if info, err := d.Ping(ctx); err == nil {
		snap.Version = info.Version
	}
	if objs, err := d.ListObjects(ctx, "main", ""); err == nil {
		for _, t := range objs.Tables {
			snap.Storage = append(snap.Storage, driver.StorageItem{Name: t.Name, Category: "TABLE", Rows: t.Rows})
		}
	}
	return snap, nil
}

func (d *DuckDBDriver) KillSession(_ context.Context, _ string) error {
	return errors.New("DuckDB does not have standalone background connection sessions")
}
