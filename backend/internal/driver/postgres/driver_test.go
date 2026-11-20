package postgres

import (
	"context"
	"database/sql"
	stdsql_driver "database/sql/driver"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"dbharbor/backend/internal/driver"
)

type mockPGState struct {
	mu          sync.Mutex
	queries     []string
	execs       []string
	txOps       []string
	failExecSub string
	autoRows    int
}

type mockPGConnector struct {
	state *mockPGState
}

func (c *mockPGConnector) Connect(context.Context) (stdsql_driver.Conn, error) {
	return &mockPGConn{state: c.state}, nil
}
func (c *mockPGConnector) Driver() stdsql_driver.Driver { return nil }

type mockPGConn struct {
	state *mockPGState
}

func (c *mockPGConn) Prepare(string) (stdsql_driver.Stmt, error) { return nil, nil }
func (c *mockPGConn) Close() error                              { return nil }
func (c *mockPGConn) Begin() (stdsql_driver.Tx, error) {
	c.state.mu.Lock()
	c.state.txOps = append(c.state.txOps, "BEGIN")
	c.state.mu.Unlock()
	return &mockPGTx{state: c.state}, nil
}
func (c *mockPGConn) ExecContext(_ context.Context, query string, _ []stdsql_driver.NamedValue) (stdsql_driver.Result, error) {
	c.state.mu.Lock()
	c.state.execs = append(c.state.execs, query)
	failSub := c.state.failExecSub
	c.state.mu.Unlock()
	if failSub != "" && strings.Contains(query, failSub) {
		return nil, errors.New("simulated exec failure")
	}
	return stdsql_driver.RowsAffected(1), nil
}
func (c *mockPGConn) QueryContext(_ context.Context, query string, _ []stdsql_driver.NamedValue) (stdsql_driver.Rows, error) {
	c.state.mu.Lock()
	c.state.queries = append(c.state.queries, query)
	autoRows := c.state.autoRows
	c.state.mu.Unlock()
	if strings.Contains(query, "information_schema.columns") {
		return &mockPGRows{total: autoRows}, nil
	}
	return &mockPGRows{total: 0}, nil
}

type mockPGTx struct{ state *mockPGState }

func (tx *mockPGTx) Commit() error {
	tx.state.mu.Lock()
	tx.state.txOps = append(tx.state.txOps, "COMMIT")
	tx.state.mu.Unlock()
	return nil
}
func (tx *mockPGTx) Rollback() error {
	tx.state.mu.Lock()
	tx.state.txOps = append(tx.state.txOps, "ROLLBACK")
	tx.state.mu.Unlock()
	return nil
}

type mockPGRows struct {
	total int
	idx   int
}

func (r *mockPGRows) Columns() []string { return []string{"table_name", "column_name"} }
func (r *mockPGRows) Close() error      { return nil }
func (r *mockPGRows) Next(dest []stdsql_driver.Value) error {
	if r.idx >= r.total {
		return io.EOF
	}
	r.idx++
	dest[0] = fmt.Sprintf("tbl_%d", r.idx)
	dest[1] = "col"
	return nil
}

func TestPoolForDBLRUEvictionBurstAndClose(t *testing.T) {
	d := &Driver{
		cfg:   driver.ConnectionConfig{Database: "main_db"},
		pools: make(map[string]*pgPoolEntry),
		openDBFn: func(dbName string) (*sql.DB, error) {
			return sql.OpenDB(&mockPGConnector{state: &mockPGState{}}), nil
		},
	}
	defer d.Close()

	// 1. Seed default + 3 secondary pools -> len == 4
	for _, db := range []string{"main_db", "db1", "db2", "db3"} {
		if _, rel, err := d.acquirePool(db); err != nil {
			t.Fatalf("acquirePool(%s): %v", db, err)
		} else {
			rel()
		}
	}
	// Touch db1 so db2 becomes LRU among secondary pools
	if _, rel, err := d.acquirePool("db1"); err != nil {
		t.Fatalf("acquirePool(db1) touch: %v", err)
	} else {
		rel()
	}
	// Adding db4 must evict LRU secondary db2, keeping main_db, db1, db3, db4 (len == 4)
	if _, rel, err := d.acquirePool("db4"); err != nil {
		t.Fatalf("acquirePool(db4): %v", err)
	} else {
		rel()
	}
	d.mu.Lock()
	if len(d.pools) != 4 {
		t.Fatalf("expected len(pools)=4, got %d", len(d.pools))
	}
	if _, exists := d.pools["db2"]; exists {
		t.Fatal("expected LRU secondary pool db2 to be evicted")
	}
	if _, exists := d.pools["main_db"]; !exists {
		t.Fatal("expected default pool main_db to be preserved")
	}
	d.mu.Unlock()

	// 2. Active lease with InUse == 0 must NOT be evicted while lease is held
	heldDB, heldRel, err := d.acquirePool("db1")
	if err != nil {
		t.Fatalf("acquirePool(db1) hold: %v", err)
	}
	if _, rel, err := d.acquirePool("db5"); err != nil {
		t.Fatalf("acquirePool(db5): %v", err)
	} else {
		rel()
	}
	if err := heldDB.Ping(); err != nil {
		t.Fatalf("expected active-leased db1 pool not to be closed by concurrent eviction: %v", err)
	}
	heldRel()

	// 3. Burst >4 active pools (InUse > 0) up to 16 total, then reject 17th (> 16)
	ctx := context.Background()
	var activeConns []*sql.Conn
	for _, existing := range []string{"main_db", "db1", "db3", "db4", "db5"} {
		p, rel, pErr := d.acquirePool(existing)
		if pErr != nil {
			t.Fatalf("acquirePool(%s): %v", existing, pErr)
		}
		c, err := p.Conn(ctx)
		rel()
		if err != nil {
			t.Fatalf("Conn(%s): %v", existing, err)
		}
		activeConns = append(activeConns, c)
	}
	for i := 6; i <= 16; i++ {
		p, rel, err := d.acquirePool(fmt.Sprintf("burst_%d", i))
		if err != nil {
			t.Fatalf("expected burst pool #%d to succeed under hard cap 16, got %v", i, err)
		}
		c, err := p.Conn(ctx)
		rel()
		if err != nil {
			t.Fatalf("Conn burst_%d: %v", i, err)
		}
		activeConns = append(activeConns, c)
	}
	if _, rel, err := d.acquirePool("burst_17"); err == nil {
		rel()
		t.Fatal("expected 17th concurrent active pool to be rejected at hard cap 16")
	}

	// Return all active connections -> next acquirePool hit must shrink idle pools back to <= 4
	for _, c := range activeConns {
		_ = c.Close()
	}
	if _, rel, err := d.acquirePool("main_db"); err != nil {
		t.Fatalf("acquirePool(main_db) after release: %v", err)
	} else {
		rel()
	}
	d.mu.Lock()
	afterLen := len(d.pools)
	d.mu.Unlock()
	if afterLen > maxPGPoolsSoft {
		t.Fatalf("expected pool count to shrink back to <= %d after burst release, got %d", maxPGPoolsSoft, afterLen)
	}

	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(d.pools) != 0 {
		t.Fatalf("expected Close() to clear d.pools")
	}
}

func TestAcquirePoolConcurrentBurstNoClosedDBRace(t *testing.T) {
	d := &Driver{
		cfg:   driver.ConnectionConfig{Database: "main_db"},
		pools: make(map[string]*pgPoolEntry),
		openDBFn: func(string) (*sql.DB, error) {
			return sql.OpenDB(&mockPGConnector{state: &mockPGState{}}), nil
		},
	}
	defer d.Close()

	const numGoroutines = 12
	const numDBs = 10
	const numRounds = 5

	var wg sync.WaitGroup
	var errCount atomic.Int64
	var closedErrCount atomic.Int64
	errCh := make(chan error, numGoroutines*numRounds)

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			ctx := context.Background()
			for r := 0; r < numRounds; r++ {
				dbName := fmt.Sprintf("conc_db_%d", (gid+r)%numDBs)
				// Phase A: direct acquirePool with yield before using *sql.DB (exposes InUse==0 eviction window)
				db, release, err := d.acquirePool(dbName)
				if err != nil {
					errCount.Add(1)
					errCh <- err
					continue
				}
				runtime.Gosched()
				if err := db.PingContext(ctx); err != nil {
					errCount.Add(1)
					if strings.Contains(err.Error(), "database is closed") {
						closedErrCount.Add(1)
					}
					errCh <- err
				}
				release()

				// Phase B: full ExecuteSQL path across rotating DBs
				if _, err := d.ExecuteSQL(ctx, dbName, "SELECT 1", 10); err != nil {
					errCount.Add(1)
					if strings.Contains(err.Error(), "database is closed") {
						closedErrCount.Add(1)
					}
					errCh <- err
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)

	if closedErrCount.Load() > 0 || errCount.Load() > 0 {
		var firstErr error
		for e := range errCh {
			firstErr = e
			break
		}
		t.Fatalf("concurrent pool burst failed: totalErrs=%d dbClosedErrs=%d first=%v", errCount.Load(), closedErrCount.Load(), firstErr)
	}

	// Verify post-burst shrink back to <= maxPGPoolsSoft when hitting default pool
	if _, rel, err := d.acquirePool("main_db"); err != nil {
		t.Fatalf("acquirePool(main_db): %v", err)
	} else {
		rel()
	}
	d.mu.Lock()
	afterLen := len(d.pools)
	d.mu.Unlock()
	if afterLen > maxPGPoolsSoft {
		t.Fatalf("expected pool count to shrink to <= %d after concurrent burst, got %d", maxPGPoolsSoft, afterLen)
	}
}

func TestPostgresAndKingbaseCreateTablePreviewAndTransaction(t *testing.T) {
	state := &mockPGState{}
	for _, variant := range []string{"postgres", "kingbase"} {
		d := &Driver{
			cfg:         driver.ConnectionConfig{Database: "appdb"},
			variantType: variant,
			pools:       make(map[string]*pgPoolEntry),
			openDBFn: func(string) (*sql.DB, error) {
				return sql.OpenDB(&mockPGConnector{state: state}), nil
			},
		}

		req := driver.CreateTableRequest{
			Table:       "orders",
			Comment:     "Order records",
			PreviewOnly: true,
			Columns: []driver.ColumnDef{
				{Name: "id", Type: "BIGINT", IsPrimaryKey: true, Comment: "PK"},
				{Name: "code", Type: "VARCHAR(64)", Nullable: false},
			},
			Indexes: []driver.IndexDef{
				{Name: "idx_orders_code", Columns: []string{"code"}, Unique: true},
			},
		}
		preview, err := d.CreateTable(context.Background(), req)
		if err != nil {
			t.Fatalf("%s CreateTable preview failed: %v", variant, err)
		}
		for _, want := range []string{
			`CREATE TABLE "public"."orders"`,
			`CREATE UNIQUE INDEX "idx_orders_code" ON "public"."orders" ("code")`,
			`COMMENT ON TABLE "public"."orders" IS 'Order records'`,
			`COMMENT ON COLUMN "public"."orders"."id" IS 'PK'`,
		} {
			if !strings.Contains(preview, want) {
				t.Fatalf("%s preview missing %q:\n%s", variant, want, preview)
			}
		}

		// Live execution commits when all statements succeed
		state.txOps = nil
		state.failExecSub = ""
		req.PreviewOnly = false
		if _, err := d.CreateTable(context.Background(), req); err != nil {
			t.Fatalf("%s CreateTable live failed: %v", variant, err)
		}
		if len(state.txOps) < 2 || state.txOps[0] != "BEGIN" || state.txOps[len(state.txOps)-1] != "COMMIT" {
			t.Fatalf("%s expected BEGIN...COMMIT, got %v", variant, state.txOps)
		}

		// Live execution rolls back when CREATE INDEX fails
		state.txOps = nil
		state.failExecSub = "CREATE UNIQUE INDEX"
		if _, err := d.CreateTable(context.Background(), req); err == nil {
			t.Fatalf("%s expected CreateTable to return error on CREATE INDEX failure", variant)
		}
		if len(state.txOps) < 2 || state.txOps[0] != "BEGIN" || state.txOps[len(state.txOps)-1] != "ROLLBACK" {
			t.Fatalf("%s expected BEGIN...ROLLBACK on index error, got %v", variant, state.txOps)
		}
		_ = d.Close()
	}
}

func TestAlterTableBuildMutationPartitionAndAutocompleteCap(t *testing.T) {
	state := &mockPGState{autoRows: 26000}
	d := &Driver{
		cfg:         driver.ConnectionConfig{Database: "appdb"},
		variantType: "postgres",
		pools:       make(map[string]*pgPoolEntry),
		openDBFn: func(string) (*sql.DB, error) {
			return sql.OpenDB(&mockPGConnector{state: state}), nil
		},
	}
	defer d.Close()
	ctx := context.Background()

	stmts, err := d.AlterTable(ctx, driver.AlterTableRequest{
		Table:       "orders",
		PreviewOnly: true,
		Actions: []driver.AlterAction{
			{Action: "ADD_INDEX", Index: driver.IndexDef{Name: "idx_o_name", Columns: []string{"name"}}},
		},
	})
	if err != nil || len(stmts) != 1 || !strings.Contains(stmts[0], `ON "public"."orders"`) {
		t.Fatalf("AlterTable preview failed: err=%v stmts=%v", err, stmts)
	}

	muts, err := d.BuildMutationPreviewSQL(driver.RowMutationRequest{
		Table:   "orders",
		Inserts: []map[string]any{{"id": 1}},
	})
	if err != nil || len(muts) != 1 || !strings.Contains(muts[0], `"public"."orders"`) {
		t.Fatalf("BuildMutationPreviewSQL failed: err=%v muts=%v", err, muts)
	}

	for _, tc := range []struct {
		req     driver.PartitionActionRequest
		wantErr bool
		wantSub string
	}{
		{driver.PartitionActionRequest{Table: "events", PartitionID: "events_2026", Action: "DETACH", PreviewOnly: true}, false, "DETACH PARTITION"},
		{driver.PartitionActionRequest{Table: "events", PartitionID: "events_2026", Action: "ATTACH", Expression: "FROM ('2026-01-01') TO ('2026-02-01')", PreviewOnly: true}, false, "FOR VALUES FROM"},
		{driver.PartitionActionRequest{Table: "events", PartitionID: "events_2026", Action: "ATTACH", Expression: "FOR VALUES IN ('a')", PreviewOnly: true}, false, "FOR VALUES IN"},
		{driver.PartitionActionRequest{Table: "events", PartitionID: "events_2026", Action: "DROP", PreviewOnly: true}, false, "DROP TABLE IF EXISTS"},
		{driver.PartitionActionRequest{Table: "events", PartitionID: "events_2026", Action: "TRUNCATE", PreviewOnly: true}, true, ""},
		{driver.PartitionActionRequest{Table: "events", PartitionID: "events_2026", Action: "ATTACH", Expression: "DEFAULT; DROP TABLE users", PreviewOnly: true}, true, ""},
	} {
		got, err := d.ManagePartition(ctx, tc.req)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("expected error for partition req %+v, got %q", tc.req, got)
			}
			continue
		}
		if err != nil || !strings.Contains(got, tc.wantSub) {
			t.Fatalf("unexpected ManagePartition result: got=%q err=%v wantSub=%q", got, err, tc.wantSub)
		}
	}

	meta, err := d.GetAutocompleteMeta(ctx, "appdb")
	if err != nil {
		t.Fatalf("GetAutocompleteMeta: %v", err)
	}
	if len(meta.Tables) != 25000 {
		t.Fatalf("expected GetAutocompleteMeta to cap at 25000 rows, got %d", len(meta.Tables))
	}
	foundLimit := false
	for _, q := range state.queries {
		if strings.Contains(q, "information_schema.columns") && strings.Contains(q, "LIMIT 25000") {
			foundLimit = true
		}
	}
	if !foundLimit {
		t.Fatalf("expected PostgreSQL GetAutocompleteMeta query to include LIMIT 25000, got: %v", state.queries)
	}
}
