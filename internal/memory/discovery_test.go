package memory

import (
	"encoding/json/jsontext"
	"slices"
	"testing"
)

func TestTermsOfText(t *testing.T) {
	var q query
	q.add("Adding DEPENDENCIES—to the go.mod file, isn't it? A b 42 größe x")
	if want := []string{"adding", "dependencies", "go", "mod", "file", "isn", "42", "größe"}; !slices.Equal(q.wordList(), want) {
		t.Fatalf("words %v, want %v", q.wordList(), want)
	}
	if want := []string{"add", "dependency", "go", "mod", "fil", "isn", "42", "größ"}; !slices.Equal(q.terms, want) {
		t.Fatalf("terms %v, want %v", q.terms, want)
	}
	for word, want := range map[string]string{
		"verifies": "verify", "verified": "verify", "verifying": "verify", "verify": "verify",
		"publishing": "publish", "published": "publish", "publishes": "publish",
		"migrate": "migrat", "migrating": "migrat", "migrated": "migrat", "migrations": "migration",
		"process": "process", "processes": "process", "class": "class",
		"uses": "use", "use": "use", "go": "go", "ties": "tie", "bed": "bed",
	} {
		if got := stem(word); got != want {
			t.Errorf("stem(%q) = %q, want %q", word, got, want)
		}
	}
	var dup query
	dup.add("builds build building")
	if !slices.Equal(dup.terms, []string{"build"}) || dup.words["build"] != "builds" {
		t.Fatalf("one term per stem, shown as its first word: %v %v", dup.terms, dup.words)
	}
}

func TestDocumentsSearchStringValuesOnly(t *testing.T) {
	d := newDocument(LatestVersion{
		CanonicalKey: "go.module.build", Goal: "Goal words", Method: "Method words", Philosophy: "Philosophy words",
		Contract:     jsontext.Value(`{"inputs":{"module":"The module path","count":3,"flag":true,"list":["Listed item"]}}`),
		Instructions: jsontext.Value(`{"steps":[{"id":"pin","action":"Pin the version"}]}`),
	})
	has := func(field int, term string) bool { return d[field][term] }
	for _, c := range []struct {
		field int
		term  string
		want  bool
	}{
		{0, "go", true}, {0, "modul", true}, {0, "build", true},
		{1, "goal", true}, {2, "method", true}, {3, "philosophy", true},
		{4, "path", true}, {4, "list", true}, {4, "inputs", false}, {4, "input", false}, {4, "count", false},
		{5, "pin", true}, {5, "version", true}, {5, "step", false}, {5, "action", false}, {5, "id", false},
	} {
		if has(c.field, c.term) != c.want {
			t.Errorf("%s has %q: %v, want %v", searchedFields[c.field].name, c.term, !c.want, c.want)
		}
	}
}
