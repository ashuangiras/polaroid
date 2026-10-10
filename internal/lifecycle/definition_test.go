package lifecycle

import (
	"context"
	json "encoding/json/v2"
	"encoding/xml"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// awkward exercises quoting: spaces, quotes, backslashes, XML and shell
// metacharacters, and systemd's specifier and variable characters.
const awkward = `/tmp/home with spaces/a "q" \b <x>&y %h $HOME`

func TestLayoutRejectsUnsupportedPlatformsAndBadNames(t *testing.T) {
	if _, err := NewLayout("windows", "/home/u", DefaultServiceName); !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "run polaroidd directly") {
		t.Fatalf("windows: %v", err)
	}
	if _, err := NewManager("freebsd", ExecRunner{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("freebsd manager: %v", err)
	}
	if _, err := Open("plan9", "/home/u", ExecRunner{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("plan9 open: %v", err)
	}
	for _, name := range []string{"", "polaroid ", "other", "polaroid_x", "polaroid-", "polaroid-A"} {
		if _, err := NewLayout("linux", "/home/u", name); err == nil {
			t.Errorf("service name %q accepted", name)
		}
	}
	if _, err := NewLayout("linux", "relative", DefaultServiceName); err == nil {
		t.Error("a relative home was accepted")
	}
	if _, err := NewLayout("linux", "/home/u", "polaroid-check-42"); err != nil {
		t.Error(err)
	}
}

// decodePlist decodes a property list's top-level dict with encoding/xml:
// dict to map, array to slice, string, integer and booleans.
func decodePlist(t *testing.T, plist []byte) map[string]any {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(string(plist)))
	d.Strict = true
	var value func(start xml.StartElement) any
	value = func(start xml.StartElement) any {
		switch start.Name.Local {
		case "dict", "array":
			m, a, key := map[string]any{}, []any{}, ""
			for {
				tok, err := d.Token()
				if err != nil {
					t.Fatalf("plist: %v", err)
				}
				switch v := tok.(type) {
				case xml.EndElement:
					if start.Name.Local == "dict" {
						return m
					}
					return a
				case xml.StartElement:
					x := value(v)
					switch {
					case v.Name.Local == "key":
						key = x.(string)
					case start.Name.Local == "dict":
						m[key] = x
					default:
						a = append(a, x)
					}
				}
			}
		case "true", "false":
			_ = d.Skip()
			return start.Name.Local == "true"
		default: // key, string, integer
			var s string
			if err := d.DecodeElement(&s, &start); err != nil {
				t.Fatalf("plist: %v", err)
			}
			return s
		}
	}
	for {
		tok, err := d.Token()
		if err != nil {
			t.Fatalf("plist has no dict: %v", err)
		}
		if start, ok := tok.(xml.StartElement); ok && start.Name.Local == "dict" {
			return value(start).(map[string]any)
		}
	}
}

func strings_(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestLaunchdDefinition(t *testing.T) {
	l, err := NewLayout("darwin", awkward, "polaroid-check-1")
	if err != nil {
		t.Fatal(err)
	}
	def, err := l.Definition("127.0.0.1:7499", filepath.Join(awkward, "db dir", "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(awkward, ".local/bin/polaroidd"), "-addr", "127.0.0.1:7499", "-db", filepath.Join(awkward, "db dir", "c.db"),
		"-log-file", filepath.Join(awkward, "Library/Logs/Polaroid/polaroidd.log")}
	p := decodePlist(t, def)
	if got := strings_(p["ProgramArguments"]); !slices.Equal(got, want) {
		t.Fatalf("ProgramArguments =\n%q\nwant\n%q", got, want)
	}
	log := filepath.Join(awkward, "Library/Logs/Polaroid/polaroidd.log")
	for k, v := range map[string]any{"Label": "io.github.ashuangiras.polaroid-check-1", "RunAtLoad": true,
		"KeepAlive": map[string]any{"SuccessfulExit": false}, "ThrottleInterval": "10", "ExitTimeOut": "20", "Umask": "63",
		"WorkingDirectory": "/", "EnvironmentVariables": map[string]any{"HOME": awkward}, "StandardOutPath": log, "StandardErrorPath": log,
		"ProcessType": "Standard"} {
		if got := p[k]; !reflect.DeepEqual(got, v) {
			t.Errorf("%s = %#v, want %#v", k, got, v)
		}
	}
	if len(p) != 12 {
		t.Errorf("the definition has %d keys, want 12: %v", len(p), p)
	}
	// The real property-list parser, where there is one.
	if _, err := exec.LookPath("plutil"); err == nil && runtime.GOOS == "darwin" {
		f := filepath.Join(t.TempDir(), "d.plist")
		if err := os.WriteFile(f, def, 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.CommandContext(context.Background(), "plutil", "-extract", "ProgramArguments", "json", "-o", "-", f).Output()
		var got []string
		if err != nil || json.Unmarshal(out, &got) != nil || !slices.Equal(got, want) {
			t.Fatalf("plutil reads ProgramArguments as %s (%v)\nwant %q", out, err, want)
		}
		t.Log("plutil agrees")
	}
}

// unquoteSystemd splits a unit command line by systemd.syntax(7) and
// systemd.service(5): double-quoted items with C escapes, %% and $$.
func unquoteSystemd(t *testing.T, line string) []string {
	t.Helper()
	var out []string
	for line = strings.TrimSpace(line); line != ""; line = strings.TrimSpace(line) {
		if line[0] != '"' {
			t.Fatalf("unquoted item in %q", line)
		}
		var b strings.Builder
		i := 1
		for ; i < len(line) && line[i] != '"'; i++ {
			c := line[i]
			switch {
			case c == '\\' && i+1 < len(line):
				i++
				b.WriteByte(map[byte]byte{'\\': '\\', '"': '"', 's': ' '}[line[i]])
			case (c == '%' || c == '$') && i+1 < len(line) && line[i+1] == c:
				i++
				b.WriteByte(c)
			case c == '%' || c == '$':
				t.Fatalf("unescaped %c in %q", c, line)
			default:
				b.WriteByte(c)
			}
		}
		out = append(out, b.String())
		line = line[i+1:]
	}
	return out
}

func TestSystemdDefinition(t *testing.T) {
	l, err := NewLayout("linux", awkward, DefaultServiceName)
	if err != nil {
		t.Fatal(err)
	}
	def, err := l.Definition("127.0.0.1:7417", l.DataDB)
	if err != nil {
		t.Fatal(err)
	}
	settings := map[string]string{}
	for _, line := range strings.Split(string(def), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, "#") {
			settings[k] = v
		}
	}
	want := []string{filepath.Join(awkward, ".local/bin/polaroidd"), "-addr", "127.0.0.1:7417", "-db", filepath.Join(awkward, ".polaroid/data/polaroid.db")}
	if got := unquoteSystemd(t, settings["ExecStart"]); !slices.Equal(got, want) {
		t.Fatalf("ExecStart =\n%q\nwant\n%q", got, want)
	}
	if !strings.Contains(settings["Environment"], `$HOME"`) || strings.Contains(settings["Environment"], "$$") {
		t.Errorf("Environment= expands no variables, so $ stays single: %s", settings["Environment"])
	}
	for k, v := range map[string]string{"Type": "exec", "Restart": "on-failure", "RestartSec": "2", "StartLimitBurst": "5",
		"StartLimitIntervalSec": "60", "TimeoutStopSec": "20", "UMask": "0077", "WantedBy": "default.target", "WorkingDirectory": "/"} {
		if settings[k] != v {
			t.Errorf("%s=%q, want %q", k, settings[k], v)
		}
	}
	if l.LogFile() != "" || strings.Contains(string(def), "log-file") {
		t.Error("the Linux service logs to the journal, not a file")
	}
	// The real unit parser, where there is one (Linux CI).
	if _, err := exec.LookPath("systemd-analyze"); err == nil && runtime.GOOS == "linux" {
		home := filepath.Join(t.TempDir(), "home with spaces")
		l, _ := NewLayout("linux", home, DefaultServiceName)
		def, _ := l.Definition("127.0.0.1:7417", l.DataDB)
		if err := os.MkdirAll(l.BinDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(l.Polaroidd(), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		unit := filepath.Join(t.TempDir(), l.Unit())
		if err := os.WriteFile(unit, def, 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.CommandContext(context.Background(), "systemd-analyze", "--user", "verify", unit).CombinedOutput(); err != nil {
			t.Fatalf("systemd-analyze verify: %v\n%s", err, out)
		}
	}
}

func TestDefinitionsRefuseControlCharacters(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		l, _ := NewLayout(goos, "/home/a\nb", DefaultServiceName)
		if _, err := l.Definition(DefaultAddr, "/x.db"); err == nil {
			t.Errorf("%s: a newline in a path was accepted", goos)
		}
	}
}
