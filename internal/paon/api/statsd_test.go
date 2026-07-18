package api

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestStatsDMiddlewarePreservesExplicitBlankRailsNamespace(t *testing.T) {
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer packetConn.Close()

	received := make(chan string, 2)
	go func() {
		buf := make([]byte, 512)
		for i := 0; i < 2; i++ {
			_ = packetConn.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, _, err := packetConn.ReadFrom(buf)
			if err != nil {
				return
			}
			received <- string(buf[:n])
		}
	}()

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/instance", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	handler := statsDMiddleware(config.Config{StatsDAddr: packetConn.LocalAddr().String(), StatsDNamespace: ""})(func(c *echo.Context) error {
		return c.String(http.StatusAccepted, "ok")
	})
	if err := handler(c); err != nil {
		t.Fatal(err)
	}

	var got []string
	for len(got) < 2 {
		select {
		case metric := <-received:
			got = append(got, metric)
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for StatsD metrics, got %#v", got)
		}
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{
		"web.request:",
		"web.status.202:1|c",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("StatsD metrics missing %q in %#v", want, got)
		}
	}
	if strings.Contains(joined, "Mastodon.") {
		t.Fatalf("blank Rails StatsD namespace should not be defaulted: %#v", got)
	}
}

func TestStatsDMiddlewareNoopsWithoutConfiguredAddr(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	called := false
	handler := statsDMiddleware(config.Config{})(func(c *echo.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})
	if err := handler(c); err != nil {
		t.Fatal(err)
	}
	if !called || rec.Code != http.StatusOK {
		t.Fatalf("middleware did not pass through: called=%v code=%d", called, rec.Code)
	}
}

func TestStartBackgroundWorkersStartsStatsDInformantWorker(t *testing.T) {
	src, err := os.ReadFile("activitypub_retry.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "StartBackgroundWorkers", "workers.Go(ctx, s.runStatsDInformantWorker)") {
		t.Fatal("StartBackgroundWorkers does not start StatsD informant worker")
	}
}
