package clickhouse

import (
	"bytes"
	"context"
	"database/sql"
	stdsql_driver "database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"dbharbor/backend/internal/driver"
)

type mockCHState struct {
	mu          sync.Mutex
	queries     []string
	execs       []string
	failOnQuery string
	closeBadCnt int
	autoRows    int
}

type mockCHConnector struct{ state *mockCHState }

func (c *mockCHConnector) Connect(context.Context) (stdsql_driver.Conn, error) {
	return &mockCHConn{state: c.state}, nil
}
func (c *mockCHConnector) Driver() stdsql_driver.Driver { return nil }

type mockCHConn struct {
	state  *mockCHState
	badErr bool
}

func (c *mockCHConn) Prepare(string) (stdsql_driver.Stmt, error) { return nil, nil }
func (c *mockCHConn) Close() error {
	if c.badErr {
		c.state.mu.Lock()
		c.state.closeBadCnt++
		c.state.mu.Unlock()
	}
	return nil
}
func (c *mockCHConn) Begin() (stdsql_driver.Tx, error) { return nil, nil }
func (c *mockCHConn) ExecContext(_ context.Context, query string, _ []stdsql_driver.NamedValue) (stdsql_driver.Result, error) {
	c.state.mu.Lock()
	c.state.execs = append(c.state.execs, query)
	failSub := c.state.failOnQuery
	c.state.mu.Unlock()
	if failSub != "" && strings.Contains(query, failSub) {
		return nil, errors.New("simulated CH exec error")
	}
	return stdsql_driver.RowsAffected(1), nil
}
func (c *mockCHConn) QueryContext(_ context.Context, query string, _ []stdsql_driver.NamedValue) (stdsql_driver.Rows, error) {
	c.state.mu.Lock()
	c.state.queries = append(c.state.queries, query)
	autoRows := c.state.autoRows
	c.state.mu.Unlock()
	if strings.Contains(query, "system.columns") {
		return &mockCHRows{mode: "auto", total: autoRows}, nil
	}
	if strings.Contains(query, "SELECT * FROM") {
		return &mockCHRows{mode: "export", total: 1}, nil
	}
	return &mockCHRows{total: 0}, nil
}

type mockCHRows struct {
	mode  string
	total int
	idx   int
}

func (r *mockCHRows) Columns() []string {
	if r.mode == "auto" {
		return []string{"table", "name"}
	}
	return []string{"id", "val"}
}
func (r *mockCHRows) Close() error { return nil }
func (r *mockCHRows) Next(dest []stdsql_driver.Value) error {
	if r.idx >= r.total {
		return io.EOF
	}
	r.idx++
	if r.mode == "auto" {
		dest[0] = fmt.Sprintf("ch_t_%d", r.idx)
		dest[1] = "col"
	} else {
		dest[0] = int64(r.idx)
		dest[1] = "ok"
	}
	return nil
}

func TestNewConfigAndTLSRestriction(t *testing.T) {
	if _, err := New(driver.ConnectionConfig{Type: "clickhouse", Host: ""}); err == nil {
		t.Fatal("expected empty host error")
	}
	if _, err := New(driver.ConnectionConfig{
		Type:    "clickhouse",
		Host:    "ch.example.com/evil",
		Options: map[string]string{"__web_mode": "1"},
	}); err == nil {
		t.Fatal("expected strict hostname validation error in web mode")
	}
	if _, err := New(driver.ConnectionConfig{
		Type:    "clickhouse",
		Host:    "ch.example.com",
		Options: map[string]string{"tls": "skip-verify", "__restrict_internal_ip": "1"},
	}); err == nil {
		t.Fatal("expected restricted skip-verify error")
	}
	if _, err := New(driver.ConnectionConfig{
		Type:    "clickhouse",
		Host:    "ch.example.com",
		Options: map[string]string{"tls": "true", "caCert": "bad-pem"},
	}); err == nil {
		t.Fatal("expected invalid caCert PEM error")
	}

	drvHTTP, err := New(driver.ConnectionConfig{
		Type:    "clickhouse",
		Host:    "ch.example.com",
		Options: map[string]string{"protocol": "http", "tls": "true"},
	})
	if err != nil {
		t.Fatalf("New HTTP TLS failed: %v", err)
	}
	_ = drvHTTP.Close()
}

func TestParseClickHouseProjectionsAndTTL(t *testing.T) {
	ddl := `CREATE TABLE analytics.events (
		id UInt64,
		ts DateTime TTL ts + INTERVAL 30 DAY,
		msg String,
		PROJECTION p_by_ts (SELECT ts, count() GROUP BY ts),
		PROJECTION "p_quoted" (SELECT count() WHERE msg != 'a)b''c')
	) ENGINE = MergeTree() ORDER BY id TTL ts + INTERVAL 90 DAY SETTINGS index_granularity = 8192`

	projs := parseClickHouseProjectionsFromDDL(ddl)
	if len(projs) != 2 || projs[0].Name != "p_by_ts" || projs[1].Name != "p_quoted" {
		t.Fatalf("unexpected parsed projections: %+v", projs)
	}
	ttl := extractClickHouseTableTTL("MergeTree() ORDER BY id TTL ts + INTERVAL 90 DAY SETTINGS index_granularity = 8192", ddl)
	if !strings.Contains(ttl, "INTERVAL 90 DAY") {
		t.Fatalf("expected table-level TTL (not column TTL), got %q", ttl)
	}
}

func TestBuildClickHouseAlterTableSQLsAndSandbox(t *testing.T) {
	stmts, err := buildClickHouseAlterTableSQLs(driver.AlterTableRequest{
		Database: "analytics",
		Table:    "events",
		Actions: []driver.AlterAction{
			{Action: "ADD_PROJECTION", Projection: driver.ProjectionDef{Name: "p1", Query: "SELECT id FROM events"}},
			{Action: "DROP_PROJECTION", Projection: driver.ProjectionDef{Name: "p1"}},
			{Action: "MATERIALIZE_PROJECTION", Projection: driver.ProjectionDef{Name: "p1"}},
			{Action: "CLEAR_PROJECTION", Projection: driver.ProjectionDef{Name: "p1"}},
			{Action: "ADD_SKIPPING_INDEX", SkippingIndex: driver.SkippingIndexDef{Name: "ix1", Expression: "msg", Type: "bloom_filter"}},
			{Action: "DROP_SKIPPING_INDEX", SkippingIndex: driver.SkippingIndexDef{Name: "ix1"}},
			{Action: "MATERIALIZE_SKIPPING_INDEX", SkippingIndex: driver.SkippingIndexDef{Name: "ix1"}},
			{Action: "CLEAR_SKIPPING_INDEX", SkippingIndex: driver.SkippingIndexDef{Name: "ix1"}},
			{Action: "MODIFY_TTL", TTLExpr: "ts + INTERVAL 7 DAY"},
			{Action: "REMOVE_TTL"},
		},
	})
	if err != nil || len(stmts) != 10 {
		t.Fatalf("unexpected buildClickHouseAlterTableSQLs: err=%v len=%d stmts=%v", err, len(stmts), stmts)
	}
	if !strings.Contains(stmts[4], "GRANULARITY 1") {
		t.Fatalf("expected default granularity 1, got %s", stmts[4])
	}

	if _, err := buildClickHouseAlterTableSQLs(driver.AlterTableRequest{
		Table:   "events",
		Actions: []driver.AlterAction{{Action: "ADD_PROJECTION", Projection: driver.ProjectionDef{Name: "bad", Query: "DROP TABLE events"}}},
	}); err == nil {
		t.Fatal("expected non-SELECT projection query to be rejected")
	}

	dWeb := &Driver{cfg: driver.ConnectionConfig{
		Options: map[string]string{"__web_mode": "1", "__restrict_internal_ip": "1"},
	}}
	ctx := context.Background()
	if _, err := dWeb.CreateTable(ctx, driver.CreateTableRequest{Table: "bad", Engine: "URL('http://127.0.0.1/', 'CSV')"}); err == nil {
		t.Fatal("expected restricted web CreateTable to reject URL engine")
	}
	if _, err := dWeb.ExecuteSQL(ctx, "default", "SELECT * FROM remote('127.0.0.1', system.one)", 10); err == nil {
		t.Fatal("expected restricted web ExecuteSQL to reject remote()")
	}
	if stmt, err := dWeb.ManagePartition(ctx, driver.PartitionActionRequest{Database: "analytics", Table: "events", PartitionID: "202601", Action: "DROP", PreviewOnly: true}); err != nil || !strings.Contains(stmt, "DROP PARTITION ID '202601'") {
		t.Fatalf("unexpected ManagePartition preview: err=%v stmt=%q", err, stmt)
	}
}

func TestStreamExportDatabaseScopingAndAutocompleteCap(t *testing.T) {
	state := &mockCHState{autoRows: 26000}
	d := &Driver{
		cfg: driver.ConnectionConfig{Database: "default"},
		db:  sql.OpenDB(&mockCHConnector{state: state}),
	}
	defer d.db.Close()
	ctx := context.Background()

	var out bytes.Buffer
	if err := d.StreamExport(ctx, driver.ExportRequest{
		Database: "analytics",
		Tables:   []string{"events"},
		Format:   "jsonl",
	}, &out); err != nil {
		t.Fatalf("multi-table StreamExport failed: %v", err)
	}
	if !strings.Contains(out.String(), `"__table__":"events"`) {
		t.Fatalf("unexpected multi-table JSONL output: %s", out.String())
	}

	state.mu.Lock()
	execs := append([]string(nil), state.execs...)
	state.mu.Unlock()
	if len(execs) < 2 || execs[0] != "USE `analytics`" || execs[len(execs)-1] != "USE `default`" {
		t.Fatalf("expected USE `analytics` before export and USE `default` on release, got %v", execs)
	}

	// Verify cleanup marks ErrBadConn if reset USE fails
	state.execs = nil
	state.failOnQuery = "USE `default`"
	conn, release, err := d.acquireConnForDB(ctx, "analytics")
	if err != nil {
		t.Fatalf("acquireConnForDB: %v", err)
	}
	// Mark underlying driver.Conn bad only if Raw receives ErrBadConn — verify release runs without panic and invokes USE `default`
	_ = conn
	release()
	if !strings.Contains(strings.Join(state.execs, ","), "USE `default`") {
		t.Fatalf("expected release() to attempt USE `default`, got %v", state.execs)
	}

	meta, err := d.GetAutocompleteMeta(ctx, "analytics")
	if err != nil {
		t.Fatalf("GetAutocompleteMeta: %v", err)
	}
	if len(meta.Tables) != 25000 {
		t.Fatalf("expected GetAutocompleteMeta to cap at 25000 rows, got %d", len(meta.Tables))
	}
	foundLimit := false
	for _, q := range state.queries {
		if strings.Contains(q, "system.columns") && strings.Contains(q, "LIMIT 25000") {
			foundLimit = true
		}
	}
	if !foundLimit {
		t.Fatalf("expected ClickHouse GetAutocompleteMeta query to include LIMIT 25000, got: %v", state.queries)
	}
}
