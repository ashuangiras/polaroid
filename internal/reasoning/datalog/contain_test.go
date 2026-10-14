package datalog

import (
	"errors"
	"strings"
	"testing"
)

// A panic in the engine becomes a fixed rule error; its value, which is
// internal, never reaches the error clients see.
func TestContainHidesPanicValue(t *testing.T) {
	f := func() (err error) {
		defer contain(&err)
		panic("internal detail")
	}
	err := f()
	var re *RuleError
	if !errors.As(err, &re) || strings.Contains(err.Error(), "internal detail") || !strings.Contains(err.Error(), "the Datalog engine failed on this program") {
		t.Fatalf("err = %v", err)
	}
}
