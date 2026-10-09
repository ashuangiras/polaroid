// Command polaroid is a generic command-line client for the polaroidd API.
//
// Every response body (JSON) is written to stdout, on success and on failure,
// so scripts can always parse the result. A one-line diagnostic goes to
// stderr on failure. Exit status: 0 success, 1 request failed, 2 usage error.
package main

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2

	defaultServer = "http://127.0.0.1:7417"
)

type command struct {
	name     string
	args     string
	summary  string
	min, max int
	run      func(c *client, args []string) error
}

var commands = []command{
	{"health", "", "check that the daemon and its storage are available", 0, 0,
		func(c *client, _ []string) error { return c.do(http.MethodGet, "/healthz", nil) }},
	{"list", "", "list procedures", 0, 0,
		func(c *client, _ []string) error { return c.do(http.MethodGet, "/v1/procedures", nil) }},
	{"create", "[FILE]", "create a procedure from request JSON in FILE or stdin", 0, 1,
		func(c *client, args []string) error { return c.send(http.MethodPost, "/v1/procedures", args) }},
	{"get", "ID", "show a procedure and its full version history", 1, 1,
		func(c *client, args []string) error { return c.do(http.MethodGet, procedurePath(args[0]), nil) }},
	{"get-by-key", "KEY", "show a procedure, looked up by canonical key", 1, 1,
		func(c *client, args []string) error {
			return c.do(http.MethodGet, "/v1/procedures/by-key/"+url.PathEscape(args[0]), nil)
		}},
	{"get-version", "ID N", "show version N of a procedure", 2, 2,
		func(c *client, args []string) error {
			return c.do(http.MethodGet, procedurePath(args[0])+"/versions/"+url.PathEscape(args[1]), nil)
		}},
	{"graph", "ID N [REPO ENV]", "show version N's composition graph, resolved in REPO and ENV if given", 2, 4,
		func(c *client, args []string) error {
			path := procedurePath(args[0]) + "/versions/" + url.PathEscape(args[1]) + "/graph"
			switch len(args) {
			case 3:
				return usageError("graph needs both REPO and ENV, or neither")
			case 4:
				path += "?" + url.Values{"repository": {args[2]}, "environment": {args[3]}}.Encode()
			}
			return c.do(http.MethodGet, path, nil)
		}},
	{"revise", "ID [FILE]", "append a version from request JSON in FILE or stdin", 1, 2,
		func(c *client, args []string) error {
			return c.send(http.MethodPost, procedurePath(args[0])+"/versions", args[1:])
		}},
	{"bindings", "REPOSITORY", "list the bindings of a repository", 1, 1,
		func(c *client, args []string) error {
			return c.do(http.MethodGet, "/v1/bindings?"+url.Values{"repository": {args[0]}}.Encode(), nil)
		}},
	{"bind", "[FILE]", "create a binding from request JSON in FILE or stdin", 0, 1,
		func(c *client, args []string) error { return c.send(http.MethodPost, "/v1/bindings", args) }},
	{"get-binding", "ID", "show a binding and its full revision history", 1, 1,
		func(c *client, args []string) error { return c.do(http.MethodGet, bindingPath(args[0]), nil) }},
	{"get-binding-revision", "ID N", "show revision N of a binding", 2, 2,
		func(c *client, args []string) error {
			return c.do(http.MethodGet, bindingPath(args[0])+"/revisions/"+url.PathEscape(args[1]), nil)
		}},
	{"revise-binding", "ID [FILE]", "append a binding revision from request JSON in FILE or stdin", 1, 2,
		func(c *client, args []string) error {
			return c.send(http.MethodPost, bindingPath(args[0])+"/revisions", args[1:])
		}},
	{"resolve", "BINDING_ID ENV", "resolve a binding's latest revision in environment ENV", 2, 2,
		func(c *client, args []string) error {
			return c.do(http.MethodGet, bindingPath(args[0])+"/resolution?"+url.Values{"environment": {args[1]}}.Encode(), nil)
		}},
	{"record", "[FILE]", "record a finished execution from request JSON in FILE or stdin", 0, 1,
		func(c *client, args []string) error { return c.send(http.MethodPost, "/v1/executions", args) }},
	{"get-execution", "ID", "show one execution with its inputs and evidence", 1, 1,
		func(c *client, args []string) error {
			return c.do(http.MethodGet, "/v1/executions/"+url.PathEscape(args[0]), nil)
		}},
	{"executions", "PROCEDURE_ID [REPOSITORY]", "list a procedure's executions, optionally in one repository", 1, 2,
		func(c *client, args []string) error {
			query := url.Values{"procedure_id": {args[0]}}
			if len(args) == 2 {
				query.Set("repository", args[1])
			}
			return c.do(http.MethodGet, "/v1/executions?"+query.Encode(), nil)
		}},
	{"verification", "ID", "show whether an execution is verified, and its combination", 1, 1,
		func(c *client, args []string) error {
			return c.do(http.MethodGet, "/v1/executions/"+url.PathEscape(args[0])+"/verification", nil)
		}},
	{"verifications", "ID N [REPO [COMMIT [ENV]]]", "list version N's combinations and their status", 2, 5,
		func(c *client, args []string) error {
			query := url.Values{}
			for i, name := range []string{"repository", "commit", "environment"} {
				if len(args) > i+2 {
					query.Set(name, args[i+2])
				}
			}
			path := procedurePath(args[0]) + "/versions/" + url.PathEscape(args[1]) + "/verifications"
			if len(query) > 0 {
				path += "?" + query.Encode()
			}
			return c.do(http.MethodGet, path, nil)
		}},
}

func usage() string {
	var b strings.Builder
	b.WriteString("Usage: polaroid [-server URL] [-timeout DURATION] COMMAND [ARGUMENTS]\n\nCommands:\n")
	for _, cmd := range commands {
		fmt.Fprintf(&b, "  %-40s %s\n", strings.TrimSpace(cmd.name+" "+cmd.args), cmd.summary)
	}
	fmt.Fprintf(&b, "  %-40s %s\n", "help", "show this help")
	fmt.Fprintf(&b, "\nThe server is -server, else $POLAROID_URL, else %s.\n", defaultServer)
	b.WriteString("Each response body is printed to stdout, also when the request fails.\n")
	b.WriteString("Exit status: 0 success, 1 request failed, 2 usage error.\n")
	return b.String()
}

// usageError marks mistakes in how the command was invoked.
type usageError string

func (e usageError) Error() string { return string(e) }

type client struct {
	base   string
	http   *http.Client
	stdin  io.Reader
	stdout io.Writer
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	server := defaultServer
	if v := getenv("POLAROID_URL"); v != "" {
		server = v
	}
	fs := flag.NewFlagSet("polaroid", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage()) }
	fs.StringVar(&server, "server", server, "")
	timeout := fs.Duration("timeout", 30*time.Second, "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return exitUsage
	}
	if fs.Arg(0) == "help" {
		fmt.Fprint(stdout, usage())
		return exitOK
	}

	err := dispatch(server, *timeout, fs.Arg(0), fs.Args()[1:], stdin, stdout)
	var invalid usageError
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &invalid):
		fmt.Fprintf(stderr, "polaroid: %v\nRun 'polaroid help' for usage.\n", err)
		return exitUsage
	default:
		fmt.Fprintf(stderr, "polaroid: %v\n", err)
		return exitFailure
	}
}

func dispatch(server string, timeout time.Duration, name string, args []string, stdin io.Reader, stdout io.Writer) error {
	u, err := url.Parse(server)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return usageError(fmt.Sprintf("invalid server URL %q: want http://host:port", server))
	}
	for _, cmd := range commands {
		if cmd.name != name {
			continue
		}
		if len(args) < cmd.min || len(args) > cmd.max {
			return usageError(strings.TrimSpace("usage: polaroid " + cmd.name + " " + cmd.args))
		}
		c := &client{base: strings.TrimRight(server, "/"), http: &http.Client{Timeout: timeout}, stdin: stdin, stdout: stdout}
		return cmd.run(c, args)
	}
	return usageError(fmt.Sprintf("unknown command %q", name))
}

// send posts the request JSON named by args (a file, or stdin when args is
// empty or "-").
func (c *client) send(method, path string, args []string) error {
	body, err := c.readRequest(args)
	if err != nil {
		return err
	}
	return c.do(method, path, body)
}

func (c *client) readRequest(args []string) ([]byte, error) {
	if len(args) == 1 && args[0] != "-" {
		b, err := os.ReadFile(args[0]) //nolint:gosec // Reading the file the user named is this command's purpose.
		if err != nil {
			return nil, fmt.Errorf("read request: %w", err)
		}
		return b, nil
	}
	if f, ok := c.stdin.(*os.File); ok {
		if info, err := f.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			return nil, usageError("no request JSON: pass a FILE argument or pipe JSON to stdin")
		}
	}
	b, err := io.ReadAll(c.stdin)
	if err != nil {
		return nil, fmt.Errorf("read request from stdin: %w", err)
	}
	return b, nil
}

// do sends one request, copies the response body to stdout, and returns an
// error unless the status is 2xx.
func (c *client) do(method, path string, body []byte) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, c.base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if _, err := c.stdout.Write(out); err != nil {
		return fmt.Errorf("write response: %w", err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("%s %s: %s%s", method, path, resp.Status, describeError(out))
}

// describeError summarises an API error body, if it is one.
func describeError(body []byte) string {
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil || e.Error.Code == "" {
		return ""
	}
	return fmt.Sprintf(": %s: %s", e.Error.Code, e.Error.Message)
}

func procedurePath(id string) string {
	return "/v1/procedures/" + url.PathEscape(id)
}

func bindingPath(id string) string {
	return "/v1/bindings/" + url.PathEscape(id)
}
