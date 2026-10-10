package lifecycle

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
)

const managedNote = "Managed by `polaroid install` (ADR-0026). Change it with install options, not by hand: " +
	"install and uninstall refuse a definition that differs from the one they wrote."

// Definition returns the service definition for l running polaroidd on addr
// with db.
func (l Layout) Definition(addr, db string) ([]byte, error) {
	args := l.Args(addr, db)
	for _, s := range append(args, l.Home) {
		if strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return nil, fmt.Errorf("path %q contains a control character", s)
		}
	}
	if l.GOOS == "darwin" {
		return launchdPlist(l, args), nil
	}
	return systemdUnit(l, args), nil
}

func launchdPlist(l Layout, args []string) []byte {
	var b bytes.Buffer
	str := func(s string) string {
		var e bytes.Buffer
		_ = xml.EscapeText(&e, []byte(s))
		return "<string>" + e.String() + "</string>"
	}
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- ` + managedNote + ` -->
<plist version="1.0">
<dict>
	<key>Label</key>
	` + str(l.Label()) + `
	<key>ProgramArguments</key>
	<array>
`)
	for _, a := range args {
		b.WriteString("\t\t" + str(a) + "\n")
	}
	b.WriteString(`	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>HOME</key>
		` + str(l.Home) + `
	</dict>
	<key>WorkingDirectory</key>
	<string>/</string>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>ExitTimeOut</key>
	<integer>20</integer>
	<key>Umask</key>
	<integer>63</integer>
	<key>ProcessType</key>
	<string>Standard</string>
	<key>StandardOutPath</key>
	` + str(l.LogFile()) + `
	<key>StandardErrorPath</key>
	` + str(l.LogFile()) + `
</dict>
</plist>
`)
	return b.Bytes()
}

// systemdQuote quotes s as one item of a unit setting (systemd.syntax(7)):
// double quotes with C-style escapes, %% for a literal percent sign (a
// specifier otherwise), and, in command lines, $$ for a literal dollar sign.
func systemdQuote(s string, command bool) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '"':
			b.WriteString(`\"`)
		case r == '%':
			b.WriteString("%%")
		case r == '$' && command:
			b.WriteString("$$")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func systemdUnit(l Layout, args []string) []byte {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = systemdQuote(a, true)
	}
	return []byte(`# ` + managedNote + `
[Unit]
Description=Polaroid procedural memory service (polaroidd)
Documentation=https://github.com/ashuangiras/polaroid
StartLimitIntervalSec=60
StartLimitBurst=5

[Service]
Type=exec
ExecStart=` + strings.Join(quoted, " ") + `
Environment=` + systemdQuote("HOME="+l.Home, false) + `
WorkingDirectory=/
UMask=0077
Restart=on-failure
RestartSec=2
TimeoutStopSec=20

[Install]
WantedBy=default.target
`)
}
