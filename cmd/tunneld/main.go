package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/controlserver"
)

var version = "dev"

func main() {
	command, err := parseServiceCommand(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if command == commandVersion {
		fmt.Println(version)
		return
	}
	cfg, err := controlserver.LoadConfig()
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
	if command == commandInitAnonymous {
		if err := controlserver.InitializeAnonymousResources(ctx, cfg, true); err != nil {
			logger.Error("anonymous initialization refused", "error", err)
			os.Exit(1)
		}
		fmt.Println("Anonymous resources initialized for the verified fresh namespace.")
		return
	}

	if err := run(ctx, cfg, logger); err != nil {
		logger.Error("tunneld stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg controlserver.Config, logger *slog.Logger) error {
	runtime, err := controlserver.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer runtime.Close()
	pluginListener, err := net.Listen("tcp", cfg.PluginListenAddr)
	if err != nil {
		return fmt.Errorf("listen on private FRP plugin address: %w", err)
	}
	defer pluginListener.Close()
	publicListener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen on public control address: %w", err)
	}
	defer publicListener.Close()
	serving, cancel := context.WithCancel(ctx)
	defer cancel()
	newHTTPServer := func(handler http.Handler) *http.Server {
		return &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
			WriteTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10,
			BaseContext: func(net.Listener) context.Context { return serving }}
	}
	publicHTTP, pluginHTTP := newHTTPServer(runtime.Handler()), newHTTPServer(runtime.PluginHandler())
	results := make(chan error, 2)
	go func() { results <- publicHTTP.Serve(publicListener) }()
	go func() { results <- pluginHTTP.Serve(pluginListener) }()
	logger.Info("persistent Tunnel control plane listening", "version", version,
		"address", publicListener.Addr().String(), "plugin_address", pluginListener.Addr().String(),
		"public_domain", cfg.PublicDomain, "frp_auth", "per-run-plugin")
	remaining := 2
	var result error
	select {
	case <-ctx.Done():
	case err := <-results:
		remaining--
		if !errors.Is(err, http.ErrServerClosed) {
			result = err
		}
	}
	cancel()
	shutdown, stopShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopShutdown()
	for _, server := range []*http.Server{publicHTTP, pluginHTTP} {
		if err := server.Shutdown(shutdown); err != nil {
			result = errors.Join(result, err)
			_ = server.Close()
		}
	}
	for range remaining {
		if err := <-results; !errors.Is(err, http.ErrServerClosed) {
			result = errors.Join(result, err)
		}
	}
	return result
}
