// Package gdrive provides a Google Drive client for backing up archived FIT files.
// Credentials (OAuth client ID + secret) are stored encrypted in the DB via the
// settings page. The OAuth loopback flow runs on a random localhost port.
package gdrive

import (
	"context"
	"encoding/json"
	"fmt"
	"bytes"
	"io"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

const (
	archiveFolder = "fitbase-archive"
)

// FITFile is metadata for a .fit file stored on Drive.
type FITFile struct {
	ID   string
	Name string
	Path string // relative path within fitbase-archive/
}

// Client wraps the Google Drive v3 service.
type Client struct {
	svc *drive.Service
}

// New creates an authenticated Drive client from an existing oauth2 token.
func New(ctx context.Context, token *oauth2.Token, clientID, clientSecret, redirectURI string) (*Client, error) {
	conf := config(clientID, clientSecret, redirectURI)
	src := conf.TokenSource(ctx, token)
	svc, err := drive.NewService(ctx, option.WithTokenSource(src))
	if err != nil {
		return nil, fmt.Errorf("create drive service: %w", err)
	}
	return &Client{svc: svc}, nil
}

// AuthURL generates the OAuth consent URL.
func AuthURL(clientID, clientSecret, redirectURI, state string) string {
	return config(clientID, clientSecret, redirectURI).AuthCodeURL(state, oauth2.AccessTypeOffline)
}

// Exchange trades an authorization code for an oauth2 token.
func Exchange(ctx context.Context, code, clientID, clientSecret, redirectURI string) (*oauth2.Token, error) {
	return config(clientID, clientSecret, redirectURI).Exchange(ctx, code)
}

// TokenToJSON serializes an oauth2 token to a JSON string.
func TokenToJSON(t *oauth2.Token) (string, error) {
	b, err := json.Marshal(t)
	return string(b), err
}

// TokenFromJSON deserializes a JSON string into an oauth2 token.
func TokenFromJSON(s string) (*oauth2.Token, error) {
	var t oauth2.Token
	return &t, json.Unmarshal([]byte(s), &t)
}

// Upload stores data as {archiveFolder}/{year}/{month}/{workoutID}.fit on Drive.
// Safe to call repeatedly — if the file already exists it is skipped.
func (c *Client) Upload(ctx context.Context, workoutID, year, month string, data []byte) error {
	folderID, err := c.ensurePath(ctx, archiveFolder, year, month)
	if err != nil {
		return fmt.Errorf("ensure path: %w", err)
	}

	filename := workoutID + ".fit"
	existing, err := c.findFile(ctx, filename, folderID)
	if err != nil {
		return err
	}
	if existing != "" {
		return nil // already uploaded
	}

	f := &drive.File{
		Name:    filename,
		Parents: []string{folderID},
	}
	_, err = c.svc.Files.Create(f).
		Media(bytes.NewReader(data)).
		Context(ctx).
		Do()
	return err
}

// ListFITFiles returns all .fit files under the fitbase-archive folder.
func (c *Client) ListFITFiles(ctx context.Context) ([]FITFile, error) {
	rootID, err := c.findFolder(ctx, archiveFolder, "root")
	if err != nil || rootID == "" {
		return nil, err
	}
	return c.listRecursive(ctx, rootID, "")
}

// Download fetches a file's content by Drive file ID.
func (c *Client) Download(ctx context.Context, fileID string) ([]byte, error) {
	resp, err := c.svc.Files.Get(fileID).Context(ctx).Download()
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", fileID, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	return io.ReadAll(resp.Body)
}

func config(clientID, clientSecret, redirectURI string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURI,
		Scopes:       []string{drive.DriveFileScope},
		Endpoint:     google.Endpoint,
	}
}

func (c *Client) ensurePath(ctx context.Context, parts ...string) (string, error) {
	parentID := "root"
	for _, part := range parts {
		id, err := c.ensureFolder(ctx, part, parentID)
		if err != nil {
			return "", err
		}
		parentID = id
	}
	return parentID, nil
}

func (c *Client) ensureFolder(ctx context.Context, name, parentID string) (string, error) {
	id, err := c.findFolder(ctx, name, parentID)
	if err != nil {
		return "", err
	}
	if id != "" {
		return id, nil
	}
	f := &drive.File{
		Name:     name,
		MimeType: "application/vnd.google-apps.folder",
		Parents:  []string{parentID},
	}
	created, err := c.svc.Files.Create(f).Context(ctx).Fields("id").Do()
	if err != nil {
		return "", fmt.Errorf("create folder %q: %w", name, err)
	}
	return created.Id, nil
}

func (c *Client) findFolder(ctx context.Context, name, parentID string) (string, error) {
	q := fmt.Sprintf("name=%q and mimeType='application/vnd.google-apps.folder' and '%s' in parents and trashed=false",
		name, parentID)
	list, err := c.svc.Files.List().Q(q).Fields("files(id)").Context(ctx).Do()
	if err != nil {
		return "", err
	}
	if len(list.Files) == 0 {
		return "", nil
	}
	return list.Files[0].Id, nil
}

func (c *Client) findFile(ctx context.Context, name, parentID string) (string, error) {
	q := fmt.Sprintf("name=%q and '%s' in parents and trashed=false", name, parentID)
	list, err := c.svc.Files.List().Q(q).Fields("files(id)").Context(ctx).Do()
	if err != nil {
		return "", err
	}
	if len(list.Files) == 0 {
		return "", nil
	}
	return list.Files[0].Id, nil
}

func (c *Client) listRecursive(ctx context.Context, folderID, pathPrefix string) ([]FITFile, error) {
	q := fmt.Sprintf("'%s' in parents and trashed=false", folderID)
	list, err := c.svc.Files.List().Q(q).Fields("files(id,name,mimeType)").Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	var all []FITFile
	for _, f := range list.Files {
		if f.MimeType == "application/vnd.google-apps.folder" {
			sub, err := c.listRecursive(ctx, f.Id, pathPrefix+f.Name+"/")
			if err != nil {
				return nil, err
			}
			all = append(all, sub...)
		} else if strings.HasSuffix(strings.ToLower(f.Name), ".fit") {
			all = append(all, FITFile{ID: f.Id, Name: f.Name, Path: pathPrefix + f.Name})
		}
	}
	return all, nil
}
