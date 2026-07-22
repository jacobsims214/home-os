// Package tika provides a client for the Apache Tika Server REST API.
//
// Tika runs as a sidecar container (apache/tika:latest-full) on port 9998 and
// performs OCR and text extraction from uploaded files (PDFs, Office docs,
// images). The worker calls ExtractText to pull plain text out of a file's
// raw bytes; the result is then stored on the files row via the file module's
// UpdateOCRStatus method.
//
// The Tika "Tika" endpoint (PUT /tika) accepts the raw file bytes as the
// request body, the file's MIME type in the Content-Type header, and returns
// the extracted text as text/plain. A 200 OK with an empty body is a valid
// response — it means Tika could not extract any text (e.g. a binary file or
// an image with no detectable text), and ExtractText returns ("", nil) in
// that case so the caller can mark OCR as skipped rather than failed.
package tika

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the address of the Tika sidecar inside the Docker
// Compose / Kubernetes network. The "tika" service name resolves via Docker
// DNS; in tests callers override it with httptest.NewServer.URL.
const DefaultBaseURL = "http://tika:9998"

// DefaultTimeout caps how long a single extraction may take. Tika OCR over
// large PDFs can take tens of seconds; 60s is generous without letting a
// wedged Tika block the worker indefinitely.
const DefaultTimeout = 60 * time.Second

// Client is the configurable Tika client used by worker actors.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// Option configures a Client at construction time.
type Option func(*Client)

// WithBaseURL overrides the default http://tika:9998 base URL. Use this in
// tests to point at an httptest.Server.
func WithBaseURL(baseURL string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(baseURL, "/") }
}

// WithHTTPClient overrides the underlying *http.Client. Use this in tests to
// inject a stub transport or to tune timeouts per call.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.httpClient = h }
}

// NewClient returns a Tika client ready to call ExtractText. The default
// base URL is DefaultBaseURL and the default HTTP client uses a 60s timeout
// (DefaultTimeout); both are overridable via options.
func NewClient(opts ...Option) *Client {
	c := &Client{
		baseURL: DefaultBaseURL,
		httpClient: &http.Client{
			Timeout: DefaultTimeout,
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// ExtractText sends the raw file bytes to Tika's PUT /tika endpoint with the
// supplied MIME type in the Content-Type header and returns the extracted
// plain text.
//
// Callers should pass the file's detected MIME type (e.g. "application/pdf",
// "image/png", "application/vnd.openxmlformats-officedocument.wordprocessingml
// .document"). Tika uses the Content-Type header to select the right parser.
//
// Behavior:
//   - On a connection error (Tika not running, DNS failure, refused
//     connection) it returns a wrapped error whose message identifies the
//     underlying cause. It never panics.
//   - On context cancellation or timeout it returns the context error.
//   - On a non-2xx HTTP response it returns an error including the status.
//   - On a 2xx response with an empty body it returns ("", nil). This is the
//     "binary file with no extractable text" case — the caller should mark
//     OCR as skipped, not failed.
//   - On a 2xx response with a body it returns the trimmed text. Tika
//     sometimes pads the response with leading/trailing whitespace.
func (c *Client) ExtractText(ctx context.Context, fileBytes []byte, contentType string) (string, error) {
	if len(fileBytes) == 0 {
		return "", nil
	}

	endpoint := c.baseURL + "/tika"

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(fileBytes))
	if err != nil {
		return "", fmt.Errorf("tika: build request: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// Request plain text output instead of Tika's default XHTML.
	req.Header.Set("Accept", "text/plain")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Distinguish context cancellation/deadline from a real transport
		// failure (e.g. connection refused). http.Client.Do wraps context
		// errors verbatim, so ctx.Err() is the authoritative signal.
		if ctx.Err() != nil {
			return "", fmt.Errorf("tika: request cancelled: %w", ctx.Err())
		}
		// Tika not running / DNS failure / connection refused. Surface a
		// friendly message but preserve the original error for debugging.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return "", fmt.Errorf("tika: cannot reach Tika server at %s: %w", endpoint, urlErr.Err)
		}
		return "", fmt.Errorf("tika: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("tika: read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("tika: unexpected status %d from %s: %s",
			resp.StatusCode, endpoint, strings.TrimSpace(string(body)))
	}

	text := strings.TrimSpace(string(body))
	slog.Debug("tika: extracted text",
		"content_type", contentType,
		"bytes_in", len(fileBytes),
		"bytes_out", len(text),
		"status", resp.StatusCode,
	)
	return text, nil
}
