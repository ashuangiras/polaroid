// Command polaroidd serves Polaroid's HTTP API, and MCP at /mcp, over a
// SQLite database file.
//
// Usage:
//
//	polaroidd [-addr host:port] [-db path]
//
// Flags override the POLAROID_ADDR and POLAROID_DB environment variables.
// Without either, the database is ~/.polaroid/data/polaroid.db (ADR-0025).
// The daemon stops gracefully on SIGINT or SIGTERM.
package main

import (
	"context"
	json "encoding/json/v2"
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

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
	httptransport "github.com/ashuangiras/polaroid/internal/transport/http"
	mcptransport "github.com/ashuangiras/polaroid/internal/transport/mcp"
	"github.com/ashuangiras/polaroid/internal/version"
)

const (
	defaultAddr     = "127.0.0.1:7417"
	shutdownTimeout = 10 * time.Second
)

// Where the database path came from, as logged at startup.
const (
	dbFromFlag    = "flag"
	dbFromEnv     = "environment"
	dbFromDefault = "default"
)

type config struct {
	addr     string
	dbPath   string
	dbSource string
	// defaultLocation is set when dbPath is the per-user default, however it
	// was given; such a database is created private (ADR-0025, ADR-0026).
	defaultLocation bool
	logFile         string
	version         bool
}

func main() {
	os.Exit(realMain())
}

func realMain() int {
	cfg, err := parseConfig(os.Args[1:], os.LookupEnv, os.UserHomeDir, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "polaroidd:", err)
		return 2
	}
	if cfg.version {
		b, _ := json.Marshal(version.Current("polaroidd"))
		fmt.Println(string(b))
		return 0
	}
	var logs io.Writer = os.Stderr
	if cfg.logFile != "" {
		f, err := openLogFile(cfg.logFile, logMaxBytes, redirectOutput)
		if err != nil {
			fmt.Fprintln(os.Stderr, "polaroidd:", err)
			return 1
		}
		defer func() { _ = f.Close() }()
		logs = f
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewTextHandler(logs, nil))
	if err := run(ctx, cfg, logger, nil); err != nil {
		logger.Error("polaroidd failed", "error", err)
		return 1
	}
	return 0
}

// parseConfig applies -db > POLAROID_DB > the per-user default. An empty -db
// or set but empty POLAROID_DB is an error, so a mistyped variable never
// selects the shared default.
func parseConfig(args []string, lookupEnv func(string) (string, bool), userHomeDir func() (string, error), output io.Writer) (config, error) {
	cfg := config{addr: defaultAddr}
	if v, ok := lookupEnv("POLAROID_ADDR"); ok && v != "" {
		cfg.addr = v
	}
	var dbFlag string
	fs := flag.NewFlagSet("polaroidd", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.StringVar(&cfg.addr, "addr", cfg.addr, "listen `address` (env POLAROID_ADDR)")
	fs.StringVar(&dbFlag, "db", "", "SQLite database `file` (env POLAROID_DB; default ~/.polaroid/data/polaroid.db in the user's home directory)")
	fs.StringVar(&cfg.logFile, "log-file", "", "write logs, standard output and standard error to `file`, rotated to file.1 at 4 MiB (default: standard error)")
	fs.BoolVar(&cfg.version, "version", false, "print the build as JSON and exit")
	fs.Usage = func() {
		fmt.Fprintln(output, "Usage: polaroidd [-addr host:port] [-db path] [-log-file path] [-version]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() > 0 {
		return config{}, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if cfg.version {
		return cfg, nil
	}
	if _, _, err := net.SplitHostPort(cfg.addr); err != nil {
		return config{}, fmt.Errorf("invalid listen address %q: %w", cfg.addr, err)
	}
	dbSet := false
	fs.Visit(func(f *flag.Flag) { dbSet = dbSet || f.Name == "db" })
	env, envSet := lookupEnv("POLAROID_DB")
	switch {
	case dbSet && dbFlag == "":
		return config{}, errors.New("-db must not be empty")
	case dbSet:
		cfg.dbPath, cfg.dbSource = dbFlag, dbFromFlag
	case envSet && env == "":
		return config{}, errors.New("POLAROID_DB is set but empty: unset it to use ~/.polaroid/data/polaroid.db, or name a database file")
	case envSet:
		cfg.dbPath, cfg.dbSource = env, dbFromEnv
	default:
		path, err := defaultDBPath(userHomeDir)
		if err != nil {
			return config{}, err
		}
		cfg.dbPath, cfg.dbSource = path, dbFromDefault
	}
	if def, err := defaultDBPath(userHomeDir); err == nil {
		if abs, err := filepath.Abs(cfg.dbPath); err == nil && abs == def {
			cfg.defaultLocation = true
		}
	}
	return cfg, nil
}

// run serves the API until ctx is canceled, then shuts down gracefully.
// If ready is not nil it is called with the bound address once the server
// accepts connections.
func run(ctx context.Context, cfg config, logger *slog.Logger, ready func(net.Addr)) (err error) {
	if cfg.dbSource == dbFromDefault || cfg.defaultLocation {
		exposed, err := prepareDefaultDB(cfg.dbPath)
		if err != nil {
			return err
		}
		for _, p := range exposed {
			logger.Warn("the per-user database location is accessible to other users; polaroidd does not change existing permissions", "path", p)
		}
	}
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
	svc := memory.NewService(store)
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcptransport.NewHandler(svc, logger))
	mux.Handle("/", httptransport.NewHandler(svc, logger))
	var handler http.Handler = mux
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
	logger.Info("polaroidd listening", "addr", ln.Addr().String(), "db", dbPath, "db_source", cfg.dbSource)
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
