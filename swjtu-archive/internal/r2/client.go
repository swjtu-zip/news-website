// Package r2 implements the small subset of the S3 API needed by the
// archive.  Keeping the client dependency-free makes it suitable for the
// scratch-based deployment image and avoids putting credentials in a CLI
// configuration file.
package r2

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	service       = "s3"
	region        = "auto"
	requestType   = "aws4_request"
	dateFormat    = "20060102"
	amzTimeFormat = "20060102T150405Z"
	emptySHA256   = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// Config contains the credentials and bucket information for an R2 S3
// endpoint. Endpoint should normally be the account endpoint without the
// bucket path; a bucket path is accepted too for convenience.
type Config struct {
	Endpoint        string
	Bucket          string
	Prefix          string
	AccessKeyID     string
	SecretAccessKey string
}

// Client uploads and probes content-addressed archive resources in R2.
type Client struct {
	endpoint        *url.URL
	bucket          string
	prefix          string
	accessKeyID     string
	secretAccessKey string
	httpClient      *http.Client
}

// New validates an R2 configuration and creates a client.
func New(cfg Config) (*Client, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" || strings.TrimSpace(cfg.Bucket) == "" ||
		strings.TrimSpace(cfg.AccessKeyID) == "" || strings.TrimSpace(cfg.SecretAccessKey) == "" {
		return nil, fmt.Errorf("R2 endpoint, bucket and credentials are required")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return nil, fmt.Errorf("invalid R2 endpoint %q", cfg.Endpoint)
	}
	bucket := strings.Trim(cfg.Bucket, "/")
	if bucket == "" || strings.Contains(bucket, "/") {
		return nil, fmt.Errorf("invalid R2 bucket %q", cfg.Bucket)
	}
	// The endpoint supplied by the R2 dashboard is sometimes copied with the
	// bucket path appended. Store only the account endpoint internally so the
	// bucket is added exactly once below.
	basePath := strings.TrimRight(parsed.Path, "/")
	if basePath == "/"+bucket {
		parsed.Path = ""
		parsed.RawPath = ""
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	prefix := strings.Trim(strings.TrimSpace(cfg.Prefix), "/")
	if prefix == "" {
		return nil, fmt.Errorf("R2 object prefix is required")
	}
	return &Client{
		endpoint:        parsed,
		bucket:          bucket,
		prefix:          prefix,
		accessKeyID:     strings.TrimSpace(cfg.AccessKeyID),
		secretAccessKey: strings.TrimSpace(cfg.SecretAccessKey),
		httpClient:      &http.Client{Timeout: 10 * time.Minute},
	}, nil
}

// ObjectKey maps a local archive path such as assets/ab/hash.pdf to the R2
// prefix while refusing paths outside the resource tree.
func ObjectKey(prefix, localPath string) (string, bool) {
	localPath = path.Clean(strings.ReplaceAll(strings.TrimSpace(localPath), "\\", "/"))
	if localPath == "." || !strings.HasPrefix(localPath, "assets/") {
		return "", false
	}
	relative := strings.TrimPrefix(localPath, "assets/")
	if relative == "" || relative == "." || strings.HasPrefix(relative, "../") || relative == ".." {
		return "", false
	}
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		return "", false
	}
	return prefix + "/" + relative, true
}

// PublicURL maps a local archive path to a public custom-domain URL. baseURL
// should include the public prefix, for example
// https://oss.swjtu.zip/news-assets.
func PublicURL(baseURL, localPath string) string {
	relative, ok := ObjectKey("assets", localPath)
	if !ok {
		return ""
	}
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ""
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + strings.TrimPrefix(relative, "assets/")
	parsed.RawPath = ""
	return parsed.String()
}

// Upload uploads one local resource. Objects are content-addressed and can be
// safely overwritten with the same bytes when a migration is resumed.
// storageClass accepts STANDARD or STANDARD_IA; an empty value uses STANDARD.
func (c *Client) Upload(ctx context.Context, fullPath, localPath, contentType, filename, kind, storageClass string) error {
	file, err := os.Open(fullPath)
	if err != nil {
		return fmt.Errorf("open resource: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read resource: %w", err)
	}
	return c.UploadBytes(ctx, data, localPath, contentType, filename, kind, storageClass)
}

// UploadBytes uploads an in-memory resource under the content-addressed
// object key derived from localPath.
func (c *Client) UploadBytes(ctx context.Context, data []byte, localPath, contentType, filename, kind, storageClass string) error {
	key, ok := ObjectKey(c.prefix, localPath)
	if !ok {
		return fmt.Errorf("invalid resource path %q", localPath)
	}
	hash := sha256.Sum256(data)
	contentType = normalizeContentType(contentType, filename)
	resp, err := c.do(ctx, http.MethodPut, key, io.NopCloser(bytes.NewReader(data)), int64(len(data)), hex.EncodeToString(hash[:]), contentType, filename, kind, storageClass)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return responseError(resp, "upload", key)
	}
	return nil
}

// ExistingStorageClass reports the storage class of an object for a local
// resource. It returns an empty string when the object does not exist.
func (c *Client) ExistingStorageClass(ctx context.Context, localPath string) (string, error) {
	key, ok := ObjectKey(c.prefix, localPath)
	if !ok {
		return "", fmt.Errorf("invalid resource path %q", localPath)
	}
	return c.ObjectStorageClass(ctx, key)
}

// ObjectStorageClass reports the storage class of an exact R2 object key. It
// returns an empty string when the object does not exist.
func (c *Client) ObjectStorageClass(ctx context.Context, key string) (string, error) {
	key = strings.TrimSpace(strings.ReplaceAll(key, "\\", "/"))
	if key == "" || path.Clean(key) != key || strings.HasPrefix(key, "../") || key == ".." {
		return "", fmt.Errorf("invalid object key %q", key)
	}
	resp, err := c.do(ctx, http.MethodHead, key, nil, 0, emptySHA256, "", "", "", "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", responseError(resp, "check", key)
	}
	storageClass := strings.TrimSpace(resp.Header.Get("x-amz-storage-class"))
	if storageClass == "" {
		storageClass = "STANDARD"
	}
	return strings.ToUpper(storageClass), nil
}

// ChangeStorageClass changes the storage class for a local archive path using
// R2's server-side CopyObject operation. It avoids transferring the object
// through the crawler host.
func (c *Client) ChangeStorageClass(ctx context.Context, localPath, storageClass string) error {
	key, ok := ObjectKey(c.prefix, localPath)
	if !ok {
		return fmt.Errorf("invalid resource path %q", localPath)
	}
	return c.ChangeObjectStorageClass(ctx, key, storageClass)
}

// ChangeObjectStorageClass changes the storage class for an exact R2 object
// key using the server-side CopyObject operation.
func (c *Client) ChangeObjectStorageClass(ctx context.Context, key, storageClass string) error {
	key = strings.TrimSpace(strings.ReplaceAll(key, "\\", "/"))
	if key == "" || path.Clean(key) != key || strings.HasPrefix(key, "../") || key == ".." {
		return fmt.Errorf("invalid object key %q", key)
	}
	storageClass = normalizeStorageClass(storageClass)
	if storageClass == "" {
		return fmt.Errorf("invalid R2 storage class %q", storageClass)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.objectURL(key), nil)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	req.Header.Set("x-amz-content-sha256", emptySHA256)
	req.Header.Set("x-amz-date", now.Format(amzTimeFormat))
	req.Header.Set("x-amz-copy-source", "/"+c.bucket+"/"+key)
	req.Header.Set("x-amz-metadata-directive", "COPY")
	req.Header.Set("x-amz-storage-class", storageClass)
	if err := c.sign(req, now, emptySHA256); err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("R2 change storage class %s %s: %w", storageClass, key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return responseError(resp, "change storage class", key)
	}
	return nil
}

// ObjectInfo contains the key and storage class returned by R2's object list.
type ObjectInfo struct {
	Key          string
	StorageClass string
}

// ListObjects returns objects below a prefix, including their storage class,
// using S3's paginated ListObjectsV2 operation.
func (c *Client) ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || strings.Contains(path.Clean(prefix), "..") {
		return nil, fmt.Errorf("invalid object prefix %q", prefix)
	}
	var objects []ObjectInfo
	var continuation string
	for {
		target := *c.endpoint
		target.Path = strings.TrimRight(target.Path, "/") + "/" + c.bucket
		query := url.Values{}
		query.Set("list-type", "2")
		query.Set("max-keys", "1000")
		query.Set("prefix", prefix)
		if continuation != "" {
			query.Set("continuation-token", continuation)
		}
		target.RawQuery = query.Encode()
		target.Fragment = ""
		now := time.Now().UTC()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-amz-content-sha256", emptySHA256)
		req.Header.Set("x-amz-date", now.Format(amzTimeFormat))
		if err := c.sign(req, now, emptySHA256); err != nil {
			return nil, err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("R2 list %s: %w", prefix, err)
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			err := responseError(resp, "list", prefix)
			resp.Body.Close()
			return nil, err
		}
		var result listObjectsV2Response
		err = xml.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode R2 list %s: %w", prefix, err)
		}
		for _, object := range result.Contents {
			if object.Key != "" {
				storageClass := strings.ToUpper(strings.TrimSpace(object.StorageClass))
				if storageClass == "" {
					storageClass = "STANDARD"
				}
				objects = append(objects, ObjectInfo{Key: object.Key, StorageClass: storageClass})
			}
		}
		if !result.IsTruncated || result.NextContinuationToken == "" {
			return objects, nil
		}
		continuation = result.NextContinuationToken
	}
}

// ListPrefix returns all object keys below a prefix using S3's paginated
// ListObjectsV2 operation.
func (c *Client) ListPrefix(ctx context.Context, prefix string) ([]string, error) {
	objects, err := c.ListObjects(ctx, prefix)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(objects))
	for _, object := range objects {
		keys = append(keys, object.Key)
	}
	return keys, nil
}

// DeleteObject removes one exact object key. Callers should list and verify a
// prefix before invoking this method for cleanup.
func (c *Client) DeleteObject(ctx context.Context, key string) error {
	key = strings.TrimSpace(strings.ReplaceAll(key, "\\", "/"))
	if key == "" || path.Clean(key) != key || strings.HasPrefix(key, "../") || key == ".." {
		return fmt.Errorf("invalid object key %q", key)
	}
	resp, err := c.do(ctx, http.MethodDelete, key, nil, 0, emptySHA256, "", "", "", "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return responseError(resp, "delete", key)
	}
	return nil
}

type listObjectsV2Response struct {
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
	Contents              []struct {
		Key          string `xml:"Key"`
		StorageClass string `xml:"StorageClass"`
	} `xml:"Contents"`
}

func (c *Client) objectURL(key string) string {
	parsed := *c.endpoint
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + c.bucket + "/" + key
	parsed.RawPath = ""
	return parsed.String()
}

func (c *Client) do(ctx context.Context, method, key string, body io.ReadCloser, size int64, payloadHash, contentType, filename, kind, storageClass string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.objectURL(key), body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
	}
	now := time.Now().UTC()
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("x-amz-date", now.Format(amzTimeFormat))
	if method == http.MethodPut {
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if kind == "attachment" && strings.TrimSpace(filename) != "" {
			req.Header.Set("Content-Disposition", contentDisposition(filename))
		}
		if normalized := normalizeStorageClass(storageClass); normalized != "" {
			req.Header.Set("x-amz-storage-class", normalized)
		}
	}
	if err := c.sign(req, now, payloadHash); err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("R2 request %s %s: %w", method, key, err)
	}
	return resp, nil
}

func (c *Client) sign(req *http.Request, now time.Time, payloadHash string) error {
	date := now.Format(dateFormat)
	credentialScope := date + "/" + region + "/" + service + "/" + requestType
	values := map[string]string{
		"host":                 req.URL.Host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           req.Header.Get("x-amz-date"),
	}
	for name, headerValues := range req.Header {
		lower := strings.ToLower(name)
		if lower != "content-type" && lower != "content-disposition" && !strings.HasPrefix(lower, "x-amz-") {
			continue
		}
		values[lower] = strings.Join(headerValues, ",")
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var canonicalHeaders strings.Builder
	for _, key := range keys {
		canonicalHeaders.WriteString(key)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(normalizeHeaderValue(values[key]))
		canonicalHeaders.WriteByte('\n')
	}
	signedHeaders := strings.Join(keys, ";")
	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL),
		canonicalQuery(req.URL.Query()),
		canonicalHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")
	requestHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := "AWS4-HMAC-SHA256\n" + now.Format(amzTimeFormat) + "\n" + credentialScope + "\n" + hex.EncodeToString(requestHash[:])
	dateKey := hmacSHA256([]byte("AWS4"+c.secretAccessKey), []byte(date))
	regionKey := hmacSHA256(dateKey, []byte(region))
	serviceKey := hmacSHA256(regionKey, []byte(service))
	signingKey := hmacSHA256(serviceKey, []byte(requestType))
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.accessKeyID+"/"+credentialScope+
		", SignedHeaders="+signedHeaders+", Signature="+signature)
	return nil
}

func canonicalURI(value *url.URL) string {
	escaped := value.EscapedPath()
	if escaped == "" {
		return "/"
	}
	return escaped
}

func canonicalQuery(values url.Values) string {
	if len(values) == 0 {
		return ""
	}
	type pair struct{ key, value string }
	pairs := make([]pair, 0)
	for key, items := range values {
		if len(items) == 0 {
			pairs = append(pairs, pair{key: awsEscape(key), value: ""})
			continue
		}
		for _, value := range items {
			pairs = append(pairs, pair{key: awsEscape(key), value: awsEscape(value)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].key == pairs[j].key {
			return pairs[i].value < pairs[j].value
		}
		return pairs[i].key < pairs[j].key
	})
	parts := make([]string, len(pairs))
	for i, item := range pairs {
		parts[i] = item.key + "=" + item.value
	}
	return strings.Join(parts, "&")
}

func awsEscape(value string) string {
	const hexDigits = "0123456789ABCDEF"
	var builder strings.Builder
	for i := 0; i < len(value); i++ {
		char := value[i]
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' || char == '~' {
			builder.WriteByte(char)
			continue
		}
		builder.WriteByte('%')
		builder.WriteByte(hexDigits[char>>4])
		builder.WriteByte(hexDigits[char&0x0f])
	}
	return builder.String()
}

func normalizeHeaderValue(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func normalizeContentType(value, filename string) string {
	guessed := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
	if parsed, _, err := mime.ParseMediaType(strings.TrimSpace(value)); err == nil && parsed != "" {
		if (parsed == "application/octet-stream" || parsed == "binary/octet-stream") && guessed != "" {
			return guessed
		}
		return parsed
	}
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	if guessed != "" {
		return guessed
	}
	return "application/octet-stream"
}

func normalizeStorageClass(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "", "STANDARD":
		if strings.TrimSpace(value) == "" {
			return "STANDARD"
		}
		return "STANDARD"
	case "STANDARD_IA", "INFREQUENTACCESS", "INFREQUENT_ACCESS":
		return "STANDARD_IA"
	default:
		return ""
	}
}

func contentDisposition(filename string) string {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return "attachment"
	}
	return `attachment; filename="download"; filename*=UTF-8''` + url.PathEscape(filename)
}

func hmacSHA256(key, value []byte) []byte {
	hash := hmac.New(sha256.New, key)
	_, _ = hash.Write(value)
	return hash.Sum(nil)
}

func responseError(resp *http.Response, operation, key string) error {
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	detailText := strings.TrimSpace(string(detail))
	if detailText == "" {
		return fmt.Errorf("R2 %s %s: HTTP %s", operation, key, resp.Status)
	}
	return fmt.Errorf("R2 %s %s: HTTP %s: %s", operation, key, resp.Status, detailText)
}
