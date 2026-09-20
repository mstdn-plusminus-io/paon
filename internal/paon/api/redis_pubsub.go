package api

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/telemetry"
)

type redisMessage struct {
	Channel string          `json:"-"`
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload"`
}

type redisConnConfig struct {
	network          string
	address          string
	username         string
	password         string
	db               int
	tls              bool
	prefix           string
	sentinelMaster   string
	sentinelAddrs    []string
	sentinelUsername string
	sentinelPassword string
}

type namedRedisConnConfig struct {
	name string
	cfg  redisConnConfig
}

func redisEndpointConfigured(cfg config.Config) bool {
	return strings.TrimSpace(cfg.RedisURL) != "" ||
		strings.TrimSpace(cfg.RedisHost) != "" ||
		strings.TrimSpace(cfg.RedisPort) != ""
}

func redisConfig(cfg config.Config) redisConnConfig {
	out := redisConnConfig{
		network:          "tcp",
		address:          net.JoinHostPort(cfg.RedisHost, cfg.RedisPort),
		password:         cfg.RedisPassword,
		prefix:           cfg.RedisNamespace,
		sentinelMaster:   cfg.RedisSentinel.MasterName,
		sentinelAddrs:    append([]string(nil), cfg.RedisSentinel.Addresses...),
		sentinelUsername: cfg.RedisSentinel.Username,
		sentinelPassword: cfg.RedisSentinel.Password,
	}
	if db, err := strconv.Atoi(cfg.RedisDB); err == nil {
		out.db = db
	}
	if cfg.RedisURL == "" {
		return out
	}
	if strings.HasPrefix(cfg.RedisURL, "unix://") {
		out.network = "unix"
		out.address = strings.TrimPrefix(cfg.RedisURL, "unix://")
		return out
	}
	parsed, err := url.Parse(cfg.RedisURL)
	if err != nil {
		return out
	}
	if parsed.Scheme == "rediss" {
		out.tls = true
	}
	if parsed.Host != "" {
		out.address = parsed.Host
		if !strings.Contains(parsed.Host, ":") {
			out.address = net.JoinHostPort(parsed.Host, "6379")
		}
	}
	if username := parsed.User.Username(); username != "" {
		out.username = username
	}
	if password, ok := parsed.User.Password(); ok {
		out.password = password
	}
	if parsed.Path != "" && parsed.Path != "/" {
		if db, err := strconv.Atoi(strings.TrimPrefix(parsed.Path, "/")); err == nil {
			out.db = db
		}
	}
	return out
}

func cacheRedisConfig(cfg config.Config) redisConnConfig {
	if strings.TrimSpace(cfg.CacheRedisURL) != "" {
		cfg.RedisURL = cfg.CacheRedisURL
	}
	cfg.RedisSentinel = cfg.CacheRedisSentinel
	return redisConfig(cfg)
}

func sidekiqRedisConfig(cfg config.Config) redisConnConfig {
	if strings.TrimSpace(cfg.SidekiqRedisURL) != "" {
		cfg.RedisURL = cfg.SidekiqRedisURL
	}
	cfg.RedisSentinel = cfg.SidekiqRedisSentinel
	return redisConfig(cfg)
}

func redisAvailabilityConfigs(cfg config.Config) []namedRedisConnConfig {
	configs := []namedRedisConnConfig{
		{name: "redis", cfg: redisConfig(cfg)},
		{name: "cache redis", cfg: cacheRedisConfig(cfg)},
		{name: "sidekiq redis", cfg: sidekiqRedisConfig(cfg)},
	}
	out := make([]namedRedisConnConfig, 0, len(configs))
	seen := map[string]struct{}{}
	for _, item := range configs {
		key := redisConnAvailabilityKey(item.cfg)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func redisConnAvailabilityKey(cfg redisConnConfig) string {
	return strings.Join([]string{
		cfg.network,
		cfg.address,
		cfg.username,
		cfg.password,
		strconv.Itoa(cfg.db),
		strconv.FormatBool(cfg.tls),
		cfg.sentinelMaster,
		strings.Join(cfg.sentinelAddrs, ","),
		cfg.sentinelUsername,
		cfg.sentinelPassword,
	}, "\x00")
}

func (s *Server) subscribeRedis(ctx context.Context, channels []string, out chan<- redisMessage) error {
	if len(channels) == 0 {
		return nil
	}
	cfg := redisConfig(s.cfg)
	conn, reader, err := dialRedis(ctx, cfg)
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	if err := redisHandshake(conn, reader, cfg); err != nil {
		return err
	}

	prefixed := make([]string, 0, len(channels))
	for _, channel := range channels {
		prefixed = append(prefixed, cfg.prefix+channel)
	}
	if err := writeRedisCommand(conn, append([]string{"SUBSCRIBE"}, prefixed...)...); err != nil {
		return err
	}
	releaseSubscriptions := s.streamMetrics.trackRedisSubscriptions(prefixed)
	defer releaseSubscriptions()

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		value, err := readRedisValue(reader)
		if err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		if redisPubSubMessageFrame(value) {
			s.streamMetrics.incrementRedisMessagesReceived()
		}
		message, ok := redisPubSubMessage(value)
		if !ok {
			continue
		}
		select {
		case out <- message:
		case <-ctx.Done():
			return nil
		}
	}
}

func (s *Server) keepRedisSubscribed(ctx context.Context, channels []string) {
	if len(channels) == 0 {
		return
	}
	tellSubscribed := func() {
		cfg := redisConfig(s.cfg)
		conn, reader, err := dialRedis(ctx, cfg)
		if err != nil {
			return
		}
		defer conn.Close()
		if err := redisHandshake(conn, reader, cfg); err != nil {
			return
		}
		for _, channel := range channels {
			_ = writeRedisCommand(conn, "SET", cfg.prefix+"subscribed:"+channel, "1", "EX", strconv.Itoa(18*60))
			_, _ = readRedisValue(reader)
		}
	}

	tellSubscribed()
	ticker := time.NewTicker(6 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tellSubscribed()
		}
	}
}

func dialRedis(ctx context.Context, cfg redisConnConfig) (net.Conn, *bufio.Reader, error) {
	if strings.TrimSpace(cfg.sentinelMaster) != "" && len(cfg.sentinelAddrs) > 0 {
		address, err := resolveRedisSentinelMaster(ctx, cfg)
		if err != nil {
			return nil, nil, err
		}
		cfg.network = "tcp"
		cfg.address = address
	}
	return dialRedisDirect(ctx, cfg)
}

func dialRedisDirect(ctx context.Context, cfg redisConnConfig) (net.Conn, *bufio.Reader, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, cfg.network, cfg.address)
	if err != nil {
		return nil, nil, err
	}
	if cfg.tls {
		tlsConn := tls.Client(conn, &tls.Config{MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, nil, err
		}
		conn = tlsConn
	}
	return conn, bufio.NewReader(conn), nil
}

func resolveRedisSentinelMaster(ctx context.Context, cfg redisConnConfig) (string, error) {
	var errs []error
	for _, address := range cfg.sentinelAddrs {
		sentinelCfg := redisConnConfig{
			network:  "tcp",
			address:  address,
			username: cfg.sentinelUsername,
			password: cfg.sentinelPassword,
		}
		conn, reader, err := dialRedisDirect(ctx, sentinelCfg)
		if err != nil {
			errs = append(errs, fmt.Errorf("sentinel %s: %w", address, err))
			continue
		}
		if err := redisHandshake(conn, reader, sentinelCfg); err != nil {
			_ = conn.Close()
			errs = append(errs, fmt.Errorf("sentinel %s handshake: %w", address, err))
			continue
		}
		if err := writeRedisCommand(conn, "SENTINEL", "get-master-addr-by-name", cfg.sentinelMaster); err != nil {
			_ = conn.Close()
			errs = append(errs, fmt.Errorf("sentinel %s query: %w", address, err))
			continue
		}
		value, err := readRedisValue(reader)
		_ = conn.Close()
		if err != nil {
			errs = append(errs, fmt.Errorf("sentinel %s response: %w", address, err))
			continue
		}
		items, ok := value.([]any)
		if !ok || len(items) != 2 {
			errs = append(errs, fmt.Errorf("sentinel %s returned no primary for %q", address, cfg.sentinelMaster))
			continue
		}
		host, hostOK := items[0].(string)
		port, portOK := items[1].(string)
		if !hostOK || !portOK || strings.TrimSpace(host) == "" {
			errs = append(errs, fmt.Errorf("sentinel %s returned an invalid primary for %q", address, cfg.sentinelMaster))
			continue
		}
		if _, err := strconv.Atoi(port); err != nil {
			errs = append(errs, fmt.Errorf("sentinel %s returned invalid primary port %q", address, port))
			continue
		}
		return net.JoinHostPort(host, port), nil
	}
	return "", fmt.Errorf("resolve Redis primary %q: %w", cfg.sentinelMaster, errors.Join(errs...))
}

var redisDial = dialRedis

func redisHandshake(conn net.Conn, reader *bufio.Reader, cfg redisConnConfig) error {
	if cfg.password != "" {
		if cfg.username != "" {
			if err := writeRedisCommand(conn, "AUTH", cfg.username, cfg.password); err != nil {
				return err
			}
		} else {
			if err := writeRedisCommand(conn, "AUTH", cfg.password); err != nil {
				return err
			}
		}
		if _, err := readRedisValue(reader); err != nil {
			return err
		}
	}
	if cfg.db != 0 {
		if err := writeRedisCommand(conn, "SELECT", strconv.Itoa(cfg.db)); err != nil {
			return err
		}
		if _, err := readRedisValue(reader); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) redisCommand(ctx context.Context, args ...string) (any, error) {
	cfg := redisConfig(s.cfg)
	return redisCommandWithConfig(ctx, cfg, args...)
}

func (s *Server) cacheRedisCommand(ctx context.Context, args ...string) (any, error) {
	cfg := cacheRedisConfig(s.cfg)
	return redisCommandWithConfig(ctx, cfg, args...)
}

func redisCommandWithConfig(ctx context.Context, cfg redisConnConfig, args ...string) (any, error) {
	command := ""
	if len(args) > 0 {
		command = args[0]
	}
	finishTelemetry := func(error) {}
	if telemetry.Enabled() {
		ctx, finishTelemetry = telemetry.StartRedis(ctx, command)
	}
	conn, reader, err := redisDial(ctx, cfg)
	if err != nil {
		finishTelemetry(err)
		return nil, err
	}
	defer conn.Close()
	if err := redisHandshake(conn, reader, cfg); err != nil {
		finishTelemetry(err)
		return nil, err
	}
	if err := writeRedisCommand(conn, args...); err != nil {
		finishTelemetry(err)
		return nil, err
	}
	value, err := readRedisValue(reader)
	finishTelemetry(err)
	return value, err
}

func RedisAvailable(ctx context.Context, cfg config.Config) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for _, item := range redisAvailabilityConfigs(cfg) {
		value, err := redisCommandWithConfig(ctx, item.cfg, "PING")
		if err != nil {
			return fmt.Errorf("%s unavailable: %w", item.name, err)
		}
		if pong, ok := value.(string); !ok || !strings.EqualFold(pong, "PONG") {
			return fmt.Errorf("unexpected %s PING response %v", item.name, value)
		}
	}
	return nil
}

func writeRedisCommand(w io.Writer, args ...string) error {
	if _, err := fmt.Fprintf(w, "*%d\r\n", len(args)); err != nil {
		return err
	}
	for _, arg := range args {
		if _, err := fmt.Fprintf(w, "$%d\r\n%s\r\n", len(arg), arg); err != nil {
			return err
		}
	}
	return nil
}

func readRedisValue(reader *bufio.Reader) (any, error) {
	prefix, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	switch prefix {
	case '+':
		return line, nil
	case '-':
		return nil, errors.New(line)
	case ':':
		return strconv.ParseInt(line, 10, 64)
	case '$':
		length, err := strconv.Atoi(line)
		if err != nil {
			return nil, err
		}
		if length == -1 {
			return nil, nil
		}
		if length < -1 {
			return nil, fmt.Errorf("invalid redis bulk length %d", length)
		}
		buf := make([]byte, length+2)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return nil, err
		}
		return string(buf[:length]), nil
	case '*':
		count, err := strconv.Atoi(line)
		if err != nil {
			return nil, err
		}
		if count == -1 {
			return nil, nil
		}
		if count < -1 {
			return nil, fmt.Errorf("invalid redis array length %d", count)
		}
		items := make([]any, 0, count)
		for i := 0; i < count; i++ {
			item, err := readRedisValue(reader)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, nil
	default:
		return nil, fmt.Errorf("unsupported redis response prefix %q", prefix)
	}
}

func redisPubSubMessageFrame(value any) bool {
	items, ok := value.([]any)
	if !ok || len(items) != 3 {
		return false
	}
	kind, _ := items[0].(string)
	return kind == "message"
}

func redisPubSubMessage(value any) (redisMessage, bool) {
	items, ok := value.([]any)
	if !ok || len(items) != 3 {
		return redisMessage{}, false
	}
	kind, _ := items[0].(string)
	if kind != "message" {
		return redisMessage{}, false
	}
	raw, ok := items[2].(string)
	if !ok {
		return redisMessage{}, false
	}
	var message redisMessage
	if err := json.Unmarshal([]byte(raw), &message); err != nil || message.Event == "" {
		return redisMessage{}, false
	}
	message.Channel, _ = items[1].(string)
	if len(message.Payload) == 0 || string(message.Payload) == "null" {
		message.Payload = []byte("{}")
	}
	return message, true
}
