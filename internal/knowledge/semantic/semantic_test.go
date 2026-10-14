package semantic_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
	"github.com/ashuangiras/polaroid/internal/knowledge/semantic"
)

func validator(t *testing.T) *semantic.Validator {
	t.Helper()
	v, err := semantic.New(semantic.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func prov() knowledge.Provenance {
	return knowledge.Provenance{Author: "test.agent", Summary: "corpus", Rationale: "test"}
}

func publishCorpus(t *testing.T, k *knowledge.Kernel) knowledge.Snapshot {
	t.Helper()
	s, err := k.Publish(context.Background(), knowledge.Proposal{
		Changes: knowledgetest.CorpusProcedures(), Records: knowledgetest.CorpusRecordChanges(), Provenance: prov()})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCorpusPublishesAndReloads(t *testing.T) {
	store := knowledgetest.NewMemoryStore()
	s := publishCorpus(t, knowledge.New(store, knowledge.WithRecordValidator(validator(t))))
	cat, err := knowledge.New(store, knowledge.WithRecordValidator(validator(t))).At(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if cat.Format() != knowledge.FormatRecords || len(cat.RecordKeys()) != len(knowledgetest.CorpusRecords()) {
		t.Fatalf("format %d, %d records", cat.Format(), len(cat.RecordKeys()))
	}
	files, err := store.Files(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := files.Read(knowledge.ManifestPath)
	if err != nil || string(manifest) != `{"format":2,"capabilities":["records/1","rules/corpus-logic-1"]}`+"\n" {
		t.Fatalf("manifest %q, %v", manifest, err)
	}
	doc, err := files.Read(knowledge.RecordPath(knowledge.TypeProcedure, "go.verify.concurrent"))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"@context":"urn:polaroid:context:v1","id":"k:go.verify.concurrent","type":"Procedure","label":"go.verify.concurrent",` +
		`"classified_as":["k:domain.software.go"],"specializes":["k:go.verify"],"requires":["k:check.leak","k:check.race"]}` + "\n"
	if string(doc) != want {
		t.Fatalf("document\n%s\nwant\n%s", doc, want)
	}
}

func TestKernelWithoutValidatorRefusesRecords(t *testing.T) {
	store := knowledgetest.NewMemoryStore()
	_, err := knowledge.New(store).Publish(context.Background(), knowledge.Proposal{
		Changes: knowledgetest.CorpusProcedures(), Records: knowledgetest.CorpusRecordChanges(), Provenance: prov()})
	if !errors.Is(err, knowledge.ErrInvalid) || !strings.Contains(err.Error(), "no semantic validator") {
		t.Fatalf("publish: %v", err)
	}
	s := publishCorpus(t, knowledge.New(store, knowledge.WithRecordValidator(validator(t))))
	if _, err := knowledge.New(store).At(context.Background(), s); !errors.Is(err, knowledge.ErrInvalid) {
		t.Fatalf("load: %v", err)
	}
}

func doc(r *knowledge.Record, body string) knowledge.RecordDocument {
	return knowledge.RecordDocument{Record: r, Document: []byte(body)}
}

func concept(key string) *knowledge.Record {
	return &knowledge.Record{Type: knowledge.TypeConcept, Key: key, Label: key}
}

func conceptDoc(key, extra string) knowledge.RecordDocument {
	return doc(concept(key), `{"@context":"urn:polaroid:context:v1","id":"k:`+key+`","type":"Concept","label":"`+key+`"`+extra+`}`)
}

func problems(t *testing.T, err error) string {
	t.Helper()
	var ve *knowledge.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("not a validation error: %v", err)
	}
	return ve.Error()
}

func TestValidatorRefusesUndefinedTerm(t *testing.T) {
	err := validator(t).ValidateRecords(context.Background(), []knowledge.RecordDocument{conceptDoc("a", `,"colour":"red"`)})
	if msg := problems(t, err); !strings.Contains(msg, "records[a]: JSON-LD") {
		t.Fatal(msg)
	}
}

func TestValidatorNeverFetchesContexts(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"@context":{"label":"http://www.w3.org/2004/02/skos/core#prefLabel"}}`))
	}))
	defer srv.Close()
	for name, ctx := range map[string]string{
		"remote":   `"` + srv.URL + `/context.jsonld"`,
		"array":    `["urn:polaroid:context:v1","` + srv.URL + `/extra.jsonld"]`,
		"imported": `{"@import":"` + srv.URL + `/import.jsonld"}`,
		"other":    `"urn:polaroid:context:v2"`,
	} {
		t.Run(name, func(t *testing.T) {
			d := doc(concept("a"), `{"@context":`+ctx+`,"id":"k:a","type":"Concept","label":"a"}`)
			err := validator(t).ValidateRecords(context.Background(), []knowledge.RecordDocument{d})
			if msg := problems(t, err); !strings.Contains(msg, "JSON-LD") {
				t.Fatal(msg)
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("%d requests reached the network", hits.Load())
	}
}

func TestValidatorChecksProjection(t *testing.T) {
	r := concept("b")
	r.Specializes = []string{"a"}
	err := validator(t).ValidateRecords(context.Background(), []knowledge.RecordDocument{conceptDoc("a", ""), doc(r, string(conceptDoc("b", "").Document))})
	if msg := problems(t, err); !strings.Contains(msg, "records[b].specializes: the RDF projection states [], not [urn:polaroid:k:a]") {
		t.Fatal(msg)
	}
}

func TestValidatorAppliesShapes(t *testing.T) {
	for name, tc := range map[string]struct {
		docs []knowledge.RecordDocument
		want string
	}{
		"class": {
			docs: func() []knowledge.RecordDocument {
				r := concept("b")
				r.ClassifiedAs = []string{"a"}
				return []knowledge.RecordDocument{conceptDoc("a", ""), doc(r, `{"@context":"urn:polaroid:context:v1","id":"k:b","type":"Concept","label":"b","classified_as":["k:a"]}`)}
			}(),
			want: "records[b]: http://purl.org/dc/terms/subject violates ClassConstraintComponent",
		},
		"closed": {
			docs: []knowledge.RecordDocument{conceptDoc("a", `,"program":"x"`)},
			want: "records[a]: urn:polaroid:corpus:program violates ClosedConstraintComponent",
		},
		"label": {
			docs: []knowledge.RecordDocument{doc(concept("a"), `{"@context":"urn:polaroid:context:v1","id":"k:a","type":"Concept","label":["x","y"]}`)},
			want: "MaxCountConstraintComponent",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := validator(t).ValidateRecords(context.Background(), tc.docs)
			if msg := problems(t, err); !strings.Contains(msg, tc.want) {
				t.Fatal(msg)
			}
		})
	}
}

func TestValidatorLimits(t *testing.T) {
	deep := `,"asserts":[{"subject":{"@value":{"x":{"y":{"z":{"w":{}}}}}}}]`
	v, err := semantic.New(semantic.Limits{Depth: 4, TriplesPerRecord: 3, Triples: 10})
	if err != nil {
		t.Fatal(err)
	}
	if msg := problems(t, v.ValidateRecords(context.Background(), []knowledge.RecordDocument{conceptDoc("a", deep)})); !strings.Contains(msg, "nests deeper") {
		t.Fatal(msg)
	}
	r := concept("a")
	r.Broader = []string{"b", "c", "d"}
	if msg := problems(t, v.ValidateRecords(context.Background(), []knowledge.RecordDocument{doc(r, string(conceptDoc("a", `,"broader":["k:b","k:c","k:d"]`).Document))})); !strings.Contains(msg, "at most 3 triples") {
		t.Fatal(msg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := validator(t).ValidateRecords(ctx, []knowledge.RecordDocument{conceptDoc("a", "")}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}
