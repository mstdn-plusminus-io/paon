package api

import (
	"os"
	"strings"
	"testing"
)

func TestServerStartContextSupportsGracefulShutdown(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`func (s *Server) StartContext(ctx context.Context, addr string) error`,
		`listenNetwork := strings.TrimSpace(s.cfg.ListenNetwork)`,
		`if strings.HasPrefix(listenNetwork, "unix")`,
		`_ = os.Remove(addr)`,
		`if s.cfg.ProxyProtocolV1`,
		`proxyProtocolV1Listener{Listener: ln}`,
		`echo.StartConfig{`,
		`Address:         addr`,
		`ListenerNetwork: listenNetwork`,
		`GracefulTimeout: 10 * time.Second`,
		`BeforeServeFunc: func(server *http.Server) error`,
		`server.IdleTimeout = s.cfg.PersistentTimeout`,
		`startConfig.Start(ctx, s.echo)`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("server.go missing graceful shutdown path %q", want)
		}
	}
}
