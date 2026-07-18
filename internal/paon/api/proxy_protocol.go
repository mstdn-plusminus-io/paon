package api

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

type proxyProtocolV1Listener struct {
	net.Listener
}

func (l proxyProtocolV1Listener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		wrapped, err := newProxyProtocolV1Conn(conn)
		if err == nil {
			return wrapped, nil
		}
		_ = conn.Close()
	}
}

type proxyProtocolV1Conn struct {
	net.Conn
	reader     *bufio.Reader
	remoteAddr net.Addr
}

func newProxyProtocolV1Conn(conn net.Conn) (*proxyProtocolV1Conn, error) {
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		return nil, err
	}
	remoteAddr, err := parseProxyProtocolV1RemoteAddr(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), conn.RemoteAddr())
	if err != nil {
		return nil, err
	}
	return &proxyProtocolV1Conn{Conn: conn, reader: reader, remoteAddr: remoteAddr}, nil
}

func (c *proxyProtocolV1Conn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func (c *proxyProtocolV1Conn) RemoteAddr() net.Addr {
	if c.remoteAddr != nil {
		return c.remoteAddr
	}
	return c.Conn.RemoteAddr()
}

func parseProxyProtocolV1RemoteAddr(line string, fallback net.Addr) (net.Addr, error) {
	if !strings.HasPrefix(line, "PROXY ") {
		return nil, errors.New("missing PROXY protocol v1 header")
	}
	parts := strings.Fields(line)
	if len(parts) == 2 && parts[1] == "UNKNOWN" {
		return fallback, nil
	}
	if len(parts) != 6 {
		return nil, fmt.Errorf("invalid PROXY protocol v1 header field count %d", len(parts))
	}
	switch parts[1] {
	case "TCP4", "TCP6":
	default:
		return nil, fmt.Errorf("unsupported PROXY protocol v1 family %q", parts[1])
	}
	ip := net.ParseIP(parts[2])
	if ip == nil {
		return nil, fmt.Errorf("invalid PROXY protocol v1 source address %q", parts[2])
	}
	port, err := strconv.Atoi(parts[4])
	if err != nil || port < 0 || port > 65535 {
		return nil, fmt.Errorf("invalid PROXY protocol v1 source port %q", parts[4])
	}
	return &net.TCPAddr{IP: ip, Port: port}, nil
}
