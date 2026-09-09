package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"teach-api/internal/httpapi"
	"teach-api/internal/huoshui"
	"teach-api/internal/store"
)

func main() {
	listen := flag.String("listen", ":8080", "HTTP listen address")
	databasePath := flag.String("db", "./data/teach.db", "SQLite database path")
	importPath := flag.String("import", "", "import a teach-crawler JSON snapshot before serving")
	importCoursesPath := flag.String("import-courses", "", "import a courses JSON snapshot (see scripts/xlsx_to_courses.py) before serving")
	importOnly := flag.Bool("import-only", false, "import the snapshot(s) and exit without starting HTTP")
	corsOrigins := flag.String("cors-origins", "", "comma-separated allowed browser origins; empty means *")
	huoshuiSync := flag.Bool("huoshui-sync", true, "mirror huoshui-scraper JSON data in the background")
	huoshuiInterval := flag.Duration("huoshui-interval", 24*time.Hour, "delay between huoshui data syncs")
	huoshuiDir := flag.String("huoshui-dir", "", "huoshui data directory (default: <dir of -db>/huoshui)")
	flag.Parse()

	logger := log.New(os.Stderr, "teach-api ", log.LstdFlags)
	database, err := store.Open(*databasePath)
	if err != nil {
		logger.Fatal(err)
	}
	defer database.Close()

	if strings.TrimSpace(*importPath) != "" {
		if err := database.ImportJSON(context.Background(), *importPath); err != nil {
			logger.Fatal(err)
		}
		logger.Printf("imported %s: records=%d version=%s", *importPath, database.Meta().RecordCount, database.Meta().Version)
	}
	if strings.TrimSpace(*importCoursesPath) != "" {
		if err := database.ImportCoursesJSON(context.Background(), *importCoursesPath); err != nil {
			logger.Fatal(err)
		}
		logger.Printf("imported %s: courses=%d term=%s", *importCoursesPath, database.Meta().CourseCount, database.Meta().CoursesTerm)
	}
	if *importOnly {
		if strings.TrimSpace(*importPath) == "" && strings.TrimSpace(*importCoursesPath) == "" {
			logger.Fatal("-import-only requires -import or -import-courses")
		}
		return
	}
	if !database.HasDataset() {
		logger.Fatal("no dataset loaded; pass -import /path/to/teachers.json")
	}

	shutdownContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	huoshuiDirPath := strings.TrimSpace(*huoshuiDir)
	if huoshuiDirPath == "" {
		huoshuiDirPath = filepath.Join(filepath.Dir(*databasePath), "huoshui")
	}
	importHuoshui := func(ctx context.Context) {
		stats, outcome, err := importHuoshuiFiles(ctx, database, huoshuiDirPath)
		switch {
		case err != nil:
			logger.Printf("huoshui import: %v", err)
		case outcome == "missing":
			logger.Printf("huoshui import skipped: no JSON files in %s", huoshuiDirPath)
		case outcome == "unchanged":
			logger.Print("huoshui import skipped: database already matches the JSON files")
		default:
			logger.Printf("imported huoshui: courses=%d reviews=%d matches=%d", stats.Courses, stats.Reviews, stats.Matches)
		}
	}
	importHuoshui(shutdownContext)

	if *huoshuiSync {
		syncer := huoshui.New(huoshui.Options{
			Dir:      huoshuiDirPath,
			Interval: *huoshuiInterval,
			Token:    huoshuiToken(),
			Logger:   logger,
			OnUpdate: importHuoshui,
		})
		go syncer.Run(shutdownContext)
		logger.Printf("huoshui sync scheduled: dir=%s interval=%s", huoshuiDirPath, *huoshuiInterval)
	}

	api := httpapi.New(database, httpapi.Options{
		AllowedOrigins: splitComma(*corsOrigins),
		Logger:         logger,
	})
	server := &http.Server{
		Addr:              *listen,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Printf("listening on %s", *listen)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serveErr <- err
		}
	}()

	select {
	case err := <-serveErr:
		logger.Fatal(err)
	case <-shutdownContext.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			logger.Printf("shutdown: %v", err)
		}
	}
}

func huoshuiToken() string {
	for _, name := range []string{"HUOSHUI_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

// importHuoshuiFiles loads the mirrored huoshui JSON snapshots into the
// database. It is idempotent: when the database already records the sha256 of
// the files on disk it does nothing. The outcome is "imported", "unchanged"
// or "missing" (no files on disk — not an error).
func importHuoshuiFiles(ctx context.Context, database *store.Store, dir string) (store.HuoshuiImportStats, string, error) {
	var stats store.HuoshuiImportStats
	coursesPath := filepath.Join(dir, "courses.json")
	reviewsPath := filepath.Join(dir, "reviews.json")
	coursesJSON, err := os.ReadFile(coursesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return stats, "missing", nil
		}
		return stats, "", err
	}
	reviewsJSON, err := os.ReadFile(reviewsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return stats, "missing", nil
		}
		return stats, "", err
	}
	coursesSum := sha256.Sum256(coursesJSON)
	reviewsSum := sha256.Sum256(reviewsJSON)
	storedCourses, storedReviews, err := database.HuoshuiSHA256(ctx)
	if err != nil {
		return stats, "", err
	}
	if storedCourses == hex.EncodeToString(coursesSum[:]) && storedReviews == hex.EncodeToString(reviewsSum[:]) {
		return stats, "unchanged", nil
	}
	stats, err = database.ImportHuoshuiBytes(ctx, coursesJSON, reviewsJSON)
	if err != nil {
		return stats, "", err
	}
	return stats, "imported", nil
}

func splitComma(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' })
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}
