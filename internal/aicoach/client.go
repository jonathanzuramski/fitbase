package aicoach

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// defaultClient is shared across providers so connection reuse works. The 3-minute
// timeout covers the entire streaming lifetime — LLM streams typically finish in
// 20–60s but reasoning models on long prompts occasionally take longer.
var defaultClient = &http.Client{Timeout: 3 * time.Minute}

// postWithRetry POSTs body (marshaled as JSON) to url, retrying on 429/5xx
// with exponential backoff (1s/4s/9s). On success (2xx) it returns the raw
// response; the caller owns resp.Body. On failure the response body's first
// 512 bytes are folded into the error.
func postWithRetry(ctx context.Context, url string, headers map[string]string, accept string, body any) (*http.Response, error) {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request body: %w", err)
	}

	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", accept)
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := defaultClient.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, err
			}
			if attempt < maxAttempts {
				time.Sleep(backoff(attempt))
				continue
			}
			return nil, fmt.Errorf("request failed after %d attempts: %w", maxAttempts, err)
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}

		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		lastErr = fmt.Errorf("status %d: %s", resp.StatusCode, string(snippet))
		if isRetryable(resp.StatusCode) && attempt < maxAttempts && ctx.Err() == nil {
			time.Sleep(backoff(attempt))
			continue
		}
		return nil, lastErr
	}
	return nil, lastErr
}

// PostStream POSTs body (marshaled as JSON) to url with the shared retry
// policy, and on success returns the response body for the caller to read as
// an SSE stream. Caller must Close the body.
func PostStream(ctx context.Context, url string, headers map[string]string, body any) (io.ReadCloser, error) {
	resp, err := postWithRetry(ctx, url, headers, "text/event-stream", body)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// PostJSON POSTs body to url and returns the full response body, with the same
// retry+backoff as PostStream. Used by the chat path: tool-calling rounds run
// buffered (no SSE) so tool_use parsing stays straightforward.
func PostJSON(ctx context.Context, url string, headers map[string]string, body any) ([]byte, error) {
	resp, err := postWithRetry(ctx, url, headers, "application/json", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	return respBody, nil
}

// GetJSON GETs url and returns the full response body. Unlike PostStream/PostJSON
// it does not retry: model-list calls are quick and user-initiated (a settings
// "refresh" click), so a failure should surface immediately rather than stalling
// the UI through backoff sleeps. The shared client's timeout still applies.
func GetJSON(ctx context.Context, url string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := defaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	respBody, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := respBody
		if len(snippet) > 512 {
			snippet = snippet[:512]
		}
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(snippet))
	}
	if readErr != nil {
		return nil, fmt.Errorf("read response body: %w", readErr)
	}
	return respBody, nil
}

// ScanSSE reads `data:` lines from an SSE stream and invokes onData for each
// non-empty payload. Stops on `[DONE]`. Event field names are ignored — every
// provider encodes event-type info inside the JSON payload.
func ScanSSE(body io.Reader, onData func([]byte) error) error {
	scanner := bufio.NewScanner(body)
	// SSE lines can carry large JSON blobs (tool-call manifests in particular).
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[5:])
		if len(data) == 0 {
			continue
		}
		if bytes.Equal(data, []byte("[DONE]")) {
			return nil
		}
		if err := onData(data); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func isRetryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// backoff returns 1s, 4s, 9s for attempts 1, 2, 3.
func backoff(attempt int) time.Duration {
	return time.Duration(attempt*attempt) * time.Second
}
