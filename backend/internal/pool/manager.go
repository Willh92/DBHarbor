package pool

import (
	"errors"
	"sync"
	"time"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/store"
)

const defaultMaxPools = 32

type pooledDriver struct {
	drv      driver.Driver
	cfg      driver.ConnectionConfig
	lastUsed time.Time
	inUse    int
	evicted  bool
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
	if idleMinutes <= 0 {
		idleMinutes = 15
	}
	m := &Manager{
		store:    st,
		idleTTL:  time.Duration(idleMinutes) * time.Minute,
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

		var toClose []driver.Driver
		var leased *pooledDriver

		m.mu.Lock()
		if cur, ok := m.inflight[connID]; ok && cur == dial {
			delete(m.inflight, connID)
		}
		if dial.cancelled {
			if drv != nil {
				toClose = append(toClose, drv)
			}
			if err == nil {
				err = errors.New("connection configuration was updated or removed during dial")
			}
			dial.err = err
			close(dial.done)
			m.mu.Unlock()
			for _, d := range toClose {
				_ = d.Close()
			}
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
					toClose = append(toClose, ev.drv)
					delete(m.conns, oldestID)
				}
			}
			leased = &pooledDriver{
				drv:      drv,
				cfg:      cfg,
				lastUsed: time.Now(),
				inUse:    1,
			}
			m.conns[connID] = leased
		}
		dial.drv, dial.cfg, dial.err = drv, cfg, err
		close(dial.done)
		m.mu.Unlock()

		for _, d := range toClose {
			_ = d.Close()
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
			var closeNow driver.Driver
			m.mu.Lock()
			entry.inUse--
			entry.lastUsed = time.Now()
			if entry.inUse <= 0 && entry.evicted {
				closeNow = entry.drv
			}
			m.mu.Unlock()
			if closeNow != nil {
				_ = closeNow.Close()
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
	var closeNow driver.Driver
	m.mu.Lock()
	if dial, ok := m.inflight[connID]; ok {
		dial.cancelled = true
		delete(m.inflight, connID)
	}
	if item, ok := m.conns[connID]; ok {
		delete(m.conns, connID)
		item.evicted = true
		if item.inUse <= 0 {
			closeNow = item.drv
		}
	}
	m.mu.Unlock()
	if closeNow != nil {
		_ = closeNow.Close()
	}
}

func (m *Manager) ReleaseIdle() int {
	var toClose []driver.Driver
	m.mu.Lock()
	for id, it := range m.conns {
		if it.inUse > 0 {
			continue
		}
		it.evicted = true
		if it.drv != nil {
			toClose = append(toClose, it.drv)
		}
		delete(m.conns, id)
	}
	m.mu.Unlock()
	for _, d := range toClose {
		_ = d.Close()
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
		if it.drv != nil {
			_ = it.drv.Close()
		}
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
			var stale []driver.Driver
			m.mu.Lock()
			for id, it := range m.conns {
				if it.inUse == 0 && it.lastUsed.Before(cutoff) {
					it.evicted = true
					stale = append(stale, it.drv)
					delete(m.conns, id)
				}
			}
			m.mu.Unlock()
			for _, d := range stale {
				_ = d.Close()
			}
		}
	}
}
