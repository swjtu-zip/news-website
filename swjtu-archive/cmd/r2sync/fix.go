package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"swjtu-archive/internal/archive"
	"swjtu-archive/internal/r2"
)

// fixResourceNames repairs two legacy naming problems:
//
// Phase 1: objects stored under a generic .bin extension (the source page and
// server gave no usable type hint) are content-sniffed and renamed to their
// real extension. The R2 object is copied server-side, the local file (when
// present) is renamed, and every resources row pointing at the old path is
// updated.
//
// Phase 2: attachment rows whose stored filename carries no extension (the
// source page labelled the link with bare text such as "附件1") get the
// extension borrowed from the original URL or, failing that, from the object
// path. The R2 object's Content-Disposition metadata is rewritten to match.
func fixResourceNames(ctx context.Context, client *r2.Client, dataDir, prefix string, workers int) error {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dataDir, "archive.db"))+"?_pragma=busy_timeout(30000)")
	if err != nil {
		return fmt.Errorf("open archive metadata: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	objects, err := retryObjects(ctx, func() ([]r2.ObjectInfo, error) {
		return client.ListObjects(ctx, strings.Trim(prefix, "/")+"/")
	})
	if err != nil {
		return fmt.Errorf("list objects below %s: %w", prefix, err)
	}
	classes := make(map[string]string, len(objects))
	for _, object := range objects {
		classes[object.Key] = object.StorageClass
	}

	renames, err := fixBinExtensions(ctx, client, db, dataDir, prefix, classes)
	if err != nil {
		return err
	}
	return fixAttachmentFilenames(ctx, client, db, prefix, classes, renames)
}

func fixBinExtensions(ctx context.Context, client *r2.Client, db *sql.DB, dataDir, prefix string, classes map[string]string) (map[string]string, error) {
	rows, err := db.Query(`SELECT DISTINCT local_path FROM resources
WHERE status='success' AND local_path LIKE 'assets/%.bin'`)
	if err != nil {
		return nil, fmt.Errorf("read .bin resources: %w", err)
	}
	var paths []string
	for rows.Next() {
		var localPath string
		if err := rows.Scan(&localPath); err != nil {
			log.Printf("scan .bin resource: %v", err)
			continue
		}
		paths = append(paths, filepath.ToSlash(path.Clean(localPath)))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read .bin resources: %w", err)
	}
	log.Printf("phase 1: %d distinct .bin paths", len(paths))

	renames := make(map[string]string, len(paths))
	var renamed, kept, failed int
	for i, localPath := range paths {
		newPath, err := fixOneBinPath(ctx, client, db, dataDir, prefix, classes, localPath)
		switch {
		case err != nil:
			failed++
			log.Printf("fix .bin %s: %v", localPath, err)
		case newPath == "":
			kept++
		default:
			renamed++
			renames[localPath] = newPath
		}
		if (i+1)%100 == 0 || i+1 == len(paths) {
			log.Printf("phase 1 progress=%d/%d renamed=%d kept=%d failed=%d", i+1, len(paths), renamed, kept, failed)
		}
	}
	log.Printf("phase 1 complete renamed=%d kept=%d failed=%d", renamed, kept, failed)
	if failed > 0 {
		return renames, fmt.Errorf("phase 1 had %d failures", failed)
	}
	return renames, ctx.Err()
}

// fixOneBinPath sniffs one .bin resource and, when a real extension is
// recognized, renames the object, the local file, and the database rows. It
// returns the new path, or "" when the content stays .bin.
func fixOneBinPath(ctx context.Context, client *r2.Client, db *sql.DB, dataDir, prefix string, classes map[string]string, localPath string) (string, error) {
	oldKey, ok := r2.ObjectKey(prefix, localPath)
	if !ok {
		return "", fmt.Errorf("invalid resource path %q", localPath)
	}
	fullPath := filepath.Join(dataDir, filepath.FromSlash(localPath))
	sniff, localErr := sniffLocalFile(fullPath)
	_, inR2 := classes[oldKey]
	if localErr != nil {
		if !inR2 {
			return "", nil
		}
		var data []byte
		data, err := retryBytes(ctx, func() ([]byte, error) {
			return client.GetObjectBytes(ctx, oldKey, archive.SniffSize)
		})
		if err != nil {
			return "", err
		}
		sniff = data
	}
	ext := archive.SniffExtension(sniff)
	if ext == "" || ext == ".bin" {
		return "", nil
	}
	newPath := strings.TrimSuffix(localPath, ".bin") + ext
	newKey, ok := r2.ObjectKey(prefix, newPath)
	if !ok {
		return "", fmt.Errorf("invalid resource path %q", newPath)
	}
	kind, filename := resourceKindAndName(db, localPath)
	contentType := mime.TypeByExtension(ext)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if _, exists := classes[newKey]; exists {
		// Same content hash means the target object is identical; only the
		// wrongly-named copy has to go.
		if inR2 {
			if err := retryErr(ctx, func() error { return client.DeleteObject(ctx, oldKey) }); err != nil {
				return "", err
			}
		}
	} else if inR2 {
		downloadName := r2.DownloadFilename(filename, newPath)
		if err := retryErr(ctx, func() error {
			return client.CopyObject(ctx, oldKey, newKey, contentType, downloadName, kind, classes[oldKey])
		}); err != nil {
			return "", err
		}
		if err := retryErr(ctx, func() error { return client.DeleteObject(ctx, oldKey) }); err != nil {
			return "", err
		}
		classes[newKey] = classes[oldKey]
		delete(classes, oldKey)
	}
	if localErr == nil {
		if err := os.Rename(fullPath, filepath.Join(dataDir, filepath.FromSlash(newPath))); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("rename local file: %w", err)
		}
	}
	if _, err := db.Exec(`UPDATE resources SET local_path=?, content_type=? WHERE local_path=?`, newPath, contentType, localPath); err != nil {
		return "", fmt.Errorf("update resources: %w", err)
	}
	return newPath, nil
}

func sniffLocalFile(fullPath string) ([]byte, error) {
	file, err := os.Open(fullPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data := make([]byte, archive.SniffSize)
	n, err := io.ReadFull(file, data)
	if err != nil && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	return data[:n], nil
}

// resourceKindAndName reports the most useful kind (attachment wins) and
// filename (one carrying an extension wins) recorded for a local path.
func resourceKindAndName(db *sql.DB, localPath string) (string, string) {
	rows, err := db.Query(`SELECT kind, filename FROM resources WHERE local_path=?`, localPath)
	if err != nil {
		return "", ""
	}
	defer rows.Close()
	kind := ""
	filename := ""
	for rows.Next() {
		var k, f string
		if err := rows.Scan(&k, &f); err != nil {
			continue
		}
		if kind != "attachment" {
			kind = k
		}
		if filename == "" || (filepath.Ext(filename) == "" && filepath.Ext(f) != "") {
			filename = f
		}
	}
	return kind, filename
}

func fixAttachmentFilenames(ctx context.Context, client *r2.Client, db *sql.DB, prefix string, classes map[string]string, renames map[string]string) error {
	type row struct {
		localPath   string
		filename    string
		originalURL string
		contentType string
	}
	rows, err := db.Query(`SELECT local_path, filename, original_url, content_type FROM resources
WHERE status='success' AND kind='attachment' AND local_path LIKE 'assets/%'`)
	if err != nil {
		return fmt.Errorf("read attachment resources: %w", err)
	}
	byPath := make(map[string][]row)
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.localPath, &r.filename, &r.originalURL, &r.contentType); err != nil {
			log.Printf("scan attachment resource: %v", err)
			continue
		}
		r.localPath = filepath.ToSlash(path.Clean(r.localPath))
		if renamed, ok := renames[r.localPath]; ok {
			r.localPath = renamed
		}
		byPath[r.localPath] = append(byPath[r.localPath], r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read attachment resources: %w", err)
	}

	var renamed, unchanged, missing, failed int
	total := len(byPath)
	i := 0
	for localPath, group := range byPath {
		i++
		filename, originalURL, contentType := "", "", ""
		for _, r := range group {
			if filename == "" || (filepath.Ext(filename) == "" && filepath.Ext(r.filename) != "") {
				filename = r.filename
			}
			// Prefer a URL that actually yields an extension; several rows
			// can share one file, and UUID-style links carry no hint.
			if originalURL == "" || (urlExtension(originalURL) == "" && urlExtension(r.originalURL) != "") {
				originalURL = r.originalURL
			}
			if contentType == "" {
				contentType = r.contentType
			}
		}
		desired := desiredAttachmentName(filename, originalURL, localPath)
		desiredType := fixContentType(contentType, localPath)
		// Rows sharing one file can disagree with each other (one article's
		// link text carried an extension, another's did not). The group only
		// counts as unchanged when every row already matches the target.
		needsUpdate := false
		for _, r := range group {
			if r.filename != desired || r.contentType != desiredType {
				needsUpdate = true
				break
			}
		}
		if !needsUpdate {
			unchanged++
			continue
		}
		key, ok := r2.ObjectKey(prefix, localPath)
		if !ok {
			failed++
			continue
		}
		class, inR2 := classes[key]
		if !inR2 {
			missing++
		} else if err := retryErr(ctx, func() error {
			return client.UpdateObjectMetadata(ctx, key, desiredType, desired, class)
		}); err != nil {
			failed++
			log.Printf("fix filename %s: %v", key, err)
			continue
		}
		if _, err := db.Exec(`UPDATE resources SET filename=?, content_type=? WHERE local_path=?`, desired, desiredType, localPath); err != nil {
			failed++
			log.Printf("update resources %s: %v", localPath, err)
			continue
		}
		renamed++
		if renamed%200 == 0 {
			log.Printf("phase 2 progress=%d/%d renamed=%d unchanged=%d missing=%d failed=%d", i, total, renamed, unchanged, missing, failed)
		}
	}
	log.Printf("phase 2 complete paths=%d renamed=%d unchanged=%d missing_object=%d failed=%d", total, renamed, unchanged, missing, failed)
	if failed > 0 {
		return fmt.Errorf("phase 2 had %d failures", failed)
	}
	return ctx.Err()
}

// desiredAttachmentName completes a stored attachment filename with the best
// available extension: the original link URL first (it reflects the source
// site's real file name), then the content-addressed path.
func desiredAttachmentName(filename, originalURL, localPath string) string {
	if filepath.Ext(filename) != "" {
		return filename
	}
	if ext := urlExtension(originalURL); ext != "" {
		return filename + ext
	}
	return r2.DownloadFilename(filename, localPath)
}

// urlExtension extracts a trustworthy file extension from a resource URL.
// CMS download endpoints (.jsp, .php, ...) are rejected: their suffix says
// nothing about the file they serve.
func urlExtension(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	ext := strings.ToLower(filepath.Ext(parsed.Path))
	if len(ext) < 2 || len(ext) > 10 || cmsEndpointExtensions[ext] {
		return ""
	}
	return ext
}

var cmsEndpointExtensions = map[string]bool{
	".jsp": true, ".jspx": true, ".php": true, ".asp": true, ".aspx": true,
	".do": true, ".action": true, ".htm": true, ".html": true, ".shtml": true,
}

func retryBytes(ctx context.Context, operation func() ([]byte, error)) ([]byte, error) {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		value, err := operation()
		if err == nil {
			return value, nil
		}
		last = err
		if err := waitRetry(ctx, attempt); err != nil {
			return nil, err
		}
	}
	return nil, last
}
