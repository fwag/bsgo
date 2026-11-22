package main

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/openSUSE/bsgo/pkg/publisher"
)

func nameConflict(name, kept string) publisher.NameConflict {
	return publisher.NameConflict{Filename: name, Kept: kept, Dropped: []string{"old/" + name}}
}

func TestConflictReporter(t *testing.T) {
	var r conflictReporter
	a := []publisher.NameConflict{nameConflict("a.rpm", "k1")}
	if got := r.lines("p", a); len(got) != 1 {
		t.Fatalf("first conflict must be logged once: %v", got)
	}
	if got := r.lines("p", a); got != nil {
		t.Errorf("same set again must stay quiet: %v", got)
	}
	if got := r.lines("q", a); len(got) != 1 {
		t.Errorf("another prpa has its own state: %v", got)
	}
	changed := []publisher.NameConflict{nameConflict("a.rpm", "k2")}
	if got := r.lines("p", changed); len(got) != 1 {
		t.Errorf("changed winner must be logged: %v", got)
	}
	if got := r.lines("p", nil); got != nil {
		t.Errorf("resolved conflicts log nothing: %v", got)
	}
	if got := r.lines("p", changed); len(got) != 1 {
		t.Errorf("a conflict that returns must be logged again: %v", got)
	}
}

func TestConflictReporterCapsLines(t *testing.T) {
	var r conflictReporter
	var cs []publisher.NameConflict
	for i := range 8 {
		cs = append(cs, nameConflict("f"+strconv.Itoa(i)+".rpm", "k"))
	}
	got := r.lines("p", cs)
	if len(got) != maxConflictLines+1 {
		t.Fatalf("got %d lines: %v", len(got), got)
	}
	want := "p: and 3 more file names produced by different units"
	if !reflect.DeepEqual(got[len(got)-1], want) {
		t.Errorf("last line %q, want %q", got[len(got)-1], want)
	}
}
