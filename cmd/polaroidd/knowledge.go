package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/ashuangiras/polaroid/internal/gitcore"
	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/semantic"
	"github.com/ashuangiras/polaroid/internal/knowledgeops"
	"github.com/ashuangiras/polaroid/internal/knowledgesync"
	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/reasoning"
	"github.com/ashuangiras/polaroid/internal/reasoning/datalog"
	"github.com/ashuangiras/polaroid/internal/reasoning/policy"
)

// defaultKnowledgeDir returns <home>/.polaroid/data/knowledge (ADR-0025's
// per-user convention, extended to Git-native living knowledge by
// ADR-0048): the bare Git-core repository that holds it.
func defaultKnowledgeDir(userHomeDir func() (string, error)) (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate the per-user knowledge directory: %w; pass -knowledge-dir or set POLAROID_KNOWLEDGE_DIR", err)
	}
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("cannot locate the per-user knowledge directory: home directory %q is not absolute; pass -knowledge-dir or set POLAROID_KNOWLEDGE_DIR", home)
	}
	return filepath.Join(home, ".polaroid", "data", "knowledge"), nil
}

// prepareKnowledgeParents creates dir's parents (.polaroid and
// .polaroid/data, or the equivalent above a custom -knowledge-dir) with
// mode 0700 if missing. gitcore.Open creates dir itself.
func prepareKnowledgeParents(dir string) error {
	data := filepath.Dir(dir)
	root := filepath.Dir(data)
	for _, d := range []string{root, data} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	return nil
}

// loadOrCreateOperatorToken reads the token at path, or creates a random
// one with mode 0600 if the file does not exist. The operator HTTP API
// never trusts binding to loopback alone, since other local processes can
// reach a loopback-bound port too, so this token is always required.
func loadOrCreateOperatorToken(path string) (string, error) {
	tok, err := readOperatorToken(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return tok, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate operator token: %w", err)
	}
	tok = hex.EncodeToString(raw)
	// Written in full beside path, then linked into place: a link never
	// replaces an existing file, so concurrent first starts agree on one
	// token and no reader sees a partial file.
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("write operator token: %w", err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	_, err = f.WriteString(tok + "\n")
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("write operator token: %w", err)
	}
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return readOperatorToken(path)
		}
		return "", fmt.Errorf("write operator token: %w", err)
	}
	return tok, nil
}

func readOperatorToken(path string) (string, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: path is the daemon's own configured token file, not user input.
	if errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err != nil {
		return "", fmt.Errorf("read operator token: %w", err)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("%s: is empty; remove it to generate a new token", path)
	}
	return tok, nil
}

// knowledgeComponents are what the daemon wires for the Git-native
// knowledge path (ADR-0048): the agent interface mounted at /mcp/agent,
// the operator interface mounted at /v1/knowledge, and the token that
// guards it.
type knowledgeComponents struct {
	agent    *knowledgeops.Agent
	resolver *knowledgeops.Resolver
	guide    *knowledgeops.Guide
	operator *knowledgeops.Operator
	token    string
}

// loadTrust reads the operator-held exception trust material (ADR-0050). A
// missing file is empty trust: no exception applies.
func loadTrust(path string) (policy.Trust, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the operator's own configuration path
	if errors.Is(err, fs.ErrNotExist) {
		return policy.Trust{}, nil
	}
	if err != nil {
		return policy.Trust{}, err
	}
	return policy.ParseTrust(b)
}

// setupKnowledge opens the knowledge repository at dir (creating it if
// missing), loads the trust policy at sourcesPath (empty if the file does
// not exist: no source is contacted), the exception trust material at
// authoritiesPath (empty if missing), and loads or creates the operator
// token at tokenPath. walkthroughs backs /mcp/agent/v3's stateful start
// and advance (ADR-0053): new, per-agent storage, not Git-native
// knowledge. A Git catalog that starts empty is not a failure here
// (ADR-0048's explicit migration boundary): only a storage or policy
// problem is.
func setupKnowledge(dir, sourcesPath, authoritiesPath, tokenPath string, fb *memory.Service, walkthroughs memory.WalkthroughStore) (*knowledgeComponents, error) {
	if err := prepareKnowledgeParents(dir); err != nil {
		return nil, err
	}
	repo, err := gitcore.Open(dir, gitcore.Options{})
	if err != nil {
		return nil, fmt.Errorf("open knowledge repository %s: %w", dir, err)
	}
	policy, err := knowledgesync.LoadPolicy(sourcesPath)
	if err != nil {
		return nil, fmt.Errorf("load knowledge trust policy %s: %w", sourcesPath, err)
	}
	trust, err := loadTrust(authoritiesPath)
	if err != nil {
		return nil, fmt.Errorf("load exception trust material %s: %w", authoritiesPath, err)
	}
	sem, err := semantic.New(semantic.DefaultLimits)
	if err != nil {
		return nil, err
	}
	ev := datalog.Evaluator{}
	syncer, err := knowledgesync.New(repo, policy,
		knowledge.WithRecordValidator(sem), knowledge.WithRecordValidator(reasoning.RuleValidator{Evaluator: ev}))
	if err != nil {
		return nil, err
	}
	token, err := loadOrCreateOperatorToken(tokenPath)
	if err != nil {
		return nil, err
	}
	return &knowledgeComponents{
		agent:    knowledgeops.NewAgent(syncer.Kernel(), fb),
		resolver: knowledgeops.NewResolver(syncer.Kernel(), ev, trust),
		guide:    knowledgeops.NewGuide(syncer.Kernel(), memory.NewWalkthroughs(walkthroughs)),
		operator: knowledgeops.NewOperator(syncer, policy, fb),
		token:    token,
	}, nil
}

// logKnowledgeUnavailable reports why the Git-native knowledge path did
// not start: /mcp/agent and /v1/knowledge are not mounted, but the legacy
// API and full-mode MCP are unaffected.
func logKnowledgeUnavailable(logger *slog.Logger, err error) {
	logger.Warn("knowledge path unavailable: /mcp/agent and /v1/knowledge are not served", "error", err)
}
