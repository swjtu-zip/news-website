// Command r2sync mirrors the local content-addressed assets tree to R2.
// It is intentionally resumable: an existing object with the requested
// storage class is skipped, while an existing object with the wrong class is
// changed server-side when possible.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"log"
	"mime"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"swjtu-archive/internal/r2"

	_ "modernc.org/sqlite"
)

type assetMetadata struct {
	contentType string
	filename    string
	kind        string
	publishedAt string
}

type assetFile struct {
	fullPath     string
	localPath    string
	contentType  string
	filename     string
	kind         string
	storageClass string
}

func main() {
	dataDir := flag.String("data", env("SWJTU_ARCHIVE_DATA", "data"), "archive data directory")
	endpoint := flag.String("endpoint", env("SWJTU_ARCHIVE_R2_ENDPOINT", ""), "R2 S3 account endpoint")
	bucket := flag.String("bucket", env("SWJTU_ARCHIVE_R2_BUCKET", "swjtu-zip"), "R2 bucket")
	prefix := flag.String("prefix", env("SWJTU_ARCHIVE_R2_PREFIX", "news-assets"), "R2 object prefix")
	accessKey := flag.String("access-key-id", firstEnv("SWJTU_ARCHIVE_R2_ACCESS_KEY_ID", "R2_ACCESS_KEY_ID"), "R2 access key ID")
	secretKey := flag.String("secret-access-key", firstEnv("SWJTU_ARCHIVE_R2_SECRET_ACCESS_KEY", "R2_SECRET_ACCESS_KEY"), "R2 secret access key")
	workers := flag.Int("workers", 12, "concurrent R2 workers")
	maxFiles := flag.Int("max-files", 0, "stop after submitting this many files (0 means all)")
	deleteLocal := flag.Bool("delete-local", false, "delete each local asset only after its R2 object is confirmed or uploaded")
	listPrefix := flag.String("list-prefix", "", "list object count below this prefix without changing anything")
	deletePrefix := flag.String("delete-prefix", "", "delete every object below this exact prefix instead of syncing")
	flag.Parse()

	if *workers < 1 || *maxFiles < 0 {
		log.Fatal("workers must be positive and max-files must not be negative")
	}
	client, err := r2.New(r2.Config{
		Endpoint: *endpoint, Bucket: *bucket, Prefix: *prefix,
		AccessKeyID: *accessKey, SecretAccessKey: *secretKey,
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if strings.TrimSpace(*deletePrefix) != "" {
		deleted, err := deleteObjects(ctx, client, *deletePrefix, *workers)
		log.Printf("deleted %d objects below %s", deleted, *deletePrefix)
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	if strings.TrimSpace(*listPrefix) != "" {
		keys, err := retryKeys(ctx, func() ([]string, error) { return client.ListPrefix(ctx, *listPrefix) })
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("found %d objects below %s", len(keys), *listPrefix)
		if len(keys) > 0 {
			log.Printf("first=%s last=%s", keys[0], keys[len(keys)-1])
		}
		return
	}
	metadata := loadMetadata(filepath.Join(*dataDir, "archive.db"))
	existingKeys := loadExistingKeys(ctx, client, *prefix)
	assetRoot := filepath.Join(*dataDir, "assets")
	jobs := make(chan assetFile, *workers*2)
	var walked int64
	var skipped int64
	var uploaded int64
	var changedClass int64
	var failed int64
	var uploadedBytes int64
	var deletedLocal int64
	var lastLog atomic.Int64
	var wg sync.WaitGroup
	var submitted int
	errMaxFiles := errors.New("max files reached")

	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for asset := range jobs {
				action, err := syncAsset(ctx, client, asset, existingKeys, *prefix)
				if err != nil {
					atomic.AddInt64(&failed, 1)
					log.Printf("resource %s: %v", asset.localPath, err)
					continue
				}
				var assetSize int64
				if action == "uploaded" {
					if info, err := os.Stat(asset.fullPath); err == nil {
						assetSize = info.Size()
					}
				}
				if *deleteLocal {
					if err := os.Remove(asset.fullPath); err != nil {
						atomic.AddInt64(&failed, 1)
						log.Printf("delete local resource %s after %s: %v", asset.localPath, action, err)
						continue
					}
					atomic.AddInt64(&deletedLocal, 1)
				}
				switch action {
				case "skipped":
					atomic.AddInt64(&skipped, 1)
				case "class-change":
					atomic.AddInt64(&changedClass, 1)
				case "uploaded":
					atomic.AddInt64(&uploaded, 1)
					atomic.AddInt64(&uploadedBytes, assetSize)
				}
				n := atomic.AddInt64(&walked, 1)
				if n%100 == 0 || time.Since(time.Unix(0, lastLog.Load())) > 30*time.Second {
					lastLog.Store(time.Now().UnixNano())
					log.Printf("progress processed=%d skipped=%d uploaded=%d class_changes=%d deleted_local=%d failed=%d bytes=%d", n, atomic.LoadInt64(&skipped), atomic.LoadInt64(&uploaded), atomic.LoadInt64(&changedClass), atomic.LoadInt64(&deletedLocal), atomic.LoadInt64(&failed), atomic.LoadInt64(&uploadedBytes))
				}
			}
		}()
	}

	walkErr := filepath.WalkDir(assetRoot, func(fullPath string, entry os.DirEntry, err error) error {
		if *maxFiles > 0 && submitted >= *maxFiles {
			return errMaxFiles
		}
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(assetRoot, fullPath)
		if err != nil {
			return err
		}
		localPath := filepath.ToSlash(filepath.Join("assets", relative))
		meta := metadata[localPath]
		filename := meta.filename
		if filename == "" {
			filename = entry.Name()
		}
		contentType := meta.contentType
		if contentType == "" {
			contentType = mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
		}
		storageClass := desiredStorageClass(meta.kind, meta.publishedAt, time.Now())
		select {
		case jobs <- assetFile{fullPath: fullPath, localPath: localPath, contentType: contentType, filename: filename, kind: meta.kind, storageClass: storageClass}:
			submitted++
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	close(jobs)
	wg.Wait()
	if walkErr != nil && !errors.Is(walkErr, errMaxFiles) && !strings.Contains(walkErr.Error(), "context canceled") {
		log.Printf("walk assets: %v", walkErr)
	}
	log.Printf("complete processed=%d skipped=%d uploaded=%d class_changes=%d deleted_local=%d failed=%d bytes=%d", atomic.LoadInt64(&walked), atomic.LoadInt64(&skipped), atomic.LoadInt64(&uploaded), atomic.LoadInt64(&changedClass), atomic.LoadInt64(&deletedLocal), atomic.LoadInt64(&failed), atomic.LoadInt64(&uploadedBytes))
	if atomic.LoadInt64(&failed) > 0 || walkErr != nil {
		os.Exit(1)
	}
}

func syncAsset(ctx context.Context, client *r2.Client, asset assetFile, existingKeys map[string]struct{}, prefix string) (string, error) {
	if existingKeys != nil {
		if key, ok := r2.ObjectKey(prefix, asset.localPath); ok {
			if _, exists := existingKeys[key]; exists {
				// Existing objects are left untouched. In particular, a resumed
				// migration never changes an object that is already STANDARD_IA.
				return "skipped", nil
			}
		}
	}
	class, err := retry(ctx, func() (string, error) {
		return client.ExistingStorageClass(ctx, asset.localPath)
	})
	if err != nil {
		return "", err
	}
	if class != "" {
		// Existing objects are left untouched. In particular, a resumed
		// migration never changes an object that is already STANDARD_IA.
		return "skipped", nil
	}
	if err := retryErr(ctx, func() error {
		return client.Upload(ctx, asset.fullPath, asset.localPath, asset.contentType, asset.filename, asset.kind, asset.storageClass)
	}); err != nil {
		return "", err
	}
	return "uploaded", nil
}

func loadExistingKeys(ctx context.Context, client *r2.Client, prefix string) map[string]struct{} {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		return nil
	}
	keys, err := retryKeys(ctx, func() ([]string, error) {
		return client.ListPrefix(ctx, prefix+"/")
	})
	if err != nil {
		log.Printf("list existing R2 objects: %v; falling back to per-object checks", err)
		return nil
	}
	existing := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		existing[key] = struct{}{}
	}
	log.Printf("loaded %d existing R2 objects below %s/", len(existing), prefix)
	return existing
}

func deleteObjects(ctx context.Context, client *r2.Client, prefix string, workers int) (int, error) {
	keys, err := retryKeys(ctx, func() ([]string, error) { return client.ListPrefix(ctx, prefix) })
	if err != nil {
		return 0, err
	}
	log.Printf("found %d objects below %s", len(keys), prefix)
	jobs := make(chan string, workers*2)
	var wg sync.WaitGroup
	var deleted int64
	var firstErr error
	var errMu sync.Mutex
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for key := range jobs {
				if err := retryErr(ctx, func() error { return client.DeleteObject(ctx, key) }); err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errMu.Unlock()
					continue
				}
				atomic.AddInt64(&deleted, 1)
			}
		}()
	}
	for _, key := range keys {
		select {
		case jobs <- key:
		case <-ctx.Done():
			break
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return int(atomic.LoadInt64(&deleted)), firstErr
	}
	return int(atomic.LoadInt64(&deleted)), ctx.Err()
}

func retry(ctx context.Context, operation func() (string, error)) (string, error) {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		value, err := operation()
		if err == nil {
			return value, nil
		}
		last = err
		if err := waitRetry(ctx, attempt); err != nil {
			return "", err
		}
	}
	return "", last
}

func retryKeys(ctx context.Context, operation func() ([]string, error)) ([]string, error) {
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

func retryErr(ctx context.Context, operation func() error) error {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if err := operation(); err == nil {
			return nil
		} else {
			last = err
		}
		if err := waitRetry(ctx, attempt); err != nil {
			return err
		}
	}
	return last
}

func waitRetry(ctx context.Context, attempt int) error {
	timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func loadMetadata(dbPath string) map[string]assetMetadata {
	metadata := make(map[string]assetMetadata)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?mode=ro&_pragma=busy_timeout(30000)")
	if err != nil {
		log.Printf("open archive metadata: %v; using file extensions", err)
		return metadata
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	rows, err := db.Query(`SELECT r.local_path, r.content_type, r.filename, r.kind, a.published_at
FROM resources r LEFT JOIN articles a ON a.id=r.article_id
WHERE r.status='success' AND r.local_path LIKE 'assets/%'`)
	if err != nil {
		log.Printf("read archive metadata: %v; using file extensions", err)
		return metadata
	}
	defer rows.Close()
	for rows.Next() {
		var localPath, contentType, filename, kind, publishedAt string
		if err := rows.Scan(&localPath, &contentType, &filename, &kind, &publishedAt); err != nil {
			log.Printf("scan archive metadata: %v", err)
			continue
		}
		localPath = filepath.ToSlash(path.Clean(localPath))
		current, exists := metadata[localPath]
		if !exists || current.contentType == "" {
			current.contentType = contentType
		}
		if current.filename == "" {
			current.filename = filename
		}
		if current.kind != "attachment" {
			current.kind = kind
		}
		if current.publishedAt == "" || (kind == "attachment" && publishedAt != "") {
			current.publishedAt = publishedAt
		}
		metadata[localPath] = current
	}
	if err := rows.Err(); err != nil {
		log.Printf("read archive metadata rows: %v", err)
	}
	log.Printf("loaded metadata for %d assets", len(metadata))
	return metadata
}

func desiredStorageClass(kind, publishedAt string, now time.Time) string {
	// All objects added by this tool use frequent-access storage. Existing
	// objects are skipped above, so their class is never changed.
	return "STANDARD"
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
