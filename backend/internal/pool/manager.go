package pool

import (
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
}

type inflightDial struct {
	done chan struct{}
	drv  driver.Driver
	cfg  driver.ConnectionConfig
	err  error
}

type PoolStats struct {
	ActivePools int `json:"activePools"`
	MaxPools    int `json:"maxPools"`
	IdleMinutes int `json:"idleMinutes"`
}

type Manager struct {
	store    *store.Store
	idleTTL  time.Duration
	maxPools int
	mu       sync.Mutex
	conns    map[string]*pooledDriver
	inflight map[string]*inflightDial
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

func (m *Manager) Get(connID string) (driver.Driver, driver.ConnectionConfig, error) {
	m.mu.Lock()
	if existing, ok := m.conns[connID]; ok {
		existing.lastUsed = time.Now()
		drv, cfg := existing.drv, existing.cfg
		m.mu.Unlock()
		return drv, cfg, nil
	}
	if dial, ok := m.inflight[connID]; ok {
		m.mu.Unlock()
		<-dial.done
		return dial.drv, dial.cfg, dial.err
	}
	dial := &inflightDial{done: make(chan struct{})}
	m.inflight[connID] = dial
	m.mu.Unlock()

	cfg, err := m.store.GetDecryptedConnection(connID)
	var drv driver.Driver
	if err == nil {
		drv, err = driver.Create(cfg)
	}

	var evicted driver.Driver
	m.mu.Lock()
	delete(m.inflight, connID)
	if err == nil {
		// Enforce LRU pool cap so idle connections cannot grow unbounded
		if len(m.conns) >= m.maxPools {
			var oldestID string
			var oldestTime time.Time
			for id, p := range m.conns {
				if oldestID == "" || p.lastUsed.Before(oldestTime) {
					oldestID = id
					oldestTime = p.lastUsed
				}
			}
			if oldestID != "" {
				evicted = m.conns[oldestID].drv
				delete(m.conns, oldestID)
			}
		}
		m.conns[connID] = &pooledDriver{
			drv:      drv,
			cfg:      cfg,
			lastUsed: time.Now(),
		}
	}
	dial.drv, dial.cfg, dial.err = drv, cfg, err
	close(dial.done)
	m.mu.Unlock()

	if evicted != nil {
		_ = evicted.Close()
	}
	return drv, cfg, err
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
	m.mu.Lock()
	item, ok := m.conns[connID]
	if ok {
		delete(m.conns, connID)
	}
	m.mu.Unlock()
	if ok && item.drv != nil {
		_ = item.drv.Close()
	}
}

func (m *Manager) ReleaseIdle() int {
	m.mu.Lock()
	items := m.conns
	m.conns = make(map[string]*pooledDriver)
	m.mu.Unlock()
	released := 0
	for _, it := range items {
		if it.drv != nil {
			_ = it.drv.Close()
			released++
		}
	}
	return released
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
				if it.lastUsed.Before(cutoff) {
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
