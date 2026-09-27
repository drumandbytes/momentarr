package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/drumandbytes/momentarr/internal/api"
	"github.com/drumandbytes/momentarr/internal/cache"
	"github.com/drumandbytes/momentarr/internal/model"
)

const heartbeat = 15 * time.Minute

func main() {
	var level slog.Level
	if err := level.UnmarshalText([]byte(env("LOG_LEVEL", "info"))); err != nil {
		fatal("invalid LOG_LEVEL, want debug|info|warn|error", "value", os.Getenv("LOG_LEVEL"))
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	backend := env("BACKEND_URL", "http://127.0.0.1:8192")
	concurrency := positive("BACKEND_CONCURRENCY", 1)
	cookieDir := env("COOKIE_DIR", "/tmp/momentarr-cookies")
	ttlHours := positive("COOKIE_TTL_HOURS", 24)
	store, err := cache.New(cookieDir, ttlHours)
	if err != nil {
		fatal("cannot open cookie cache", "dir", cookieDir, "err", err)
	}

	server := api.New(backend, concurrency, store)
	httpSrv := &http.Server{
		Addr:              api.ListenAddr(),
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go server.Heartbeat(ctx, heartbeat)
	go func() {
		slog.Info("momentarr starting", "version", model.Version, "addr", httpSrv.Addr, "backend", backend,
			"concurrency", concurrency, "cookieDir", cookieDir, "cookieTTLHours", ttlHours, "logLevel", level)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal("http server failed", "err", err)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("unclean shutdown", "err", err)
	}
	slog.Info("stopped")
}

func fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func positive(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		fatal("invalid "+name+", want a positive integer", "value", value)
	}
	return parsed
}
