package main

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ashuangiras/polaroid/internal/lifecycle"
	"github.com/ashuangiras/polaroid/internal/recovery"
)

// Backup and restore (ADR-0030) are local commands too: this process opens
// the catalog files itself, through internal/recovery.

// exitRefused is restore's status when it refused and changed nothing.
const exitRefused = 3

// userHome locates the home directory; tests replace it.
var userHome = os.UserHomeDir

func isRecovery(name string) bool {
	return name == "backup" || name == "inspect-backup" || name == "restore"
}

func printJSON(w io.Writer, v any) {
	b, _ := json.Marshal(v, json.Deterministic(true))
	fmt.Fprintln(w, string(b))
}

func runRecovery(name string, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("polaroid "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var db, dir string
	var plan, replace bool
	var wait time.Duration
	if name != "inspect-backup" {
		fs.StringVar(&db, "db", "", "the catalog (default $POLAROID_DB, else the installed service's, else ~/.polaroid/data/polaroid.db)")
		fs.StringVar(&dir, "dir", "", "the backup directory (default ~/.polaroid/backups)")
	}
	if name == "restore" {
		fs.BoolVar(&plan, "plan", false, "print the plan and change nothing")
		fs.BoolVar(&replace, "replace", false, "confirm replacing an existing catalog")
		fs.DurationVar(&wait, "wait", 30*time.Second, "how long to wait for the managed service to stop, and to become healthy again")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	want := 1
	if name == "backup" {
		want = 0
	}
	if fs.NArg() != want {
		fmt.Fprintf(stderr, "polaroid: usage: polaroid %s\n", usageOf(name))
		return exitUsage
	}
	ctx := context.Background()
	if name == "inspect-backup" {
		r := recovery.Inspect(ctx, fs.Arg(0))
		printJSON(stdout, r)
		if !r.Valid {
			fmt.Fprintf(stderr, "polaroid: %s is not a valid backup: %s\n", r.Path, strings.Join(r.Problems, "; "))
			return exitFailure
		}
		fmt.Fprintf(stderr, "polaroid: %s is a valid backup of %s, taken %s, schema %d\n", r.Path, r.Metadata.Source.Path, r.Metadata.CreatedAt, r.SchemaVersion)
		return exitOK
	}

	var svc recovery.Service
	if s, err := openService(); err == nil {
		s.Wait = wait
		svc = s
	}
	catalog, managed, err := resolveCatalog(ctx, db, getenv, svc)
	if err != nil {
		fmt.Fprintf(stderr, "polaroid: %v\n", err)
		return exitFailure
	}
	if dir == "" {
		home, err := userHome()
		if err != nil || !filepath.IsAbs(home) {
			fmt.Fprintf(stderr, "polaroid: cannot locate the home directory for ~/.polaroid/backups (%v); pass -dir\n", err)
			return exitFailure
		}
		dir = filepath.Join(home, ".polaroid", "backups")
	} else if dir, err = filepath.Abs(dir); err != nil {
		fmt.Fprintf(stderr, "polaroid: %v\n", err)
		return exitFailure
	}

	if name == "backup" {
		res, err := recovery.Backup(ctx, recovery.BackupRequest{Source: catalog, Dir: dir, Managed: managed})
		if err != nil {
			fmt.Fprintf(stderr, "polaroid: backup failed: %v\n", err)
			return exitFailure
		}
		printJSON(stdout, res)
		fmt.Fprintf(stderr, "polaroid: backed up %s (schema %d, %d bytes) into %s\n", catalog, res.Metadata.SchemaVersion, res.Metadata.Database.Size, res.Path)
		return exitOK
	}

	req := recovery.RestoreRequest{Backup: fs.Arg(0), Destination: catalog, BackupDir: dir, Service: svc, Replace: replace}
	if plan {
		p := recovery.MakePlan(ctx, req)
		printJSON(stdout, p)
		if !p.Ready {
			fmt.Fprintf(stderr, "polaroid: the restore is blocked: %s\n", strings.Join(p.Blockers, "; "))
			return exitRefused
		}
		note := ""
		if p.RequiresReplace {
			note = "; it replaces an existing catalog, so it needs -replace"
		}
		fmt.Fprintf(stderr, "polaroid: the restore of %s into %s can run%s\n", p.Backup.Path, catalog, note)
		return exitOK
	}
	res, err := recovery.Restore(ctx, req)
	printJSON(stdout, res)
	switch {
	case errors.Is(err, recovery.ErrRefused):
		fmt.Fprintf(stderr, "polaroid: %v\n", err)
		return exitRefused
	case err != nil:
		fmt.Fprintf(stderr, "polaroid: restore failed (%s): %v\n", res.Outcome, err)
		return exitFailure
	}
	fmt.Fprintf(stderr, "polaroid: restored %s from %s", catalog, res.Plan.Backup.Path)
	if res.RecoveryBackup != "" {
		fmt.Fprintf(stderr, "; the replaced catalog is backed up in %s", res.RecoveryBackup)
	}
	fmt.Fprintln(stderr)
	return exitOK
}

// resolveCatalog returns the catalog -db names, else $POLAROID_DB, else the
// installed service's, else ~/.polaroid/data/polaroid.db, and whether it is
// the managed service's catalog.
func resolveCatalog(ctx context.Context, db string, getenv func(string) string, svc recovery.Service) (string, bool, error) {
	installed := ""
	if svc != nil {
		if st := svc.Status(ctx); st.State != lifecycle.NotInstalled {
			installed = st.Database
		}
	}
	path := db
	if path == "" {
		path = getenv("POLAROID_DB")
	}
	if path == "" {
		path = installed
	}
	if path == "" {
		home, err := userHome()
		if err != nil || !filepath.IsAbs(home) {
			return "", false, fmt.Errorf("cannot locate the per-user catalog (%w); pass -db or set POLAROID_DB", err)
		}
		path = filepath.Join(home, ".polaroid", "data", "polaroid.db")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false, err
	}
	return abs, installed != "" && recovery.SamePath(installed, abs), nil
}

func usageOf(name string) string {
	for _, c := range localCommands {
		if c.name == name {
			return strings.TrimSpace(c.name + " " + c.args)
		}
	}
	return name
}
