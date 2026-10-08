package http_test

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// Example records are task knowledge, not code; this keeps them valid
// against the current record contract.
func TestExampleRecordsAreAccepted(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join("..", "..", "..", "examples", "procedures", "*"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no example procedures found: %v", err)
	}
	s := newTestServer(t)
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			files, err := filepath.Glob(filepath.Join(dir, "*.json"))
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(dir, "v1.create.json"))
			if err != nil {
				t.Fatal(err)
			}
			resp := s.call(t, http.MethodPost, "/v1/procedures", string(body))
			expect(t, resp, http.StatusCreated, "")
			h := decodeStrict[historyJSON](t, resp.body)

			versions := 1
			for n := 2; ; n++ {
				body, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("v%d.revise.json", n)))
				if errors.Is(err, fs.ErrNotExist) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				resp := s.call(t, http.MethodPost, "/v1/procedures/"+h.ID+"/versions", string(body))
				expect(t, resp, http.StatusCreated, "")
				versions = n
			}
			if versions != len(files) {
				t.Fatalf("%d JSON files but only versions 1..%d are in sequence; name them v1.create.json, v2.revise.json, ...", len(files), versions)
			}
		})
	}
}
