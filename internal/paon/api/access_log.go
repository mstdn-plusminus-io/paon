package api

import (
	"errors"
	"log"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/telemetry"
)

type accessLogPrintf func(format string, args ...any)

func accessLogMiddleware(cfg config.Config) echo.MiddlewareFunc {
	return accessLogMiddlewareWithLogger(cfg, log.Printf)
}

func accessLogMiddlewareWithLogger(cfg config.Config, logf accessLogPrintf) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			startedAt := time.Now()
			if cfg.ShouldLog("info") {
				request := c.Request()
				requestID, _ := c.Get("request_id").(string)
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
				traceID, spanID := "", ""
				if request != nil {
					traceID, spanID = telemetry.TraceIDs(request.Context())
				}
				logf("level=INFO event=http_request_started request_id=%q trace_id=%q span_id=%q method=%q path=%q remote_ip=%q user_agent=%q",
					requestID, traceID, spanID, method, path, c.RealIP(), userAgent)
			}
			err := next(c)
			if !cfg.ShouldLog("info") {
				return err
			}

			response, status := echo.ResolveResponseStatus(c.Response(), err)
			var apiErr apiHTTPError
			if errors.As(err, &apiErr) {
				status = apiErr.status
			}
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
			traceID, spanID := "", ""
			if request != nil {
				traceID, spanID = telemetry.TraceIDs(request.Context())
			}
			logf("level=INFO event=http_access request_id=%q trace_id=%q span_id=%q method=%q path=%q status=%d duration_ms=%.2f bytes=%d remote_ip=%q user_agent=%q",
				requestID, traceID, spanID, method, path, status, time.Since(startedAt).Seconds()*1000, response.Size, c.RealIP(), userAgent)
			return err
		}
	}
}
