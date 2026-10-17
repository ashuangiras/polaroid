package knowledgeops

import (
	"errors"
	"testing"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

func TestClassifyFieldDraftConflict(t *testing.T) {
	err := classify(memory.ErrFieldDraftConflict)
	var knowledgeError *wire.KnowledgeError
	if !errors.As(err, &knowledgeError) || knowledgeError.Status != 409 || knowledgeError.Detail.Code != "draft_conflict" {
		t.Fatalf("classified error = %#v, want HTTP 409 draft_conflict", err)
	}
}
