package driver

import (
	"fmt"
	"sort"
	"sync"
)

type Factory func(cfg ConnectionConfig) (Driver, error)

type entry struct {
	order   int
	meta    DriverMeta
	factory Factory
}

var (
	mu       sync.RWMutex
	registry = make(map[string]entry)
	seq      int
)

// Register registers a pluggable database driver.
func Register(meta DriverMeta, factory Factory) {
	mu.Lock()
	defer mu.Unlock()
	seq++
	registry[meta.Type] = entry{
		order:   seq,
		meta:    meta,
		factory: factory,
	}
}

// Create instantiates a driver for the given ConnectionConfig.
func Create(cfg ConnectionConfig) (Driver, error) {
	mu.RLock()
	e, ok := registry[cfg.Type]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unsupported database driver type: %q", cfg.Type)
	}
	return e.factory(cfg)
}

// GetMeta returns metadata for a registered driver type.
func GetMeta(driverType string) (DriverMeta, bool) {
	mu.RLock()
	defer mu.RUnlock()
	e, ok := registry[driverType]
	return e.meta, ok
}

var preferredOrder = map[string]int{
	"clickhouse": 1,
	"mysql":      2,
	"postgres":   3,
	"sqlite":     4,
	"duckdb":     5,
	"redis":      6,
	"mongodb":    7,
	"mariadb":    8,
	"tidb":       9,
	"oceanbase":  10,
	"sqlserver":  11,
	"oracle":     12,
	"dameng":     13,
	"kingbase":   14,
}

// ListMetas returns all registered drivers in preferred display order.
func ListMetas() []DriverMeta {
	mu.RLock()
	defer mu.RUnlock()
	list := make([]entry, 0, len(registry))
	for _, e := range registry {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool {
		pi, oki := preferredOrder[list[i].meta.Type]
		pj, okj := preferredOrder[list[j].meta.Type]
		if oki && okj {
			return pi < pj
		}
		if oki != okj {
			return oki
		}
		return list[i].order < list[j].order
	})
	out := make([]DriverMeta, 0, len(list))
	for _, e := range list {
		out = append(out, e.meta)
	}
	return out
}

// StandardHostFields builds standard host/port/user/password/database schema descriptors.
func StandardHostFields(defaultPort int, dbLabel string, dbRequired bool) []ConnFieldSchema {
	return []ConnFieldSchema{
		{Key: "host", Label: "Host", Type: "text", Target: "root", Placeholder: "127.0.0.1", DefaultValue: "127.0.0.1", Required: true},
		{Key: "port", Label: "Port", Type: "number", Target: "root", DefaultValue: fmt.Sprintf("%d", defaultPort), Required: true},
		{Key: "username", Label: "Username", Type: "text", Target: "root", Placeholder: "root / default"},
		{Key: "password", Label: "Password", Type: "password", Target: "root", Placeholder: "Optional or enter password"},
		{Key: "database", Label: dbLabel, Type: "text", Target: "root", Required: dbRequired},
	}
}
