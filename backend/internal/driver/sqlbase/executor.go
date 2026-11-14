package sqlbase

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"dbharbor/backend/internal/driver"
)

// Queryer abstracts *sql.DB, *sql.Conn, and *sql.Tx for unified query execution.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// TxQueryer abstracts *sql.DB and *sql.Conn when transaction initiation is needed on a bound session.
type TxQueryer interface {
	Queryer
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

var exportBufPool = sync.Pool{
	New: func() any {
		return bufio.NewWriterSize(io.Discard, 64*1024)
	},
}

type scanSlicePair struct {
	rawVals  []any
	scanPtrs []any
}

var scanSlicePool = sync.Pool{
	New: func() any {
		rv := make([]any, 64)
		sp := make([]any, 64)
		for i := range rv {
			sp[i] = &rv[i]
		}
		return &scanSlicePair{rawVals: rv, scanPtrs: sp}
	},
}

func acquireBufWriter(w io.Writer) *bufio.Writer {
	bw := exportBufPool.Get().(*bufio.Writer)
	bw.Reset(w)
	return bw
}

func releaseBufWriter(bw *bufio.Writer) {
	bw.Reset(io.Discard)
	exportBufPool.Put(bw)
}

// IsProtectedSystemDatabase prevents accidental deletion of built-in engine catalogs.
func IsProtectedSystemDatabase(dbType, name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return true
	}
	switch lower {
	case "information_schema", "performance_schema", "mysql", "sys",
		"postgres", "template0", "template1", "pg_catalog",
		"master", "tempdb", "model", "msdb",
		"admin", "local", "config",
		"system", "default", "main", "temp", "sysaux", "ctisys":
		return true
	}
	if (dbType == "kingbase" || dbType == "postgres") && (lower == "security" || lower == "sysmac") {
		return true
	}
	return false
}

func IsQueryStatement(stmt string) bool {
	s := strings.TrimSpace(StripLeadingSQLComments(stmt))
	if s == "" {
		return false
	}
	upper := strings.ToUpper(s)
	prefixes := []string{"SELECT", "SHOW", "DESCRIBE", "DESC ", "EXPLAIN", "WITH", "PRAGMA", "VALUES", "TABLE "}
	for _, p := range prefixes {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	if strings.Contains(upper, " RETURNING ") {
		return true
	}
	return false
}

// IsDDLStatement reports whether stmt mutates schema metadata so caches can be invalidated.
func IsDDLStatement(stmt string) bool {
	kw := leadingSQLKeyword(stmt)
	switch kw {
	case "CREATE", "ALTER", "DROP", "RENAME", "TRUNCATE":
		return true
	default:
		return false
	}
}

const maxQueryResultBytes = 32 << 20 // 32 MiB ceiling for interactive query result sets

func approxRowBytes(row []any) int {
	n := 24
	for _, v := range row {
		n += 24 // 64-bit Go interface + string/boxed value header overhead per cell
		switch x := v.(type) {
		case nil:
			n += 8
		case string:
			n += 16 + len(x)
		case []byte:
			n += 24 + len(x)
		default:
			n += 16
		}
	}
	return n
}

func ExecuteSingleQuery(ctx context.Context, db *sql.DB, stmt string, maxRows int) (driver.QueryResult, error) {
	return ExecuteSingleQueryConn(ctx, db, stmt, maxRows)
}

func ExecuteSingleQueryConn(ctx context.Context, q Queryer, stmt string, maxRows int) (driver.QueryResult, error) {
	remainingBytes := maxQueryResultBytes
	return executeSingleQueryWithBudget(ctx, q, stmt, maxRows, &remainingBytes)
}

func executeSingleQueryWithBudget(ctx context.Context, q Queryer, stmt string, maxRows int, remainingBytes *int) (driver.QueryResult, error) {
	start := time.Now()
	if maxRows <= 0 {
		maxRows = 1000
	}
	if remainingBytes == nil {
		b := maxQueryResultBytes
		remainingBytes = &b
	}

	if IsQueryStatement(stmt) {
		rows, err := q.QueryContext(ctx, stmt)
		if err != nil {
			return driver.QueryResult{}, err
		}
		defer rows.Close()

		colTypes, err := rows.ColumnTypes()
		if err != nil {
			return driver.QueryResult{}, err
		}
		colNames, err := rows.Columns()
		if err != nil {
			return driver.QueryResult{}, err
		}

		colCount := len(colNames)
		cols := make([]driver.QueryColumn, colCount)
		scanMeta := make([]ColumnScanMeta, colCount)
		for i, name := range colNames {
			tName := ""
			nullable := true
			if i < len(colTypes) && colTypes[i] != nil {
				tName = colTypes[i].DatabaseTypeName()
				if n, ok := colTypes[i].Nullable(); ok {
					nullable = n
				}
			}
			scanMeta[i] = NewColumnScanMeta(tName)
			cols[i] = driver.QueryColumn{
				Name:     name,
				Type:     tName,
				Nullable: nullable,
			}
		}

		var rawVals []any
		var scanPtrs []any
		if colCount > 0 && colCount <= 64 {
			pair := scanSlicePool.Get().(*scanSlicePair)
			rawVals = pair.rawVals[:colCount]
			scanPtrs = pair.scanPtrs[:colCount]
			defer func() {
				for i := 0; i < colCount; i++ {
					pair.rawVals[i] = nil
				}
				scanSlicePool.Put(pair)
			}()
		} else {
			rawVals = make([]any, colCount)
			scanPtrs = make([]any, colCount)
			for i := range rawVals {
				scanPtrs[i] = &rawVals[i]
			}
		}

		initCap := 64
		if maxRows < initCap {
			initCap = maxRows
		}
		data := make([][]any, 0, initCap)
		truncated := false
		truncatedByMem := false
		for rows.Next() {
			if len(data) >= maxRows {
				truncated = true
				break
			}
			for i := range rawVals {
				rawVals[i] = nil
			}
			if err := rows.Scan(scanPtrs...); err != nil {
				return driver.QueryResult{}, err
			}
			rowOut := make([]any, colCount)
			for i, v := range rawVals {
				rowOut[i] = NormalizeValueFast(v, scanMeta[i])
			}
			rowBytes := approxRowBytes(rowOut)
			if rowBytes > *remainingBytes && (len(data) > 0 || *remainingBytes < maxQueryResultBytes) {
				truncated = true
				truncatedByMem = true
				break
			}
			*remainingBytes -= rowBytes
			if *remainingBytes < 0 {
				*remainingBytes = 0
			}
			data = append(data, rowOut)
		}
		if err := rows.Err(); err != nil {
			return driver.QueryResult{}, err
		}

		msg := ""
		if truncatedByMem {
			msg = "Result truncated at 32 MB memory safety ceiling"
		}

		return driver.QueryResult{
			Statement: stmt,
			Columns:   cols,
			Rows:      data,
			Total:     int64(len(data)),
			ElapsedMs: time.Since(start).Milliseconds(),
			IsSelect:  true,
			Truncated: truncated,
			Message:   msg,
		}, nil
	}

	res, err := q.ExecContext(ctx, stmt)
	if err != nil {
		return driver.QueryResult{}, err
	}
	affected, _ := res.RowsAffected()
	return driver.QueryResult{
		Statement:    stmt,
		Columns:      []driver.QueryColumn{},
		Rows:         [][]any{},
		AffectedRows: affected,
		ElapsedMs:    time.Since(start).Milliseconds(),
		IsSelect:     false,
		Message:      fmt.Sprintf("Executed successfully, rows affected: %d", affected),
	}, nil
}

func ExecuteMultiSQL(ctx context.Context, db *sql.DB, script string, maxRows int) ([]driver.QueryResult, error) {
	return ExecuteMultiSQLConn(ctx, db, script, maxRows)
}

func ExecuteMultiSQLConn(ctx context.Context, q Queryer, script string, maxRows int) ([]driver.QueryResult, error) {
	stmts := SplitStatements(script)
	if len(stmts) == 0 {
		return []driver.QueryResult{}, nil
	}
	remainingBytes := maxQueryResultBytes
	results := make([]driver.QueryResult, 0, len(stmts))
	for _, stmt := range stmts {
		r, err := executeSingleQueryWithBudget(ctx, q, stmt, maxRows, &remainingBytes)
		if err != nil {
			return results, fmt.Errorf("statement execution failed [%s]: %w", truncateStr(stmt, 120), err)
		}
		results = append(results, r)
	}
	return results, nil
}

func execBatchOnQueryer(ctx context.Context, q Queryer, sqls []string) error {
	for _, stmt := range sqls {
		if _, err := q.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// QueryCountConn executes a single scalar COUNT(*) query over a bound Queryer.
func QueryCountConn(ctx context.Context, q Queryer, countSQL string) (int64, bool) {
	rows, err := q.QueryContext(ctx, countSQL)
	if err != nil {
		return 0, false
	}
	defer rows.Close()
	if !rows.Next() {
		return 0, false
	}
	var total int64
	if err := rows.Scan(&total); err != nil {
		return 0, false
	}
	return total, true
}

func ApplyMutations(ctx context.Context, db *sql.DB, d Dialect, req driver.RowMutationRequest) (driver.MutationResult, error) {
	return ApplyMutationsConn(ctx, db, d, req)
}

func ApplyMutationsConn(ctx context.Context, q TxQueryer, d Dialect, req driver.RowMutationRequest) (driver.MutationResult, error) {
	sqls, err := BuildMutationSQLs(d, req)
	if err != nil {
		return driver.MutationResult{}, err
	}
	out := driver.MutationResult{
		ExecutedSQL: sqls,
	}
	if len(sqls) == 0 {
		return out, nil
	}

	// ClickHouse ALTER TABLE mutations are non-transactional
	if d == DialectClickHouse {
		if err := execBatchOnQueryer(ctx, q, sqls); err != nil {
			return out, err
		}
		out.Inserted = len(req.Inserts)
		out.Updated = len(req.Updates)
		out.Deleted = len(req.Deletes)
		return out, nil
	}

	tx, err := q.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := execBatchOnQueryer(ctx, tx, sqls); err != nil {
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return out, err
	}
	out.Inserted = len(req.Inserts)
	out.Updated = len(req.Updates)
	out.Deleted = len(req.Deletes)
	return out, nil
}

// TableExportItem describes a single table (and its optional DDL + secondary index DDLs) for multi-table exports.
type TableExportItem struct {
	Table     string
	DDL       string
	IndexDDLs []string
}

// StreamMultiTableExport streams a full database or multi-table export (SQL dump, JSON, or JSONL).
func StreamMultiTableExport(ctx context.Context, db *sql.DB, d Dialect, req driver.ExportRequest, items []TableExportItem, w io.Writer) error {
	return StreamMultiTableExportConn(ctx, db, d, req, items, w)
}

// StreamMultiTableExportConn streams a full database or multi-table export over a bound Queryer (*sql.DB or *sql.Conn).
func StreamMultiTableExportConn(ctx context.Context, q Queryer, d Dialect, req driver.ExportRequest, items []TableExportItem, w io.Writer) error {
	bw := acquireBufWriter(w)
	defer releaseBufWriter(bw)

	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = "sql"
	}

	switch format {
	case "sql":
		_, _ = bw.WriteString("-- DBHarbor Database SQL Dump\n")
		_, _ = bw.WriteString("-- Dialect: " + string(d) + " | Database: " + req.Database + "\n")
		_, _ = bw.WriteString("-- Generated at: " + time.Now().Format(time.RFC3339) + "\n\n")

		for _, item := range items {
			if err := ctx.Err(); err != nil {
				return err
			}
			tbl := QualifyTable(d, req.Database, req.Schema, item.Table)
			_, _ = bw.WriteString("-- --------------------------------------------------------\n")
			_, _ = bw.WriteString("-- Table: " + item.Table + "\n")
			_, _ = bw.WriteString("-- --------------------------------------------------------\n")
			if req.DropIfExists {
				if d == DialectOracle {
					_, _ = bw.WriteString("DROP TABLE " + tbl + ";\n")
				} else {
					_, _ = bw.WriteString("DROP TABLE IF EXISTS " + tbl + ";\n")
				}
			}
			if req.IncludeDDL && strings.TrimSpace(item.DDL) != "" {
				ddl := strings.TrimRight(strings.TrimSpace(item.DDL), ";")
				_, _ = bw.WriteString(ddl + ";\n")
				for _, idxDDL := range item.IndexDDLs {
					cleanIdx := strings.TrimRight(strings.TrimSpace(idxDDL), ";")
					if cleanIdx != "" {
						_, _ = bw.WriteString(cleanIdx + ";\n")
					}
				}
				_, _ = bw.WriteString("\n")
			}

			query := "SELECT * FROM " + tbl
			if req.MaxRows > 0 && d != DialectSQLServer && d != DialectOracle {
				query += fmt.Sprintf(" LIMIT %d", req.MaxRows)
			}
			if err := streamTableSQLInserts(ctx, q, d, tbl, query, req.MaxRows, bw); err != nil {
				return fmt.Errorf("failed to export table %s: %w", item.Table, err)
			}
			_, _ = bw.WriteString("\n")
		}
		return bw.Flush()

	case "json":
		dbJSON, _ := json.Marshal(req.Database)
		_, _ = bw.WriteString("{\n  \"database\": ")
		_, _ = bw.Write(dbJSON)
		_, _ = bw.WriteString(",\n  \"tables\": {\n")
		for idx, item := range items {
			if idx > 0 {
				_, _ = bw.WriteString(",\n")
			}
			tblJSON, _ := json.Marshal(item.Table)
			_, _ = bw.WriteString("    ")
			_, _ = bw.Write(tblJSON)
			_, _ = bw.WriteString(": ")
			subReq := req
			subReq.Table = item.Table
			subReq.Format = "json"
			if err := StreamExportConn(ctx, q, d, subReq, "", bw); err != nil {
				return err
			}
		}
		_, _ = bw.WriteString("\n  }\n}\n")
		return bw.Flush()

	case "jsonl":
		for _, item := range items {
			subReq := req
			subReq.Table = item.Table
			subReq.Format = "jsonl"
			if err := StreamExportConn(ctx, q, d, subReq, "", bw); err != nil {
				return err
			}
		}
		return bw.Flush()

	default:
		return errors.New("full-database or multi-table export requires SQL, JSON, or JSONL format (CSV/Markdown apply only to single tables or custom SQL)")
	}
}

func StreamExport(ctx context.Context, db *sql.DB, d Dialect, req driver.ExportRequest, ddlText string, w io.Writer) error {
	return StreamExportConn(ctx, db, d, req, ddlText, w)
}

func StreamExportConn(ctx context.Context, q Queryer, d Dialect, req driver.ExportRequest, ddlText string, w io.Writer) error {
	query := strings.TrimSpace(req.CustomSQL)
	tbl := QualifyTable(d, req.Database, req.Schema, req.Table)
	if query == "" {
		if req.Table == "" {
			return errors.New("export requires specifying a table or custom SQL")
		}
		query = "SELECT * FROM " + tbl
		if req.MaxRows > 0 && d != DialectSQLServer && d != DialectOracle {
			query += fmt.Sprintf(" LIMIT %d", req.MaxRows)
		}
	}

	bw := acquireBufWriter(w)
	defer releaseBufWriter(bw)

	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = "sql"
	}

	if format == "sql" {
		_, _ = bw.WriteString("-- DBHarbor SQL Export\n-- Generated at " + time.Now().Format(time.RFC3339) + "\n\n")
		targetTbl := tbl
		if req.Table == "" {
			targetTbl = QuoteIdent(d, "exported_table")
		} else if req.DropIfExists {
			if d == DialectOracle {
				_, _ = bw.WriteString("DROP TABLE " + targetTbl + ";\n")
			} else {
				_, _ = bw.WriteString("DROP TABLE IF EXISTS " + targetTbl + ";\n")
			}
		}
		if req.IncludeDDL && strings.TrimSpace(ddlText) != "" {
			cleanDDL := strings.TrimRight(strings.TrimSpace(ddlText), ";")
			_, _ = bw.WriteString(cleanDDL + ";\n\n")
		}
		if err := streamTableSQLInserts(ctx, q, d, targetTbl, query, req.MaxRows, bw); err != nil {
			return err
		}
		return bw.Flush()
	}

	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()

	colNames, err := rows.Columns()
	if err != nil {
		return err
	}
	colCount := len(colNames)
	colTypes, _ := rows.ColumnTypes()
	scanMeta := make([]ColumnScanMeta, colCount)
	for i := range colNames {
		if i < len(colTypes) && colTypes[i] != nil {
			scanMeta[i] = NewColumnScanMeta(colTypes[i].DatabaseTypeName())
		}
	}

	rawVals := make([]any, colCount)
	scanPtrs := make([]any, colCount)
	for i := range rawVals {
		scanPtrs[i] = &rawVals[i]
	}
	normVals := make([]any, colCount)

	scanNext := func() error {
		for i := range rawVals {
			rawVals[i] = nil
		}
		if err := rows.Scan(scanPtrs...); err != nil {
			return err
		}
		for i, v := range rawVals {
			normVals[i] = NormalizeValueFast(v, scanMeta[i])
		}
		return nil
	}

	switch format {
	case "csv", "csv_bom":
		if format == "csv_bom" {
			_, _ = bw.Write([]byte{0xEF, 0xBB, 0xBF})
		}
		cw := csv.NewWriter(bw)
		if err := cw.Write(colNames); err != nil {
			return err
		}
		rec := make([]string, colCount)
		count := 0
		for rows.Next() {
			if req.MaxRows > 0 && count >= req.MaxRows {
				break
			}
			if err := scanNext(); err != nil {
				return err
			}
			for i, v := range normVals {
				if v == nil {
					rec[i] = ""
				} else if s, ok := v.(string); ok {
					rec[i] = s
				} else {
					rec[i] = fmt.Sprintf("%v", v)
				}
			}
			if err := cw.Write(rec); err != nil {
				return err
			}
			count++
			if count%500 == 0 {
				cw.Flush()
			}
		}
		cw.Flush()
		if err := cw.Error(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return bw.Flush()

	case "json":
		_, _ = bw.WriteString("[\n")
		obj := make(map[string]any, colCount)
		first := true
		count := 0
		for rows.Next() {
			if req.MaxRows > 0 && count >= req.MaxRows {
				break
			}
			if err := scanNext(); err != nil {
				return err
			}
			for i, col := range colNames {
				obj[col] = normVals[i]
			}
			b, err := json.Marshal(obj)
			if err != nil {
				return err
			}
			if !first {
				_, _ = bw.WriteString(",\n")
			}
			first = false
			if _, err := bw.Write(b); err != nil {
				return err
			}
			count++
		}
		_, _ = bw.WriteString("\n]")
		if err := rows.Err(); err != nil {
			return err
		}
		return bw.Flush()

	case "jsonl":
		obj := make(map[string]any, colCount+1)
		count := 0
		for rows.Next() {
			if req.MaxRows > 0 && count >= req.MaxRows {
				break
			}
			if err := scanNext(); err != nil {
				return err
			}
			if req.Table != "" {
				obj["__table__"] = req.Table
			}
			for i, col := range colNames {
				obj[col] = normVals[i]
			}
			b, err := json.Marshal(obj)
			if err != nil {
				return err
			}
			if _, err := bw.Write(b); err != nil {
				return err
			}
			_ = bw.WriteByte('\n')
			count++
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return bw.Flush()

	case "markdown":
		// Header row
		_, _ = bw.WriteString("| ")
		for i, c := range colNames {
			if i > 0 {
				_, _ = bw.WriteString(" | ")
			}
			_, _ = bw.WriteString(escapeMarkdownCell(c))
		}
		_, _ = bw.WriteString(" |\n| ")
		for i := range colNames {
			if i > 0 {
				_, _ = bw.WriteString(" | ")
			}
			_, _ = bw.WriteString("---")
		}
		_, _ = bw.WriteString(" |\n")

		count := 0
		for rows.Next() {
			if req.MaxRows > 0 && count >= req.MaxRows {
				break
			}
			if err := scanNext(); err != nil {
				return err
			}
			_, _ = bw.WriteString("| ")
			for i, v := range normVals {
				if i > 0 {
					_, _ = bw.WriteString(" | ")
				}
				if v == nil {
					_, _ = bw.WriteString("`NULL`")
				} else {
					_, _ = bw.WriteString(escapeMarkdownCell(fmt.Sprintf("%v", v)))
				}
			}
			_, _ = bw.WriteString(" |\n")
			count++
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return bw.Flush()

	default:
		return fmt.Errorf("unsupported export format: %s", req.Format)
	}
}

func streamTableSQLInserts(ctx context.Context, q Queryer, d Dialect, targetTbl, query string, maxRows int, bw *bufio.Writer) error {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()

	colNames, err := rows.Columns()
	if err != nil {
		return err
	}
	colCount := len(colNames)
	if colCount == 0 {
		return nil
	}
	colTypes, _ := rows.ColumnTypes()
	scanMeta := make([]ColumnScanMeta, colCount)
	for i := range colNames {
		if i < len(colTypes) && colTypes[i] != nil {
			scanMeta[i] = NewColumnScanMeta(colTypes[i].DatabaseTypeName())
		}
	}

	qCols := make([]string, colCount)
	for i, c := range colNames {
		qCols[i] = QuoteIdent(d, c)
	}
	insertPrefix := "INSERT INTO " + targetTbl + " (" + strings.Join(qCols, ", ") + ") VALUES\n"

	// Oracle prior to 23ai does not support multi-row INSERT VALUES (...), (...); emit 1 row per INSERT for Oracle
	batchCap := 100
	if d == DialectOracle || d == DialectDameng {
		batchCap = 1
	}

	rawVals := make([]any, colCount)
	scanPtrs := make([]any, colCount)
	for i := range rawVals {
		scanPtrs[i] = &rawVals[i]
	}

	count := 0
	inBatch := 0
	for rows.Next() {
		if maxRows > 0 && count >= maxRows {
			break
		}
		for i := range rawVals {
			rawVals[i] = nil
		}
		if err := rows.Scan(scanPtrs...); err != nil {
			return err
		}

		if batchCap == 1 {
			_, _ = bw.WriteString("INSERT INTO " + targetTbl + " (" + strings.Join(qCols, ", ") + ") VALUES (")
			for i, v := range rawVals {
				if i > 0 {
					_, _ = bw.WriteString(", ")
				}
				_, _ = bw.WriteString(FormatLiteralForDialect(d, NormalizeValueFast(v, scanMeta[i])))
			}
			if _, err := bw.WriteString(");\n"); err != nil {
				return err
			}
			count++
			continue
		}

		if inBatch == 0 {
			if _, err := bw.WriteString(insertPrefix); err != nil {
				return err
			}
			_, _ = bw.WriteString("  (")
		} else {
			_, _ = bw.WriteString(",\n  (")
		}
		for i, v := range rawVals {
			if i > 0 {
				_, _ = bw.WriteString(", ")
			}
			_, _ = bw.WriteString(FormatLiteralForDialect(d, NormalizeValueFast(v, scanMeta[i])))
		}
		_ = bw.WriteByte(')')
		inBatch++
		count++

		if inBatch >= batchCap {
			if _, err := bw.WriteString(";\n"); err != nil {
				return err
			}
			inBatch = 0
		}
	}
	if inBatch > 0 {
		if _, err := bw.WriteString(";\n"); err != nil {
			return err
		}
	}
	return rows.Err()
}

func leadingSQLKeyword(stmt string) string {
	s := StripLeadingSQLComments(stmt)
	end := 0
	for end < len(s) && ((s[end] >= 'a' && s[end] <= 'z') || (s[end] >= 'A' && s[end] <= 'Z') || s[end] == '_') {
		end++
	}
	return strings.ToUpper(s[:end])
}

func isAutoTxBatchable(d Dialect, kw string) bool {
	switch kw {
	case "INSERT", "UPDATE", "DELETE", "REPLACE":
		return true
	case "CREATE", "DROP", "ALTER":
		return d == DialectSQLite
	default:
		return false
	}
}

func BatchImport(ctx context.Context, db *sql.DB, d Dialect, req driver.ImportRequest) (driver.ImportResult, error) {
	return BatchImportConn(ctx, db, d, req, nil)
}

func BatchImportConn(
	ctx context.Context,
	q TxQueryer,
	d Dialect,
	req driver.ImportRequest,
	validateStmt func(stmt string) error,
) (driver.ImportResult, error) {
	start := time.Now()
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = "sql"
	}

	switch format {
	case "sql":
		res := driver.ImportResult{}
		execConn := q
		if sqlDB, ok := q.(*sql.DB); ok {
			conn, connErr := sqlDB.Conn(ctx)
			if connErr != nil {
				return res, connErr
			}
			defer conn.Close()
			execConn = conn
		}

		canAutoBatch := !req.ContinueOnError && d != DialectClickHouse
		userInTx := false
		var autoTx *sql.Tx
		autoTxCount := 0

		commitAutoTx := func() error {
			if autoTx == nil {
				return nil
			}
			cErr := autoTx.Commit()
			autoTx = nil
			autoTxCount = 0
			return cErr
		}
		defer func() {
			if autoTx != nil {
				_ = autoTx.Rollback()
			}
		}()

		err := StreamStatements(req.Reader, func(stmt string) error {
			if cErr := ctx.Err(); cErr != nil {
				return cErr
			}
			if validateStmt != nil {
				if vErr := validateStmt(stmt); vErr != nil {
					if autoTx != nil {
						_ = autoTx.Rollback()
						autoTx = nil
						autoTxCount = 0
					}
					return vErr
				}
			}
			kw := leadingSQLKeyword(stmt)

			if canAutoBatch {
				switch kw {
				case "BEGIN", "START":
					if cErr := commitAutoTx(); cErr != nil {
						return cErr
					}
					userInTx = true
				case "COMMIT", "END", "ROLLBACK":
					if cErr := commitAutoTx(); cErr != nil {
						return cErr
					}
					userInTx = false
				}

				if !userInTx && isAutoTxBatchable(d, kw) {
					if autoTx == nil {
						tx, txErr := execConn.BeginTx(ctx, nil)
						if txErr == nil {
							autoTx = tx
							autoTxCount = 0
						}
					}
					if autoTx != nil {
						if _, execErr := autoTx.ExecContext(ctx, stmt); execErr != nil {
							_ = autoTx.Rollback()
							autoTx = nil
							autoTxCount = 0
							return fmt.Errorf("SQL import execution failed [%s]: %w", truncateStr(stmt, 100), execErr)
						}
						res.ImportedRows++
						autoTxCount++
						if autoTxCount >= 500 {
							if cErr := commitAutoTx(); cErr != nil {
								return cErr
							}
						}
						return nil
					}
				} else if !userInTx && autoTx != nil {
					if cErr := commitAutoTx(); cErr != nil {
						return cErr
					}
				}
			}

			if _, execErr := execConn.ExecContext(ctx, stmt); execErr != nil {
				if req.ContinueOnError {
					res.FailedCount++
					if len(res.Warnings) < 20 {
						res.Warnings = append(res.Warnings, fmt.Sprintf("[%s]: %v", truncateStr(stmt, 80), execErr))
					}
					return nil
				}
				return fmt.Errorf("SQL import execution failed [%s]: %w", truncateStr(stmt, 100), execErr)
			}
			res.ImportedRows++
			return nil
		})
		if err == nil {
			err = commitAutoTx()
		}
		res.ElapsedMs = time.Since(start).Milliseconds()
		return res, err

	case "csv":
		if strings.TrimSpace(req.Table) == "" {
			return driver.ImportResult{}, errors.New("CSV import requires a target table")
		}
		cr := csv.NewReader(req.Reader)
		cr.LazyQuotes = true
		cr.FieldsPerRecord = -1

		headers, err := cr.Read()
		if err != nil {
			return driver.ImportResult{}, fmt.Errorf("failed to read CSV header: %w", err)
		}
		if len(headers) == 0 {
			return driver.ImportResult{}, errors.New("CSV header is empty")
		}
		headers[0] = strings.TrimPrefix(headers[0], "\uFEFF")
		for i := range headers {
			headers[i] = strings.TrimSpace(headers[i])
		}

		tbl := QualifyTable(d, req.Database, req.Schema, req.Table)
		qCols := make([]string, len(headers))
		for i, h := range headers {
			qCols[i] = QuoteIdent(d, h)
		}
		colList := strings.Join(qCols, ", ")

		batchCap := 200
		if d == DialectOracle || d == DialectDameng {
			batchCap = 1
		}

		var execTarget Queryer = q
		var tx *sql.Tx
		if d != DialectClickHouse && !req.ContinueOnError {
			if t, txErr := q.BeginTx(ctx, nil); txErr == nil {
				tx = t
				execTarget = tx
				defer func() { _ = tx.Rollback() }()
			}
		}

		res := driver.ImportResult{}
		batchRows := make([][]string, 0, batchCap)

		flushCSVBatch := func() error {
			if len(batchRows) == 0 {
				return nil
			}
			var sb strings.Builder
			sb.WriteString("INSERT INTO ")
			sb.WriteString(tbl)
			sb.WriteString(" (")
			sb.WriteString(colList)
			sb.WriteString(") VALUES ")
			for rIdx, vals := range batchRows {
				if rIdx > 0 {
					sb.WriteString(", ")
				}
				sb.WriteByte('(')
				sb.WriteString(strings.Join(vals, ", "))
				sb.WriteByte(')')
			}
			if _, execErr := execTarget.ExecContext(ctx, sb.String()); execErr != nil {
				if req.ContinueOnError {
					// Fallback row-by-row for this batch
					for _, vals := range batchRows {
						single := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", tbl, colList, strings.Join(vals, ", "))
						if _, sErr := execTarget.ExecContext(ctx, single); sErr != nil {
							res.FailedCount++
							if len(res.Warnings) < 20 {
								res.Warnings = append(res.Warnings, sErr.Error())
							}
						} else {
							res.ImportedRows++
						}
					}
					batchRows = batchRows[:0]
					return nil
				}
				return fmt.Errorf("CSV batch insert failed (near row %d): %w", res.ImportedRows+1, execErr)
			}
			res.ImportedRows += len(batchRows)
			batchRows = batchRows[:0]
			return nil
		}

		lineNum := 1
		for {
			rec, rErr := cr.Read()
			if errors.Is(rErr, io.EOF) {
				break
			}
			lineNum++
			if rErr != nil {
				if req.ContinueOnError {
					res.FailedCount++
					if len(res.Warnings) < 20 {
						res.Warnings = append(res.Warnings, fmt.Sprintf("line %d parse error: %v", lineNum, rErr))
					}
					continue
				}
				return res, fmt.Errorf("CSV parse error (line %d): %w", lineNum, rErr)
			}
			vals := make([]string, len(headers))
			for i := range headers {
				if i >= len(rec) || rec[i] == "" {
					vals[i] = "NULL"
				} else {
					vals[i] = FormatLiteralForDialect(d, rec[i])
				}
			}
			batchRows = append(batchRows, vals)
			if len(batchRows) >= batchCap {
				if err := flushCSVBatch(); err != nil {
					return res, err
				}
			}
		}
		if err := flushCSVBatch(); err != nil {
			return res, err
		}
		if tx != nil {
			if err := tx.Commit(); err != nil {
				return res, err
			}
		}
		res.ElapsedMs = time.Since(start).Milliseconds()
		return res, nil

	case "json":
		if strings.TrimSpace(req.Table) == "" {
			return driver.ImportResult{}, errors.New("JSON import requires a target table")
		}
		dec := json.NewDecoder(req.Reader)
		dec.UseNumber()
		tok, err := dec.Token()
		if err != nil {
			return driver.ImportResult{}, fmt.Errorf("failed to read JSON content: %w", err)
		}
		delim, ok := tok.(json.Delim)
		if !ok || delim != '[' {
			return driver.ImportResult{}, errors.New("JSON import requires a top-level object array [...] or JSONL format")
		}

		res, err := batchInsertJSONRecords(ctx, q, d, req, func(next *map[string]any) (bool, error) {
			if !dec.More() {
				return false, nil
			}
			if err := dec.Decode(next); err != nil {
				return false, err
			}
			return true, nil
		})
		res.ElapsedMs = time.Since(start).Milliseconds()
		return res, err

	case "jsonl":
		if strings.TrimSpace(req.Table) == "" {
			return driver.ImportResult{}, errors.New("JSONL import requires a target table")
		}
		scanner := bufio.NewScanner(req.Reader)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		res, err := batchInsertJSONRecords(ctx, q, d, req, func(next *map[string]any) (bool, error) {
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line == "" {
					continue
				}
				dec := json.NewDecoder(strings.NewReader(line))
				dec.UseNumber()
				if err := dec.Decode(next); err != nil {
					return false, err
				}
				del(*next, "__table__")
				return true, nil
			}
			return false, scanner.Err()
		})
		res.ElapsedMs = time.Since(start).Milliseconds()
		return res, err

	default:
		return driver.ImportResult{}, fmt.Errorf("unsupported import format: %s", req.Format)
	}
}

func del(m map[string]any, k string) {
	delete(m, k)
}

func batchInsertJSONRecords(
	ctx context.Context,
	q TxQueryer,
	d Dialect,
	req driver.ImportRequest,
	readNext func(out *map[string]any) (bool, error),
) (driver.ImportResult, error) {
	var execTarget Queryer = q
	var tx *sql.Tx
	if d != DialectClickHouse && !req.ContinueOnError {
		if t, txErr := q.BeginTx(ctx, nil); txErr == nil {
			tx = t
			execTarget = tx
			defer func() { _ = tx.Rollback() }()
		}
	}

	tbl := QualifyTable(d, req.Database, req.Schema, req.Table)
	batchCap := 200
	if d == DialectOracle || d == DialectDameng {
		batchCap = 1
	}

	res := driver.ImportResult{}
	batch := make([]map[string]any, 0, batchCap)

	flushBatch := func() error {
		if len(batch) == 0 {
			return nil
		}
		colSet := map[string]struct{}{}
		for _, r := range batch {
			for k := range r {
				if k != "__dbharbor_rowid__" && k != "__table__" {
					colSet[k] = struct{}{}
				}
			}
		}
		if len(colSet) == 0 {
			batch = batch[:0]
			return nil
		}
		cols := make([]string, 0, len(colSet))
		for k := range colSet {
			cols = append(cols, k)
		}
		sort.Strings(cols)
		qCols := make([]string, len(cols))
		for i, c := range cols {
			qCols[i] = QuoteIdent(d, c)
		}
		colList := strings.Join(qCols, ", ")

		var sb strings.Builder
		sb.WriteString("INSERT INTO ")
		sb.WriteString(tbl)
		sb.WriteString(" (")
		sb.WriteString(colList)
		sb.WriteString(") VALUES ")
		for rIdx, r := range batch {
			if rIdx > 0 {
				sb.WriteString(", ")
			}
			sb.WriteByte('(')
			for cIdx, c := range cols {
				if cIdx > 0 {
					sb.WriteString(", ")
				}
				val := normalizeJSONNumber(r[c])
				sb.WriteString(FormatLiteralForDialect(d, val))
			}
			sb.WriteByte(')')
		}
		if _, err := execTarget.ExecContext(ctx, sb.String()); err != nil {
			if req.ContinueOnError {
				res.FailedCount += len(batch)
				if len(res.Warnings) < 20 {
					res.Warnings = append(res.Warnings, err.Error())
				}
				batch = batch[:0]
				return nil
			}
			return err
		}
		res.ImportedRows += len(batch)
		batch = batch[:0]
		return nil
	}

	for {
		var item map[string]any
		ok, err := readNext(&item)
		if err != nil {
			return res, err
		}
		if !ok {
			break
		}
		if len(item) == 0 {
			continue
		}
		batch = append(batch, item)
		if len(batch) >= batchCap {
			if err := flushBatch(); err != nil {
				return res, err
			}
		}
	}
	if err := flushBatch(); err != nil {
		return res, err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return res, err
		}
	}
	return res, nil
}

func normalizeJSONNumber(v any) any {
	if num, ok := v.(json.Number); ok {
		if i, err := num.Int64(); err == nil {
			return i
		}
		if f, err := num.Float64(); err == nil {
			return f
		}
		return num.String()
	}
	return v
}

func escapeMarkdownCell(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "|", `\|`)
	return s
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
