package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/lease"
	"github.com/Wy2926/nodelane-tunneld/internal/server"
	"github.com/Wy2926/nodelane-tunneld/internal/store"
)

var version = "dev"

func main() {
	cfg, err := server.LoadConfig()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	var logLevel slog.Level
	if err := logLevel.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		slog.Error("invalid log level", "error", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var repository domain.Repository
	if cfg.DatabaseURL == "" {
		repository = store.NewMemory()
		logger.Warn("using in-memory repository; data will be lost on restart")
	} else {
		repository, err = store.OpenPostgres(ctx, cfg.DatabaseURL)
		if err != nil {
			logger.Error("open repository", "error", err)
			os.Exit(1)
		}
	}
	defer repository.Close()

	var leases domain.LeaseManager
	if cfg.RedisAddr == "" {
		leases = lease.NewMemory()
		logger.Warn("using in-memory leases; multi-instance limits are disabled")
	} else {
		leases, err = lease.OpenRedis(ctx, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisPrefix)
		if err != nil {
			logger.Error("open lease manager", "error", err)
			os.Exit(1)
		}
	}
	defer leases.Close()

	handler := server.New(cfg, repository, leases, logger).Handler()
	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	go func() {
		logger.Info("tunneld listening",
			"version", version,
			"address", cfg.ListenAddr,
			"public_scheme", cfg.PublicScheme,
			"public_domain", cfg.PublicDomain,
			"node", cfg.NodeID,
			"frp_server", cfg.FRPServerAddr,
			"frp_port", cfg.FRPServerPort,
			"log_level", cfg.LogLevel,
		)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server stopped", "error", err)
			cancel()
		}
	}()

	<-ctx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown", "error", err)
	}
}
