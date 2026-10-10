package recovery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ashuangiras/polaroid/internal/lifecycle"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
)

var (
	// ErrRefused means the restore was refused before anything was changed.
	ErrRefused = errors.New("restore refused; nothing was changed")
	// ErrConfirmationRequired means the catalog exists and -replace was not given.
	ErrConfirmationRequired = fmt.Errorf("%w: the catalog exists; pass -replace to replace it", ErrRefused)
)

// Service is the managed service, as internal/lifecycle runs it.
type Service interface {
	Status(context.Context) lifecycle.Status
	Stop(context.Context) (lifecycle.Status, error)
	Start(context.Context) (lifecycle.Status, error)
}

// RestoreRequest says which backup replaces which catalog.
type RestoreRequest struct {
	Backup      string  // backup directory
	Destination string  // absolute path of the catalog to replace or create
	BackupDir   string  // absolute directory for the recovery backup
	Service     Service // nil where no service manager is available
	Replace     bool    // confirms replacing an existing catalog
	Now         func() time.Time
}

// Plan is what restore -plan prints. Making it changes nothing.
type Plan struct {
	Backup                 Report        `json:"backup"`
	Destination            Destination   `json:"destination"`
	Service                *ServiceState `json:"service,omitempty"`
	SupportedSchemaVersion int           `json:"supported_schema_version"`
	Migration              *Migration    `json:"migration,omitempty"`
	RecoveryBackupDir      string        `json:"recovery_backup_dir,omitempty"`
	RequiresReplace        bool          `json:"requires_replace"`
	Ready                  bool          `json:"ready"`
	Blockers               []string      `json:"blockers,omitempty"`
	Actions                []string      `json:"actions"`
	History                string        `json:"history"`
}

// Destination is the catalog a restore replaces or creates.
type Destination struct {
	Path          string   `json:"path"`
	Exists        bool     `json:"exists"`
	SchemaVersion int      `json:"schema_version,omitzero"`
	Sidecars      []string `json:"sidecars,omitempty"`
	Managed       bool     `json:"managed"`
}

// ServiceState is the managed service as the plan found it.
type ServiceState struct {
	State  string `json:"state"`
	PID    int    `json:"pid,omitzero"`
	Detail string `json:"detail,omitempty"`
}

// Migration is the schema change a restore applies to the staged copy.
type Migration struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// Result is what restore prints.
type Result struct {
	Plan           Plan              `json:"plan"`
	Outcome        string            `json:"outcome"` // restored, unchanged or rolled-back
	RecoveryBackup string            `json:"recovery_backup,omitempty"`
	Kept           []string          `json:"kept,omitempty"`
	Service        *lifecycle.Status `json:"service,omitempty"`
	Detail         string            `json:"detail,omitempty"`
}

// Restore outcomes.
const (
	Restored   = "restored"
	Unchanged  = "unchanged"
	RolledBack = "rolled-back"
)

// File operations, replaceable by tests to inject failures; afterPlan lets
// tests change files between the plan and the staging.
var (
	linkFile   = os.Link
	renameFile = os.Rename
	afterPlan  = func() {}
)

// MakePlan reports what Restore would do, and what blocks it, without
// changing any file or the service.
func MakePlan(ctx context.Context, req RestoreRequest) (p Plan) {
	p = Plan{Backup: Inspect(ctx, req.Backup), RecoveryBackupDir: req.BackupDir,
		History: "Restoring replaces the catalog with the backup's snapshot: every record written to it after the backup was taken is not in the restored catalog, and remains only in the recovery backup. Records are never merged."}
	block := func(format string, args ...any) { p.Blockers = append(p.Blockers, fmt.Sprintf(format, args...)) }
	defer func() { p.Ready = len(p.Blockers) == 0 }()
	p.SupportedSchemaVersion = p.Backup.SupportedSchemaVersion
	if !p.Backup.Valid {
		block("the backup is not valid: %v", p.Backup.Problems)
	}
	dest := req.Destination
	p.Destination.Path = dest
	if !filepath.IsAbs(dest) || !filepath.IsAbs(req.BackupDir) {
		block("the catalog %q and the backup directory %q must be absolute paths", dest, req.BackupDir)
		return p
	}
	st, err := os.Lstat(dest)
	switch {
	case err == nil && !st.Mode().IsRegular():
		block("%s is not a regular file", dest)
		return p
	case err == nil:
		p.Destination.Exists = true
		p.RequiresReplace = true
		if v, err := sqlite.CatalogSchemaVersion(ctx, dest); err != nil {
			block("cannot read the schema version of %s: %v", dest, err)
		} else {
			p.Destination.SchemaVersion = v
			if v > p.SupportedSchemaVersion {
				block("the catalog has schema version %d, newer than this polaroid supports (%d): replacing it would downgrade it; use a polaroid that supports it", v, p.SupportedSchemaVersion)
			}
		}
	case !os.IsNotExist(err):
		block("cannot read %s: %v", dest, err)
		return p
	}
	p.Destination.Sidecars = sqlite.Sidecars(dest)

	if req.Service != nil {
		s := req.Service.Status(ctx)
		if s.State != lifecycle.NotInstalled && s.Database != "" && SamePath(s.Database, dest) {
			p.Destination.Managed = true
			p.Service = &ServiceState{State: s.State, PID: s.PID, Detail: s.Detail}
			switch {
			case s.Conflict != nil:
				block("the managed service's endpoint is held by another process (%s): the catalog's users are ambiguous; resolve it with polaroid status first", s.Detail)
			case s.State != lifecycle.Running && s.State != lifecycle.Stopped:
				block("the managed service is %s (%s); restore needs it running or stopped: run polaroid stop, or fix it, and retry", s.State, s.Detail)
			}
		}
	}
	if !p.Destination.Managed && len(p.Destination.Sidecars) > 0 {
		block("%v exist beside the catalog: a process may have it open, or one ended without closing it. Restore only an offline catalog: stop every process using it; if none is running, run polaroid backup -db %q once (its last connection closes the catalog cleanly) and retry", p.Destination.Sidecars, dest)
	}

	if p.Backup.Valid && p.Backup.SchemaVersion < p.SupportedSchemaVersion {
		p.Migration = &Migration{From: p.Backup.SchemaVersion, To: p.SupportedSchemaVersion}
	}
	act := func(format string, args ...any) { p.Actions = append(p.Actions, fmt.Sprintf(format, args...)) }
	act("copy the backup's database into a private staging directory beside %s and check its SHA-256", dest)
	if p.Migration != nil {
		act("migrate the staged copy from schema %d to %d (the backup itself is not changed)", p.Migration.From, p.Migration.To)
	}
	act("check the staged copy: integrity, foreign keys, schema %d", p.SupportedSchemaVersion)
	running := p.Service != nil && p.Service.State == lifecycle.Running
	if running {
		act("stop the managed service (process %d), and abort if -wal or -shm files remain", p.Service.PID)
	}
	if p.Destination.Exists {
		act("back up the current catalog into %s (the recovery backup) and check it; abort if that fails", req.BackupDir)
		act("keep the current catalog as %s.pre-restore-<time> until the restore succeeds", dest)
		act("replace %s with the staged copy in one rename (mode 0600)", dest)
	} else {
		act("create %s from the staged copy (mode 0600)", dest)
	}
	if running {
		act("start the managed service and wait until it is healthy; if it fails, put the original catalog back and start it again")
	} else if p.Service != nil {
		act("leave the managed service stopped")
	}
	return p
}

// Restore performs the plan (ADR-0030). It returns an error wrapping
// ErrRefused when it changed nothing because the plan was blocked or the
// replacement not confirmed; any other error says in Result what was changed
// and what was kept.
func Restore(ctx context.Context, req RestoreRequest) (Result, error) {
	now := time.Now
	if req.Now != nil {
		now = req.Now
	}
	p := MakePlan(ctx, req)
	res := Result{Plan: p, Outcome: Unchanged}
	if !p.Ready {
		return res, fmt.Errorf("%w: %v", ErrRefused, p.Blockers)
	}
	if p.RequiresReplace && !req.Replace {
		return res, ErrConfirmationRequired
	}
	afterPlan()
	dest := p.Destination.Path
	dir := filepath.Dir(dest)
	stamp := now().UTC().Format("20060102T150405Z")

	if err := mkdirPrivate(dir); err != nil {
		return res, err
	}
	stage, err := os.MkdirTemp(dir, ".polaroid-restore-")
	if err != nil {
		return res, fmt.Errorf("stage the restore beside %s: %w", dest, err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	staged := filepath.Join(stage, DatabaseFile)
	if err := stageCopy(ctx, p, staged); err != nil {
		return res, fmt.Errorf("stage the backup: %w; the catalog was not changed", err)
	}

	wasRunning := false
	if p.Destination.Managed {
		st := req.Service.Status(ctx)
		switch {
		case st.State == lifecycle.Running && st.Conflict == nil:
			if s, err := req.Service.Stop(ctx); err != nil {
				res.Service = &s
				return res, fmt.Errorf("stop the managed service: %w; the catalog was not changed", err)
			}
			wasRunning = true
		case st.State != lifecycle.Stopped || st.Conflict != nil:
			return res, fmt.Errorf("%w: the managed service is now %s (%s)", ErrRefused, st.State, st.Detail)
		}
	}
	// restart undoes this run's stop when it ends without replacing the catalog.
	restart := func(cause error) error {
		if !wasRunning {
			return cause
		}
		s, err := req.Service.Start(ctx)
		res.Service = &s
		if err != nil {
			return fmt.Errorf("%w; starting the service again also failed: %w", cause, err)
		}
		return cause
	}
	if sc := sqlite.Sidecars(dest); len(sc) > 0 {
		return res, restart(fmt.Errorf("%w: %v exist beside the catalog after stopping it: a process may still have it open", ErrRefused, sc))
	}

	keep := ""
	if p.Destination.Exists {
		rb, err := Backup(ctx, BackupRequest{Source: dest, Dir: req.BackupDir, Managed: p.Destination.Managed, Reason: ReasonPreRestore, Now: now})
		if err != nil {
			return res, restart(fmt.Errorf("recovery backup of the current catalog: %w; the catalog was not changed", err))
		}
		res.RecoveryBackup = rb.Path
		if sc := sqlite.Sidecars(dest); len(sc) > 0 {
			return res, restart(fmt.Errorf("%w: %v appeared beside the catalog during its recovery backup: a process opened it", ErrRefused, sc))
		}
		keep = dest + ".pre-restore-" + stamp
		if err := linkFile(dest, keep); err != nil {
			return res, restart(fmt.Errorf("keep the current catalog as %s: %w; the catalog was not changed", keep, err))
		}
		if err := renameFile(staged, dest); err != nil {
			_ = os.Remove(keep)
			return res, restart(fmt.Errorf("replace %s: %w; the catalog was not changed", dest, err))
		}
	} else if err := linkFile(staged, dest); err != nil {
		return res, restart(fmt.Errorf("create %s: %w", dest, err))
	}
	if err := syncDir(dir); err != nil {
		res.Detail = "syncing " + dir + " failed: " + err.Error()
	}
	res.Outcome = Restored

	if wasRunning {
		s, err := req.Service.Start(ctx)
		res.Service = &s
		if err != nil {
			return rollBack(ctx, req, res, dest, keep, stamp, err)
		}
	}
	if keep != "" {
		_ = os.Remove(keep)
	}
	return res, nil
}

// rollBack puts the original catalog back after the restored one did not
// start, keeps the restored copy as evidence, and starts the service again.
func rollBack(ctx context.Context, req RestoreRequest, res Result, dest, keep, stamp string, cause error) (Result, error) {
	if keep == "" {
		return res, fmt.Errorf("the service did not start on the restored catalog: %w; there was no previous catalog to put back", cause)
	}
	failed := dest + ".failed-restore-" + stamp
	if err := linkFile(dest, failed); err != nil {
		return res, fmt.Errorf("the service did not start on the restored catalog: %w; keeping the restored copy failed (%w), so nothing was rolled back: the original is %s", cause, err, keep)
	}
	if err := renameFile(keep, dest); err != nil {
		res.Kept = []string{keep, failed, res.RecoveryBackup}
		return res, fmt.Errorf("the service did not start on the restored catalog: %w; putting the original back failed (%w): it is %s", cause, err, keep)
	}
	_ = syncDir(filepath.Dir(dest))
	res.Outcome = RolledBack
	res.Kept = []string{failed, res.RecoveryBackup}
	s, err := req.Service.Start(ctx)
	res.Service = &s
	if err != nil {
		return res, fmt.Errorf("the service did not start on the restored catalog: %w; the original catalog was put back, but the service did not start on it either: %w", cause, err)
	}
	return res, fmt.Errorf("the service did not start on the restored catalog: %w; the original catalog was put back and the service runs on it", cause)
}

// stageCopy copies the backup's database to staged (mode 0600), checks the
// copy's SHA-256 against the metadata, migrates it if the plan says so, and
// validates the result.
func stageCopy(ctx context.Context, p Plan, staged string) error {
	src := filepath.Join(p.Backup.Path, DatabaseFile)
	in, err := os.Open(src) //nolint:gosec // the backup the user named, already inspected
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := createPrivate(staged)
	if err != nil {
		return err
	}
	_, cerr := io.Copy(out, in)
	if err := errors.Join(cerr, out.Sync(), out.Close()); err != nil {
		return err
	}
	if _, sum, err := fileDigest(staged); err != nil {
		return err
	} else if sum != p.Backup.Metadata.Database.SHA256 {
		return fmt.Errorf("the staged copy's SHA-256 %s differs from the backup's %s", sum, p.Backup.Metadata.Database.SHA256)
	}
	if p.Migration != nil {
		from, to, err := sqlite.Upgrade(ctx, staged)
		if err != nil {
			return fmt.Errorf("migrate the staged copy: %w", err)
		}
		if from != p.Migration.From || to != p.Migration.To {
			return fmt.Errorf("the staged copy migrated from %d to %d, the plan said %d to %d", from, to, p.Migration.From, p.Migration.To)
		}
	}
	if sc := sqlite.Sidecars(staged); len(sc) > 0 {
		return fmt.Errorf("the staged copy left %v", sc)
	}
	ex, err := sqlite.Examine(ctx, staged)
	switch {
	case err != nil:
		return err
	case !ex.IntegrityOK():
		return fmt.Errorf("the staged copy fails its integrity check: %v", ex.Integrity)
	case ex.SchemaVersion != p.SupportedSchemaVersion:
		return fmt.Errorf("the staged copy has schema %d, want %d", ex.SchemaVersion, p.SupportedSchemaVersion)
	}
	return nil
}
