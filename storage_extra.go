package vaif

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
)

// ListObjectsOptions pages through a bucket (Bucket is a name or UUID).
type ListObjectsOptions struct {
	Bucket, Prefix, Cursor string
	Limit                  int
}

// StorageObject is one listed object.
type StorageObject struct {
	Key          string `json:"key"`
	Size         int64  `json:"size"`
	LastModified string `json:"lastModified"`
	ETag         string `json:"etag"`
}

// ListObjectsResult is one page of a listing; pass NextCursor to continue.
type ListObjectsResult struct {
	Files      []StorageObject
	Prefixes   []string
	NextCursor string
	HasMore    bool
}

var bucketUUID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (s *StorageClient) bucketID(ctx context.Context, name string) (string, error) {
	if bucketUUID.MatchString(name) {
		return name, nil
	}
	resp, err := s.client.send(ctx, request{method: http.MethodGet, path: "/storage/buckets", query: url.Values{"projectId": {s.client.projectID}}}, false)
	if err != nil {
		return "", err
	}
	var out struct {
		Buckets []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Default bool   `json:"isDefault"`
		} `json:"buckets"`
	}
	if err = decodeJSON(resp, &out); err != nil {
		return "", err
	}
	for _, b := range out.Buckets {
		if b.Name == name || (name == "" && (b.Default || b.Name == "default")) {
			return b.ID, nil
		}
	}
	return "", &Error{Kind: ErrNotFound, Code: "BUCKET_NOT_FOUND", Message: "bucket not found; pass its name or UUID"}
}

// List pages through a bucket's objects. Dashboard routes require platform
// authorization; pass the appropriate session when listing private buckets.
func (s *StorageClient) List(ctx context.Context, opts *ListObjectsOptions) (*ListObjectsResult, error) {
	o := ListObjectsOptions{}
	if opts != nil {
		o = *opts
	}
	if o.Limit < 0 || o.Limit > 1000 {
		return nil, invalidArg("limit must be 1..1000, or zero for default")
	}
	id, err := s.bucketID(ctx, o.Bucket)
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	if o.Prefix != "" {
		query.Set("prefix", o.Prefix)
	}
	if o.Cursor != "" {
		query.Set("continuationToken", o.Cursor)
	}
	if o.Limit > 0 {
		query.Set("maxKeys", strconv.Itoa(o.Limit))
	}
	resp, err := s.client.send(ctx, request{method: http.MethodGet, path: "/buckets/" + url.PathEscape(id) + "/files", query: query}, false)
	if err != nil {
		return nil, err
	}
	var out struct {
		Files       []StorageObject `json:"files"`
		Folders     []string        `json:"folders"`
		NextToken   string          `json:"nextToken"`
		IsTruncated bool            `json:"isTruncated"`
	}
	if err = decodeJSON(resp, &out); err != nil {
		return nil, err
	}
	return &ListObjectsResult{Files: out.Files, Prefixes: out.Folders, NextCursor: out.NextToken, HasMore: out.IsTruncated}, nil
}

// Delete removes one object. Like List, it needs platform authorization.
func (s *StorageClient) Delete(ctx context.Context, key string, opts *ObjectOptions) error {
	if err := checkKey(key); err != nil {
		return err
	}
	id, err := s.bucketID(ctx, bucketOf(opts))
	if err != nil {
		return err
	}
	_, err = s.client.send(ctx, request{method: http.MethodDelete, path: "/buckets/" + url.PathEscape(id) + "/files", query: url.Values{"key": {key}}}, false)
	return err
}
