package power

import (
	"os"
	"regexp"
	"testing"
)

// Kinds IS EVERY KIND. It is a hand list, so it is held against the constants
// in track.go: a new kind left out of it would escape the notification ledger.
func TestKindsListsEveryKind(t *testing.T) {
	src, err := os.ReadFile("track.go")
	if err != nil {
		t.Fatal(err)
	}
	declared := regexp.MustCompile(`(?m)^\s*(Kind\w+)\s+Kind\s*=`).FindAllStringSubmatch(string(src), -1)
	if len(declared) == 0 {
		t.Fatal("no Kind constants found in track.go - this scan broke")
	}
	listed := map[Kind]bool{}
	for _, k := range Kinds {
		listed[k] = true
	}
	if len(declared) != len(Kinds) || len(listed) != len(Kinds) {
		t.Errorf("track.go declares %d kinds, Kinds lists %d (%d distinct)", len(declared), len(Kinds), len(listed))
	}
}
