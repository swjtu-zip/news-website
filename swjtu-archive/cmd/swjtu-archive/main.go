package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"swjtu-archive/internal/archive"
	"swjtu-archive/internal/config"
	"swjtu-archive/internal/web"
	"swjtu-cli/pkg/sdk"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	logger := log.New(os.Stdout, "swjtu-archive ", log.LstdFlags)
	store, err := archive.Open(cfg.DBPath())
	if err != nil {
		logger.Fatal(err)
	}
	defer store.Close()

	client := sdk.New(sdk.Options{})
	syncer := archive.NewSyncer(store, client, archive.SyncOptions{
		Backfill: cfg.Backfill, MaxPages: cfg.MaxPages, MaxConcurrent: cfg.MaxConcurrent,
		RequestGap: cfg.RequestGap, RefreshAfter: cfg.RefreshAfter,
		MaxResourceBytes: cfg.MaxResourceBytes, Logger: logger,
	})

	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	switch command {
	case "sync":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		result, err := syncer.Sync(ctx)
		if err != nil {
			logger.Printf("sync stopped: %v", err)
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				os.Exit(2)
			}
		}
		logger.Printf("sync complete: run=%d feeds=%d articles=%d resources=%d failures=%d", result.RunID, result.Feeds, result.Articles, result.Resources, result.Failures)
		if result.Failures > 0 {
			os.Exit(1)
		}
	case "serve":
		runServer(cfg, syncer, logger)
	default:
		logger.Printf("usage: %s [serve|sync]", os.Args[0])
		os.Exit(2)
	}
}

func runServer(cfg config.Config, syncer *archive.Syncer, logger *log.Logger) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gate := make(chan struct{}, 1)
	runSync := func() {
		select {
		case gate <- struct{}{}:
		default:
			logger.Printf("sync already running; skipping scheduled run")
			return
		}
		defer func() { <-gate }()
		if _, err := syncer.Sync(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Printf("sync stopped: %v", err)
		}
	}
	if cfg.SyncOnStart {
		go runSync()
	}

	server := &http.Server{Addr: cfg.Addr, Handler: web.NewServer(syncer.Store()).Handler()}
	// Keep the store owned by the syncer but expose it to the HTTP layer through
	// this small accessor rather than duplicating database connections.
	go func() {
		logger.Printf("listening on %s", cfg.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Printf("http server: %v", err)
			stop()
		}
	}()

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = server.Shutdown(shutdownCtx)
			cancel()
			return
		case <-ticker.C:
			go runSync()
		}
	}
}
