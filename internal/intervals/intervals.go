// Package intervals provides an intervals.icu API client for listing and
// downloading activities as FIT files.
//
// Authentication: find your athlete ID in your intervals.icu profile URL
// (e.g. "i12345"), and generate an API key from Settings → API.
// Auth uses HTTP Basic: username "API_KEY", password is your API key.
package intervals

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Activity is a summary returned by the activities list endpoint.
type Activity struct {
	ID             string `json:"id"`
	StartDateLocal string `json:"start_date_local"`
	Type           string `json:"type"`
	Name           string `json:"name"`
}

// Client is an authenticated intervals.icu API client.
type Client struct {
	athleteID string
	apiKey    string
	http      *http.Client
	apiBase   string
}

// New creates a Client using the given athlete ID and API key.
func New(athleteID, apiKey string) *Client {
	return NewWithBase(athleteID, apiKey, "https://intervals.icu/api/v1")
}

// NewWithBase creates a Client with a configurable base URL (used in tests).
func NewWithBase(athleteID, apiKey, base string) *Client {
	return &Client{
		athleteID: athleteID,
		apiKey:    apiKey,
		http:      &http.Client{Timeout: 60 * time.Second},
		apiBase:   base,
	}
}

const maxRetries = 4

func (c *Client) get(ctx context.Context, u string) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.SetBasicAuth("API_KEY", c.apiKey)
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusTooManyRequests || attempt >= maxRetries {
			return resp, nil
		}
		_ = resp.Body.Close()

		wait := time.Duration(2<<uint(attempt)) * time.Second // 2s, 4s, 8s, 16s
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
				wait = time.Duration(secs) * time.Second
			}
		}
		slog.Warn("intervals.icu rate limited, backing off", "attempt", attempt+1, "wait", wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// ValidateCredentials verifies the athlete ID and API key are correct.
func (c *Client) ValidateCredentials(ctx context.Context) error {
	resp, err := c.get(ctx, c.apiBase+"/athlete/"+c.athleteID)
	if err != nil {
		return fmt.Errorf("validate: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("invalid athlete ID or API key")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

// ListActivities returns activities in the given date range (YYYY-MM-DD).
// Pass empty strings to use the server default (recent activities).
func (c *Client) ListActivities(ctx context.Context, oldest, newest string) ([]Activity, error) {
	u, _ := url.Parse(c.apiBase + "/athlete/" + c.athleteID + "/activities")
	q := u.Query()
	if oldest != "" {
		q.Set("oldest", oldest)
	}
	if newest != "" {
		q.Set("newest", newest)
	}
	u.RawQuery = q.Encode()

	resp, err := c.get(ctx, u.String())
	if err != nil {
		return nil, fmt.Errorf("list activities: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list activities: HTTP %d: %s", resp.StatusCode, body)
	}

	var activities []Activity
	if err := json.NewDecoder(resp.Body).Decode(&activities); err != nil {
		return nil, fmt.Errorf("decode activities: %w", err)
	}
	return activities, nil
}

// DownloadFIT downloads the FIT file for a given activity ID.
// The response is gzip-compressed; this method decompresses it automatically.
func (c *Client) DownloadFIT(ctx context.Context, activityID string) ([]byte, error) {
	url := fmt.Sprintf("%s/activity/%s/fit-file", c.apiBase, activityID)
	resp, err := c.get(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("download FIT %s: %w", activityID, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("download FIT %s: HTTP %d: %s", activityID, resp.StatusCode, body)
	}
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "application/json") {
		return nil, fmt.Errorf("activity %s has no FIT file", activityID)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read FIT %s: %w", activityID, err)
	}

	if strings.Contains(ct, "gzip") {
		gr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("decompress FIT %s: %w", activityID, err)
		}
		defer gr.Close() //nolint:errcheck
		return io.ReadAll(gr)
	}

	return data, nil
}
