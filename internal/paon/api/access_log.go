package api

import (
	"log"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

type accessLogPrintf func(format string, args ...any)

func accessLogMiddleware(cfg config.Config) echo.MiddlewareFunc {
	return accessLogMiddlewareWithLogger(cfg, log.Printf)
}

func accessLogMiddlewareWithLogger(cfg config.Config, logf accessLogPrintf) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			startedAt := time.Now()
			err := next(c)
			if !cfg.ShouldLog("info") {
				return err
			}

			response, status := echo.ResolveResponseStatus(c.Response(), err)
			request := c.Request()
			requestID, _ := c.Get("request_id").(string)
			if requestID == "" {
				requestID = response.Header().Get(echo.HeaderXRequestID)
			}
			method := ""
			path := ""
			userAgent := ""
			if request != nil {
				method = request.Method
				userAgent = request.UserAgent()
				if request.URL != nil {
					path = request.URL.Path
				}
			}
			logf("level=INFO event=http_access request_id=%q method=%q path=%q status=%d duration_ms=%.2f bytes=%d remote_ip=%q user_agent=%q",
				requestID, method, path, status, time.Since(startedAt).Seconds()*1000, response.Size, c.RealIP(), userAgent)
			return err
		}
	}
}
