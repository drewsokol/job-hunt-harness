package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/buildinfo"
)

const (
	// DefaultMaxBytes caps a board response. The largest real feeds with
	// descriptions are a few MB.
	DefaultMaxBytes = 32 << 20
	requestTimeout  = 30 * time.Second
	maxRetryWait    = 10 * time.Second
)

// Client fetches JSON from job boards. Its zero value is not usable; call
// NewClient.
type Client struct {
	HTTP      *http.Client
	UserAgent string
	MaxBytes  int64
}

// NewClient returns a Client with jhh's defaults.
func NewClient() *Client {
	return &Client{
		HTTP:      &http.Client{Timeout: requestTimeout},
		UserAgent: "job-hunt-harness/" + buildinfo.Version() + " (+https://github.com/drewsokol/job-hunt-harness)",
		MaxBytes:  DefaultMaxBytes,
	}
}

// ErrTooLarge is returned when a response exceeds Client.MaxBytes.
var ErrTooLarge = errors.New("response too large")

// StatusError is an HTTP reply other than 200, 304 or 404.
type StatusError struct {
	URL  string
	Code int
}

func (e *StatusError) Error() string { return fmt.Sprintf("%s: HTTP %d", e.URL, e.Code) }

// GetJSON fetches url and decodes the JSON body into v. If etag is not
// empty it is sent as If-None-Match, and a 304 reply returns
// notModified=true without touching v. A 404 returns ErrBoardNotFound. A 429
// or 5xx reply is retried once, honoring a short Retry-After.
func (c *Client) GetJSON(ctx context.Context, url, etag string, v any) (notModified bool, newETag string, err error) {
	for attempt := 0; ; attempt++ {
		notModified, newETag, retryAfter, err := c.get(ctx, url, etag, v)
		if retryAfter < 0 || attempt == 1 {
			return notModified, newETag, err
		}
		select {
		case <-ctx.Done():
			return false, "", ctx.Err()
		case <-time.After(retryAfter):
		}
	}
}

// get makes one request. retryAfter is negative when the result is final.
func (c *Client) get(ctx context.Context, url, etag string, v any) (notModified bool, newETag string, retryAfter time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, "", -1, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "application/json")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false, "", -1, err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotModified:
		return true, etag, -1, nil
	case resp.StatusCode == http.StatusNotFound:
		return false, "", -1, ErrBoardNotFound
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return false, "", retryDelay(resp.Header.Get("Retry-After")), &StatusError{URL: url, Code: resp.StatusCode}
	case resp.StatusCode != http.StatusOK:
		return false, "", -1, &StatusError{URL: url, Code: resp.StatusCode}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, c.MaxBytes+1))
	if err != nil {
		return false, "", -1, fmt.Errorf("%s: reading body: %w", url, err)
	}
	if int64(len(body)) > c.MaxBytes {
		return false, "", -1, fmt.Errorf("%s: %w (over %d bytes)", url, ErrTooLarge, c.MaxBytes)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return false, "", -1, fmt.Errorf("%s: decoding JSON: %w", url, err)
	}
	return false, resp.Header.Get("ETag"), -1, nil
}

// retryDelay reads Retry-After in seconds, defaulting to 2 s and capping at
// maxRetryWait so one slow board can't stall a refresh.
func retryDelay(h string) time.Duration {
	d := 2 * time.Second
	if secs, err := strconv.Atoi(h); err == nil && secs >= 0 {
		d = time.Duration(secs) * time.Second
	}
	return min(d, maxRetryWait)
}
