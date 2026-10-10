package lifecycle

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ashuangiras/polaroid/internal/version"
)

// These tests run the stand-in manager of helpers_test.go, which executes the
// written definition but is no evidence of launchd or systemd behaviour; the
// real-manager recovery check is scripts/lifecycle-check.sh (make lifecycle).

func reportedBuild(t *testing.T, path string) version.Build {
	t.Helper()
	arg := "version"
	if filepath.Base(path) == "polaroidd" {
		arg = "-version"
	}
	res, err := ExecRunner{}.Run(context.Background(), path, arg)
	var b version.Build
	if err != nil || res.Code != 0 || json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &b) != nil {
		t.Fatalf("%s %s: %v %+v", path, arg, err, res)
	}
	return b
}

// installation is what an install leaves: binary and definition hashes,
// the record's bytes and the recovery copy.
type installation struct {
	files    map[string]string
	record   []byte
	previous map[string]string
}

func snapshot(t *testing.T, l Layout) installation {
	t.Helper()
	in := installation{files: map[string]string{}, previous: map[string]string{}}
	for _, p := range []string{l.Polaroid(), l.Polaroidd(), l.DefinitionPath()} {
		in.files[p] = sha(t, p)
	}
	var err error
	if in.record, err = os.ReadFile(l.manifestPath()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(l.previousDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		in.previous[e.Name()] = sha(t, filepath.Join(l.previousDir(), e.Name()))
	}
	return in
}

func (in installation) equal(o installation) bool {
	return bytes.Equal(in.record, o.record) && maps(in.files, o.files) && maps(in.previous, o.previous)
}

func maps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// noStaging fails if anything but the record and previous/ is left in the
// state directory, or anything at all in the test's temporary directory.
func noStaging(t *testing.T, l Layout, tmp string) {
	t.Helper()
	var left []string
	for dir, allowed := range map[string][]string{l.StateDir: {"install.json", "previous"}, tmp: nil} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if !slices.Contains(allowed, e.Name()) {
				left = append(left, filepath.Join(dir, e.Name()))
			}
		}
	}
	if len(left) > 0 {
		t.Fatalf("staging left behind: %v", left)
	}
}

func TestRecoverFromPrevious(t *testing.T) {
	a := builds[0]
	revA := reportedBuild(t, filepath.Join(a, "polaroidd")).Revision
	if revA == "" || revA == reportedBuild(t, filepath.Join(builds[1], "polaroidd")).Revision {
		t.Fatalf("builds A and B must have distinct revisions (A %q)", revA)
	}
	for name, tc := range map[string]struct {
		upgrade string
		source  func(t *testing.T, l Layout) string
	}{
		"the documented command, after B ran": {builds[1], func(_ *testing.T, l Layout) string { return l.previousDir() }},
		"after B failed to start":             {broken, func(_ *testing.T, l Layout) string { return l.previousDir() }},
		"a symlink with spaces to previous/": {builds[1], func(t *testing.T, l Layout) string {
			link := filepath.Join(t.TempDir(), "recovery link")
			if err := os.Symlink(l.previousDir(), link); err != nil {
				t.Fatal(err)
			}
			return link
		}},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			s, mgr := newTestService(t)
			l := s.Layout
			addr := freeAddr(t)
			mustState(t, firstOf(s.Install(ctx, InstallOptions{From: a, Addr: addr})))
			if code, body := call(t, http.MethodPost, "http://"+addr+"/v1/procedures", procedure); code != http.StatusCreated {
				t.Fatalf("create: %d %s", code, body)
			}
			_, before := call(t, http.MethodGet, "http://"+addr+"/v1/procedures/by-key/lifecycle.check", "")

			st, err := s.Install(ctx, InstallOptions{From: tc.upgrade})
			if tc.upgrade == broken {
				if !errors.Is(err, ErrStartFailed) || st.State != Failed || !strings.Contains(st.Detail, "polaroid install -from "+l.previousDir()) {
					t.Fatalf("upgrade to a build that cannot start: %+v, %v", st, err)
				}
			} else {
				mustBe(t, st, err, Running)
			}

			st = mustState(t, firstOf(s.Install(ctx, InstallOptions{From: tc.source(t, l)})))
			for _, n := range []string{"polaroid", "polaroidd"} {
				installed := filepath.Join(l.BinDir, n)
				if sha(t, installed) != sha(t, filepath.Join(a, n)) {
					t.Errorf("the installed %s is not build A", n)
				}
				if got := reportedBuild(t, installed).Revision; got != revA {
					t.Errorf("the installed %s reports %s, want A %s", n, got, revA)
				}
				if sha(t, filepath.Join(l.previousDir(), n)) != sha(t, filepath.Join(tc.upgrade, n)) {
					t.Errorf("previous/%s is not the build recovered from", n)
				}
			}
			if st.Build == nil || st.Build.Revision != revA {
				t.Errorf("status reports build %+v, want A %s", st.Build, revA)
			}
			m, err := s.manifest()
			if err != nil || m.Build.Revision != revA {
				t.Fatalf("record: %+v, %v", m, err)
			}
			for _, f := range m.Files {
				if f.SHA256 != sha(t, f.Path) {
					t.Errorf("the record's %s hash does not describe %s", f.Role, f.Path)
				}
			}
			if got := mgr.executed[len(mgr.executed)-1]; got != sha(t, filepath.Join(a, "polaroidd")) {
				t.Errorf("the restarted daemon is not build A")
			}
			if st := s.Status(ctx); len(st.Problems) != 0 {
				t.Errorf("status problems: %v", st.Problems)
			}
			if _, after := call(t, http.MethodGet, "http://"+addr+"/v1/procedures/by-key/lifecycle.check", ""); after != before {
				t.Errorf("the record changed: %s\nwant %s", after, before)
			}
			noStaging(t, l, tmp)
		})
	}
}

func TestFailedInstallLeavesInstallationAndRecoveryUnchanged(t *testing.T) {
	pair := func(t *testing.T, polaroidd, polaroid string) string {
		dir := filepath.Join(t.TempDir(), "source with spaces")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for n, from := range map[string]string{"polaroidd": polaroidd, "polaroid": polaroid} {
			b, err := os.ReadFile(filepath.Join(from, n))
			if err == nil {
				err = os.WriteFile(filepath.Join(dir, n), b, 0o755)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	for name, tc := range map[string]struct {
		source  func(t *testing.T, l Layout) string
		stops   bool // the failure happens after the service was stopped
		wantErr string
	}{
		"mismatched pair": {func(t *testing.T, _ Layout) string { return pair(t, builds[0], broken) }, false, "install a matching pair"},
		"unreadable source": {func(t *testing.T, _ Layout) string {
			dir := pair(t, builds[0], builds[0])
			if err := os.Chmod(filepath.Join(dir, "polaroidd"), 0o311); err != nil {
				t.Fatal(err)
			}
			return dir
		}, false, "permission denied"},
		"previous/ cannot be rotated": {func(t *testing.T, l Layout) string {
			if err := os.Chmod(l.StateDir, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(l.StateDir, 0o700) })
			return builds[0]
		}, true, "keep the previous installation"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			s, mgr := newTestService(t)
			l := s.Layout
			addr := freeAddr(t)
			mustState(t, firstOf(s.Install(ctx, InstallOptions{From: builds[0], Addr: addr})))
			if code, body := call(t, http.MethodPost, "http://"+addr+"/v1/procedures", procedure); code != http.StatusCreated {
				t.Fatalf("create: %d %s", code, body)
			}
			_, before := call(t, http.MethodGet, "http://"+addr+"/v1/procedures/by-key/lifecycle.check", "")
			running := mustState(t, firstOf(s.Install(ctx, InstallOptions{From: builds[1]})))
			want := snapshot(t, l)
			stops := mgr.stops

			st, err := s.Install(ctx, InstallOptions{From: tc.source(t, l)})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("install: %v, want %q", err, tc.wantErr)
			}
			_ = os.Chmod(l.StateDir, 0o700)
			if got := snapshot(t, l); !got.equal(want) {
				t.Errorf("the installation or previous/ changed:\n%+v\nwant %+v", got, want)
			}
			if st.State != Running || (!tc.stops && (st.PID != running.PID || mgr.stops != stops)) {
				t.Errorf("after the failure: %+v, %d stops (was %d)", st, mgr.stops, stops)
			}
			if _, after := call(t, http.MethodGet, "http://"+addr+"/v1/procedures/by-key/lifecycle.check", ""); after != before {
				t.Errorf("the record changed: %s\nwant %s", after, before)
			}
			noStaging(t, l, tmp)
		})
	}
}
