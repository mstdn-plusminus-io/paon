package api

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestRedisConfigMatchesNodeStreamingDefaults(t *testing.T) {
	cfg := redisConfig(config.Config{RedisHost: "redis.internal", RedisPort: "6380", RedisDB: "2", RedisPassword: "secret", RedisNamespace: "paon:"})
	if cfg.network != "tcp" || cfg.address != "redis.internal:6380" || cfg.db != 2 || cfg.password != "secret" || cfg.prefix != "paon:" {
		t.Fatalf("cfg = %#v", cfg)
	}

	cfg = redisConfig(config.Config{RedisURL: "unix:///tmp/redis.sock", RedisHost: "ignored", RedisPort: "1"})
	if cfg.network != "unix" || cfg.address != "/tmp/redis.sock" {
		t.Fatalf("unix cfg = %#v", cfg)
	}

	cfg = redisConfig(config.Config{RedisURL: "redis://:pass@example.test:6381/4"})
	if cfg.network != "tcp" || cfg.address != "example.test:6381" || cfg.password != "pass" || cfg.db != 4 {
		t.Fatalf("url cfg = %#v", cfg)
	}

	cfg = redisConfig(config.Config{RedisURL: "redis://alice:secret@example.test:6381/4"})
	if cfg.username != "alice" || cfg.password != "secret" || cfg.address != "example.test:6381" || cfg.db != 4 {
		t.Fatalf("acl url cfg = %#v", cfg)
	}
}

func TestRedisHandshakeUsesRedisACLUsernameWhenPresent(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	defer client.Close()

	requests := make(chan string, 2)
	go func() {
		auth := make([]byte, len("*3\r\n$4\r\nAUTH\r\n$5\r\nalice\r\n$6\r\nsecret\r\n"))
		_, _ = io.ReadFull(server, auth)
		requests <- string(auth)
		_, _ = io.WriteString(server, "+OK\r\n")
		selectCmd := make([]byte, len("*2\r\n$6\r\nSELECT\r\n$1\r\n2\r\n"))
		_, _ = io.ReadFull(server, selectCmd)
		requests <- string(selectCmd)
		_, _ = io.WriteString(server, "+OK\r\n")
	}()

	cfg := redisConnConfig{username: "alice", password: "secret", db: 2}
	if err := redisHandshake(client, bufio.NewReader(client), cfg); err != nil {
		t.Fatalf("redisHandshake returned error: %v", err)
	}
	if got := <-requests; got != "*3\r\n$4\r\nAUTH\r\n$5\r\nalice\r\n$6\r\nsecret\r\n" {
		t.Fatalf("AUTH command = %q", got)
	}
	if got := <-requests; got != "*2\r\n$6\r\nSELECT\r\n$1\r\n2\r\n" {
		t.Fatalf("SELECT command = %q", got)
	}
}

func TestCacheRedisConfigUsesRailsCacheRedisURLWhenPresent(t *testing.T) {
	cfg := cacheRedisConfig(config.Config{
		RedisURL:      "redis://main.example.test:6379/0",
		CacheRedisURL: "redis://:cachepass@cache.example.test:6380/3",
	})
	if cfg.network != "tcp" || cfg.address != "cache.example.test:6380" || cfg.password != "cachepass" || cfg.db != 3 {
		t.Fatalf("cache cfg = %#v", cfg)
	}

	cfg = cacheRedisConfig(config.Config{
		RedisURL:      "redis://main.example.test:6379/2",
		CacheRedisURL: "",
	})
	if cfg.address != "main.example.test:6379" || cfg.db != 2 {
		t.Fatalf("cache fallback cfg = %#v", cfg)
	}
}

func TestSidekiqRedisConfigUsesRailsSidekiqRedisURLWhenPresent(t *testing.T) {
	cfg := sidekiqRedisConfig(config.Config{
		RedisURL:        "redis://main.example.test:6379/0",
		SidekiqRedisURL: "redis://:sidekiqpass@sidekiq.example.test:6382/4",
	})
	if cfg.network != "tcp" || cfg.address != "sidekiq.example.test:6382" || cfg.password != "sidekiqpass" || cfg.db != 4 {
		t.Fatalf("sidekiq cfg = %#v", cfg)
	}

	cfg = sidekiqRedisConfig(config.Config{
		RedisURL:        "redis://main.example.test:6379/2",
		SidekiqRedisURL: "",
	})
	if cfg.address != "main.example.test:6379" || cfg.db != 2 {
		t.Fatalf("sidekiq fallback cfg = %#v", cfg)
	}
}

func TestWriteRedisCommandUsesRESPArrays(t *testing.T) {
	var buf bytes.Buffer
	if err := writeRedisCommand(&buf, "SUBSCRIBE", "timeline:public"); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "*2\r\n$9\r\nSUBSCRIBE\r\n$15\r\ntimeline:public\r\n" {
		t.Fatalf("command = %q", got)
	}
}

func TestRedisAvailablePingsConfiguredRedis(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	originalDial := redisDial
	redisDial = func(_ context.Context, _ redisConnConfig) (net.Conn, *bufio.Reader, error) {
		return client, bufio.NewReader(client), nil
	}
	defer func() { redisDial = originalDial }()

	requests := make(chan string, 2)
	go func() {
		buf := make([]byte, len("*1\r\n$4\r\nPING\r\n"))
		_, _ = io.ReadFull(server, buf)
		_, _ = io.WriteString(server, "+PONG\r\n")
		requests <- string(buf)
		buf = make([]byte, len("*2\r\n$4\r\nINFO\r\n$6\r\nserver\r\n"))
		_, _ = io.ReadFull(server, buf)
		info := "# Server\r\nredis_version:7.2.5\r\n"
		_, _ = io.WriteString(server, "$"+strconv.Itoa(len(info))+"\r\n"+info+"\r\n")
		requests <- string(buf)
	}()

	cfg := config.Config{RedisHost: "redis.internal", RedisPort: "6379"}
	if err := RedisAvailable(t.Context(), cfg); err != nil {
		t.Fatalf("RedisAvailable returned error: %v", err)
	}
	if got := <-requests; got != "*1\r\n$4\r\nPING\r\n" {
		t.Fatalf("redis request = %q", got)
	}
	if got := <-requests; got != "*2\r\n$4\r\nINFO\r\n$6\r\nserver\r\n" {
		t.Fatalf("redis request = %q", got)
	}
}

func TestRedisAvailablePingsRoleSpecificRedisLikeRails(t *testing.T) {
	originalDial := redisDial
	defer func() { redisDial = originalDial }()
	configs := make(chan redisConnConfig, 3)
	redisDial = func(_ context.Context, cfg redisConnConfig) (net.Conn, *bufio.Reader, error) {
		client, server := net.Pipe()
		configs <- cfg
		go serveRedisHandshakeAndAvailability(t, server, "7.2.5")
		return client, bufio.NewReader(client), nil
	}

	cfg := config.Config{
		RedisURL:        "redis://main.example.test:6379/0",
		CacheRedisURL:   "redis://cache.example.test:6380/2",
		SidekiqRedisURL: "redis://sidekiq.example.test:6381/3",
	}
	if err := RedisAvailable(t.Context(), cfg); err != nil {
		t.Fatalf("RedisAvailable returned error: %v", err)
	}
	close(configs)
	var got []string
	for cfg := range configs {
		got = append(got, cfg.address+"/"+strconv.Itoa(cfg.db))
	}
	sort.Strings(got)
	want := []string{"cache.example.test:6380/2", "main.example.test:6379/0", "sidekiq.example.test:6381/3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pinged redis configs = %#v, want %#v", got, want)
	}
}

func TestRedisAvailableDeduplicatesRoleSpecificRedisFallbacks(t *testing.T) {
	originalDial := redisDial
	defer func() { redisDial = originalDial }()
	var calls int
	redisDial = func(_ context.Context, cfg redisConnConfig) (net.Conn, *bufio.Reader, error) {
		calls++
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			reader := bufio.NewReader(server)
			_, _ = readRedisValue(reader)
			_, _ = io.WriteString(server, "+PONG\r\n")
			_, _ = readRedisValue(reader)
			info := "# Server\r\nredis_version:7.2.5\r\n"
			_, _ = io.WriteString(server, "$"+strconv.Itoa(len(info))+"\r\n"+info+"\r\n")
		}()
		return client, bufio.NewReader(client), nil
	}

	cfg := config.Config{
		RedisURL:        "redis://main.example.test:6379/0",
		CacheRedisURL:   "redis://main.example.test:6379/0",
		SidekiqRedisURL: "redis://main.example.test:6379/0",
	}
	if err := RedisAvailable(t.Context(), cfg); err != nil {
		t.Fatalf("RedisAvailable returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("RedisAvailable should ping shared role Redis once, calls=%d", calls)
	}
}

func TestRedisAvailableRejectsRedisOlderThanSixPointTwo(t *testing.T) {
	originalDial := redisDial
	defer func() { redisDial = originalDial }()
	redisDial = func(_ context.Context, _ redisConnConfig) (net.Conn, *bufio.Reader, error) {
		client, server := net.Pipe()
		go serveRedisHandshakeAndAvailability(t, server, "6.0.20")
		return client, bufio.NewReader(client), nil
	}

	err := RedisAvailable(t.Context(), config.Config{RedisHost: "redis.internal", RedisPort: "6379"})
	if err == nil || !strings.Contains(err.Error(), "Redis 6.2 or newer") {
		t.Fatalf("RedisAvailable error = %v, want minimum version error", err)
	}
}

func TestValidateRedisVersion(t *testing.T) {
	for _, version := range []string{"6.2.0", "7.0.0", "10.1.3"} {
		if err := validateRedisVersion(version); err != nil {
			t.Errorf("validateRedisVersion(%q) = %v", version, err)
		}
	}
	for _, version := range []string{"", "6", "5.9.9", "6.0.20", "six.2"} {
		if err := validateRedisVersion(version); err == nil {
			t.Errorf("validateRedisVersion(%q) accepted unsupported or invalid version", version)
		}
	}
}

func TestReadRedisValueAndPubSubMessage(t *testing.T) {
	input := "*3\r\n$7\r\nmessage\r\n$15\r\ntimeline:public\r\n$41\r\n{\"event\":\"update\",\"payload\":{\"id\":\"100\"}}\r\n"
	value, err := readRedisValue(bufio.NewReader(stringsReader(input)))
	if err != nil {
		t.Fatal(err)
	}
	items, ok := value.([]any)
	if !ok || len(items) != 3 {
		t.Fatalf("value = %#v", value)
	}
	if !reflect.DeepEqual(items[:2], []any{"message", "timeline:public"}) {
		t.Fatalf("items = %#v", items)
	}
	message, ok := redisPubSubMessage(value)
	if !ok {
		t.Fatal("message not parsed")
	}
	if message.Event != "update" || string(message.Payload) != `{"id":"100"}` {
		t.Fatalf("message = %#v payload=%s", message, string(message.Payload))
	}
}

func TestReadRedisValueDistinguishesNilAndEmptyBulk(t *testing.T) {
	value, err := readRedisValue(bufio.NewReader(stringsReader("$-1\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if value != nil {
		t.Fatalf("nil bulk = %#v, want nil", value)
	}

	value, err = readRedisValue(bufio.NewReader(stringsReader("$0\r\n\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if value != "" {
		t.Fatalf("empty bulk = %#v, want empty string", value)
	}
}

func TestReadRedisValueKeepsNilBulkInsideArrays(t *testing.T) {
	value, err := readRedisValue(bufio.NewReader(stringsReader("*2\r\n$3\r\nfoo\r\n$-1\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	want := []any{"foo", nil}
	if !reflect.DeepEqual(value, want) {
		t.Fatalf("array = %#v, want %#v", value, want)
	}
}

func TestReadRedisValueHandlesNilAndInvalidArrays(t *testing.T) {
	value, err := readRedisValue(bufio.NewReader(stringsReader("*-1\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if value != nil {
		t.Fatalf("nil array = %#v, want nil", value)
	}

	if _, err := readRedisValue(bufio.NewReader(stringsReader("*-2\r\n"))); err == nil {
		t.Fatal("invalid array length was accepted")
	}
	if _, err := readRedisValue(bufio.NewReader(stringsReader("$-2\r\n"))); err == nil {
		t.Fatal("invalid bulk length was accepted")
	}
}

func stringsReader(value string) *bytes.Reader {
	return bytes.NewReader([]byte(value))
}

func serveRedisHandshakeAndAvailability(t *testing.T, conn net.Conn, version string) {
	t.Helper()
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		value, err := readRedisValue(reader)
		if err != nil {
			t.Errorf("read redis command: %v", err)
			return
		}
		items, ok := value.([]any)
		if !ok || len(items) == 0 {
			t.Errorf("redis command = %#v", value)
			return
		}
		command, ok := items[0].(string)
		if !ok {
			t.Errorf("redis command name = %#v", items[0])
			return
		}
		switch strings.ToUpper(command) {
		case "AUTH", "SELECT":
			if _, err := io.WriteString(conn, "+OK\r\n"); err != nil {
				t.Errorf("write redis OK: %v", err)
				return
			}
		case "PING":
			if _, err := io.WriteString(conn, "+PONG\r\n"); err != nil {
				t.Errorf("write redis PONG: %v", err)
			}
		case "INFO":
			info := "# Server\r\nredis_version:" + version + "\r\n"
			if _, err := io.WriteString(conn, "$"+strconv.Itoa(len(info))+"\r\n"+info+"\r\n"); err != nil {
				t.Errorf("write redis INFO: %v", err)
			}
			return
		default:
			t.Errorf("unexpected redis command %q", command)
			return
		}
	}
}
