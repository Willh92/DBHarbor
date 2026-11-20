package redis

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	"dbharbor/backend/internal/driver"
)

func TestSplitCommandArgs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   \t  ", nil},
		{"GET mykey", []string{"GET", "mykey"}},
		{`SET "my key" 'hello world'`, []string{"SET", "my key", "hello world"}},
		{`SET k "a\"b\\c"`, []string{"SET", "k", `a"b\c`}},
	}
	for _, tc := range cases {
		got := splitCommandArgs(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("splitCommandArgs(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Fatalf("splitCommandArgs(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

func TestIsReadOnlyCommand(t *testing.T) {
	allowed := []string{
		"", "get foo", "HGETALL h1", "ZSCAN z1 0", "XINFO STREAM s1", "INFO", "PING",
		"SORT mylist BY weight_*",
		"SLOWLOG GET 10", "SLOWLOG LEN",
		"CLIENT LIST", "CLIENT INFO",
		"CLUSTER NODES", "CLUSTER SHARDS",
		"LATENCY DOCTOR",
	}
	for _, cmd := range allowed {
		if ok, reason := IsReadOnlyCommand(cmd); !ok {
			t.Fatalf("expected IsReadOnlyCommand(%q) to allow, reason=%q", cmd, reason)
		}
	}

	blocked := []string{
		"SET k v", "DEL k", "FLUSHDB", "ACL SETUSER u1", "CONFIG SET maxmemory 0",
		"EVAL 'return 1' 0",
		"SORT mylist STORE outlist",
		"SLOWLOG RESET",
		"CLIENT KILL 127.0.0.1:1234",
		"CLUSTER FAILOVER",
		"LATENCY RESET",
	}
	for _, cmd := range blocked {
		if ok, _ := IsReadOnlyCommand(cmd); ok {
			t.Fatalf("expected IsReadOnlyCommand(%q) to block", cmd)
		}
	}
}

func TestParseRedisInfoField(t *testing.T) {
	raw := "redis_version:7.2.4\r\nused_memory_human:1.25M\nrole:master:extra\n"
	if got := parseRedisInfoField(raw, "redis_version"); got != "7.2.4" {
		t.Fatalf("unexpected version: %q", got)
	}
	if got := parseRedisInfoField(raw, "role"); got != "master:extra" {
		t.Fatalf("expected colon value preserved, got %q", got)
	}
	if got := parseRedisInfoField(raw, "missing"); got != "" {
		t.Fatalf("expected empty for missing key, got %q", got)
	}
}

func TestNewAndClientForDBTLSAndClamping(t *testing.T) {
	if _, err := New(driver.ConnectionConfig{
		Type:    "redis",
		Host:    "10.0.0.5",
		Options: map[string]string{"tls": "skip-verify", "__restrict_internal_ip": "1"},
	}); err == nil {
		t.Fatal("expected restricted skip-verify to fail in New")
	}

	if _, err := New(driver.ConnectionConfig{
		Type:    "redis",
		Host:    "db.example.com",
		Options: map[string]string{"tls": "true", "caCert": "invalid-pem"},
	}); err == nil {
		t.Fatal("expected invalid caCert PEM to fail in New")
	}

	drvAny, err := New(driver.ConnectionConfig{
		Type:    "redis",
		Host:    "db.example.com",
		Options: map[string]string{"tls": "true"},
	})
	if err != nil {
		t.Fatalf("New verified TLS failed: %v", err)
	}
	drv := drvAny.(*Driver)
	defer drv.Close()

	cNeg := drv.clientForDB(-5)
	cZero := drv.clientForDB(0)
	if cNeg != cZero || cNeg.Options().DB != 0 {
		t.Fatal("expected negative DB index to clamp to 0")
	}
	cHigh := drv.clientForDB(99)
	c15 := drv.clientForDB(15)
	if cHigh != c15 || cHigh.Options().DB != 15 {
		t.Fatal("expected >15 DB index to clamp to 15")
	}
	if cZero.Options().TLSConfig == nil || cZero.Options().TLSConfig.InsecureSkipVerify || cZero.Options().TLSConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("expected verified TLS 1.2+ config without InsecureSkipVerify, got %+v", cZero.Options().TLSConfig)
	}
	if err := drv.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	drv.mu.Lock()
	nClients := len(drv.clients)
	drv.mu.Unlock()
	if nClients != 0 {
		t.Fatalf("expected Close() to clear clients map, got %d", nClients)
	}
}

func TestValidationErrorsAndExecuteCommandArgsWire(t *testing.T) {
	drvAny, err := New(driver.ConnectionConfig{Type: "redis", Host: "127.0.0.1", Port: 6379})
	if err != nil {
		t.Fatal(err)
	}
	drv := drvAny.(*Driver)
	defer drv.Close()

	ctx := context.Background()
	if _, err := drv.GetStreamInfo(ctx, 0, ""); err == nil {
		t.Fatal("expected empty key error on GetStreamInfo")
	}
	if err := drv.ManageStreamGroup(ctx, driver.KVStreamGroupActionRequest{DB: 0, Key: "", Group: "g1", StartID: "$", Action: "CREATE"}); err == nil {
		t.Fatal("expected empty key/group error on ManageStreamGroup")
	}
	if err := drv.SetConfig(ctx, "", "100"); err == nil {
		t.Fatal("expected empty config parameter error")
	}
	if _, err := drv.PublishMessage(ctx, "", "hi"); err == nil {
		t.Fatal("expected empty channel error on PublishMessage")
	}

	// Spin up in-process RESP2 TCP stub to verify exact wire arguments from ExecuteCommandArgs
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	var mu sync.Mutex
	var recorded [][]string
	go func() {
		for {
			conn, aErr := ln.Accept()
			if aErr != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					line, rErr := r.ReadString('\n')
					if rErr != nil {
						return
					}
					line = strings.TrimRight(line, "\r\n")
					if !strings.HasPrefix(line, "*") {
						continue
					}
					argc, _ := strconv.Atoi(line[1:])
					args := make([]string, 0, argc)
					for i := 0; i < argc; i++ {
						lenLine, lErr := r.ReadString('\n')
						if lErr != nil {
							return
						}
						sz, _ := strconv.Atoi(strings.TrimRight(strings.TrimPrefix(lenLine, "$"), "\r\n"))
						buf := make([]byte, sz+2)
						if _, rErr := io.ReadFull(r, buf); rErr != nil {
							return
						}
						args = append(args, string(buf[:sz]))
					}
					mu.Lock()
					recorded = append(recorded, args)
					mu.Unlock()
					if len(args) > 0 && strings.EqualFold(args[0], "HELLO") {
						_, _ = c.Write([]byte("-ERR unknown command 'HELLO'\r\n"))
					} else {
						_, _ = c.Write([]byte("+OK\r\n"))
					}
				}
			}(conn)
		}
	}()

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	stubDrvAny, err := New(driver.ConnectionConfig{Type: "redis", Host: host, Port: port})
	if err != nil {
		t.Fatalf("New stub driver: %v", err)
	}
	stubDrv := stubDrvAny.(*Driver)
	defer stubDrv.Close()

	specialPassArg := `>p@"s's\w#o r d!`
	res, err := stubDrv.ExecuteCommandArgs(ctx, 0, "ACL", "SETUSER", "u1", "on", specialPassArg, "~*", "+@read")
	if err != nil || res.Output != `"OK"` {
		t.Fatalf("ExecuteCommandArgs failed: err=%v res=%+v", err, res)
	}

	mu.Lock()
	defer mu.Unlock()
	foundACL := false
	for _, cmd := range recorded {
		if len(cmd) == 7 && cmd[0] == "ACL" && cmd[1] == "SETUSER" && cmd[4] == specialPassArg {
			foundACL = true
			break
		}
	}
	if !foundACL {
		t.Fatalf("expected RESP wire args with exact password %q, got %#v", specialPassArg, recorded)
	}
}
