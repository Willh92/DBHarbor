package pool

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dbharbor/backend/internal/driver"
)

type mockPoolDriver struct {
	closeFn func() error
}

func (m *mockPoolDriver) Meta() driver.DriverMeta { return driver.DriverMeta{Type: "mockpool"} }
func (m *mockPoolDriver) Ping(context.Context) (driver.ServerInfo, error) {
	return driver.ServerInfo{}, nil
}
func (m *mockPoolDriver) Close() error {
	if m.closeFn != nil {
		return m.closeFn()
	}
	return nil
}
func (m *mockPoolDriver) ListDatabases(context.Context) ([]driver.DatabaseSummary, error) {
	return nil, nil
}
func (m *mockPoolDriver) CreateDatabase(context.Context, string, string) error { return nil }

var (
	mockPoolRegOnce sync.Once
	mockPoolMu      sync.Mutex
	mockCreateHook  func(driver.ConnectionConfig) (driver.Driver, error)
)

func ensureMockPoolDriverRegistered() {
	mockPoolRegOnce.Do(func() {
		driver.Register(driver.DriverMeta{Type: "mockpool", Name: "MockPool"}, func(cfg driver.ConnectionConfig) (driver.Driver, error) {
			mockPoolMu.Lock()
			fn := mockCreateHook
			mockPoolMu.Unlock()
			if fn != nil {
				return fn(cfg)
			}
			return &mockPoolDriver{}, nil
		})
	})
}

func setMockCreateHook(t *testing.T, fn func(driver.ConnectionConfig) (driver.Driver, error)) {
	t.Helper()
	ensureMockPoolDriverRegistered()
	mockPoolMu.Lock()
	mockCreateHook = fn
	mockPoolMu.Unlock()
	t.Cleanup(func() {
		mockPoolMu.Lock()
		mockCreateHook = nil
		mockPoolMu.Unlock()
	})
}

func TestAcquireWithConfigCacheHitAndLeaseIdempotency(t *testing.T) {
	var createCount, closeCount atomic.Int32
	setMockCreateHook(t, func(cfg driver.ConnectionConfig) (driver.Driver, error) {
		createCount.Add(1)
		return &mockPoolDriver{closeFn: func() error { closeCount.Add(1); return nil }}, nil
	})

	m := NewManager(nil, 10)
	defer m.CloseAll()

	loader := func() (driver.ConnectionConfig, error) {
		return driver.ConnectionConfig{ID: "c1", Type: "mockpool"}, nil
	}
	d1, _, rel1, err := m.AcquireWithConfig("c1", loader)
	if err != nil {
		t.Fatalf("AcquireWithConfig #1: %v", err)
	}
	d2, _, rel2, err := m.AcquireWithConfig("c1", loader)
	if err != nil {
		t.Fatalf("AcquireWithConfig #2: %v", err)
	}
	if d1 != d2 || createCount.Load() != 1 {
		t.Fatalf("expected cache hit returning same driver with 1 dial, got createCount=%d", createCount.Load())
	}

	// Calling rel1 twice must only decrement inUse once (sync.Once)
	rel1()
	rel1()
	m.mu.Lock()
	inUse := m.conns["c1"].inUse
	m.mu.Unlock()
	if inUse != 1 {
		t.Fatalf("expected inUse=1 after double-release of first lease, got %d", inUse)
	}
	rel2()
	if m.ReleaseIdle() != 1 || closeCount.Load() != 1 {
		t.Fatalf("expected ReleaseIdle to close 1 idle pool, closeCount=%d", closeCount.Load())
	}
}

func TestAcquireWithConfigSingleflight(t *testing.T) {
	var createCount atomic.Int32
	setMockCreateHook(t, func(cfg driver.ConnectionConfig) (driver.Driver, error) {
		createCount.Add(1)
		time.Sleep(40 * time.Millisecond)
		return &mockPoolDriver{}, nil
	})

	m := NewManager(nil, 10)
	defer m.CloseAll()

	const workers = 12
	drvs := make([]driver.Driver, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			d, _, rel, err := m.AcquireWithConfig("sf-key", func() (driver.ConnectionConfig, error) {
				return driver.ConnectionConfig{ID: "sf", Type: "mockpool"}, nil
			})
			drvs[idx] = d
			errs[idx] = err
			if err == nil {
				rel()
			}
		}(i)
	}
	wg.Wait()

	if createCount.Load() != 1 {
		t.Fatalf("expected singleflight to dial once, got %d", createCount.Load())
	}
	for i := 0; i < workers; i++ {
		if errs[i] != nil || drvs[i] != drvs[0] {
			t.Fatalf("worker %d failed or got distinct driver: err=%v", i, errs[i])
		}
	}
}

func TestEvictDeferredWhileInUseAndInflightCancel(t *testing.T) {
	var closeCount atomic.Int32
	dialStarted := make(chan struct{}, 1)
	allowDialFinish := make(chan struct{})
	setMockCreateHook(t, func(cfg driver.ConnectionConfig) (driver.Driver, error) {
		if cfg.ID == "slow" {
			select {
			case dialStarted <- struct{}{}:
			default:
			}
			<-allowDialFinish
		}
		return &mockPoolDriver{closeFn: func() error { closeCount.Add(1); return nil }}, nil
	})

	m := NewManager(nil, 10)
	defer m.CloseAll()

	// 1. Deferred close while leased
	_, _, rel, err := m.AcquireWithConfig("active-c", func() (driver.ConnectionConfig, error) {
		return driver.ConnectionConfig{ID: "active-c", Type: "mockpool"}, nil
	})
	if err != nil {
		t.Fatalf("AcquireWithConfig: %v", err)
	}
	m.Evict("active-c")
	if closeCount.Load() != 0 {
		t.Fatalf("expected Close not to run while lease is active")
	}
	rel()
	if closeCount.Load() != 1 {
		t.Fatalf("expected Close to run immediately on lease release, got %d", closeCount.Load())
	}

	// 2. Evict cancels in-flight dial and closes newly created driver
	var dialErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, r, err := m.AcquireWithConfig("slow-key", func() (driver.ConnectionConfig, error) {
			return driver.ConnectionConfig{ID: "slow", Type: "mockpool"}, nil
		})
		dialErr = err
		if err == nil {
			r()
		}
	}()
	<-dialStarted
	m.Evict("slow-key")
	// Prevent infinite retry loop on subsequent attempts by failing loader after cancellation
	close(allowDialFinish)
	<-done
	_ = dialErr
	if closeCount.Load() < 2 {
		t.Fatalf("expected cancelled in-flight driver to be closed, closeCount=%d", closeCount.Load())
	}
}

func TestEvictConnectionAllAndBySessionScope(t *testing.T) {
	var closed []string
	var mu sync.Mutex
	setMockCreateHook(t, func(cfg driver.ConnectionConfig) (driver.Driver, error) {
		id := cfg.Name
		return &mockPoolDriver{closeFn: func() error {
			mu.Lock()
			closed = append(closed, id)
			mu.Unlock()
			return nil
		}}, nil
	})

	m := NewManager(nil, 10)
	defer m.CloseAll()

	for _, key := range []string{"connA", "connA::s1", "connA::s2", "connB::s1"} {
		k := key
		_, _, rel, err := m.AcquireWithConfig(k, func() (driver.ConnectionConfig, error) {
			return driver.ConnectionConfig{Name: k, Type: "mockpool"}, nil
		})
		if err != nil {
			t.Fatalf("AcquireWithConfig(%s): %v", k, err)
		}
		rel()
	}

	m.EvictBySessionScope("") // no-op
	if m.Stats().ActivePools != 4 {
		t.Fatalf("expected 4 pools after empty EvictBySessionScope, got %d", m.Stats().ActivePools)
	}

	m.EvictBySessionScope("s1")
	if m.Stats().ActivePools != 2 {
		t.Fatalf("expected 2 pools remaining after EvictBySessionScope(s1), got %d", m.Stats().ActivePools)
	}

	m.EvictConnectionAll("connA")
	if m.Stats().ActivePools != 0 {
		t.Fatalf("expected 0 pools remaining after EvictConnectionAll(connA), got %d", m.Stats().ActivePools)
	}
	if len(closed) != 4 {
		t.Fatalf("expected all 4 drivers closed, got %v", closed)
	}
}

func TestEvictAndWaitTimeoutAndCompletion(t *testing.T) {
	blockClose := make(chan struct{})
	setMockCreateHook(t, func(cfg driver.ConnectionConfig) (driver.Driver, error) {
		return &mockPoolDriver{closeFn: func() error {
			if cfg.Name == "blocked" {
				<-blockClose
			}
			return nil
		}}, nil
	})

	m := NewManager(nil, 10)
	defer func() {
		close(blockClose)
		m.CloseAll()
	}()

	_, _, rel1, _ := m.AcquireWithConfig("fast", func() (driver.ConnectionConfig, error) {
		return driver.ConnectionConfig{Name: "fast", Type: "mockpool"}, nil
	})
	go func() {
		time.Sleep(30 * time.Millisecond)
		rel1()
	}()
	start := time.Now()
	m.EvictAndWait("fast", time.Second)
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("EvictAndWait took too long on released lease: %v", time.Since(start))
	}

	_, _, rel2, _ := m.AcquireWithConfig("blocked", func() (driver.ConnectionConfig, error) {
		return driver.ConnectionConfig{Name: "blocked", Type: "mockpool"}, nil
	})
	rel2Once := sync.Once{}
	go func() {
		time.Sleep(10 * time.Millisecond)
		rel2Once.Do(rel2)
	}()
	start2 := time.Now()
	m.EvictAndWait("blocked", 80*time.Millisecond)
	if time.Since(start2) < 60*time.Millisecond {
		t.Fatalf("expected EvictAndWait to wait up to maxWait on blocked Close(), elapsed=%v", time.Since(start2))
	}
}

func TestLRUEvictionRespectsActiveLeasesAndPreparer(t *testing.T) {
	var closed []string
	var sawPrepared bool
	setMockCreateHook(t, func(cfg driver.ConnectionConfig) (driver.Driver, error) {
		if cfg.Option("prepared", "") == "yes" {
			sawPrepared = true
		}
		name := cfg.Name
		return &mockPoolDriver{closeFn: func() error {
			closed = append(closed, name)
			return nil
		}}, nil
	})

	m := NewManager(nil, 5)
	m.maxPools = 2
	defer m.CloseAll()

	m.SetConnectionPreparer(func(cfg driver.ConnectionConfig) driver.ConnectionConfig {
		if cfg.Options == nil {
			cfg.Options = map[string]string{}
		}
		cfg.Options["prepared"] = "yes"
		return cfg
	})

	_, _, relActive, _ := m.AcquireWithConfig("k1", func() (driver.ConnectionConfig, error) {
		return driver.ConnectionConfig{Name: "k1", Type: "mockpool"}, nil
	})
	defer relActive() // keep k1 active!

	_, _, relIdle, _ := m.AcquireWithConfig("k2", func() (driver.ConnectionConfig, error) {
		return driver.ConnectionConfig{Name: "k2", Type: "mockpool"}, nil
	})
	relIdle()

	// Acquiring k3 at cap=2 must evict idle k2, never active k1
	_, _, rel3, err := m.AcquireWithConfig("k3", func() (driver.ConnectionConfig, error) {
		return driver.ConnectionConfig{Name: "k3", Type: "mockpool"}, nil
	})
	if err != nil {
		t.Fatalf("Acquire k3: %v", err)
	}
	defer rel3()

	if !sawPrepared {
		t.Fatal("expected ConnectionPreparer to mutate config before Create")
	}
	if len(closed) != 1 || closed[0] != "k2" {
		t.Fatalf("expected idle k2 to be evicted (preserving active k1), closed=%v", closed)
	}
	if !m.IsConnectionActiveWithin("k1", time.Minute) {
		t.Fatal("expected active k1 to report IsConnectionActiveWithin=true")
	}
	if m.Stats().IdleMinutes != 5 || m.Stats().ActivePools != 2 {
		t.Fatalf("unexpected PoolStats: %+v", m.Stats())
	}

	_ = errors.New("unused")
}
