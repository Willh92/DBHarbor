package redis

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/sqlbase"

	goredis "github.com/redis/go-redis/v9"
)

func init() {
	driver.Register(driver.DriverMeta{
		Type:        "redis",
		Name:        "Redis",
		Category:    driver.CategoryKeyValue,
		BadgeColor:  "#dc2626",
		DefaultPort: 6379,
		Description: "High-performance in-memory Key-Value database (supports DB0-15, String/Hash/List/Set/ZSet/Stream editing, and command console)",
		Fields: []driver.ConnFieldSchema{
			{Key: "host", Label: "Host", Type: "text", Target: "root", DefaultValue: "127.0.0.1", Required: true},
			{Key: "port", Label: "Port", Type: "number", Target: "root", DefaultValue: "6379", Required: true},
			{Key: "username", Label: "ACL Username (Redis 6+, optional)", Type: "text", Target: "root"},
			{Key: "password", Label: "Password", Type: "password", Target: "root"},
			{Key: "database", Label: "Default DB Index (0-15)", Type: "number", Target: "root", DefaultValue: "0"},
			{
				Key: "tls", Label: "Enable TLS", Type: "select", Target: "options", DefaultValue: "false",
				SelectOptions: []driver.SelectOption{
					{Label: "Disabled", Value: "false"},
					{Label: "TLS (Verify Server Certificate)", Value: "true"},
					{Label: "TLS (Skip Certificate Verification)", Value: "skip-verify"},
				},
			},
		},
	}, New)
}

type Driver struct {
	cfg     driver.ConnectionConfig
	mu      sync.Mutex
	clients map[int]*goredis.Client
}

var (
	_ driver.KeyValueDriver      = (*Driver)(nil)
	_ driver.KVExtendedDriver    = (*Driver)(nil)
	_ driver.KVDiagnosticsDriver = (*Driver)(nil)
)

func New(cfg driver.ConnectionConfig) (driver.Driver, error) {
	if cfg.Option("tls", "false") == "skip-verify" && cfg.Option("__restrict_internal_ip", "") == "1" {
		return nil, errors.New("TLS skip-verify is not permitted for restricted non-admin connections")
	}
	return &Driver{
		cfg:     cfg,
		clients: make(map[int]*goredis.Client),
	}, nil
}

func (d *Driver) clientForDB(dbIdx int) *goredis.Client {
	if dbIdx < 0 {
		dbIdx = 0
	} else if dbIdx > 15 {
		dbIdx = 15
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.clients[dbIdx]; ok {
		return c
	}
	restrictIP := d.cfg.Option("__restrict_internal_ip", "") == "1"
	host := strings.TrimSpace(d.cfg.Host)
	if host == "" && !restrictIP {
		host = "127.0.0.1"
	}
	cleanHost := strings.Trim(host, "[]")
	port := d.cfg.Port
	if port == 0 {
		port = 6379
	}
	opts := &goredis.Options{
		Addr:            net.JoinHostPort(cleanHost, strconv.Itoa(port)),
		Username:        d.cfg.Username,
		Password:        d.cfg.Password,
		DB:              dbIdx,
		DialTimeout:     6 * time.Second,
		PoolSize:        4,
		MaxIdleConns:    1,
		ConnMaxIdleTime: 3 * time.Minute,
	}
	if restrictIP {
		opts.Dialer = sqlbase.NewRestrictedDialContext(8 * time.Second)
	}
	if tlsMode := d.cfg.Option("tls", "false"); tlsMode == "true" || tlsMode == "skip-verify" {
		skipVerify := tlsMode == "skip-verify" && !restrictIP
		opts.TLSConfig = &tls.Config{ServerName: cleanHost, MinVersion: tls.VersionTLS12, InsecureSkipVerify: skipVerify} // #nosec G402
	}
	c := goredis.NewClient(opts)
	d.clients[dbIdx] = c
	return c
}

func (d *Driver) Meta() driver.DriverMeta {
	m, _ := driver.GetMeta("redis")
	return m
}

func (d *Driver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.clients {
		_ = c.Close()
	}
	d.clients = map[int]*goredis.Client{}
	return nil
}

func (d *Driver) Ping(ctx context.Context) (driver.ServerInfo, error) {
	start := time.Now()
	c := d.clientForDB(0)
	if err := c.Ping(ctx).Err(); err != nil {
		return driver.ServerInfo{}, err
	}
	infoStr, _ := c.Info(ctx, "server").Result()
	ver := parseRedisInfoField(infoStr, "redis_version")
	if ver == "" {
		ver = "Connected"
	}
	return driver.ServerInfo{Version: ver, Product: "Redis", LatencyMs: time.Since(start).Milliseconds()}, nil
}

func (d *Driver) ListDatabases(ctx context.Context) ([]driver.DatabaseSummary, error) {
	dbMap, err := d.keyspaceCounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]driver.DatabaseSummary, 16)
	for i := 0; i < 16; i++ {
		out[i] = driver.DatabaseSummary{
			Name:        fmt.Sprintf("db%d", i),
			TablesCount: int(dbMap[i]),
		}
	}
	return out, nil
}

func (d *Driver) CreateDatabase(_ context.Context, _, _ string) error {
	return errors.New("Redis uses fixed logical database indexes db0 to db15")
}

func (d *Driver) keyspaceCounts(ctx context.Context) (map[int]int64, error) {
	res := map[int]int64{}
	c := d.clientForDB(0)
	info, err := c.Info(ctx, "keyspace").Result()
	if err != nil {
		if sqlbase.IsAuthError("redis", err) {
			return nil, err
		}
		// ACL-restricted Redis users may forbid INFO while permitting PING/SCAN; verify connectivity & auth via PING.
		if pingErr := c.Ping(ctx).Err(); pingErr != nil {
			return nil, pingErr
		}
		return res, nil
	}
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "db") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		idx, err := strconv.Atoi(strings.TrimPrefix(parts[0], "db"))
		if err != nil {
			continue
		}
		for _, kv := range strings.Split(parts[1], ",") {
			if strings.HasPrefix(kv, "keys=") {
				cnt, _ := strconv.ParseInt(strings.TrimPrefix(kv, "keys="), 10, 64)
				res[idx] = cnt
			}
		}
	}
	return res, nil
}

func (d *Driver) ScanKeys(ctx context.Context, dbIndex int, pattern string, cursor uint64, count int64) (driver.KVScanResult, error) {
	if pattern == "" {
		pattern = "*"
	}
	if count <= 0 || count > 500 {
		count = 200
	}
	c := d.clientForDB(dbIndex)
	keys, nextCursor, err := c.Scan(ctx, cursor, pattern, count).Result()
	if err != nil {
		return driver.KVScanResult{}, err
	}
	sort.Strings(keys)

	pipe := c.Pipeline()
	typeCmds := make([]*goredis.StatusCmd, len(keys))
	ttlCmds := make([]*goredis.DurationCmd, len(keys))
	for i, k := range keys {
		typeCmds[i] = pipe.Type(ctx, k)
		ttlCmds[i] = pipe.TTL(ctx, k)
	}
	_, _ = pipe.Exec(ctx)

	summaries := make([]driver.KVKeySummary, len(keys))
	for i, k := range keys {
		ttlSec := int64(-1)
		if dur := ttlCmds[i].Val(); dur > 0 {
			ttlSec = int64(dur.Seconds())
		} else if dur == -2*time.Second {
			ttlSec = -2
		}
		summaries[i] = driver.KVKeySummary{
			Key:  k,
			Type: typeCmds[i].Val(),
			TTL:  ttlSec,
		}
	}
	dbKeys, _ := d.keyspaceCounts(ctx)
	return driver.KVScanResult{
		Cursor: nextCursor,
		Keys:   summaries,
		DBKeys: dbKeys,
	}, nil
}

func (d *Driver) GetKey(ctx context.Context, dbIndex int, key string) (driver.KVEntryDetail, error) {
	c := d.clientForDB(dbIndex)
	pipe := c.Pipeline()
	typeCmd := pipe.Type(ctx, key)
	ttlCmd := pipe.TTL(ctx, key)
	memCmd := pipe.MemoryUsage(ctx, key, 0)
	encCmd := pipe.ObjectEncoding(ctx, key)
	_, _ = pipe.Exec(ctx)

	kt, err := typeCmd.Result()
	if err != nil {
		return driver.KVEntryDetail{}, err
	}
	if kt == "none" {
		return driver.KVEntryDetail{}, fmt.Errorf("key not found: %s", key)
	}
	ttlDur, _ := ttlCmd.Result()
	ttlSec := int64(-1)
	if ttlDur > 0 {
		ttlSec = int64(ttlDur.Seconds())
	}

	detail := driver.KVEntryDetail{
		Key:   key,
		Type:  kt,
		TTL:   ttlSec,
		Items: []driver.KVCollectionItem{},
	}
	if memBytes, mErr := memCmd.Result(); mErr == nil && memBytes > 0 {
		detail.MemoryBytes = memBytes
	}
	if enc, eErr := encCmd.Result(); eErr == nil && enc != "" {
		detail.Encoding = enc
	}

	switch kt {
	case "string":
		strLen, _ := c.StrLen(ctx, key).Result()
		val, err := c.GetRange(ctx, key, 0, (1<<20)-1).Result()
		if err != nil {
			return detail, err
		}
		detail.StringValue = val
		if strLen > int64(len(val)) {
			detail.Length = strLen
		} else {
			detail.Length = int64(len(val))
		}
	case "hash":
		hlen, _ := c.HLen(ctx, key).Result()
		detail.Length = hlen
		if hlen <= 500 {
			m, err := c.HGetAll(ctx, key).Result()
			if err != nil {
				return detail, err
			}
			fields := make([]string, 0, len(m))
			for f := range m {
				fields = append(fields, f)
			}
			sort.Strings(fields)
			for _, f := range fields {
				detail.Items = append(detail.Items, driver.KVCollectionItem{Field: f, Value: m[f]})
			}
		} else {
			var cursor uint64
			for len(detail.Items) < 500 {
				kvs, next, err := c.HScan(ctx, key, cursor, "*", 200).Result()
				if err != nil {
					return detail, err
				}
				for i := 0; i+1 < len(kvs) && len(detail.Items) < 500; i += 2 {
					detail.Items = append(detail.Items, driver.KVCollectionItem{Field: kvs[i], Value: kvs[i+1]})
				}
				cursor = next
				if cursor == 0 {
					break
				}
			}
		}
	case "list":
		vals, err := c.LRange(ctx, key, 0, 499).Result()
		if err != nil {
			return detail, err
		}
		detail.Length, _ = c.LLen(ctx, key).Result()
		for idx, v := range vals {
			detail.Items = append(detail.Items, driver.KVCollectionItem{Index: idx, Value: v})
		}
	case "set":
		scard, _ := c.SCard(ctx, key).Result()
		detail.Length = scard
		if scard <= 500 {
			vals, err := c.SMembers(ctx, key).Result()
			if err != nil {
				return detail, err
			}
			sort.Strings(vals)
			for _, v := range vals {
				detail.Items = append(detail.Items, driver.KVCollectionItem{Value: v})
			}
		} else {
			var cursor uint64
			for len(detail.Items) < 500 {
				vals, next, err := c.SScan(ctx, key, cursor, "*", 200).Result()
				if err != nil {
					return detail, err
				}
				for _, v := range vals {
					if len(detail.Items) >= 500 {
						break
					}
					detail.Items = append(detail.Items, driver.KVCollectionItem{Value: v})
				}
				cursor = next
				if cursor == 0 {
					break
				}
			}
		}
	case "zset":
		zs, err := c.ZRangeWithScores(ctx, key, 0, 499).Result()
		if err != nil {
			return detail, err
		}
		detail.Length, _ = c.ZCard(ctx, key).Result()
		for _, z := range zs {
			detail.Items = append(detail.Items, driver.KVCollectionItem{
				Value: fmt.Sprintf("%v", z.Member),
				Score: z.Score,
			})
		}
	case "stream":
		msgs, err := c.XRangeN(ctx, key, "-", "+", 200).Result()
		if err != nil {
			return detail, err
		}
		detail.Length, _ = c.XLen(ctx, key).Result()
		for _, m := range msgs {
			b, _ := json.Marshal(m.Values)
			detail.Items = append(detail.Items, driver.KVCollectionItem{
				ID:    m.ID,
				Value: string(b),
			})
		}
	}
	return detail, nil
}

func (d *Driver) SetKey(ctx context.Context, dbIndex int, req driver.KVSetRequest) error {
	c := d.clientForDB(dbIndex)
	switch strings.ToLower(req.Type) {
	case "string":
		var exp time.Duration
		if req.TTL > 0 {
			exp = time.Duration(req.TTL) * time.Second
		}
		return c.Set(ctx, req.Key, req.StringValue, exp).Err()
	case "hash":
		if len(req.Items) == 0 {
			return errors.New("Hash requires at least one field")
		}
		kv := make([]any, 0, len(req.Items)*2)
		for _, it := range req.Items {
			kv = append(kv, it.Field, it.Value)
		}
		if err := c.HSet(ctx, req.Key, kv...).Err(); err != nil {
			return err
		}
	case "list":
		vals := make([]any, len(req.Items))
		for i, it := range req.Items {
			vals[i] = it.Value
		}
		if len(vals) > 0 {
			if err := c.RPush(ctx, req.Key, vals...).Err(); err != nil {
				return err
			}
		}
	case "set":
		vals := make([]any, len(req.Items))
		for i, it := range req.Items {
			vals[i] = it.Value
		}
		if len(vals) > 0 {
			if err := c.SAdd(ctx, req.Key, vals...).Err(); err != nil {
				return err
			}
		}
	case "zset":
		zs := make([]goredis.Z, len(req.Items))
		for i, it := range req.Items {
			zs[i] = goredis.Z{Score: it.Score, Member: it.Value}
		}
		if len(zs) > 0 {
			if err := c.ZAdd(ctx, req.Key, zs...).Err(); err != nil {
				return err
			}
		}
	case "stream":
		id := "*"
		values := map[string]any{}
		for _, it := range req.Items {
			if strings.TrimSpace(it.ID) != "" {
				id = strings.TrimSpace(it.ID)
			}
			if f := strings.TrimSpace(it.Field); f != "" {
				values[f] = it.Value
			}
		}
		if len(values) == 0 && len(req.Items) == 1 && strings.TrimSpace(req.Items[0].Value) != "" {
			rawVal := strings.TrimSpace(req.Items[0].Value)
			var obj map[string]any
			if json.Unmarshal([]byte(rawVal), &obj) == nil && len(obj) > 0 {
				for k, v := range obj {
					values[k] = fmt.Sprintf("%v", v)
				}
			} else {
				values["data"] = req.Items[0].Value
			}
		}
		if len(values) == 0 {
			if rawStr := strings.TrimSpace(req.StringValue); rawStr != "" {
				var obj map[string]any
				if json.Unmarshal([]byte(rawStr), &obj) == nil && len(obj) > 0 {
					for k, v := range obj {
						values[k] = fmt.Sprintf("%v", v)
					}
				} else {
					values["data"] = req.StringValue
				}
			} else {
				values["data"] = ""
			}
		}
		if err := c.XAdd(ctx, &goredis.XAddArgs{
			Stream: req.Key,
			ID:     id,
			Values: values,
		}).Err(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("creating key of type %s is not supported", req.Type)
	}
	if req.TTL > 0 {
		_ = c.Expire(ctx, req.Key, time.Duration(req.TTL)*time.Second).Err()
	}
	return nil
}

func (d *Driver) MutateCollectionItem(ctx context.Context, dbIndex int, req driver.KVItemMutationRequest) error {
	c := d.clientForDB(dbIndex)
	act := strings.ToUpper(strings.TrimSpace(req.Action))
	switch strings.ToLower(strings.TrimSpace(req.Type)) {
	case "hash":
		if act == "DELETE" {
			return c.HDel(ctx, req.Key, req.Field).Err()
		}
		return c.HSet(ctx, req.Key, req.Field, req.Value).Err()
	case "list":
		switch act {
		case "ADD":
			return c.RPush(ctx, req.Key, req.Value).Err()
		case "UPDATE":
			return c.LSet(ctx, req.Key, int64(req.Index), req.Value).Err()
		case "DELETE":
			return c.LRem(ctx, req.Key, 1, req.Value).Err()
		}
	case "set":
		if act == "DELETE" {
			return c.SRem(ctx, req.Key, req.Value).Err()
		}
		if act == "UPDATE" && req.OldValue != "" && req.OldValue != req.Value {
			_ = c.SRem(ctx, req.Key, req.OldValue).Err()
		}
		return c.SAdd(ctx, req.Key, req.Value).Err()
	case "zset":
		if act == "DELETE" {
			return c.ZRem(ctx, req.Key, req.Value).Err()
		}
		if act == "UPDATE" && req.OldValue != "" && req.OldValue != req.Value {
			_ = c.ZRem(ctx, req.Key, req.OldValue).Err()
		}
		return c.ZAdd(ctx, req.Key, goredis.Z{Score: req.Score, Member: req.Value}).Err()
	case "stream":
		switch act {
		case "ADD":
			id := strings.TrimSpace(req.ID)
			if id == "" {
				id = "*"
			}
			values := map[string]any{}
			if len(req.Fields) > 0 {
				for k, v := range req.Fields {
					if fk := strings.TrimSpace(k); fk != "" {
						values[fk] = v
					}
				}
			} else if f := strings.TrimSpace(req.Field); f != "" {
				values[f] = req.Value
			} else if raw := strings.TrimSpace(req.Value); raw != "" {
				var parsed map[string]any
				if json.Unmarshal([]byte(raw), &parsed) == nil && len(parsed) > 0 {
					for k, v := range parsed {
						values[k] = fmt.Sprintf("%v", v)
					}
				} else {
					values["data"] = req.Value
				}
			}
			if len(values) == 0 {
				return errors.New("stream entry requires at least one field/value pair")
			}
			xArgs := &goredis.XAddArgs{
				Stream: req.Key,
				ID:     id,
				Values: values,
			}
			if req.MaxLen > 0 {
				xArgs.MaxLen = req.MaxLen
				xArgs.Approx = true
			}
			return c.XAdd(ctx, xArgs).Err()
		case "DELETE":
			targetID := strings.TrimSpace(req.ID)
			if targetID == "" {
				targetID = strings.TrimSpace(req.Field)
			}
			if targetID == "" {
				targetID = strings.TrimSpace(req.Value)
			}
			if targetID == "" {
				return errors.New("stream entry ID is required for DELETE")
			}
			return c.XDel(ctx, req.Key, targetID).Err()
		case "TRIM":
			if req.MaxLen < 0 {
				return errors.New("maxLen must be >= 0 for stream TRIM")
			}
			return c.XTrimMaxLenApprox(ctx, req.Key, req.MaxLen, 0).Err()
		}
	}
	return fmt.Errorf("unsupported operation: %s / %s", req.Type, req.Action)
}

func (d *Driver) DeleteKeys(ctx context.Context, dbIndex int, keys []string) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	return d.clientForDB(dbIndex).Del(ctx, keys...).Result()
}

func (d *Driver) SetTTL(ctx context.Context, dbIndex int, key string, ttlSeconds int64) error {
	c := d.clientForDB(dbIndex)
	if ttlSeconds < 0 {
		return c.Persist(ctx, key).Err()
	}
	return c.Expire(ctx, key, time.Duration(ttlSeconds)*time.Second).Err()
}

func (d *Driver) RenameKey(ctx context.Context, dbIndex int, oldKey, newKey string, nxOnly bool) error {
	oldKey = strings.TrimSpace(oldKey)
	newKey = strings.TrimSpace(newKey)
	if oldKey == "" || newKey == "" {
		return errors.New("both oldKey and newKey are required")
	}
	if oldKey == newKey {
		return nil
	}
	c := d.clientForDB(dbIndex)
	if nxOnly {
		ok, err := c.RenameNX(ctx, oldKey, newKey).Result()
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("target key %q already exists", newKey)
		}
		return nil
	}
	return c.Rename(ctx, oldKey, newKey).Err()
}

func (d *Driver) DuplicateKey(ctx context.Context, dbIndex int, srcKey, dstKey string, replace bool) error {
	srcKey = strings.TrimSpace(srcKey)
	dstKey = strings.TrimSpace(dstKey)
	if srcKey == "" || dstKey == "" {
		return errors.New("both source and destination keys are required")
	}
	if srcKey == dstKey {
		return errors.New("destination key must differ from source key")
	}
	c := d.clientForDB(dbIndex)

	// Try native Redis 6.2+ COPY first
	copyArgs := []any{"COPY", srcKey, dstKey}
	if replace {
		copyArgs = append(copyArgs, "REPLACE")
	}
	if res, err := c.Do(ctx, copyArgs...).Int(); err == nil {
		if res == 0 {
			return fmt.Errorf("failed to copy %q to %q (target may already exist or source missing)", srcKey, dstKey)
		}
		return nil
	}

	// Fallback to DUMP + PTTL + RESTORE for Redis < 6.2
	payload, err := c.Dump(ctx, srcKey).Result()
	if err != nil {
		return fmt.Errorf("failed to dump source key %q: %w", srcKey, err)
	}
	pttl, _ := c.PTTL(ctx, srcKey).Result()
	if pttl < 0 {
		pttl = 0
	}
	if replace {
		return c.RestoreReplace(ctx, dstKey, pttl, payload).Err()
	}
	return c.Restore(ctx, dstKey, pttl, payload).Err()
}

func (d *Driver) FlushDB(ctx context.Context, dbIndex int) error {
	return d.clientForDB(dbIndex).FlushDB(ctx).Err()
}

var readOnlyRedisCommands = map[string]struct{}{
	"GET": {}, "MGET": {}, "STRLEN": {}, "GETRANGE": {}, "SUBSTR": {}, "GETBIT": {}, "BITCOUNT": {}, "BITPOS": {},
	"HGET": {}, "HMGET": {}, "HGETALL": {}, "HKEYS": {}, "HVALS": {}, "HLEN": {}, "HEXISTS": {}, "HSCAN": {}, "HSTRLEN": {}, "HRANDFIELD": {},
	"LRANGE": {}, "LLEN": {}, "LINDEX": {}, "LPOS": {},
	"SMEMBERS": {}, "SCARD": {}, "SISMEMBER": {}, "SMISMEMBER": {}, "SRANDMEMBER": {}, "SSCAN": {}, "SUNION": {}, "SINTER": {}, "SDIFF": {},
	"ZRANGE": {}, "ZREVRANGE": {}, "ZRANGEBYSCORE": {}, "ZREVRANGEBYSCORE": {}, "ZRANGEBYLEX": {}, "ZCARD": {}, "ZCOUNT": {}, "ZSCORE": {}, "ZMSCORE": {}, "ZRANK": {}, "ZREVRANK": {}, "ZSCAN": {}, "ZRANDMEMBER": {},
	"XLEN": {}, "XRANGE": {}, "XREVRANGE": {}, "XINFO": {},
	"PFCOUNT": {}, "GEOPOS": {}, "GEODIST": {}, "GEOHASH": {}, "GEOSEARCH": {},
	"SCAN": {}, "TYPE": {}, "TTL": {}, "PTTL": {}, "EXISTS": {}, "RANDOMKEY": {}, "DBSIZE": {}, "DUMP": {}, "OBJECT": {}, "MEMORY": {},
	"PING": {}, "ECHO": {}, "TIME": {}, "INFO": {}, "ROLE": {}, "LOLWUT": {}, "COMMAND": {}, "PUBSUB": {},
}

// IsReadOnlyCommand checks whether a raw Redis CLI command is safe for read-only users.
func IsReadOnlyCommand(cmdLine string) (bool, string) {
	args := splitCommandArgs(cmdLine)
	if len(args) == 0 {
		return true, ""
	}
	cmd := strings.ToUpper(fmt.Sprintf("%v", args[0]))
	if _, ok := readOnlyRedisCommands[cmd]; ok {
		return true, ""
	}
	sub := ""
	if len(args) >= 2 {
		sub = strings.ToUpper(fmt.Sprintf("%v", args[1]))
	}
	switch cmd {
	case "SORT":
		for i := 1; i < len(args); i++ {
			if strings.EqualFold(fmt.Sprintf("%v", args[i]), "STORE") {
				return false, "SORT ... STORE is not permitted in read-only mode"
			}
		}
		return true, ""
	case "SLOWLOG":
		if sub == "GET" || sub == "LEN" {
			return true, ""
		}
	case "CLIENT":
		if sub == "LIST" || sub == "GETNAME" || sub == "ID" || sub == "INFO" {
			return true, ""
		}
	case "CLUSTER":
		if sub == "INFO" || sub == "NODES" || sub == "SLOTS" || sub == "SHARDS" || sub == "KEYSLOT" {
			return true, ""
		}
	case "LATENCY":
		if sub == "LATEST" || sub == "HISTORY" || sub == "GRAPH" || sub == "DOCTOR" {
			return true, ""
		}
	}
	return false, fmt.Sprintf("Redis command %q is not permitted in read-only mode", cmd)
}

func (d *Driver) ExecuteCommand(ctx context.Context, dbIndex int, cmdLine string) (driver.KVCommandResult, error) {
	args := splitCommandArgs(cmdLine)
	if len(args) == 0 {
		return driver.KVCommandResult{}, errors.New("please enter a Redis command")
	}
	start := time.Now()
	val, err := d.clientForDB(dbIndex).Do(ctx, args...).Result()
	if errors.Is(err, goredis.Nil) {
		return driver.KVCommandResult{Command: cmdLine, Output: "(nil)", ElapsedMs: time.Since(start).Milliseconds()}, nil
	}
	if err != nil {
		return driver.KVCommandResult{}, err
	}
	b, jErr := json.MarshalIndent(val, "", "  ")
	out := ""
	if jErr == nil {
		out = string(b)
	} else {
		out = fmt.Sprintf("%v", val)
	}
	return driver.KVCommandResult{
		Command:   cmdLine,
		Output:    out,
		ElapsedMs: time.Since(start).Milliseconds(),
	}, nil
}

func (d *Driver) GetMonitorSnapshot(ctx context.Context) (driver.MonitorSnapshot, error) {
	c := d.clientForDB(0)
	snap := driver.MonitorSnapshot{
		Timestamp:      time.Now().Format(time.RFC3339),
		Metrics:        []driver.MonitorMetric{},
		Storage:        []driver.StorageItem{},
		Sessions:       []driver.SessionItem{},
		CanKillSession: true,
	}
	info, err := c.Info(ctx).Result()
	if err != nil {
		return snap, err
	}
	snap.Version = parseRedisInfoField(info, "redis_version")
	snap.UptimeSec, _ = strconv.ParseInt(parseRedisInfoField(info, "uptime_in_seconds"), 10, 64)

	snap.Metrics = append(snap.Metrics,
		driver.MonitorMetric{Key: "connected_clients", Label: "Connected Clients", Value: parseRedisInfoField(info, "connected_clients")},
		driver.MonitorMetric{Key: "used_memory_human", Label: "Memory Usage", Value: parseRedisInfoField(info, "used_memory_human")},
		driver.MonitorMetric{Key: "instantaneous_ops_per_sec", Label: "Ops / Sec", Value: parseRedisInfoField(info, "instantaneous_ops_per_sec")},
		driver.MonitorMetric{Key: "keyspace_hits", Label: "Keyspace Hits", Value: parseRedisInfoField(info, "keyspace_hits")},
	)

	counts, _ := d.keyspaceCounts(ctx)
	for i := 0; i < 16; i++ {
		if counts[i] > 0 {
			snap.Storage = append(snap.Storage, driver.StorageItem{
				Name: fmt.Sprintf("db%d", i), Category: "KEYSPACE", Rows: counts[i],
			})
		}
	}

	clientList, err := c.ClientList(ctx).Result()
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(clientList), "\n") {
			if line == "" {
				continue
			}
			kv := map[string]string{}
			for _, part := range strings.Fields(line) {
				p := strings.SplitN(part, "=", 2)
				if len(p) == 2 {
					kv[p[0]] = p[1]
				}
			}
			age, _ := strconv.ParseInt(kv["age"], 10, 64)
			snap.Sessions = append(snap.Sessions, driver.SessionItem{
				ID: kv["id"], User: kv["user"], Host: kv["addr"], Database: "db" + kv["db"],
				Command: kv["cmd"], State: "CONNECTED", DurationSec: age, Query: kv["cmd"],
			})
		}
	}
	return snap, nil
}

func (d *Driver) KillSession(ctx context.Context, sessionID string) error {
	return d.clientForDB(0).Do(ctx, "CLIENT", "KILL", "ID", sessionID).Err()
}

func (d *Driver) GetStreamInfo(ctx context.Context, dbIndex int, key string) (driver.KVStreamInfo, error) {
	if strings.TrimSpace(key) == "" {
		return driver.KVStreamInfo{}, errors.New("stream key is required")
	}
	c := d.clientForDB(dbIndex)
	info, err := c.XInfoStream(ctx, key).Result()
	if err != nil {
		return driver.KVStreamInfo{}, err
	}
	out := driver.KVStreamInfo{
		Length:      info.Length,
		RadixKeys:   info.RadixTreeKeys,
		FirstID:     info.FirstEntry.ID,
		LastID:      info.LastEntry.ID,
		GroupsCount: info.Groups,
		Groups:      []driver.KVStreamGroup{},
	}
	groups, gErr := c.XInfoGroups(ctx, key).Result()
	if gErr == nil {
		for _, g := range groups {
			consList := []driver.KVStreamConsumer{}
			if consumers, cErr := c.XInfoConsumers(ctx, key, g.Name).Result(); cErr == nil {
				limit := len(consumers)
				if limit > 50 {
					limit = 50
				}
				for i := 0; i < limit; i++ {
					cons := consumers[i]
					consList = append(consList, driver.KVStreamConsumer{
						Name:    cons.Name,
						Pending: cons.Pending,
						IdleMs:  cons.Idle.Milliseconds(),
					})
				}
			}
			out.Groups = append(out.Groups, driver.KVStreamGroup{
				Name:            g.Name,
				Consumers:       g.Consumers,
				Pending:         g.Pending,
				LastDeliveredID: g.LastDeliveredID,
				EntriesRead:     g.EntriesRead,
				Lag:             g.Lag,
				ConsumerList:    consList,
			})
		}
		if out.GroupsCount == 0 && len(out.Groups) > 0 {
			out.GroupsCount = int64(len(out.Groups))
		}
	}
	return out, nil
}

func (d *Driver) ManageStreamGroup(ctx context.Context, req driver.KVStreamGroupActionRequest) error {
	key := strings.TrimSpace(req.Key)
	group := strings.TrimSpace(req.Group)
	if key == "" || group == "" {
		return errors.New("stream key and consumer group name are required")
	}
	c := d.clientForDB(req.DB)
	switch strings.ToUpper(strings.TrimSpace(req.Action)) {
	case "CREATE":
		startID := strings.TrimSpace(req.StartID)
		if startID == "" {
			startID = "$"
		}
		return c.XGroupCreateMkStream(ctx, key, group, startID).Err()
	case "SETID":
		startID := strings.TrimSpace(req.StartID)
		if startID == "" {
			startID = "$"
		}
		return c.XGroupSetID(ctx, key, group, startID).Err()
	case "DESTROY":
		return c.XGroupDestroy(ctx, key, group).Err()
	case "DELCONSUMER":
		consumer := strings.TrimSpace(req.Consumer)
		if consumer == "" {
			return errors.New("consumer name is required for DELCONSUMER")
		}
		return c.XGroupDelConsumer(ctx, key, group, consumer).Err()
	default:
		return fmt.Errorf("unsupported stream group action: %s", req.Action)
	}
}

func (d *Driver) GetSlowlog(ctx context.Context, count int64) ([]driver.KVSlowlogEntry, error) {
	if count <= 0 {
		count = 50
	} else if count > 500 {
		count = 500
	}
	entries, err := d.clientForDB(0).SlowLogGet(ctx, count).Result()
	if err != nil {
		return nil, err
	}
	out := make([]driver.KVSlowlogEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, driver.KVSlowlogEntry{
			ID:         e.ID,
			Timestamp:  e.Time.Format(time.RFC3339),
			DurationUs: e.Duration.Microseconds(),
			Command:    strings.Join(e.Args, " "),
			ClientAddr: e.ClientAddr,
			ClientName: e.ClientName,
		})
	}
	return out, nil
}

func (d *Driver) ResetSlowlog(ctx context.Context) error {
	return d.clientForDB(0).Do(ctx, "SLOWLOG", "RESET").Err()
}

func (d *Driver) GetConfig(ctx context.Context, pattern string) ([]driver.KVConfigItem, error) {
	p := strings.TrimSpace(pattern)
	if p == "" {
		p = "*"
	}
	m, err := d.clientForDB(0).ConfigGet(ctx, p).Result()
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]driver.KVConfigItem, 0, len(keys))
	for _, k := range keys {
		out = append(out, driver.KVConfigItem{
			Key:   k,
			Value: m[k],
		})
	}
	return out, nil
}

func (d *Driver) SetConfig(ctx context.Context, key, value string) error {
	k := strings.TrimSpace(key)
	if k == "" {
		return errors.New("configuration parameter key is required")
	}
	return d.clientForDB(0).ConfigSet(ctx, k, value).Err()
}

func (d *Driver) PublishMessage(ctx context.Context, channel, message string) (int64, error) {
	ch := strings.TrimSpace(channel)
	if ch == "" {
		return 0, errors.New("pub/sub channel name is required")
	}
	return d.clientForDB(0).Publish(ctx, ch, message).Result()
}

func parseRedisInfoField(info, field string) string {
	prefix := field + ":"
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func splitCommandArgs(cmdLine string) []any {
	var args []any
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(cmdLine); i++ {
		ch := cmdLine[i]
		if quote != 0 {
			if ch == '\\' && i+1 < len(cmdLine) {
				i++
				cur.WriteByte(cmdLine[i])
				continue
			}
			if ch == quote {
				quote = 0
				continue
			}
			cur.WriteByte(ch)
			continue
		}
		if ch == '"' || ch == '\'' {
			quote = ch
			continue
		}
		if ch == ' ' || ch == '\t' {
			if cur.Len() > 0 {
				args = append(args, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteByte(ch)
	}
	if cur.Len() > 0 {
		args = append(args, cur.String())
	}
	return args
}
