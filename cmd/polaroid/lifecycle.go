package main

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/ashuangiras/polaroid/internal/lifecycle"
	"github.com/ashuangiras/polaroid/internal/version"
)

// Lifecycle commands are local operations on this user's installation
// (ADR-0026); they never call the API, so they work with the daemon stopped.
var localCommands = []struct{ name, args, summary string }{
	{"install", "-from DIR [-addr HOST:PORT] [-db FILE] [-wait DURATION]", "install polaroid and polaroidd from DIR into ~/.local/bin, register the per-user service, start it and wait until it is healthy"},
	{"start", "[-wait DURATION]", "start the installed service and wait until it is healthy"},
	{"stop", "[-wait DURATION]", "stop the service until the next start or login"},
	{"restart", "[-wait DURATION]", "stop and start the service"},
	{"status", "", "report the installation and the service as JSON"},
	{"uninstall", "[-wait DURATION]", "stop and unregister the service and remove the installed files; ~/.polaroid is kept"},
	{"backup", "[-db FILE] [-dir DIR]", "back up the catalog into a new directory under DIR (default ~/.polaroid/backups); polaroidd may keep running"},
	{"inspect-backup", "BACKUP", "check a backup without restoring it or changing it"},
	{"restore", "[-db FILE] [-dir DIR] [-plan] [-replace] [-wait DURATION] BACKUP", "replace the catalog with BACKUP's snapshot, after backing up the current one into DIR; -plan only prints the plan; -replace confirms replacing an existing catalog"},
	{"version", "", "print this polaroid's build as JSON"},
}

// openService opens the lifecycle service for this user; tests replace it.
var openService = func() (*lifecycle.Service, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot locate the home directory: %w", err)
	}
	return lifecycle.Open(runtime.GOOS, home, lifecycle.ExecRunner{})
}

func isLocal(name string) bool {
	for _, c := range localCommands {
		if c.name == name {
			return true
		}
	}
	return false
}

// Exit status of the lifecycle commands besides 0, 1 and 2: status uses the
// states' codes; the others use 4 when nothing is installed.
const exitNotInstalled = 4

func runLocal(name string, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if isRecovery(name) {
		return runRecovery(name, args, stdout, stderr, getenv)
	}
	if name == "version" {
		if len(args) > 0 {
			fmt.Fprintln(stderr, "polaroid: usage: polaroid version")
			return exitUsage
		}
		b, _ := json.Marshal(version.Current("polaroid"))
		fmt.Fprintln(stdout, string(b))
		return exitOK
	}
	fs := flag.NewFlagSet("polaroid "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o lifecycle.InstallOptions
	var serviceName string
	wait := fs.Duration("wait", 30*time.Second, "how long to wait for the service to become healthy, or to stop")
	if name == "install" {
		fs.StringVar(&o.From, "from", "", "directory holding the built polaroid and polaroidd")
		fs.StringVar(&o.Addr, "addr", "", "loopback address the service listens on (default "+lifecycle.DefaultAddr+", or the installed one)")
		fs.StringVar(&o.DB, "db", "", "absolute database path (default ~/.polaroid/data/polaroid.db, or the installed one)")
		fs.StringVar(&serviceName, "service-name", "", "service name, for isolated validation installs with their own HOME (default polaroid)")
	}
	if name == "status" {
		fs.Init("polaroid status", flag.ContinueOnError)
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "polaroid: unexpected arguments: %v\n", fs.Args())
		return exitUsage
	}
	svc, err := openService()
	if err != nil {
		fmt.Fprintf(stderr, "polaroid: %v\n", err)
		return exitFailure
	}
	svc.Wait = *wait
	if serviceName != "" && serviceName != svc.Layout.ServiceName {
		if svc.Status(context.Background()).State != lifecycle.NotInstalled {
			fmt.Fprintf(stderr, "polaroid: the installation is named %s; uninstall it before installing %s\n", svc.Layout.ServiceName, serviceName)
			return exitFailure
		}
		l, err := lifecycle.NewLayout(svc.Layout.GOOS, svc.Layout.Home, serviceName)
		if err != nil {
			fmt.Fprintf(stderr, "polaroid: %v\n", err)
			return exitUsage
		}
		svc.Layout = l
	}
	ctx := context.Background()
	var st lifecycle.Status
	switch name {
	case "install":
		st, err = svc.Install(ctx, o)
	case "start":
		st, err = svc.Start(ctx)
	case "stop":
		st, err = svc.Stop(ctx)
	case "restart":
		st, err = svc.Restart(ctx)
	case "uninstall":
		st, err = svc.Uninstall(ctx)
	case "status":
		st = svc.Status(ctx)
	}
	b, _ := json.Marshal(st)
	fmt.Fprintln(stdout, string(b))
	summary := strings.TrimSpace(strings.Join([]string{st.Action, st.State}, ": "))
	if st.Detail != "" {
		summary += ": " + st.Detail
	}
	fmt.Fprintf(stderr, "polaroid: %s\n", strings.TrimPrefix(summary, ": "))
	switch {
	case name == "status":
		return st.ExitCode()
	case errors.Is(err, lifecycle.ErrNotInstalled):
		fmt.Fprintf(stderr, "polaroid: %v\n", err)
		return exitNotInstalled
	case err != nil:
		fmt.Fprintf(stderr, "polaroid: %v\n", err)
		return exitFailure
	}
	return exitOK
}
