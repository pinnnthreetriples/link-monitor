package foldersync

import (
	"fmt"
	"testing"
)

// The two guards. They are not examples: they enumerate every shape one file
// can be in — present or missing on each machine, weighed or not, over the cap
// or not, readable or not, against three different histories — and assert the
// two rules that, if they ever broke, would cost the user data rather than
// convenience.
//
// What they cannot see is what the adapters do with a plan; a deletion issued
// by internal/adapters/syncfs would be invisible here. That half is held by
// TestNothingInTheSharedFolderCanDeleteAFile in tools/gates, which reads the
// source. The two together are the whole argument.

// everyWay is the complete vocabulary of directions, written out here so that
// adding a third one means answering, in this file, what it does to a file.
var everyWay = map[Way]bool{ToPeer: true, ToUs: true}

// shape is one machine's version of one file: nil means that machine does not
// have it.
type shape struct {
	name  string
	entry *Entry
}

// theKey is the file every case in the matrix is about.
const theKey = "notes.txt"

// shapes are the states one machine's copy of that file can be in.
func shapes() []shape {
	e := func(size, sec int64, hash string) *Entry {
		entry := file(theKey, size, sec, hash)
		return &entry
	}
	return []shape{
		{"missing", nil},
		{"as recorded", e(10, 100, "old")},
		{"as recorded, unweighed", e(10, 100, "")},
		{"changed here", e(12, 300, "mine")},
		{"changed here, unweighed", e(12, 300, "")},
		{"changed there", e(14, 400, "theirs")},
		{"same new bytes on both", e(16, 500, "shared")},
		{"over any sane cap", e(5000, 600, "huge")},
		{"the same length, new bytes", e(10, 100, "rewritten")},
	}
}

// histories are the three things the state file can be saying.
func histories() []struct {
	name string
	base Baseline
} {
	return []struct {
		name string
		base Baseline
	}{
		{"no history", nil},
		{"agreed", Baseline{theKey: agreed(10, 100, "old")}},
		{"a conflict already recorded", Baseline{
			theKey: {Local: st(12, 300, "mine"), Remote: st(14, 400, "theirs")},
		}},
	}
}

// matrix walks every combination and hands each plan to fn.
func matrix(t *testing.T, fn func(t *testing.T, c planCase)) {
	t.Helper()

	for _, local := range shapes() {
		for _, remote := range shapes() {
			for _, history := range histories() {
				for _, unread := range []bool{false, true} {
					for _, limit := range []int64{0, 1000} {
						c := newPlanCase(local, remote, history.base, unread, limit)
						name := fmt.Sprintf("here %s/peer %s/%s/unreadable=%v/cap=%d",
							local.name, remote.name, history.name, unread, limit)
						t.Run(name, func(t *testing.T) { fn(t, c) })
					}
				}
			}
		}
	}
}

// planCase is one row of the matrix, with the plan it produced.
type planCase struct {
	local  *Entry
	remote *Entry
	base   Baseline
	plan   Plan
	// here and there are the names each machine held before the plan ran.
	here  map[string]bool
	there map[string]bool
}

func newPlanCase(local, remote shape, base Baseline, unread bool, limit int64) planCase {
	in := input(entries(local.entry), entries(remote.entry), base)
	in.Limits = Limits{MaxBytes: limit}
	if unread {
		in.Unreadable = []string{theKey}
	}
	return planCase{
		local: local.entry, remote: remote.entry, base: base,
		plan:  Decide(in),
		here:  names(local.entry),
		there: names(remote.entry),
	}
}

func entries(e *Entry) []Entry {
	if e == nil {
		return nil
	}
	return []Entry{*e}
}

func names(e *Entry) map[string]bool {
	out := map[string]bool{}
	if e != nil {
		out[fold(e.Path)] = true
	}
	return out
}

// TestNoPlanCanEverRemoveAFile is rule 1, enumerated.
//
// Three things are asserted of every plan the matrix can produce. Every action
// travels in one of the two directions the package declares, reads a file the
// source machine actually has, and names a destination. A file only one
// machine has is copied to the machine that lacks it and nothing else is done
// to it. And it is never merely forgotten either: either it is copied, or the
// pass says out loud why it was not.
func TestNoPlanCanEverRemoveAFile(t *testing.T) {
	t.Parallel()

	matrix(t, func(t *testing.T, c planCase) {
		for _, a := range c.plan.Actions {
			if !everyWay[a.Way] {
				t.Fatalf("action %+v travels in an unknown direction — "+
					"a new Way must be answered for in everyWay", a)
			}
			if a.Path == "" || a.Dest == "" {
				t.Fatalf("action %+v names no source or no destination", a)
			}
			if from := c.sourceNames(a.Way); !from[fold(a.Path)] {
				t.Fatalf("action %+v reads a file the source machine does not have", a)
			}
		}

		switch {
		case c.local != nil && c.remote == nil:
			c.assertOnlyCopiedAcross(t, ToPeer, "this machine")
		case c.local == nil && c.remote != nil:
			c.assertOnlyCopiedAcross(t, ToUs, "the peer")
		}
	})
}

// sourceNames is the set of names the machine an action reads from held.
func (c planCase) sourceNames(way Way) map[string]bool {
	if way == ToUs {
		return c.there
	}
	return c.here
}

// assertOnlyCopiedAcross checks that the one thing the plan may do with a file
// only one machine has is copy it to the machine that lacks it.
//
// It is written as a whitelist rather than as "not the other way" on purpose.
// A saboteur adding a third direction can add it to everyWay as well and get
// past the check above; they cannot get past this one, because anything that
// is not the copy toward the missing side fails here whatever it is called.
// And a plan that neither copies the file nor says why fails too: quietly
// forgetting a file is how a sync stops being one.
func (c planCase) assertOnlyCopiedAcross(t *testing.T, toward Way, holder string) {
	t.Helper()

	for _, a := range c.plan.Actions {
		if a.Way != toward {
			t.Fatalf("only %s has %s, so the only thing that may happen to it is a copy "+
				"to the machine that lacks it; the plan does this instead: %+v",
				holder, theKey, a)
		}
	}
	if len(c.plan.Actions) == 0 && len(c.plan.Skips) == 0 {
		t.Fatalf("only %s has %s, and the plan neither copies it nor says why not: %+v",
			holder, theKey, c.plan)
	}
}

// TestATwoSidedChangeCanNeverOverwriteEitherSide is rule 2, enumerated.
//
// Whenever both machines hold the file, their contents differ, and neither
// content is the one the history remembers, the only thing the plan may do is
// write the peer's copy under a name that did not exist. No action may target
// a name either machine already holds, and the record left behind must
// remember both sides — because a record that remembers only one is what would
// let the *next* pass do the overwriting this one refused.
func TestATwoSidedChangeCanNeverOverwriteEitherSide(t *testing.T) {
	t.Parallel()

	matrix(t, func(t *testing.T, c planCase) {
		if !c.twoSided() {
			return
		}
		for _, a := range c.plan.Actions {
			if !a.Conflict {
				t.Fatalf("both machines changed %s and the plan copies over it: %+v", theKey, a)
			}
			dest := c.destinationNames(a.Way)
			if dest[fold(a.Dest)] {
				t.Fatalf("both machines changed %s and the conflict copy lands on "+
					"a file that already exists: %+v", theKey, a)
			}
		}
		if len(c.plan.Actions) == 0 {
			// Nothing to copy is a legitimate answer only when the pass could
			// not read the file: rule 4 beats rule 2's copy, never its refusal.
			return
		}
		rec, recorded := c.plan.Next[fold(theKey)]
		if !recorded || rec.Local.Hash != c.local.Hash || rec.Remote.Hash != c.remote.Hash {
			t.Fatalf("record = %+v (recorded %v), want both machines' own content "+
				"remembered so the next pass cannot pick a winner", rec, recorded)
		}
	})
}

// destinationNames is the set of names the machine an action writes to held.
func (c planCase) destinationNames(way Way) map[string]bool {
	if way == ToPeer {
		return c.there
	}
	return c.here
}

// twoSided reports the condition rule 2 is about, worked out from the fixture
// rather than from the plan: both machines hold the file, both have been
// weighed, their contents differ, and neither is what the history remembers.
func (c planCase) twoSided() bool {
	if c.local == nil || c.remote == nil {
		return false
	}
	if c.local.Hash == "" || c.remote.Hash == "" || c.local.Hash == c.remote.Hash {
		return false
	}
	rec, hasRec := c.base[fold(theKey)]
	return changed(*c.local, rec.Local, hasRec) && changed(*c.remote, rec.Remote, hasRec)
}
