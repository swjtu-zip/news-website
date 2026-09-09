// Command repair-articles re-parses the retained raw snapshots of archived
// articles whose plain-text content is empty and fills in the content and
// search index when the current site adapters extract a non-empty body.
// Older parser versions left hundreds of image-less articles with visible
// HTML but no searchable text; this brings them up to date without
// re-downloading anything from the source sites.
//
// Usage: repair-articles [-data DIR] [-apply]
//
// Without -apply it only reports how many articles would be repaired.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
	"swjtu-cli/pkg/sdk"
)

type candidate struct {
	id       int64
	url      string
	title    string
	source   string
	siteName string
	rawPath  string
}

func main() {
	dataDir := flag.String("data", "data", "archive data directory")
	apply := flag.Bool("apply", false, "write repairs to the database")
	flag.Parse()

	dbPath := filepath.Join(*dataDir, "archive.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout = 30000"); err != nil {
		log.Fatalf("configure sqlite: %v", err)
	}

	rows, err := db.Query(`SELECT id, canonical_url, title, source, site_name, raw_path
FROM articles WHERE fetch_status='success' AND trim(content)='' AND raw_path<>''`)
	if err != nil {
		log.Fatalf("query candidates: %v", err)
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.url, &c.title, &c.source, &c.siteName, &c.rawPath); err != nil {
			log.Fatalf("scan candidate: %v", err)
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Fatalf("iterate candidates: %v", err)
	}

	repaired, unchanged, failed := 0, 0, 0
	for _, c := range candidates {
		raw, err := os.ReadFile(filepath.Join(*dataDir, c.rawPath))
		if err != nil {
			failed++
			log.Printf("read raw %d %s: %v", c.id, c.url, err)
			continue
		}
		article, err := sdk.ParseRawArticle(c.url, raw)
		if err != nil {
			failed++
			continue
		}
		if strings.TrimSpace(article.Content) == "" {
			unchanged++
			continue
		}
		repaired++
		if !*apply {
			if repaired <= 10 {
				fmt.Printf("would repair %d [%s] %s (%d chars)\n", c.id, c.siteName, c.title, len(article.Content))
			}
			continue
		}
		if err := writeRepair(db, c, article.Content); err != nil {
			repaired--
			failed++
			log.Printf("write repair %d %s: %v", c.id, c.url, err)
		}
	}
	fmt.Printf("candidates=%d repaired=%d unchanged=%d failed=%d apply=%v\n",
		len(candidates), repaired, unchanged, failed, *apply)
}

func writeRepair(db *sql.DB, c candidate, content string) error {
	deadline := time.Now().Add(60 * time.Second)
	for {
		tx, err := db.Begin()
		if err == nil {
			if _, err = tx.Exec("UPDATE articles SET content=? WHERE id=?", content, c.id); err == nil {
				_, err = tx.Exec(`INSERT OR REPLACE INTO article_fts(rowid, title, content, source, site_name)
VALUES (?, ?, ?, ?, ?)`, c.id, c.title, content, c.source, c.siteName)
			}
			if err == nil {
				err = tx.Commit()
			} else {
				tx.Rollback()
			}
		}
		if err == nil {
			return nil
		}
		if !strings.Contains(err.Error(), "locked") || time.Now().After(deadline) {
			return err
		}
		time.Sleep(500 * time.Millisecond)
	}
}
