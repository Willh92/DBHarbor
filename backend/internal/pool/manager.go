package pool

import (
	"errors"
	"sync"
	"time"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/store"
)

const (
	defaultMaxPools = 32
	defaultIdleTTL  = 15 * time.Minute
)

type pooledDriver struct {
	drv       driver.Driver
	cfg       driver.ConnectionConfig
	lastUsed  time.Time
	inUse     int
	evicted   bool
	closedCh  chan struct{}
	closeOnce sync.Once
}

func (p *pooledDriver) closeDriver() {
	if p == nil {
		return
	}
	p.closeOnce.Do(func() {
		if p.drv != nil {
			_ = p.drv.Close()
		}
		if p.closedCh != nil {
			close(p.closedCh)
		}
	})
}

type inflightDial struct {
	done      chan struct{}
	drv       driver.Driver
	cfg       driver.ConnectionConfig
	err       error
	cancelled bool
}

type PoolStats struct {
	ActivePools int `json:"activePools"`
	MaxPools    int `json:"maxPools"`
	IdleMinutes int `json:"idleMinutes"`
}

type ConnectionPreparer func(cfg driver.ConnectionConfig) driver.ConnectionConfig

type Manager struct {
	store    *store.Store
	idleTTL  time.Duration
	maxPools int
	mu       sync.Mutex
	conns    map[string]*pooledDriver
	inflight map[string]*inflightDial
	preparer ConnectionPreparer
	stopCh   chan struct{}
}

func NewManager(st *store.Store, idleMinutes int) *Manager {
	idleTTL := defaultIdleTTL
	if idleMinutes > 0 {
		idleTTL = time.Duration(idleMinutes) * time.Minute
	}
	m := &Manager{
		store:    st,
		idleTTL:  idleTTL,
		maxPools: defaultMaxPools,
		conns:    make(map[string]*pooledDriver),
		inflight: make(map[string]*inflightDial),
		stopCh:   make(chan struct{}),
	}
	go m.reaper()
	return m
}

// SetConnectionPreparer registers an optional runtime policy hook (used by Web Server Edition)
// before creating pooled database driver instances.
func (m *Manager) SetConnectionPreparer(fn ConnectionPreparer) {
	m.mu.Lock()
	m.preparer = fn
	m.mu.Unlock()
}

// Acquire checks out a pooled driver with an active lease callback so LRU/idle eviction
// never closes a driver mid-export or mid-query.
func (m *Manager) Acquire(connID string) (driver.Driver, driver.ConnectionConfig, func(), error) {
	for attempt := 0; attempt < 2; attempt++ {
		m.mu.Lock()
		if existing, ok := m.conns[connID]; ok && !existing.evicted {
			existing.lastUsed = time.Now()
			existing.inUse++
			drv, cfg := existing.drv, existing.cfg
			m.mu.Unlock()
			return drv, cfg, m.makeReleaseFunc(existing), nil
		}
		if dial, ok := m.inflight[connID]; ok {
			m.mu.Unlock()
			<-dial.done
			if dial.cancelled && attempt == 0 {
				continue
			}
			if dial.err != nil {
				return nil, driver.ConnectionConfig{}, func() {}, dial.err
			}
			continue
		}
		dial := &inflightDial{done: make(chan struct{})}
		m.inflight[connID] = dial
		prep := m.preparer
		m.mu.Unlock()

		cfg, err := m.store.GetDecryptedConnection(connID)
		var drv driver.Driver
		if err == nil {
			if prep != nil {
				cfg = prep(cfg)
			}
			drv, err = driver.Create(cfg)
		}

		var evictedPool *pooledDriver
		var leased *pooledDriver

		m.mu.Lock()
		if cur, ok := m.inflight[connID]; ok && cur == dial {
			delete(m.inflight, connID)
		}
		if dial.cancelled {
			if err == nil {
				err = errors.New("connection configuration was updated or removed during dial")
			}
			dial.err = err
			m.mu.Unlock()
			if drv != nil {
				_ = drv.Close()
			}
			close(dial.done)
			if attempt == 0 {
				continue
			}
			return nil, driver.ConnectionConfig{}, func() {}, err
		}

		if err == nil {
			// Enforce LRU pool cap among idle (inUse == 0) entries so active exports/queries are never interrupted
			if len(m.conns) >= m.maxPools {
				var oldestID string
				var oldestTime time.Time
				for id, p := range m.conns {
					if p.inUse > 0 {
						continue
					}
					if oldestID == "" || p.lastUsed.Before(oldestTime) {
						oldestID = id
						oldestTime = p.lastUsed
					}
				}
				if oldestID != "" {
					ev := m.conns[oldestID]
					ev.evicted = true
					evictedPool = ev
					delete(m.conns, oldestID)
				}
			}
			leased = &pooledDriver{
				drv:      drv,
				cfg:      cfg,
				lastUsed: time.Now(),
				inUse:    1,
				closedCh: make(chan struct{}),
			}
			m.conns[connID] = leased
		}
		dial.drv, dial.cfg, dial.err = drv, cfg, err
		close(dial.done)
		m.mu.Unlock()

		if evictedPool != nil {
			evictedPool.closeDriver()
		}
		if err != nil {
			return nil, driver.ConnectionConfig{}, func() {}, err
		}
		return drv, cfg, m.makeReleaseFunc(leased), nil
	}
	return nil, driver.ConnectionConfig{}, func() {}, errors.New("failed to acquire database connection")
}

func (m *Manager) makeReleaseFunc(entry *pooledDriver) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			shouldClose := false
			m.mu.Lock()
			entry.inUse--
			entry.lastUsed = time.Now()
			if entry.inUse <= 0 && entry.evicted {
				shouldClose = true
			}
			m.mu.Unlock()
			if shouldClose {
				entry.closeDriver()
			}
		})
	}
}

// Get returns a pooled driver (backwards-compatible immediate release wrapper around Acquire).
func (m *Manager) Get(connID string) (driver.Driver, driver.ConnectionConfig, error) {
	drv, cfg, release, err := m.Acquire(connID)
	if err != nil {
		return nil, driver.ConnectionConfig{}, err
	}
	release()
	return drv, cfg, nil
}

func (m *Manager) Stats() PoolStats {
	m.mu.Lock()
	cnt := len(m.conns)
	m.mu.Unlock()
	return PoolStats{
		ActivePools: cnt,
		MaxPools:    m.maxPools,
		IdleMinutes: int(m.idleTTL.Minutes()),
	}
}

func (m *Manager) Evict(connID string) {
	var closeNow *pooledDriver
	m.mu.Lock()
	if dial, ok := m.inflight[connID]; ok {
		dial.cancelled = true
		delete(m.inflight, connID)
	}
	if item, ok := m.conns[connID]; ok {
		delete(m.conns, connID)
		item.evicted = true
		if item.inUse <= 0 {
			closeNow = item
		}
	}
	m.mu.Unlock()
	if closeNow != nil {
		closeNow.closeDriver()
	}
}

// EvictAndWait removes connID from the pool and, if an active lease or in-flight dial
// is currently holding the driver, waits up to maxWait for its Close() call to complete
// so single-file OS locks (SQLite WAL/SHM, DuckDB) are released before disk deletion or overwrite.
func (m *Manager) EvictAndWait(connID string, maxWait time.Duration) {
	var closeNow *pooledDriver
	var waitItem *pooledDriver
	var waitDial *inflightDial

	m.mu.Lock()
	if dial, ok := m.inflight[connID]; ok {
		dial.cancelled = true
		delete(m.inflight, connID)
		waitDial = dial
	}
	if item, ok := m.conns[connID]; ok {
		delete(m.conns, connID)
		item.evicted = true
		if item.inUse <= 0 {
			closeNow = item
		} else {
			waitItem = item
		}
	}
	m.mu.Unlock()

	if closeNow != nil {
		closeNow.closeDriver()
	}
	if maxWait <= 0 {
		return
	}
	deadline := time.Now().Add(maxWait)
	if waitDial != nil && waitDial.done != nil {
		if rem := time.Until(deadline); rem > 0 {
			timer := time.NewTimer(rem)
			select {
			case <-waitDial.done:
			case <-timer.C:
			}
			timer.Stop()
		}
	}
	if waitItem != nil && waitItem.closedCh != nil {
		if rem := time.Until(deadline); rem > 0 {
			timer := time.NewTimer(rem)
			select {
			case <-waitItem.closedCh:
			case <-timer.C:
			}
			timer.Stop()
		}
	}
}

// IsConnectionActiveWithin reports whether the connection currently has an active lease
// or has been used in the connection pool within the given duration window.
func (m *Manager) IsConnectionActiveWithin(connID string, window time.Duration) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dialing := m.inflight[connID]; dialing {
		return true
	}
	item, ok := m.conns[connID]
	if !ok {
		return false
	}
	if item.inUse > 0 {
		return true
	}
	return time.Since(item.lastUsed) < window
}

func (m *Manager) ReleaseIdle() int {
	var toClose []*pooledDriver
	m.mu.Lock()
	for id, it := range m.conns {
		if it.inUse > 0 {
			continue
		}
		it.evicted = true
		toClose = append(toClose, it)
		delete(m.conns, id)
	}
	m.mu.Unlock()
	for _, it := range toClose {
		it.closeDriver()
	}
	return len(toClose)
}

func (m *Manager) CloseAll() {
	select {
	case <-m.stopCh:
	default:
		close(m.stopCh)
	}
	m.mu.Lock()
	items := m.conns
	m.conns = make(map[string]*pooledDriver)
	for _, dial := range m.inflight {
		dial.cancelled = true
	}
	m.inflight = make(map[string]*inflightDial)
	m.mu.Unlock()
	for _, it := range items {
		it.evicted = true
		it.closeDriver()
	}
}

func (m *Manager) reaper() {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-m.idleTTL)
			var stale []*pooledDriver
			m.mu.Lock()
			for id, it := range m.conns {
				if it.inUse == 0 && it.lastUsed.Before(cutoff) {
					it.evicted = true
					stale = append(stale, it)
					delete(m.conns, id)
				}
			}
			m.mu.Unlock()
			for _, it := range stale {
				it.closeDriver()
			}
		}
	}
}
