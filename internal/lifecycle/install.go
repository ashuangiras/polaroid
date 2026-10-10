package lifecycle

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ashuangiras/polaroid/internal/version"
)

// InstallOptions configure Install. Empty Addr and DB keep the installed
// values, else the defaults.
type InstallOptions struct {
	From string // directory holding the polaroid and polaroidd to install
	Addr string
	DB   string
}

// target is one file Install writes.
type target struct {
	role, path string
	mode       fs.FileMode
	content    func() (io.ReadCloser, error)
	sum        string
	existed    bool
}

// Install installs the binaries in o.From and the service definition,
// registers login startup and starts the service (ADR-0026).
func (s *Service) Install(ctx context.Context, o InstallOptions) (Status, error) {
	l := s.Layout
	fail := func(err error) (Status, error) { return s.Status(ctx), err }
	if err := s.Manager.Check(ctx); err != nil {
		return fail(err)
	}
	old, err := s.manifest()
	if err != nil {
		return fail(err)
	}
	addr, db := o.Addr, o.DB
	if old != nil {
		addr, db = cmpOr(addr, old.Addr), cmpOr(db, old.DB)
	}
	addr, db = cmpOr(addr, DefaultAddr), cmpOr(db, l.DataDB)
	if err := checkLoopback(addr); err != nil {
		return fail(err)
	}
	if !filepath.IsAbs(db) {
		return fail(fmt.Errorf("-db %q must be an absolute path", db))
	}
	from, err := filepath.Abs(o.From)
	if o.From == "" || err != nil {
		return fail(errors.New("-from is required: the directory holding the built polaroid and polaroidd, for example the checkout's bin/ after make build"))
	}
	build, err := s.sourceBuild(ctx, from)
	if err != nil {
		return fail(err)
	}
	def, err := l.Definition(addr, db)
	if err != nil {
		return fail(err)
	}
	targets := []*target{
		{role: "polaroidd", path: l.Polaroidd(), mode: 0o755, content: openFile(filepath.Join(from, "polaroidd"))},
		{role: "polaroid", path: l.Polaroid(), mode: 0o755, content: openFile(filepath.Join(from, "polaroid"))},
		{role: "definition", path: l.DefinitionPath(), mode: 0o644, content: openBytes(def)},
	}
	unchanged := old != nil && old.Addr == addr && old.DB == db
	for _, t := range targets {
		if t.sum, err = sumOf(t.content); err != nil {
			return fail(err)
		}
		existing, err := fileSHA256(t.path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			unchanged = false
		case err != nil:
			return fail(err)
		default:
			t.existed = true
			if f, ok := old.fileOrNil(t.role); !ok || f.SHA256 != existing || f.Path != t.path {
				return fail(fmt.Errorf("%w: %s; move it away, or uninstall what put it there", ErrNotOwned, t.path))
			}
			unchanged = unchanged && existing == t.sum
		}
	}
	if unchanged {
		if err := s.Manager.Register(ctx, l); err != nil {
			return fail(err)
		}
		st, err := s.Start(ctx)
		if err == nil {
			st.Action = "unchanged; " + st.Action
		}
		return st, err
	}
	if h := s.holder(ctx, addr); h != nil {
		if ms, err := s.Manager.State(ctx, l); err != nil || h.PID == 0 || h.PID != ms.PID {
			return fail(fmt.Errorf("%w: %s holds %s; stop it first, or install with another -addr", ErrConflict, describe(h), addr))
		}
	}
	ms, err := s.Manager.State(ctx, l)
	if err != nil {
		return fail(err)
	}
	wasRunning := ms.Active || ms.PID > 0
	if wasRunning {
		if err := s.stopService(ctx); err != nil {
			return fail(fmt.Errorf("stop the running service before replacing it: %w", err))
		}
	}
	if old != nil {
		if err := s.keepPrevious(targets); err != nil {
			return s.recoverOld(ctx, wasRunning, fmt.Errorf("keep the previous installation: %w", err))
		}
	}
	for _, dir := range []struct {
		path string
		mode fs.FileMode
	}{{l.BinDir, 0o755}, {l.StateDir, 0o700}, {filepath.Dir(l.DefinitionPath()), 0o755}, {l.LogDir, 0o700}} {
		if dir.path != "" {
			if err := os.MkdirAll(dir.path, dir.mode); err != nil {
				return s.recoverOld(ctx, wasRunning, err)
			}
		}
	}
	if f := l.LogFile(); f != "" {
		lf, err := os.OpenFile(filepath.Clean(f), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return s.recoverOld(ctx, wasRunning, err)
		}
		_ = lf.Close()
	}
	m := Manifest{Format: 1, Manager: s.Manager.Kind(), ServiceName: l.ServiceName, Addr: addr, DB: db, Build: build,
		InstalledAt: time.Now().UTC().Format(time.RFC3339)}
	for _, t := range targets {
		m.Files = append(m.Files, File{Role: t.role, Path: t.path, SHA256: t.sum})
	}
	manifest, err := json.Marshal(m, json.Deterministic(true))
	if err != nil {
		return s.recoverOld(ctx, wasRunning, err)
	}
	targets = append(targets, &target{role: "record", path: l.manifestPath(), mode: 0o600, content: openBytes(manifest), existed: old != nil})
	var done []*target
	for _, t := range targets {
		if err := replace(t.path, t.mode, t.content); err != nil {
			return s.rollback(ctx, done, wasRunning, fmt.Errorf("install %s: %w", t.path, err))
		}
		done = append(done, t)
	}
	if err := s.Manager.Register(ctx, l); err != nil {
		return s.rollback(ctx, done, wasRunning, fmt.Errorf("register the service: %w", err))
	}
	st, err := s.Start(ctx)
	action := "installed"
	if old != nil {
		action = "upgraded"
	}
	if err != nil {
		st.Action = action + "; start failed"
		if st.Previous != "" {
			st.Detail += fmt.Sprintf("; the previous installation is in %s: `polaroid install -from %s` reinstalls it, but it refuses a database this build has migrated (ADR-0026)", st.Previous, st.Previous)
		}
		return st, err
	}
	st.Action = action
	return st, nil
}

func (m *Manifest) fileOrNil(role string) (File, bool) {
	if m == nil {
		return File{}, false
	}
	return m.file(role)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func checkLoopback(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("-addr %q: %w", addr, err)
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil || port == "0" {
		return fmt.Errorf("-addr %q: the managed service needs a fixed port", addr)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("-addr %q: the managed service listens on loopback only (ADR-0006)", addr)
	}
	return nil
}

// sourceBuild checks that from holds a polaroid and a polaroidd of one build
// with an embedded VCS revision, and returns that build.
func (s *Service) sourceBuild(ctx context.Context, from string) (version.Build, error) {
	var builds []version.Build
	for _, c := range []struct{ name, arg string }{{"polaroidd", "-version"}, {"polaroid", "version"}} {
		path := filepath.Join(from, c.name)
		st, err := os.Stat(path)
		if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0o100 == 0 {
			return version.Build{}, fmt.Errorf("-from %s: no executable %s there; run make build first", from, c.name)
		}
		res, err := s.Runner.Run(ctx, path, c.arg)
		var b version.Build
		if err != nil || res.Code != 0 || json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &b) != nil || b.Name != c.name {
			return version.Build{}, fmt.Errorf("-from %s: %s does not report a Polaroid build (%s %s)", from, c.name, path, c.arg)
		}
		if b.Revision == "" {
			return version.Build{}, fmt.Errorf("-from %s: %s has no embedded VCS revision; build it with make build in a git checkout", from, c.name)
		}
		builds = append(builds, b)
	}
	if !version.Same(builds[0], builds[1]) {
		return version.Build{}, fmt.Errorf("-from %s: polaroidd is build %s and polaroid build %s; install a matching pair", from, builds[0].Revision, builds[1].Revision)
	}
	b := builds[0]
	b.Name = ""
	return b, nil
}

func openFile(path string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return os.Open(path) } //nolint:gosec // the operator names the source directory
}

func openBytes(b []byte) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(string(b))), nil }
}

func sumOf(content func() (io.ReadCloser, error)) (string, error) {
	r, err := content()
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()
	tmp, err := os.CreateTemp("", "polaroid-sum-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp.Name()); _ = tmp.Close() }()
	if _, err := io.Copy(tmp, r); err != nil {
		return "", err
	}
	return fileSHA256(tmp.Name())
}

// replace writes content beside path and renames it into place, so path is
// either the old file or the complete new one.
func replace(path string, mode fs.FileMode, content func() (io.ReadCloser, error)) error {
	r, err := content()
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	staged := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".staged."+strconv.Itoa(os.Getpid()))
	f, err := os.OpenFile(filepath.Clean(staged), os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(staged, mode)
	}
	if err == nil {
		err = os.Rename(staged, path)
	}
	if err != nil {
		_ = os.Remove(staged)
	}
	return err
}

// keepPrevious copies the installed files to previous/, replacing an older
// copy, so that a failed upgrade can be recovered by installing from there.
func (s *Service) keepPrevious(targets []*target) error {
	prev := s.Layout.previousDir()
	if err := os.RemoveAll(prev); err != nil {
		return err
	}
	if err := os.MkdirAll(prev, 0o700); err != nil {
		return err
	}
	for _, t := range append(targets, &target{path: s.Layout.manifestPath(), mode: 0o600, existed: true}) {
		if !t.existed {
			continue
		}
		if err := replace(filepath.Join(prev, filepath.Base(t.path)), t.mode, openFile(t.path)); err != nil {
			return err
		}
	}
	return nil
}

// rollback restores the files replaced so far from previous/, removes new
// ones, and restarts the old service if it ran.
func (s *Service) rollback(ctx context.Context, done []*target, wasRunning bool, cause error) (Status, error) {
	var problems []string
	for i := len(done) - 1; i >= 0; i-- {
		t := done[i]
		var err error
		if t.existed {
			err = replace(t.path, t.mode, openFile(filepath.Join(s.Layout.previousDir(), filepath.Base(t.path))))
		} else {
			err = os.Remove(t.path)
		}
		if err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		return s.Status(ctx), fmt.Errorf("%w; rolling back also failed (%s); the previous installation is in %s", cause, strings.Join(problems, "; "), s.Layout.previousDir())
	}
	return s.recoverOld(ctx, wasRunning, fmt.Errorf("%w; the previous installation was restored", cause))
}

// recoverOld re-registers and restarts the old installation if it ran.
func (s *Service) recoverOld(ctx context.Context, wasRunning bool, cause error) (Status, error) {
	if old, _ := s.manifest(); old != nil {
		_ = s.Manager.Register(ctx, s.Layout)
		if wasRunning {
			if _, err := s.Start(ctx); err != nil {
				cause = fmt.Errorf("%w; restarting it failed: %w", cause, err)
			}
		}
	} else {
		_ = s.Manager.Reload(ctx)
	}
	return s.Status(ctx), cause
}

// Uninstall stops and unregisters the service and removes what the
// installation wrote: the definition, the binaries, the macOS log files and
// the installation state. ~/.polaroid is never touched.
func (s *Service) Uninstall(ctx context.Context) (Status, error) {
	l := s.Layout
	m, err := s.manifest()
	if err != nil {
		return Status{State: Failed, Detail: err.Error()}, err
	}
	if m == nil {
		st := s.Status(ctx)
		st.Action = "nothing to uninstall"
		return st, nil
	}
	if err := s.Manager.Check(ctx); err != nil {
		return s.Status(ctx), err
	}
	if err := s.stopService(ctx); err != nil {
		return s.Status(ctx), err
	}
	var kept []string
	owned := func(role string) (string, bool) {
		f, ok := m.file(role)
		if !ok {
			return "", false
		}
		sum, err := fileSHA256(f.Path)
		if errors.Is(err, fs.ErrNotExist) {
			return "", false
		}
		if err != nil || sum != f.SHA256 {
			kept = append(kept, "changed since installation, left in place: "+f.Path)
			return "", false
		}
		return f.Path, true
	}
	if p, ok := owned("definition"); ok {
		if err := s.Manager.Unregister(ctx, l); err != nil {
			return s.Status(ctx), err
		}
		if err := os.Remove(p); err != nil {
			return s.Status(ctx), err
		}
		if err := s.Manager.Reload(ctx); err != nil {
			return s.Status(ctx), err
		}
	}
	for _, role := range []string{"polaroid", "polaroidd"} {
		if p, ok := owned(role); ok {
			if err := os.Remove(p); err != nil {
				return s.Status(ctx), err
			}
		}
	}
	if f := l.LogFile(); f != "" {
		_ = os.Remove(f)
		_ = os.Remove(f + ".1")
		_ = os.Remove(l.LogDir)
	}
	if err := os.RemoveAll(l.StateDir); err != nil {
		return s.Status(ctx), err
	}
	st := s.Status(ctx)
	st.Action = "uninstalled; " + filepath.Dir(m.DB) + " and " + filepath.Join(l.Home, ".polaroid", "backups") + " are kept"
	// A file kept because it changed is not also reported as foreign.
	problems := kept
	for _, p := range st.Problems {
		if !slices.ContainsFunc(kept, func(k string) bool {
			return strings.HasSuffix(k, ": "+strings.TrimPrefix(p, "not written by this installation, left alone: "))
		}) {
			problems = append(problems, p)
		}
	}
	st.Problems = problems
	return st, nil
}
