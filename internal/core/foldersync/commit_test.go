package foldersync

import "testing"

func TestCommitRecordsACopyThatLanded(t *testing.T) {
	t.Parallel()

	plan := Decide(input([]Entry{file("notes.txt", 10, 100, "")}, nil, nil))
	action := plan.Actions[0]

	base := Commit(plan, []Result{{
		Key:   action.Key,
		OK:    true,
		Read:  st(10, 100, "aa"),
		Wrote: st(10, 100, "aa"),
	}})

	got, recorded := base["notes.txt"]
	if !recorded {
		t.Fatal("a copy that landed was not recorded")
	}
	if got.Local.Hash != "aa" || got.Remote.Hash != "aa" {
		t.Errorf("record = %+v, want both sides at the hash that travelled", got)
	}
}

// This is the rule Commit exists for. A copy that failed must not be recorded
// as synced: the next pass would then read the side still holding the older
// content as "unchanged" and overwrite the newer one with it.
func TestCommitRefusesToBelieveACopyThatFailed(t *testing.T) {
	t.Parallel()

	previous := agreed(10, 100, "old")
	plan := Decide(input(
		[]Entry{file("notes.txt", 12, 300, "new")},
		[]Entry{file("notes.txt", 10, 100, "old")},
		Baseline{"notes.txt": previous},
	))

	for _, tc := range []struct {
		name    string
		results []Result
	}{
		{"reported as failed", []Result{{Key: "notes.txt", OK: false}}},
		{"not reported at all", nil},
		{"reported for another file", []Result{{Key: "other.txt", OK: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			base := Commit(plan, tc.results)
			if got := base["notes.txt"]; got != previous {
				t.Errorf("record = %+v, want the previous one to stand: %+v", got, previous)
			}
		})
	}
}

// A copy that failed and had no previous record must leave nothing behind.
// With no record the next pass calls a difference a conflict, which is the
// safe answer; a fabricated record would make it call it a change.
func TestCommitForgetsAFileItNeverAgreedOn(t *testing.T) {
	t.Parallel()

	plan := Decide(input([]Entry{file("notes.txt", 10, 100, "")}, nil, nil))

	base := Commit(plan, []Result{{Key: "notes.txt", OK: false}})
	if _, recorded := base["notes.txt"]; recorded {
		t.Errorf("base = %+v, want nothing recorded for a copy that never landed", base)
	}
}

func TestCommitPutsEachSidesStateOnTheRightSide(t *testing.T) {
	t.Parallel()

	base := Baseline{"notes.txt": agreed(10, 100, "old")}
	for _, tc := range []struct {
		name       string
		local      Entry
		remote     Entry
		wantLocal  int64
		wantRemote int64
	}{
		{
			name:  "to the peer",
			local: file("notes.txt", 12, 300, "new"), remote: file("notes.txt", 10, 100, "old"),
			wantLocal: 300, wantRemote: 301,
		},
		{
			name:  "to us",
			local: file("notes.txt", 10, 100, "old"), remote: file("notes.txt", 12, 300, "new"),
			wantLocal: 301, wantRemote: 300,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			plan := Decide(input([]Entry{tc.local}, []Entry{tc.remote}, base))
			action := plan.Actions[0]
			// Read is the source as it was left; Wrote is the destination as
			// it came out, one second later here so the two are told apart.
			next := Commit(plan, []Result{{
				Key: action.Key, OK: true,
				Read:  st(12, 300, "new"),
				Wrote: st(12, 301, "new"),
			}})

			got := next["notes.txt"]
			if got.Local.MTime.Unix() != tc.wantLocal || got.Remote.MTime.Unix() != tc.wantRemote {
				t.Errorf("record = %+v, want local at %d and peer at %d",
					got, tc.wantLocal, tc.wantRemote)
			}
		})
	}
}

// A conflict copy landed under another name, so it says nothing about the file
// it was made from: the record the plan already worked out — both sides, as
// they are — is the one that stands.
func TestCommitKeepsTheTwoSidedRecordAConflictLeaves(t *testing.T) {
	t.Parallel()

	plan := Decide(input(
		[]Entry{file("notes.txt", 12, 300, "mine")},
		[]Entry{file("notes.txt", 14, 400, "theirs")},
		Baseline{"notes.txt": agreed(10, 100, "old")},
	))
	action := plan.Actions[0]
	if !action.Conflict {
		t.Fatalf("action = %+v, want a conflict", action)
	}

	next := Commit(plan, []Result{{
		Key: action.Key, OK: true,
		Read:  st(14, 400, "theirs"),
		Wrote: st(14, 400, "theirs"),
	}})

	got := next["notes.txt"]
	if got.Local.Hash != "mine" || got.Remote.Hash != "theirs" {
		t.Errorf("record = %+v, want each machine's own content remembered", got)
	}
}

// The same conflict, whose copy could not be written: the previous record must
// stand so the next pass tries the copy again rather than deciding the
// divergence is settled.
func TestCommitRetriesAConflictCopyThatFailed(t *testing.T) {
	t.Parallel()

	previous := agreed(10, 100, "old")
	plan := Decide(input(
		[]Entry{file("notes.txt", 12, 300, "mine")},
		[]Entry{file("notes.txt", 14, 400, "theirs")},
		Baseline{"notes.txt": previous},
	))

	next := Commit(plan, []Result{{Key: plan.Actions[0].Key, OK: false}})
	if got := next["notes.txt"]; got != previous {
		t.Errorf("record = %+v, want the previous one: %+v", got, previous)
	}
}

func TestCommitDoesNotWriteThroughToThePlan(t *testing.T) {
	t.Parallel()

	plan := Decide(input(
		[]Entry{file("quiet.txt", 10, 100, "")},
		[]Entry{file("quiet.txt", 10, 100, "")},
		Baseline{"quiet.txt": agreed(10, 100, "aa")},
	))

	next := Commit(plan, nil)
	next["quiet.txt"] = agreed(99, 99, "tampered")

	if plan.Next["quiet.txt"].Local.Hash != "aa" {
		t.Error("the plan's own record was overwritten through the baseline Commit returned")
	}
}

// A file that has gone from both machines is dropped from the baseline: the
// state file describes what is there, and a row about a file nobody has is a
// row that will be wrong one day.
func TestCommitForgetsFilesNeitherMachineHasAnyMore(t *testing.T) {
	t.Parallel()

	plan := Decide(input(nil, nil, Baseline{"gone.txt": agreed(10, 100, "aa")}))

	if next := Commit(plan, nil); len(next) != 0 {
		t.Errorf("base = %+v, want nothing carried for a file neither machine has", next)
	}
}
