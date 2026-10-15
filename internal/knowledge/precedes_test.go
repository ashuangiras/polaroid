package knowledge_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
)

// precedesFixture publishes a parent composing three children (a, b, c),
// plus a sibling-less outsider procedure, each as a typed Procedure record
// with a matching legacy procedure document.
func precedesFixture(t *testing.T, k *knowledge.Kernel, precedes map[string][]string) (knowledge.Snapshot, error) {
	t.Helper()
	parent := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "parent", Label: "parent", Definition: "d", Composes: []string{"a", "b", "c"}}
	var changes []knowledge.Change
	var records []knowledge.RecordChange
	for _, key := range []string{"parent", "a", "b", "c", "outsider"} {
		changes = append(changes, put(knowledgetest.SharedProc(key, key)))
		if key == "parent" {
			records = append(records, knowledge.RecordChange{Key: parent.Key, Record: parent})
			continue
		}
		r := &knowledge.Record{Type: knowledge.TypeProcedure, Key: key, Label: key, Definition: "d"}
		if key != "outsider" {
			r.Precedes = precedes[key]
		}
		records = append(records, knowledge.RecordChange{Key: key, Record: r})
	}
	return k.Publish(context.Background(), knowledge.Proposal{Changes: changes, Records: records, Provenance: prov()})
}

func TestPrecedesAmongSiblingsAccepted(t *testing.T) {
	k := knowledge.New(knowledgetest.NewMemoryStore(), knowledge.WithRecordValidator(&acceptAll{}))
	if _, err := precedesFixture(t, k, map[string][]string{"a": {"b"}, "b": {"c"}}); err != nil {
		t.Fatalf("a precedes b precedes c, all siblings under parent, must be accepted: %v", err)
	}
}

func TestPrecedesNonSiblingRejected(t *testing.T) {
	k := knowledge.New(knowledgetest.NewMemoryStore(), knowledge.WithRecordValidator(&acceptAll{}))
	_, err := precedesFixture(t, k, map[string][]string{"a": {"outsider"}})
	var ve *knowledge.ValidationError
	if !errors.As(err, &ve) || !strings.Contains(err.Error(), "must be a sibling") {
		t.Fatalf("a precedes outsider (not a sibling under any shared parent) must be refused, got %v", err)
	}
}

func TestPrecedesCycleRejected(t *testing.T) {
	k := knowledge.New(knowledgetest.NewMemoryStore(), knowledge.WithRecordValidator(&acceptAll{}))
	_, err := precedesFixture(t, k, map[string][]string{"a": {"b"}, "b": {"a"}})
	var ve *knowledge.ValidationError
	if !errors.As(err, &ve) || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("a precedes b precedes a must be refused as a cycle, got %v", err)
	}
}

func TestPrecedesTargetWrongTypeRejected(t *testing.T) {
	k := knowledge.New(knowledgetest.NewMemoryStore(), knowledge.WithRecordValidator(&acceptAll{}))
	concept := &knowledge.Record{Type: knowledge.TypeConcept, Key: "concept.not-a-procedure", Label: "x", Definition: "d"}
	proc := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "proc.bad-precedes", Label: "x", Definition: "d", Precedes: []string{"concept.not-a-procedure"}}
	_, err := k.Publish(context.Background(), knowledge.Proposal{
		Changes:    []knowledge.Change{put(knowledgetest.SharedProc("proc.bad-precedes", "x"))},
		Records:    []knowledge.RecordChange{{Key: concept.Key, Record: concept}, {Key: proc.Key, Record: proc}},
		Provenance: prov(),
	})
	if err == nil || !strings.Contains(err.Error(), "must name one of") {
		t.Fatalf("precedes naming a Concept, not a Procedure, must be refused, got %v", err)
	}
}
