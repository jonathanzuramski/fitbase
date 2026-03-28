// Package dropbox provides a Dropbox API client for listing and downloading
// FIT files synced by Wahoo ELEMNT devices.
//
// Authentication: create a Dropbox app at https://www.dropbox.com/developers/apps,
// set "Token expiration" to "No expiration", then click "Generate access token"
// and paste the result into fitbase settings. No OAuth flow needed.
package dropbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// FileMetadata represents a Dropbox file entry.
type FileMetadata struct {
	Name           string    `json:"name"`
	PathLower      string    `json:"path_lower"`
	ServerModified time.Time `json:"server_modified"`
}

// Client is an authenticated Dropbox API client.
type Client struct {
	token       string
	http        *http.Client
	apiBase     string
	contentBase string
	notifyBase  string
}

// New creates a Client using the given access token.
func New(accessToken string) *Client {
	return NewWithConfig(accessToken,
		"https://api.dropboxapi.com/2",
		"https://content.dropboxapi.com/2",
		"https://notify.dropboxapi.com/2",
	)
}

// NewWithConfig creates a Client with configurable base URLs (used in tests).
func NewWithConfig(token, apiBase, contentBase, notifyBase string) *Client {
	return &Client{
		token:       token,
		http:        &http.Client{Timeout: 60 * time.Second},
		apiBase:     apiBase,
		contentBase: contentBase,
		notifyBase:  notifyBase,
	}
}

// ValidateToken verifies the access token by calling the current account endpoint.
func (c *Client) ValidateToken(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.apiBase+"/users/get_current_account",
		strings.NewReader("null"))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("validate token: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("invalid token (HTTP %d): %s", resp.StatusCode, body)
	}
	return nil
}

// ListFITFiles returns all .fit files under folderPath (recursive).
// Use an empty string or "/" for the root Dropbox folder.
func (c *Client) ListFITFiles(ctx context.Context, folderPath string) ([]FileMetadata, error) {
	// Dropbox API uses "" for root, not "/"
	path := strings.TrimRight(folderPath, "/")
	if path == "/" {
		path = ""
	}

	type listReq struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive"`
	}
	type contReq struct {
		Cursor string `json:"cursor"`
	}
	type entry struct {
		Tag            string    `json:".tag"`
		Name           string    `json:"name"`
		PathLower      string    `json:"path_lower"`
		ServerModified time.Time `json:"server_modified"`
	}
	type listResp struct {
		Entries []entry `json:"entries"`
		Cursor  string  `json:"cursor"`
		HasMore bool    `json:"has_more"`
	}

	var all []FileMetadata
	cursor := ""

	for {
		var reqBody []byte
		var url string
		if cursor == "" {
			reqBody, _ = json.Marshal(listReq{Path: path, Recursive: true})
			url = c.apiBase + "/files/list_folder"
		} else {
			reqBody, _ = json.Marshal(contReq{Cursor: cursor})
			url = c.apiBase + "/files/list_folder/continue"
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url,
			bytes.NewReader(reqBody))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list folder: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return nil, fmt.Errorf("list folder: HTTP %d: %s", resp.StatusCode, body)
		}

		var result listResp
		err = json.NewDecoder(resp.Body).Decode(&result)
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode list response: %w", err)
		}

		for _, e := range result.Entries {
			if e.Tag == "file" && strings.HasSuffix(strings.ToLower(e.Name), ".fit") {
				all = append(all, FileMetadata{
					Name:           e.Name,
					PathLower:      e.PathLower,
					ServerModified: e.ServerModified,
				})
			}
		}

		if !result.HasMore {
			break
		}
		cursor = result.Cursor
	}

	return all, nil
}

// Download fetches the raw bytes of a file by its Dropbox path.
func (c *Client) Download(ctx context.Context, path string) ([]byte, error) {
	arg, _ := json.Marshal(map[string]string{"path": path})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.contentBase+"/files/download", http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Dropbox-API-Arg", string(arg))

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("download %s: HTTP %d: %s", path, resp.StatusCode, body)
	}
	return io.ReadAll(resp.Body)
}

// GetLatestCursor returns a cursor representing the current state of folderPath
// without listing any files. Use this to initialize the longpoll after a manual sync.
func (c *Client) GetLatestCursor(ctx context.Context, folderPath string) (string, error) {
	path := strings.TrimRight(folderPath, "/")
	if path == "/" {
		path = ""
	}
	body, _ := json.Marshal(map[string]any{"path": path, "recursive": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.apiBase+"/files/list_folder/get_latest_cursor", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("get_latest_cursor: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("get_latest_cursor: HTTP %d: %s", resp.StatusCode, b)
	}
	var result struct {
		Cursor string `json:"cursor"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode get_latest_cursor: %w", err)
	}
	return result.Cursor, nil
}

// Longpoll holds open a connection to Dropbox until files change in the folder
// associated with cursor, or until timeoutSecs elapses (max 480).
// Returns (changes, backoffSecs, error). If backoffSecs > 0 the caller should
// wait that long before calling Longpoll again.
func (c *Client) Longpoll(ctx context.Context, cursor string, timeoutSecs int) (bool, int, error) {
	body, _ := json.Marshal(map[string]any{"cursor": cursor, "timeout": timeoutSecs})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.notifyBase+"/files/list_folder/longpoll", bytes.NewReader(body))
	if err != nil {
		return false, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return false, 0, fmt.Errorf("longpoll: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return false, 0, fmt.Errorf("longpoll: HTTP %d: %s", resp.StatusCode, b)
	}
	var result struct {
		Changes bool `json:"changes"`
		Backoff int  `json:"backoff"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, 0, fmt.Errorf("decode longpoll: %w", err)
	}
	return result.Changes, result.Backoff, nil
}

// ListFolderContinue fetches the next page of changes after a longpoll notification.
// Returns the new files, the updated cursor, whether there are more pages, and any error.
func (c *Client) ListFolderContinue(ctx context.Context, cursor string) ([]FileMetadata, string, bool, error) {
	body, _ := json.Marshal(map[string]string{"cursor": cursor})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.apiBase+"/files/list_folder/continue", bytes.NewReader(body))
	if err != nil {
		return nil, "", false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", false, fmt.Errorf("list_folder/continue: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, "", false, fmt.Errorf("list_folder/continue: HTTP %d: %s", resp.StatusCode, body)
	}
	type entry struct {
		Tag            string    `json:".tag"`
		Name           string    `json:"name"`
		PathLower      string    `json:"path_lower"`
		ServerModified time.Time `json:"server_modified"`
	}
	var result struct {
		Entries []entry `json:"entries"`
		Cursor  string  `json:"cursor"`
		HasMore bool    `json:"has_more"`
	}
	err = json.NewDecoder(resp.Body).Decode(&result)
	_ = resp.Body.Close()
	if err != nil {
		return nil, "", false, fmt.Errorf("decode list_folder/continue: %w", err)
	}
	var files []FileMetadata
	for _, e := range result.Entries {
		if e.Tag == "file" && strings.HasSuffix(strings.ToLower(e.Name), ".fit") {
			files = append(files, FileMetadata{
				Name:           e.Name,
				PathLower:      e.PathLower,
				ServerModified: e.ServerModified,
			})
		}
	}
	return files, result.Cursor, result.HasMore, nil
}
