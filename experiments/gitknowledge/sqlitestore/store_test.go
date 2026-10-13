package sqlitestore_test

import (
	"path/filepath"
	"testing"

	"github.com/ashuangiras/polaroid/experiments/gitknowledge/knowledge"
	"github.com/ashuangiras/polaroid/experiments/gitknowledge/knowledge/storetest"
	"github.com/ashuangiras/polaroid/experiments/gitknowledge/sqlitestore"
)

func TestBehaviour(t *testing.T) {
	storetest.Run(t, func(t *testing.T, dir string) knowledge.Store {
		s, err := sqlitestore.Open(filepath.Join(dir, "knowledge.db"), knowledge.DefaultPolicy)
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}
