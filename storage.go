package vaif

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// maxObjectBytes is the largest single-part object a presigned PUT accepts.
const maxObjectBytes = 5 << 30

// StorageClient uploads, downloads and signs URLs for the project's objects.
// Bucket is optional everywhere; without it the project's default bucket is
// used. Keys are relative paths such as "avatars/u1.png".
type StorageClient struct {
	client *Client
}

// ObjectOptions selects a bucket.
type ObjectOptions struct {
	Bucket string
}

// UploadOptions configures Upload.
type UploadOptions struct {
	Bucket      string
	ContentType string // default application/octet-stream
}

func checkKey(key string) error {
	if key == "" {
		return invalidArg("key must not be empty")
	}
	if strings.HasPrefix(key, "/") {
		return invalidArg("key must be relative")
	}
	for _, part := range strings.Split(key, "/") {
		if part == ".." {
			return invalidArg("key must not contain '..' segments")
		}
	}
	for _, r := range key {
		if r < 32 {
			return invalidArg("key must not contain control characters")
		}
	}
	return nil
}

func target(key, bucket string) (map[string]any, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	t := map[string]any{"key": key}
	if bucket != "" {
		t["bucket"] = bucket
	}
	return t, nil
}

type urlBody struct {
	URL string `json:"url"`
}

func (s *StorageClient) mint(ctx context.Context, path string, payload map[string]any, idempotent bool) (string, error) {
	resp, err := s.client.send(ctx, request{method: http.MethodPost, path: path, body: payload, idempotent: boolPtr(idempotent)}, false)
	if err != nil {
		return "", err
	}
	var body urlBody
	if err := decodeJSON(resp, &body); err != nil {
		return "", err
	}
	if body.URL == "" {
		return "", &Error{Kind: ErrBadResponse, Status: resp.status, Message: "response did not include a URL"}
	}
	if !secureStorageURL(body.URL) {
		// The URL carries a signature, so it stays out of the error.
		return "", &Error{Kind: ErrBadResponse, Status: resp.status, Code: "INSECURE_STORAGE_URL", Message: "API returned a storage URL that is not https"}
	}
	return body.URL, nil
}

// secureStorageURL reports whether raw, a signed URL the API issued, may carry
// an object's bytes: it must be absolute https, or http for the loopback hosts
// allowsCleartext names, as the base URL must.
func secureStorageURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "https" || (u.Scheme == "http" && allowsCleartext(u.Hostname()))
}

// CreateSignedURL returns a time-limited download URL (the API issues it for
// 10 minutes). The URL must be https, or http on a loopback host; any other
// URL is refused with code INSECURE_STORAGE_URL.
func (s *StorageClient) CreateSignedURL(ctx context.Context, key string, opts *ObjectOptions) (string, error) {
	payload, err := target(key, bucketOf(opts))
	if err != nil {
		return "", err
	}
	// Minting a download URL has no side effects, so it may be retried.
	return s.mint(ctx, "/storage/download-url", payload, true)
}

// CreateSignedUploadURL returns a time-limited URL that accepts an HTTP PUT
// of exactly sizeBytes bytes (1 byte to 5 GiB) and, when opts.ContentType is
// set, exactly that Content-Type: both are signed into the URL, so the object
// store refuses any other PUT. The API checks the size against the bucket's
// and project's limits and the storage quota, and meters it. The URL must be
// https, or http on a loopback host; any other URL is refused with code
// INSECURE_STORAGE_URL.
func (s *StorageClient) CreateSignedUploadURL(ctx context.Context, key string, sizeBytes int64, opts *UploadOptions) (string, error) {
	var bucket, contentType string
	if opts != nil {
		bucket, contentType = opts.Bucket, opts.ContentType
	}
	payload, err := target(key, bucket)
	if err != nil {
		return "", err
	}
	if sizeBytes < 1 || sizeBytes > maxObjectBytes {
		return "", invalidArg("sizeBytes must be between 1 and 5 GiB")
	}
	payload["sizeBytes"] = sizeBytes
	if contentType != "" {
		payload["contentType"] = contentType
	}
	// Metered on the server: never replayed after it may have been received.
	return s.mint(ctx, "/storage/upload-url", payload, false)
}

// Upload stores data at key, overwriting any existing object. It mints a
// presigned URL and PUTs the bytes straight to the object store; the API key
// and session token are never sent there. The PUT is retried on transient
// failures, the metered mint is not.
func (s *StorageClient) Upload(ctx context.Context, key string, data []byte, opts *UploadOptions) (*UploadResult, error) {
	var bucket, contentType string
	if opts != nil {
		bucket, contentType = opts.Bucket, opts.ContentType
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if len(data) == 0 {
		return nil, invalidArg("data must not be empty")
	}
	if len(data) > maxObjectBytes {
		return nil, invalidArg("Upload accepts at most 5 GiB per object")
	}
	signed, err := s.CreateSignedUploadURL(ctx, key, int64(len(data)), &UploadOptions{Bucket: bucket, ContentType: contentType})
	if err != nil {
		return nil, err
	}
	_, err = s.client.do(ctx, request{
		method:      http.MethodPut,
		path:        signed,
		absoluteURL: true,
		rawBody:     data,
		contentType: contentType,
		idempotent:  boolPtr(true),
	})
	if err != nil {
		if apiErr, ok := err.(*Error); ok && apiErr.Code == "" {
			apiErr.Code = "STORAGE_UPLOAD_FAILED"
		}
		return nil, err
	}
	return &UploadResult{Key: key, Bucket: bucket, Size: len(data)}, nil
}

// Download returns an object's bytes; errors.Is(err, ErrNotFound) if missing.
// It asks the API for a signed download URL, as CreateSignedURL does, and
// reads the object from that URL; the API key and session token are never
// sent there. Objects larger than 64 MiB are refused with ErrBadResponse.
func (s *StorageClient) Download(ctx context.Context, key string, opts *ObjectOptions) ([]byte, error) {
	signed, err := s.CreateSignedURL(ctx, key, opts)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.do(ctx, request{method: http.MethodGet, path: signed, absoluteURL: true})
	if err != nil {
		if apiErr, ok := err.(*Error); ok && apiErr.Code == "" {
			apiErr.Code = "STORAGE_DOWNLOAD_FAILED"
		}
		return nil, err
	}
	return resp.body, nil
}

func bucketOf(opts *ObjectOptions) string {
	if opts == nil {
		return ""
	}
	return opts.Bucket
}
