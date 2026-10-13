package knowledgesync

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
)

// sourceFile is one entry of a trust-policy file. The file never holds
// credentials: Fetch and Publish take them per call, so they are never
// written to disk or committed alongside the policy.
type sourceFile struct {
	Name       string   `json:"name"`
	URL        string   `json:"url"`
	Branch     string   `json:"branch,omitzero"`
	Publishers []string `json:"publishers"`
	Publish    bool     `json:"publish,omitzero"`
}

// LoadPolicy reads a trust-policy file: a JSON array of sources, each an
// object with name, url, branch (optional, default "main"), publishers and
// publish. A missing file is an empty policy (no source is contacted),
// never an error, so an instance with no configured sources still starts.
func LoadPolicy(path string) (Policy, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is the daemon's own configured trust-policy file, not user input.
	if errors.Is(err, os.ErrNotExist) {
		return Policy{}, nil
	}
	if err != nil {
		return Policy{}, err
	}
	var entries []sourceFile
	if err := json.Unmarshal(data, &entries, json.RejectUnknownMembers(true)); err != nil {
		return Policy{}, fmt.Errorf("knowledgesync: %s: %w", path, err)
	}
	p := Policy{Sources: make([]Source, len(entries))}
	for i, e := range entries {
		p.Sources[i] = Source{Name: e.Name, URL: e.URL, Branch: e.Branch, Publishers: e.Publishers, Publish: e.Publish}
	}
	return p, nil
}
