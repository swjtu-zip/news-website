// swjtu.zip 聚合 MCP 网关:统一暴露 news 与 teach 两个上游服务的 MCP 工具。
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"swjtu-mcp/internal/gateway"
)

type config struct {
	addr     string
	newsURL  string
	teachURL string
}

func configFromEnv() config {
	return config{
		addr:     getenv("MCP_GATEWAY_ADDR", ":8080"),
		newsURL:  getenv("MCP_NEWS_URL", "https://news.swjtu.zip/mcp"),
		teachURL: getenv("MCP_TEACH_URL", "https://teach.swjtu.zip/mcp"),
	}
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := configFromEnv()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gw := gateway.New(ctx, logger, cfg.newsURL, cfg.teachURL)
	defer gw.Close()

	server := &http.Server{
		Addr:              cfg.addr,
		Handler:           gw.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		logger.Info("listening", "addr", cfg.addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("http server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown failed", "error", err)
	}
}
