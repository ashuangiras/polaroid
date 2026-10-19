// Command polaroid is a generic command-line client for the polaroidd API,
// with local commands for this user's installation, service and catalog
// backups (ADR-0026, ADR-0030).
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

// queryCommands take trailing NAME=VALUE query parameters, such as filters,
// limit and after (ADR-0021); run receives them in client.query, and min
// and max count only the other arguments.
var queryCommands = map[string]bool{"list": true, "discover": true, "bindings": true, "executions": true, "feedbacks": true, "repositories": true, "toil-items": true, "knowledge-activations": true, "walkthroughs": true}

var commands = []command{
	{"health", "", "check that the daemon and its storage are available", 0, 0,
		func(c *client, _ []string) error { return c.do(http.MethodGet, "/healthz", nil) }},
	{"statistics", "", "show read-only catalog statistics: record counts, outcome and evidence status, payload sizes, storage", 0, 0,
		func(c *client, _ []string) error { return c.do(http.MethodGet, "/v1/statistics", nil) }},
	{"list", "[NAME=VALUE...]", "list procedures; NAME is repository, scope, q, lifecycle (active, the default, retired or all), limit, after or snapshot", 0, 0,
		func(c *client, _ []string) error { return c.do(http.MethodGet, c.path("/v1/procedures", nil), nil) }},
	{"discover", "TASK [NAME=VALUE...]", "find procedures for a task described in words, by shared words (lexical), ranked and explained; NAME is repository, lifecycle or limit", 1, 1,
		func(c *client, args []string) error {
			return c.do(http.MethodGet, c.path("/v1/procedures/discovery", url.Values{"task": {args[0]}}), nil)
		}},
	{"duplicates", "[FILE]", "check a proposed procedure in FILE or stdin for an existing canonical key and overlapping procedures; stores nothing", 0, 1,
		func(c *client, args []string) error {
			return c.send(http.MethodPost, "/v1/procedures/duplicates", args)
		}},
	{"create", "[FILE]", "create a procedure from request JSON in FILE or stdin", 0, 1,
		func(c *client, args []string) error { return c.send(http.MethodPost, "/v1/procedures", args) }},
	{"get", "ID", "show a procedure and its full version history", 1, 1,
		func(c *client, args []string) error { return c.do(http.MethodGet, procedurePath(args[0]), nil) }},
	{"get-by-key", "KEY", "show a procedure, looked up by canonical key", 1, 1,
		func(c *client, args []string) error {
			return c.do(http.MethodGet, "/v1/procedures/by-key/"+url.PathEscape(args[0]), nil)
		}},
	{"origin", "ID [FILE]", "record a procedure's origin, once, from request JSON in FILE or stdin", 1, 2,
		func(c *client, args []string) error {
			return c.send(http.MethodPost, procedurePath(args[0])+"/origin", args[1:])
		}},
	{"get-version", "ID N", "show version N of a procedure", 2, 2,
		func(c *client, args []string) error {
			return c.do(http.MethodGet, procedurePath(args[0])+"/versions/"+url.PathEscape(args[1]), nil)
		}},
	{"get-procedure-lifecycle", "ID", "show a procedure's lifecycle state (active or retired) and every retirement and reactivation", 1, 1,
		func(c *client, args []string) error {
			return c.do(http.MethodGet, procedurePath(args[0])+"/lifecycle/events", nil)
		}},
	{"retire-procedure", "ID [FILE]", "retire a procedure, from request JSON (base_event, reason, actor) in FILE or stdin", 1, 2,
		func(c *client, args []string) error {
			return c.send(http.MethodPost, procedurePath(args[0])+"/retirement", args[1:])
		}},
	{"reactivate-procedure", "ID [FILE]", "reactivate a retired procedure, from request JSON (base_event, reason, actor) in FILE or stdin", 1, 2,
		func(c *client, args []string) error {
			return c.send(http.MethodPost, procedurePath(args[0])+"/reactivation", args[1:])
		}},
	{"graph", "ID N [REPO ENV [COMMIT INPUTS [DECISIONS]]]", "show version N's composition graph, resolved in REPO and ENV if given, and verified at COMMIT with INPUTS (a JSON object) and the conditional references' DECISIONS (a JSON object of reference path to true or false) if given", 2, 7,
		func(c *client, args []string) error {
			path := procedurePath(args[0]) + "/versions/" + url.PathEscape(args[1]) + "/graph"
			switch len(args) {
			case 3:
				return usageError("graph needs both REPO and ENV, or neither")
			case 5:
				return usageError("graph needs both COMMIT and INPUTS, or neither")
			case 4:
				path += "?" + url.Values{"repository": {args[2]}, "environment": {args[3]}}.Encode()
			case 6, 7:
				query := url.Values{"repository": {args[2]}, "environment": {args[3]}, "commit": {args[4]}, "inputs": {args[5]}}
				if len(args) == 7 {
					query.Set("decisions", args[6])
				}
				path += "?" + query.Encode()
			}
			return c.do(http.MethodGet, path, nil)
		}},
	{"revise", "ID [FILE]", "append a version from request JSON in FILE or stdin", 1, 2,
		func(c *client, args []string) error {
			return c.send(http.MethodPost, procedurePath(args[0])+"/versions", args[1:])
		}},
	{"bindings", "REPOSITORY [NAME=VALUE...]", "list the bindings of a repository; NAME is limit, after or snapshot", 1, 1,
		func(c *client, args []string) error {
			return c.do(http.MethodGet, c.path("/v1/bindings", url.Values{"repository": {args[0]}}), nil)
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
	{"resolve", "BINDING_ID ENV [COMMIT INPUTS [DECISIONS]]", "resolve a binding's latest revision in environment ENV, and verify it at COMMIT with INPUTS (a JSON object) and the conditional references' DECISIONS (a JSON object of reference path to true or false) if given", 2, 5,
		func(c *client, args []string) error {
			query := url.Values{"environment": {args[1]}}
			switch len(args) {
			case 3:
				return usageError("resolve needs both COMMIT and INPUTS, or neither")
			case 4, 5:
				query.Set("commit", args[2])
				query.Set("inputs", args[3])
				if len(args) == 5 {
					query.Set("decisions", args[4])
				}
			}
			return c.do(http.MethodGet, bindingPath(args[0])+"/resolution?"+query.Encode(), nil)
		}},
	{"record", "[FILE]", "record a finished execution from request JSON in FILE or stdin", 0, 1,
		func(c *client, args []string) error { return c.send(http.MethodPost, "/v1/executions", args) }},
	{"get-execution", "ID", "show one execution with its inputs and evidence", 1, 1,
		func(c *client, args []string) error {
			return c.do(http.MethodGet, "/v1/executions/"+url.PathEscape(args[0]), nil)
		}},
	{"executions", "[PROCEDURE_ID [REPOSITORY]] [NAME=VALUE...]", "list executions, optionally of a procedure and in a repository; NAME is version, repository, commit, evidence_status (valid or invalidated), limit or after", 0, 2,
		func(c *client, args []string) error {
			query := url.Values{}
			for i, name := range []string{"procedure_id", "repository"} {
				if len(args) > i {
					query.Set(name, args[i])
				}
			}
			return c.do(http.MethodGet, c.path("/v1/executions", query), nil)
		}},
	{"verification", "ID", "show whether an execution is verified, and its combination", 1, 1,
		func(c *client, args []string) error {
			return c.do(http.MethodGet, "/v1/executions/"+url.PathEscape(args[0])+"/verification", nil)
		}},
	{"invalidate-execution", "ID [FILE]", "withdraw an execution as evidence, permanently, from request JSON (reason, actor) in FILE or stdin; the execution is kept unchanged", 1, 2,
		func(c *client, args []string) error {
			return c.send(http.MethodPost, "/v1/executions/"+url.PathEscape(args[0])+"/invalidation", args[1:])
		}},
	{"get-execution-invalidation", "ID", "show whether an execution is still valid evidence, and its invalidation", 1, 1,
		func(c *client, args []string) error {
			return c.do(http.MethodGet, "/v1/executions/"+url.PathEscape(args[0])+"/invalidation", nil)
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
	{"feedback", "[FILE]", "report a problem with Polaroid, or a suggestion, from request JSON in FILE or stdin", 0, 1,
		func(c *client, args []string) error { return c.send(http.MethodPost, "/v1/feedback", args) }},
	{"feedbacks", "[KIND] [NAME=VALUE...]", "list feedback reports, optionally only KIND (problem or suggestion); NAME is subject_type, subject_id, subject_version, repository, status (open or resolved), limit or after", 0, 1,
		func(c *client, args []string) error {
			query := url.Values{}
			if len(args) == 1 {
				query.Set("kind", args[0])
			}
			return c.do(http.MethodGet, c.path("/v1/feedback", query), nil)
		}},
	{"get-feedback", "ID", "show one feedback report", 1, 1,
		func(c *client, args []string) error {
			return c.do(http.MethodGet, "/v1/feedback/"+url.PathEscape(args[0]), nil)
		}},
	{"resolve-feedback", "ID [FILE]", "record how a feedback report was handled, permanently, from request JSON (outcome, reason, actor, references?) in FILE or stdin; the report is kept unchanged", 1, 2,
		func(c *client, args []string) error {
			return c.send(http.MethodPost, "/v1/feedback/"+url.PathEscape(args[0])+"/resolution", args[1:])
		}},
	{"register", "[FILE]", "register a repository from request JSON in FILE or stdin", 0, 1,
		func(c *client, args []string) error { return c.send(http.MethodPost, "/v1/repositories", args) }},
	{"alias", "ID [FILE]", "add an alias to a repository from request JSON in FILE or stdin", 1, 2,
		func(c *client, args []string) error {
			return c.send(http.MethodPost, "/v1/repositories/"+url.PathEscape(args[0])+"/aliases", args[1:])
		}},
	{"repository", "ID", "show a registered repository", 1, 1,
		func(c *client, args []string) error {
			return c.do(http.MethodGet, "/v1/repositories/"+url.PathEscape(args[0]), nil)
		}},
	{"repository-by-identifier", "IDENTIFIER", "show the repository an identifier, canonical or alias, is registered to", 1, 1,
		func(c *client, args []string) error {
			return c.do(http.MethodGet, "/v1/repositories/by-identifier/"+args[0], nil)
		}},
	{"repositories", "[NAME=VALUE...]", "list registered repositories; NAME is limit or after", 0, 0,
		func(c *client, _ []string) error { return c.do(http.MethodGet, c.path("/v1/repositories", nil), nil) }},
	{"toil-items", "[NAME=VALUE...]", "list toil items (assessments of repetitive work) by their latest revision; NAME is status, repository, procedure_id, limit, after or snapshot", 0, 0,
		func(c *client, _ []string) error { return c.do(http.MethodGet, c.path("/v1/toil-items", nil), nil) }},
	{"create-toil-item", "[FILE]", "create a toil item and its revision 1 from request JSON in FILE or stdin", 0, 1,
		func(c *client, args []string) error { return c.send(http.MethodPost, "/v1/toil-items", args) }},
	{"get-toil-item", "ID", "show a toil item and its full revision history", 1, 1,
		func(c *client, args []string) error { return c.do(http.MethodGet, toilPath(args[0]), nil) }},
	{"get-toil-item-by-key", "KEY", "show a toil item, looked up by canonical key", 1, 1,
		func(c *client, args []string) error {
			return c.do(http.MethodGet, "/v1/toil-items/by-key/"+url.PathEscape(args[0]), nil)
		}},
	{"get-toil-revision", "ID N", "show revision N of a toil item", 2, 2,
		func(c *client, args []string) error {
			return c.do(http.MethodGet, toilPath(args[0])+"/revisions/"+url.PathEscape(args[1]), nil)
		}},
	{"revise-toil-item", "ID [FILE]", "append a toil item revision from request JSON in FILE or stdin", 1, 2,
		func(c *client, args []string) error {
			return c.send(http.MethodPost, toilPath(args[0])+"/revisions", args[1:])
		}},
	// walkthroughs and get-walkthrough are an operator's read-only
	// visibility into guided-procedure walkthroughs (ADR-0056): agents run
	// through them with /mcp/agent/v8's start and advance, not this CLI.
	{"walkthroughs", "[NAME=VALUE...]", "list guided-procedure walkthroughs; NAME is repository, goal, status (active, escalate or blocked), limit or after", 0, 0,
		func(c *client, _ []string) error { return c.do(http.MethodGet, c.path("/v1/walkthroughs", nil), nil) }},
	{"get-walkthrough", "TOKEN", "show one guided-procedure walkthrough's current state", 1, 1,
		func(c *client, args []string) error {
			return c.do(http.MethodGet, "/v1/walkthroughs/"+url.PathEscape(args[0]), nil)
		}},
	// The knowledge-* commands are the operator surface of Git-native living
	// knowledge (ADR-0048): explicit status, publish, fetch, validate,
	// activate, push, activations and reconcile. Every request needs the
	// token configured on the daemon, given here as $POLAROID_KNOWLEDGE_TOKEN.
	{"knowledge-status", "", "show Git-native knowledge: the active snapshot, its procedure count, and configured sources", 0, 0,
		func(c *client, _ []string) error { return c.do(http.MethodGet, "/v1/knowledge/status", nil) }},
	{"knowledge-publish", "[FILE]", "publish field-reviewed knowledge changes from request JSON (base, changes, records, review, author, summary, rationale, finding, validation) in FILE or stdin", 0, 1,
		func(c *client, args []string) error {
			return c.send(http.MethodPost, "/v2/knowledge/publications", args)
		}},
	{"knowledge-rollback", "[FILE]", "restore an earlier published snapshot as a new, field-reviewed publication from request JSON (base, to, review, author, summary, rationale, validation) in FILE or stdin", 0, 1,
		func(c *client, args []string) error {
			return c.send(http.MethodPost, "/v2/knowledge/rollbacks", args)
		}},
	{"knowledge-fetch", "SOURCE [FILE]", "fetch a source's knowledge into quarantine, with optional credentials JSON in FILE or stdin", 1, 2,
		func(c *client, args []string) error {
			return c.sendOptional(http.MethodPost, knowledgeSourcePath(args[0])+"/fetch", args[1:])
		}},
	{"knowledge-validate", "SOURCE", "validate the source's fetched candidate: Git integrity, trust policy, complete catalog", 1, 1,
		func(c *client, args []string) error {
			return c.do(http.MethodPost, knowledgeSourcePath(args[0])+"/validation", nil)
		}},
	{"knowledge-activate", "SOURCE [FILE]", "activate the source's validated candidate, from decision JSON (author, summary, rationale, finding, validation) in FILE or stdin", 1, 2,
		func(c *client, args []string) error {
			return c.send(http.MethodPost, knowledgeSourcePath(args[0])+"/activation", args[1:])
		}},
	{"knowledge-push", "SOURCE [FILE]", "push the active knowledge to a source the policy permits, with optional credentials JSON in FILE or stdin", 1, 2,
		func(c *client, args []string) error {
			return c.sendOptional(http.MethodPost, knowledgeSourcePath(args[0])+"/push", args[1:])
		}},
	{"knowledge-activations", "[NAME=VALUE...]", "list accepted activations, newest first; NAME is limit", 0, 0,
		func(c *client, _ []string) error {
			return c.do(http.MethodGet, c.path("/v1/knowledge/activations", nil), nil)
		}},
	{"knowledge-reconcile", "FINDING_ID", "resolve a feedback report as addressed once a local publication addresses it, or report that none has yet", 1, 1,
		func(c *client, args []string) error {
			return c.do(http.MethodPost, "/v1/knowledge/reconciliations/"+url.PathEscape(args[0]), nil)
		}},
}

func usage() string {
	var b strings.Builder
	b.WriteString("Usage: polaroid [-server URL] [-timeout DURATION] COMMAND [ARGUMENTS]\n\nCommands:\n")
	for _, cmd := range commands {
		fmt.Fprintf(&b, "  %-40s %s\n", strings.TrimSpace(cmd.name+" "+cmd.args), cmd.summary)
	}
	fmt.Fprintf(&b, "  %-40s %s\n", "help", "show this help")
	b.WriteString("\nLocal commands, for this user's installation and service (no request to the daemon):\n")
	for _, cmd := range localCommands {
		fmt.Fprintf(&b, "  %-40s %s\n", strings.TrimSpace(cmd.name+" "+cmd.args), cmd.summary)
	}
	fmt.Fprintf(&b, "\nThe server is -server, else $POLAROID_URL, else %s.\n", defaultServer)
	b.WriteString("Each response body is printed to stdout, also when the request fails.\n")
	b.WriteString("Exit status: 0 success, 1 request failed, 2 usage error.\n")
	b.WriteString("Local commands print the resulting status as JSON. status exits 0 running, 1 failed or unreachable,\n")
	b.WriteString("3 stopped, 4 not installed, 5 starting; the others exit 0 done, 1 failed, 2 usage, 4 not installed.\n")
	b.WriteString("backup, inspect-backup and restore print their result as JSON; inspect-backup exits 1 for an invalid\n")
	b.WriteString("backup, and restore (also with -plan) exits 3 when it refused and changed nothing.\n")
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
	query  url.Values
	// token is sent as Polaroid-Operator-Token on every request, when set:
	// it guards the knowledge-* commands (ADR-0048). It is never logged.
	token string
}

// path returns path with the command's own parameters and the NAME=VALUE
// parameters given on the command line.
func (c *client) path(path string, query url.Values) string {
	all := url.Values{}
	for _, q := range []url.Values{query, c.query} {
		for name, values := range q {
			all[name] = append(all[name], values...)
		}
	}
	if len(all) == 0 {
		return path
	}
	return path + "?" + all.Encode()
}

// splitQuery separates trailing NAME=VALUE arguments from the others. The
// first required arguments are positional whatever they contain, so a
// free-text TASK may include "=".
func splitQuery(args []string, required int) ([]string, url.Values, error) {
	query := url.Values{}
	n := min(required, len(args))
	rest := append([]string(nil), args[:n]...)
	for _, a := range args[n:] {
		name, value, ok := strings.Cut(a, "=")
		switch {
		case !ok:
			if len(query) > 0 {
				return nil, nil, usageError(fmt.Sprintf("argument %q follows a NAME=VALUE parameter", a))
			}
			rest = append(rest, a)
		case name == "":
			return nil, nil, usageError(fmt.Sprintf("parameter %q has no name", a))
		default:
			query.Add(name, value)
		}
	}
	return rest, query, nil
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
	if isLocal(fs.Arg(0)) {
		return runLocal(fs.Arg(0), fs.Args()[1:], stdout, stderr, getenv)
	}

	err := dispatch(server, *timeout, getenv("POLAROID_KNOWLEDGE_TOKEN"), fs.Arg(0), fs.Args()[1:], stdin, stdout)
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

func dispatch(server string, timeout time.Duration, token, name string, args []string, stdin io.Reader, stdout io.Writer) error {
	u, err := url.Parse(server)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return usageError(fmt.Sprintf("invalid server URL %q: want http://host:port", server))
	}
	for _, cmd := range commands {
		if cmd.name != name {
			continue
		}
		var query url.Values
		if queryCommands[name] {
			var err error
			if args, query, err = splitQuery(args, cmd.min); err != nil {
				return err
			}
		}
		if len(args) < cmd.min || len(args) > cmd.max {
			return usageError(strings.TrimSpace("usage: polaroid " + cmd.name + " " + cmd.args))
		}
		c := &client{base: strings.TrimRight(server, "/"), http: &http.Client{Timeout: timeout}, stdin: stdin, stdout: stdout, query: query, token: token}
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

// sendOptional is send, but a truly absent FILE sends no body at all
// (never reading stdin): for a request whose body is entirely optional,
// such as credentials.
func (c *client) sendOptional(method, path string, args []string) error {
	if len(args) == 0 {
		return c.do(method, path, nil)
	}
	return c.send(method, path, args)
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
	if c.token != "" {
		req.Header.Set("Polaroid-Operator-Token", c.token)
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

func toilPath(id string) string {
	return "/v1/toil-items/" + url.PathEscape(id)
}

func knowledgeSourcePath(name string) string {
	return "/v1/knowledge/sources/" + url.PathEscape(name)
}
