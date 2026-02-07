package vaif

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

// Storage provides file storage operations including upload, download, and
// bucket management. All operations require a context.Context.
type Storage struct {
	client *Client
}

// From returns a StorageBucket for the given bucket name.
//
//	bucket := client.Storage.From("avatars")
//	result, err := bucket.Upload(ctx, "photo.jpg", reader, nil)
func (s *Storage) From(bucket string) *StorageBucket {
	return &StorageBucket{client: s.client, bucket: bucket}
}

// ListBuckets returns all storage buckets.
func (s *Storage) ListBuckets(ctx context.Context) ([]Bucket, error) {
	raw, err := s.client.request(ctx, "GET", "/storage/buckets", nil, nil)
	if err != nil {
		return nil, err
	}

	resp := parseResponse[[]Bucket](raw)
	if resp.Error != nil {
		return nil, &StorageError{Message: resp.Error.Message, StatusCode: strconv.Itoa(resp.Status), Err: resp.Error.Code, sentinel: resp.Error.sentinel}
	}

	return resp.Data, nil
}

// CreateBucket creates a new storage bucket.
func (s *Storage) CreateBucket(ctx context.Context, name string, opts *CreateBucketOptions) (*Bucket, error) {
	body := map[string]any{"name": name, "public": false}
	if opts != nil {
		body["public"] = opts.Public
		if opts.FileSizeLimit > 0 {
			body["fileSizeLimit"] = opts.FileSizeLimit
		}
		if len(opts.AllowedMimeTypes) > 0 {
			body["allowedMimeTypes"] = opts.AllowedMimeTypes
		}
	}

	raw, err := s.client.request(ctx, "POST", "/storage/buckets", body, nil)
	if err != nil {
		return nil, err
	}

	resp := parseResponse[Bucket](raw)
	if resp.Error != nil {
		return nil, &StorageError{Message: resp.Error.Message, StatusCode: strconv.Itoa(resp.Status), Err: resp.Error.Code, sentinel: resp.Error.sentinel}
	}

	return &resp.Data, nil
}

// CreateBucketOptions configures bucket creation.
type CreateBucketOptions struct {
	Public           bool
	FileSizeLimit    int64
	AllowedMimeTypes []string
}

// DeleteBucket deletes a storage bucket.
func (s *Storage) DeleteBucket(ctx context.Context, name string) error {
	raw, err := s.client.request(ctx, "DELETE", "/storage/buckets/"+name, nil, nil)
	if err != nil {
		return err
	}

	resp := parseResponse[any](raw)
	if resp.Error != nil {
		return &StorageError{Message: resp.Error.Message, StatusCode: strconv.Itoa(resp.Status), Err: resp.Error.Code, sentinel: resp.Error.sentinel}
	}

	return nil
}

// StorageBucket provides operations on a specific bucket.
type StorageBucket struct {
	client *Client
	bucket string
}

// Upload uploads a file to the bucket. The path is the destination path within
// the bucket. If opts.ContentType is empty, it is detected from the file extension.
func (sb *StorageBucket) Upload(ctx context.Context, path string, data io.Reader, opts *UploadOptions) (*UploadResult, error) {
	if opts == nil {
		opts = &UploadOptions{}
	}

	config := sb.client.GetConfig()

	// Build multipart form
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	filename := filepath.Base(path)
	contentType := opts.ContentType
	if contentType == "" {
		contentType = detectContentType(path)
	}

	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}

	if _, err := io.Copy(part, data); err != nil {
		return nil, fmt.Errorf("copy file data: %w", err)
	}

	if contentType != "" {
		writer.WriteField("contentType", contentType)
	}
	if opts.CacheControl != "" {
		writer.WriteField("cacheControl", opts.CacheControl)
	}
	if opts.Upsert {
		writer.WriteField("upsert", "true")
	}
	if opts.Metadata != nil {
		metaBytes, _ := json.Marshal(opts.Metadata)
		writer.WriteField("metadata", string(metaBytes))
	}

	writer.Close()

	url := fmt.Sprintf("%s/storage/%s/%s", config.APIURL, sb.bucket, path)
	req, err := http.NewRequestWithContext(ctx, "POST", url, &body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-Project-ID", config.ProjectID)
	req.Header.Set("apikey", config.APIKey)

	if token := sb.client.GetAccessToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := sb.client.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upload request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read upload response: %w", err)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var result UploadResult
		if err := json.Unmarshal(respBody, &result); err != nil {
			return nil, fmt.Errorf("parse upload result: %w", err)
		}
		return &result, nil
	}

	var errBody map[string]any
	json.Unmarshal(respBody, &errBody)
	msg, _ := errBody["message"].(string)
	if msg == "" {
		msg = fmt.Sprintf("Upload failed: %d", resp.StatusCode)
	}
	return nil, &StorageError{Message: msg, StatusCode: strconv.Itoa(resp.StatusCode), Err: "UPLOAD_ERROR", sentinel: sentinelForStatus(resp.StatusCode)}
}

// UploadWithProgress uploads a file with a progress callback. The progressFn
// is called periodically with the number of bytes transferred and total size.
func (sb *StorageBucket) UploadWithProgress(ctx context.Context, path string, data io.Reader, size int64, progressFn ProgressFunc, opts *UploadOptions) (*UploadResult, error) {
	pr := &progressReader{
		reader:     data,
		total:      size,
		progressFn: progressFn,
	}
	return sb.Upload(ctx, path, pr, opts)
}

// UploadFile uploads a local file to the bucket. Content type is detected from
// the file extension.
func (sb *StorageBucket) UploadFile(ctx context.Context, remotePath, localPath string, opts *UploadOptions) (*UploadResult, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	if opts == nil {
		opts = &UploadOptions{}
	}
	if opts.ContentType == "" {
		opts.ContentType = detectContentType(localPath)
	}

	return sb.Upload(ctx, remotePath, f, opts)
}

// Download downloads a file from the bucket as bytes. Use transform to apply
// image transformations (resize, format conversion, etc.).
func (sb *StorageBucket) Download(ctx context.Context, path string, transform *ImageTransformOptions) ([]byte, error) {
	config := sb.client.GetConfig()
	url := fmt.Sprintf("%s/storage/%s/%s", config.APIURL, sb.bucket, path)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	if transform != nil {
		q := req.URL.Query()
		if transform.Width > 0 {
			q.Set("width", strconv.Itoa(transform.Width))
		}
		if transform.Height > 0 {
			q.Set("height", strconv.Itoa(transform.Height))
		}
		if transform.Resize != "" {
			q.Set("resize", transform.Resize)
		}
		if transform.Format != "" {
			q.Set("format", transform.Format)
		}
		if transform.Quality > 0 {
			q.Set("quality", strconv.Itoa(transform.Quality))
		}
		req.URL.RawQuery = q.Encode()
	}

	req.Header.Set("apikey", config.APIKey)
	if token := sb.client.GetAccessToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := sb.client.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return io.ReadAll(resp.Body)
	}

	return nil, &StorageError{
		Message:    fmt.Sprintf("Download failed: %d", resp.StatusCode),
		StatusCode: strconv.Itoa(resp.StatusCode),
		Err:        "DOWNLOAD_ERROR",
		sentinel:   sentinelForStatus(resp.StatusCode),
	}
}

// DownloadToFile downloads a file from the bucket and writes it to a local file.
func (sb *StorageBucket) DownloadToFile(ctx context.Context, remotePath, localPath string, transform *ImageTransformOptions) error {
	data, err := sb.Download(ctx, remotePath, transform)
	if err != nil {
		return err
	}

	dir := filepath.Dir(localPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	if err := os.WriteFile(localPath, data, 0o644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	return nil
}

// GetPublicURL returns the public URL for a file. The file must be in a public bucket.
func (sb *StorageBucket) GetPublicURL(path string) string {
	config := sb.client.GetConfig()
	return fmt.Sprintf("%s/storage/%s/%s", config.APIURL, sb.bucket, path)
}

// CreateSignedURL creates a signed URL for temporary file access.
// ExpiresIn is in seconds.
func (sb *StorageBucket) CreateSignedURL(ctx context.Context, path string, opts SignedURLOptions) (string, error) {
	body := map[string]any{"expiresIn": opts.ExpiresIn}
	if opts.Transform != nil {
		body["transform"] = opts.Transform
	}
	if opts.Download != nil {
		body["download"] = opts.Download
	}

	raw, err := sb.client.request(ctx, "POST", fmt.Sprintf("/storage/%s/%s/sign", sb.bucket, path), body, nil)
	if err != nil {
		return "", err
	}

	resp := parseResponse[map[string]any](raw)
	if resp.Error != nil {
		return "", &StorageError{Message: resp.Error.Message, StatusCode: strconv.Itoa(resp.Status), Err: resp.Error.Code, sentinel: resp.Error.sentinel}
	}

	signedURL, _ := resp.Data["signedUrl"].(string)
	return signedURL, nil
}

// List lists files in the bucket under the given path prefix.
func (sb *StorageBucket) List(ctx context.Context, path string, opts *ListFilesOptions) ([]FileObject, error) {
	body := map[string]any{"prefix": path}
	if opts != nil {
		if opts.Limit > 0 {
			body["limit"] = opts.Limit
		}
		if opts.Offset > 0 {
			body["offset"] = opts.Offset
		}
		if opts.Search != "" {
			body["search"] = opts.Search
		}
		if opts.SortBy != nil {
			body["sortBy"] = opts.SortBy
		}
	}

	raw, err := sb.client.request(ctx, "POST", fmt.Sprintf("/storage/%s/list", sb.bucket), body, nil)
	if err != nil {
		return nil, err
	}

	resp := parseResponse[[]FileObject](raw)
	if resp.Error != nil {
		return nil, &StorageError{Message: resp.Error.Message, StatusCode: strconv.Itoa(resp.Status), Err: resp.Error.Code, sentinel: resp.Error.sentinel}
	}

	return resp.Data, nil
}

// Move moves/renames a file within the bucket.
func (sb *StorageBucket) Move(ctx context.Context, fromPath, toPath string) error {
	body := map[string]any{"from": fromPath, "to": toPath}

	raw, err := sb.client.request(ctx, "POST", fmt.Sprintf("/storage/%s/move", sb.bucket), body, nil)
	if err != nil {
		return err
	}

	resp := parseResponse[any](raw)
	if resp.Error != nil {
		return &StorageError{Message: resp.Error.Message, StatusCode: strconv.Itoa(resp.Status), Err: resp.Error.Code, sentinel: resp.Error.sentinel}
	}

	return nil
}

// Copy copies a file within the bucket.
func (sb *StorageBucket) Copy(ctx context.Context, fromPath, toPath string) error {
	body := map[string]any{"from": fromPath, "to": toPath}

	raw, err := sb.client.request(ctx, "POST", fmt.Sprintf("/storage/%s/copy", sb.bucket), body, nil)
	if err != nil {
		return err
	}

	resp := parseResponse[any](raw)
	if resp.Error != nil {
		return &StorageError{Message: resp.Error.Message, StatusCode: strconv.Itoa(resp.Status), Err: resp.Error.Code, sentinel: resp.Error.sentinel}
	}

	return nil
}

// Remove deletes one or more files from the bucket.
func (sb *StorageBucket) Remove(ctx context.Context, paths []string) error {
	body := map[string]any{"paths": paths}

	raw, err := sb.client.request(ctx, "DELETE", fmt.Sprintf("/storage/%s/delete", sb.bucket), body, nil)
	if err != nil {
		return err
	}

	resp := parseResponse[any](raw)
	if resp.Error != nil {
		return &StorageError{Message: resp.Error.Message, StatusCode: strconv.Itoa(resp.Status), Err: resp.Error.Code, sentinel: resp.Error.sentinel}
	}

	return nil
}

// detectContentType returns the MIME type based on file extension.
func detectContentType(path string) string {
	ext := filepath.Ext(path)
	if ext == "" {
		return "application/octet-stream"
	}
	ct := mime.TypeByExtension(ext)
	if ct == "" {
		return "application/octet-stream"
	}
	return ct
}

// progressReader wraps an io.Reader to report progress.
type progressReader struct {
	reader      io.Reader
	total       int64
	transferred int64
	progressFn  ProgressFunc
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	pr.transferred += int64(n)
	if pr.progressFn != nil && n > 0 {
		pr.progressFn(pr.transferred, pr.total)
	}
	return n, err
}
