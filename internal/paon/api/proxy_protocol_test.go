package api

import (
	"net"
	"testing"
)

func TestProxyProtocolV1ConnReplacesRemoteAddrBeforeHTTPRead(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	errc := make(chan error, 1)
	var conn *proxyProtocolV1Conn
	go func() {
		var err error
		conn, err = newProxyProtocolV1Conn(server)
		errc <- err
	}()
	if _, err := client.Write([]byte("PROXY TCP4 203.0.113.11 198.51.100.20 42302 443\r\nGET / HTTP/1.1\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if got := conn.RemoteAddr().String(); got != "203.0.113.11:42302" {
		t.Fatalf("RemoteAddr = %q", got)
	}
	buf := make([]byte, 3)
	if n, err := conn.Read(buf); err != nil || string(buf[:n]) != "GET" {
		t.Fatalf("first request bytes = %q, %v", string(buf[:n]), err)
	}
}

func TestProxyProtocolV1RejectsInvalidHeader(t *testing.T) {
	for _, line := range []string{
		"GET / HTTP/1.1",
		"PROXY TCP4 203.0.113.10 198.51.100.20 nope 443",
		"PROXY UDP4 203.0.113.10 198.51.100.20 42300 443",
	} {
		if _, err := parseProxyProtocolV1RemoteAddr(line, nil); err == nil {
			t.Fatalf("expected %q to be rejected", line)
		}
	}
}
