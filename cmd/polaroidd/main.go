// Command polaroidd serves Polaroid's HTTP API over a SQLite database file.
//
// Usage:
//
//	polaroidd [-addr host:port] [-db path]
//
// Flags override the POLAROID_ADDR and POLAROID_DB environment variables.
// The daemon stops gracefully on SIGINT or SIGTERM.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"example.com/polaroid/internal/memory"
	"example.com/polaroid/internal/storage/sqlite"
	httptransport "example.com/polaroid/internal/transport/http"
)

const (
	defaultAddr     = "127.0.0.1:7417"
	defaultDBPath   = "polaroid.db"
	shutdownTimeout = 10 * time.Second
)

type config struct {
	addr   string
	dbPath string
}

func main() {
	os.Exit(realMain())
}

func realMain() int {
	cfg, err := parseConfig(os.Args[1:], os.Getenv, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "polaroidd:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(ctx, cfg, logger, nil); err != nil {
		logger.Error("polaroidd failed", "error", err)
		return 1
	}
	return 0
}

func parseConfig(args []string, getenv func(string) string, output io.Writer) (config, error) {
	cfg := config{addr: defaultAddr, dbPath: defaultDBPath}
	if v := getenv("POLAROID_ADDR"); v != "" {
		cfg.addr = v
	}
	if v := getenv("POLAROID_DB"); v != "" {
		cfg.dbPath = v
	}
	fs := flag.NewFlagSet("polaroidd", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.StringVar(&cfg.addr, "addr", cfg.addr, "listen `address` (env POLAROID_ADDR)")
	fs.StringVar(&cfg.dbPath, "db", cfg.dbPath, "SQLite database `file` (env POLAROID_DB)")
	fs.Usage = func() {
		fmt.Fprintln(output, "Usage: polaroidd [-addr host:port] [-db path]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() > 0 {
		return config{}, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if _, _, err := net.SplitHostPort(cfg.addr); err != nil {
		return config{}, fmt.Errorf("invalid listen address %q: %w", cfg.addr, err)
	}
	return cfg, nil
}

// run serves the API until ctx is canceled, then shuts down gracefully.
// If ready is not nil it is called with the bound address once the server
// accepts connections.
func run(ctx context.Context, cfg config, logger *slog.Logger, ready func(net.Addr)) (err error) {
	store, err := sqlite.Open(ctx, cfg.dbPath)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := store.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close database: %w", cerr)
		}
	}()

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	handler := httptransport.NewHandler(memory.NewService(store), logger)
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok && tcp.IP.IsLoopback() {
		handler = httptransport.RequireLoopbackHost(handler)
	} else {
		logger.Warn("listening on a non-loopback address: the API has no authentication", "addr", ln.Addr().String())
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	dbPath := cfg.dbPath
	if abs, err := filepath.Abs(dbPath); err == nil {
		dbPath = abs
	}
	logger.Info("polaroidd listening", "addr", ln.Addr().String(), "db", dbPath)
	if ready != nil {
		ready(ln.Addr())
	}

	select {
	case err := <-served:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	logger.Info("polaroidd shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	logger.Info("polaroidd stopped")
	return nil
}
