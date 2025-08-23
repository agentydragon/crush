package logging

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// NewHTTPClient creates an HTTP client with debug logging via slog RoundTripper.
func NewHTTPClient() *http.Client {
	return &http.Client{Transport: &HTTPRoundTripLogger{Transport: http.DefaultTransport}}
}

type HTTPRoundTripLogger struct{ Transport http.RoundTripper }

func (h *HTTPRoundTripLogger) RoundTrip(req *http.Request) (*http.Response, error) {
	save, body, err := drainBody(req.Body)
	if err != nil {
		slog.Error("http.req.read_fail", "method", req.Method, "url", req.URL, "error", err)
		return nil, err
	}
	req.Body = body
	slog.Info("http.req", "method", req.Method, "url", req.URL, "body", bodyToString(save))

	start := time.Now()
	resp, err := h.Transport.RoundTrip(req)
	dur := time.Since(start)
	if err != nil {
		slog.Error("http.resp.err", "method", req.Method, "url", req.URL, "duration_ms", dur.Milliseconds(), "error", err)
		return resp, err
	}

	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "text/event-stream") {
		slog.Info("http.resp.stream", "status_code", resp.StatusCode, "status", resp.Status, "headers", formatHeaders(resp.Header), "duration_ms", dur.Milliseconds())
		return resp, nil
	}

	save, body, err = drainBody(resp.Body)
	resp.Body = body
	slog.Info("http.resp", "status_code", resp.StatusCode, "status", resp.Status, "headers", formatHeaders(resp.Header), "body", bodyToString(save), "content_length", resp.ContentLength, "duration_ms", dur.Milliseconds(), "error", err)
	return resp, err
}

func bodyToString(body io.ReadCloser) string {
	if body == nil {
		return ""
	}
	src, err := io.ReadAll(body)
	if err != nil {
		slog.Error("http.body.read_fail", "error", err)
		return ""
	}
	var b bytes.Buffer
	if json.Compact(&b, bytes.TrimSpace(src)) != nil {
		return string(src)
	}
	return b.String()
}

func formatHeaders(headers http.Header) map[string][]string {
	filtered := make(map[string][]string)
	for key, values := range headers {
		lowerKey := strings.ToLower(key)
		if strings.Contains(lowerKey, "authorization") || strings.Contains(lowerKey, "api-key") || strings.Contains(lowerKey, "token") || strings.Contains(lowerKey, "secret") {
			filtered[key] = []string{"[REDACTED]"}
		} else {
			filtered[key] = values
		}
	}
	return filtered
}

func drainBody(b io.ReadCloser) (r1, r2 io.ReadCloser, err error) {
	if b == nil || b == http.NoBody {
		return http.NoBody, http.NoBody, nil
	}
	var buf bytes.Buffer
	if _, err = buf.ReadFrom(b); err != nil { return nil, b, err }
	if err = b.Close(); err != nil { return nil, b, err }
	return io.NopCloser(&buf), io.NopCloser(bytes.NewReader(buf.Bytes())), nil
}
