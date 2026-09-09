package foldersync

import (
	"testing"
	"time"
)

// The fixtures. Times are whole seconds because that is the granularity the
// decision compares at, and stating them as integers keeps a test readable.

// at is a stamp, given as seconds since the epoch.
func at(sec int64) time.Time { return time.Unix(sec, 0).UTC() }

// file is one listing entry.
func file(p string, size, sec int64, hash string) Entry {
	return Entry{Path: p, Size: size, MTime: at(sec), Hash: hash}
}

// st is one side of a record.
func st(size, sec int64, hash string) State {
	return State{Size: size, MTime: at(sec), Hash: hash}
}

// agreed is a record both machines were last seen holding the same content in.
func agreed(size, sec int64, hash string) Record {
	return Record{Local: st(size, sec, hash), Remote: st(size, sec, hash)}
}

// noon is the stamp a conflict copy in these tests is named after: 12:34.
var noon = time.Date(2026, 9, 8, 12, 34, 0, 0, time.UTC)

// input is an Input with the fields every test sets the same way.
func input(local, remote []Entry, base Baseline) Input {
	return Input{
		Local: local, Remote: remote, Baseline: base,
		PeerName: "win-sttm11d02rd", Now: noon,
	}
}

// onlyAction insists the plan holds exactly one action and returns it.
func onlyAction(t *testing.T, plan Plan) Action {
	t.Helper()

	if len(plan.Actions) != 1 {
		t.Fatalf("actions = %+v, want exactly one", plan.Actions)
	}
	if len(plan.Hash) != 0 {
		t.Fatalf("the plan still wants hashes: %+v", plan.Hash)
	}
	return plan.Actions[0]
}

// onlySkip insists the plan holds exactly one skip and returns it.
func onlySkip(t *testing.T, plan Plan) Skip {
	t.Helper()

	if len(plan.Skips) != 1 {
		t.Fatalf("skips = %+v, want exactly one", plan.Skips)
	}
	return plan.Skips[0]
}

func TestAFileOnlyThisMachineHasGoesToThePeer(t *testing.T) {
	t.Parallel()

	plan := Decide(input([]Entry{file("notes.txt", 10, 100, "")}, nil, nil))

	got := onlyAction(t, plan)
	want := Action{Key: "notes.txt", Way: ToPeer, Path: "notes.txt", Dest: "notes.txt", Size: 10}
	if got != want {
		t.Errorf("action = %+v, want %+v", got, want)
	}
	if len(plan.Skips) != 0 {
		t.Errorf("skips = %+v, want none", plan.Skips)
	}
}

// This is rule 1 seen from the side that most often gets it wrong. The peer
// has a file we do not; whether that is because it is new there or because
// somebody deleted ours, the answer is the same and it is never a deletion.
func TestAFileOnlyThePeerHasComesHereEvenWhenWeOnceHadIt(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		base Baseline
	}{
		{"new on the peer", nil},
		{"deleted here", Baseline{"notes.txt": agreed(10, 100, "aa")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			plan := Decide(input(nil, []Entry{file("notes.txt", 10, 100, "")}, tc.base))

			got := onlyAction(t, plan)
			want := Action{Key: "notes.txt", Way: ToUs, Path: "notes.txt", Dest: "notes.txt", Size: 10}
			if got != want {
				t.Errorf("action = %+v, want %+v", got, want)
			}
		})
	}
}

func TestAFilePresentOnBothSidesIsWeighedBeforeAnythingIsDecided(t *testing.T) {
	t.Parallel()

	plan := Decide(input(
		[]Entry{file("notes.txt", 10, 100, "")},
		[]Entry{file("notes.txt", 11, 200, "")},
		nil,
	))

	if len(plan.Actions) != 0 {
		t.Errorf("actions = %+v, want none until the hashes are known", plan.Actions)
	}
	want := []Need{{Local: "notes.txt", Remote: "notes.txt"}}
	if len(plan.Hash) != 1 || plan.Hash[0] != want[0] {
		t.Errorf("hash = %+v, want %+v", plan.Hash, want)
	}
}

func TestTheSameBytesOnBothMachinesNeedNothingDoing(t *testing.T) {
	t.Parallel()

	plan := Decide(input(
		[]Entry{file("notes.txt", 10, 100, "aa")},
		[]Entry{file("notes.txt", 10, 900, "aa")},
		nil,
	))

	if len(plan.Actions) != 0 || len(plan.Skips) != 0 {
		t.Fatalf("plan = %+v, want nothing to do", plan)
	}
	// The stamps differed and the record now says so, so the next pass reads
	// both sides as unchanged and does not weigh them again.
	got, recorded := plan.Next["notes.txt"]
	if !recorded {
		t.Fatal("nothing was recorded for a file that is already in step")
	}
	if got.Local.MTime != at(100) || got.Remote.MTime != at(900) {
		t.Errorf("record = %+v, want each side's own stamp", got)
	}
}

func TestAFileChangedOnOneSideOnlyIsCopiedTheOtherWay(t *testing.T) {
	t.Parallel()

	base := Baseline{"notes.txt": agreed(10, 100, "old")}
	for _, tc := range []struct {
		name   string
		local  Entry
		remote Entry
		want   Action
	}{
		{
			name:   "changed here",
			local:  file("notes.txt", 12, 300, "new"),
			remote: file("notes.txt", 10, 100, "old"),
			want: Action{
				Key: "notes.txt", Way: ToPeer, Path: "notes.txt", Dest: "notes.txt", Size: 12,
			},
		},
		{
			name:   "changed on the peer",
			local:  file("notes.txt", 10, 100, "old"),
			remote: file("notes.txt", 12, 300, "new"),
			want: Action{
				Key: "notes.txt", Way: ToUs, Path: "notes.txt", Dest: "notes.txt", Size: 12,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			plan := Decide(input([]Entry{tc.local}, []Entry{tc.remote}, base))

			if got := onlyAction(t, plan); got != tc.want {
				t.Errorf("action = %+v, want %+v", got, tc.want)
			}
			if got, kept := plan.Fallback["notes.txt"]; !kept || got != base["notes.txt"] {
				t.Errorf("fallback = %+v (kept %v), want the previous record", got, kept)
			}
		})
	}
}

// Rule 2. Both machines changed the file, so both copies survive: the peer's
// arrives under a new name and the local file is not in any action at all.
func TestAFileChangedOnBothMachinesBecomesAConflictCopy(t *testing.T) {
	t.Parallel()

	base := Baseline{"заметки.txt": agreed(10, 100, "old")}
	plan := Decide(input(
		[]Entry{file("заметки.txt", 12, 300, "mine")},
		[]Entry{file("заметки.txt", 14, 400, "theirs")},
		base,
	))

	got := onlyAction(t, plan)
	want := Action{
		Key:  "заметки.txt",
		Way:  ToUs,
		Path: "заметки.txt",
		Dest: "заметки (с win-sttm11d02rd, 12-34).txt",
		Size: 14, Conflict: true,
	}
	if got != want {
		t.Errorf("action = %+v, want %+v", got, want)
	}
	// The record remembers both sides on purpose, so the next pass does not
	// read the divergence as a one-sided change.
	rec := plan.Next["заметки.txt"]
	if rec.Local.Hash != "mine" || rec.Remote.Hash != "theirs" {
		t.Errorf("record = %+v, want both sides remembered", rec)
	}
}

// With no history there is no way to tell which side changed, so the answer is
// the safe one. This is the whole behaviour a lost state file buys.
func TestWithNoHistoryADifferenceIsAConflictAndNeverAnOverwrite(t *testing.T) {
	t.Parallel()

	plan := Decide(input(
		[]Entry{file("notes.txt", 12, 300, "mine")},
		[]Entry{file("notes.txt", 14, 100, "theirs")},
		nil,
	))

	if got := onlyAction(t, plan); !got.Conflict || got.Dest == got.Path {
		t.Errorf("action = %+v, want a conflict copy under a new name", got)
	}
}

// A record with no hash in it cannot say whether a side changed, so it must
// not be believed. The safe reading is "changed", and two of those is a
// conflict.
func TestARecordWithNoHashIsNotEvidenceOfAnything(t *testing.T) {
	t.Parallel()

	base := Baseline{"notes.txt": Record{Local: st(10, 100, ""), Remote: st(10, 100, "")}}
	plan := Decide(input(
		[]Entry{file("notes.txt", 12, 300, "mine")},
		[]Entry{file("notes.txt", 14, 400, "theirs")},
		base,
	))

	if got := onlyAction(t, plan); !got.Conflict {
		t.Errorf("action = %+v, want a conflict copy", got)
	}
}

func TestAConflictAlreadyReportedIsSaidAgainAndNotActedOn(t *testing.T) {
	t.Parallel()

	base := Baseline{"notes.txt": Record{Local: st(12, 300, "mine"), Remote: st(14, 400, "theirs")}}
	plan := Decide(input(
		[]Entry{file("notes.txt", 12, 300, "mine")},
		[]Entry{file("notes.txt", 14, 400, "theirs")},
		base,
	))

	if len(plan.Actions) != 0 {
		t.Errorf("actions = %+v, want none: nobody has resolved it yet", plan.Actions)
	}
	if got := onlySkip(t, plan); got.Why != WhyUnresolved || got.Path != "notes.txt" {
		t.Errorf("skip = %+v, want notes.txt unresolved", got)
	}
	if plan.Next["notes.txt"] != base["notes.txt"] {
		t.Error("the record moved, so the same conflict would be reported differently next pass")
	}
}

// The same divergence, found the expensive way: something touched the local
// file's stamp, so the pass had to weigh it — and the weighing says the
// content is the one already recorded. Still nothing to do, still said aloud.
func TestATouchedFileInAConflictIsWeighedAndStillLeftAlone(t *testing.T) {
	t.Parallel()

	base := Baseline{"notes.txt": Record{Local: st(12, 300, "mine"), Remote: st(14, 400, "theirs")}}
	plan := Decide(input(
		[]Entry{file("notes.txt", 12, 301, "mine")},
		[]Entry{file("notes.txt", 14, 400, "theirs")},
		base,
	))

	if len(plan.Actions) != 0 {
		t.Errorf("actions = %+v, want none", plan.Actions)
	}
	if got := onlySkip(t, plan); got.Why != WhyUnresolved {
		t.Errorf("skip = %+v, want the divergence reported again", got)
	}
	if plan.Next["notes.txt"] != base["notes.txt"] {
		t.Error("the record moved for a file nothing was done to")
	}
}

func TestBothSidesExactlyAsTheyWereLeftCostNotEvenAHash(t *testing.T) {
	t.Parallel()

	base := Baseline{"notes.txt": agreed(10, 100, "aa")}
	plan := Decide(input(
		[]Entry{file("notes.txt", 10, 100, "")},
		[]Entry{file("notes.txt", 10, 100, "")},
		base,
	))

	if len(plan.Hash) != 0 || len(plan.Actions) != 0 || len(plan.Skips) != 0 {
		t.Fatalf("plan = %+v, want nothing at all", plan)
	}
	if plan.Next["notes.txt"].Local.Hash != "aa" {
		t.Errorf("record = %+v, want the hash carried forward", plan.Next["notes.txt"])
	}
}

// Rule 4. A file the pass could not read is left for the next one, with no
// error and with its record untouched.
func TestAFileThatCouldNotBeReadIsLeftForNextTime(t *testing.T) {
	t.Parallel()

	base := Baseline{"notes.txt": agreed(10, 100, "aa")}
	in := input(
		[]Entry{file("notes.txt", 12, 300, "")},
		[]Entry{file("notes.txt", 10, 100, "")},
		base,
	)
	in.Unreadable = []string{"notes.txt"}

	plan := Decide(in)

	if len(plan.Actions) != 0 {
		t.Errorf("actions = %+v, want none", plan.Actions)
	}
	if got := onlySkip(t, plan); got.Why != WhyBusy || got.Size != 12 {
		t.Errorf("skip = %+v, want notes.txt busy at its local size", got)
	}
	if plan.Next["notes.txt"] != base["notes.txt"] {
		t.Error("the record changed for a file nothing was done to")
	}
}

// The same, for a file only the peer has: the skip must still name it, and it
// must name it from the only listing that holds it.
func TestABusyFileOnlyThePeerHasIsStillNamed(t *testing.T) {
	t.Parallel()

	in := input(nil, []Entry{file("theirs.txt", 7, 100, "")}, nil)
	in.Unreadable = []string{"theirs.txt"}

	plan := Decide(in)

	got := onlySkip(t, plan)
	if got.Path != "theirs.txt" || got.Size != 7 || got.Why != WhyBusy {
		t.Errorf("skip = %+v, want theirs.txt named at 7 bytes", got)
	}
}

// Rule 5. A file over the cap is named, and it is left alone on both machines:
// a cap that applied to one copy and not the other would be a licence to
// overwrite the big one with the small one.
func TestAFileOverTheCapIsNamedAndLeftAloneOnBothMachines(t *testing.T) {
	t.Parallel()

	in := input(
		[]Entry{file("big.iso", 5000, 100, "mine")},
		[]Entry{file("big.iso", 3, 100, "theirs")},
		nil,
	)
	in.Limits = Limits{MaxBytes: 1000}

	plan := Decide(in)

	if len(plan.Actions) != 0 {
		t.Fatalf("actions = %+v, want none for a file over the cap", plan.Actions)
	}
	if got := onlySkip(t, plan); got.Why != WhyTooBig || got.Path != "big.iso" || got.Size != 5000 {
		t.Errorf("skip = %+v, want big.iso named at its real size", got)
	}
}

// The other way round: the peer's copy is the big one. It must still be named
// once, not twice, and the small local copy must not be pushed over it.
func TestAFileOverTheCapOnThePeerIsNamedOnceAndNotOverwritten(t *testing.T) {
	t.Parallel()

	in := input(
		[]Entry{file("big.iso", 3, 100, "mine")},
		[]Entry{file("big.iso", 5000, 100, "theirs")},
		nil,
	)
	in.Limits = Limits{MaxBytes: 1000}

	plan := Decide(in)

	if len(plan.Actions) != 0 {
		t.Fatalf("actions = %+v, want none", plan.Actions)
	}
	if got := onlySkip(t, plan); got.Size != 5000 {
		t.Errorf("skip = %+v, want the peer's size named", got)
	}
}

func TestAFileOverTheCapOnBothMachinesIsNamedOnlyOnce(t *testing.T) {
	t.Parallel()

	in := input(
		[]Entry{file("big.iso", 5000, 100, "")},
		[]Entry{file("big.iso", 6000, 100, "")},
		nil,
	)
	in.Limits = Limits{MaxBytes: 1000}

	if got := onlySkip(t, Decide(in)); got.Path != "big.iso" {
		t.Errorf("skip = %+v, want one entry for big.iso", got)
	}
}

// Rule 7. A name from the peer is untrusted input: it is refused, named, and
// it takes the local file of the same key with it rather than being quietly
// paired with one.
func TestANameThatReachesOutsideTheFolderIsRefusedAndNamed(t *testing.T) {
	t.Parallel()

	plan := Decide(input(nil, []Entry{file("../../windows/system32/evil.dll", 9, 100, "")}, nil))

	if len(plan.Actions) != 0 {
		t.Fatalf("actions = %+v, want none for an unsafe name", plan.Actions)
	}
	if got := onlySkip(t, plan); got.Why != WhyUnsafeName {
		t.Errorf("skip = %+v, want the name refused", got)
	}
}

func TestAnExcludedFileIsNotEvenMentioned(t *testing.T) {
	t.Parallel()

	in := input(
		[]Entry{file(".git/config", 10, 100, ""), file("build/out.tmp", 10, 100, "")},
		nil, nil,
	)

	plan := Decide(in)

	if len(plan.Actions) != 0 || len(plan.Skips) != 0 || len(plan.Hash) != 0 {
		t.Errorf("plan = %+v, want nothing: .git and *.tmp are excluded", plan)
	}
}

// Two machines that disagree only about a name's case hold one file, not two.
// Keying them apart is how a case-only rename could copy the older content
// over the newer one.
func TestNamesThatDifferOnlyInCaseAreOneFile(t *testing.T) {
	t.Parallel()

	base := Baseline{"notes.txt": agreed(10, 100, "old")}
	plan := Decide(input(
		[]Entry{file("Notes.TXT", 12, 300, "new")},
		[]Entry{file("notes.txt", 10, 100, "old")},
		base,
	))

	got := onlyAction(t, plan)
	if got.Way != ToPeer || got.Path != "Notes.TXT" || got.Dest != "notes.txt" {
		t.Errorf("action = %+v, want our Notes.TXT written onto their notes.txt", got)
	}
}

func TestAPassIsDeterministicWhateverOrderTheListingsArriveIn(t *testing.T) {
	t.Parallel()

	forward := []Entry{
		file("b.txt", 1, 100, ""), file("a.txt", 1, 100, ""), file("c.txt", 1, 100, ""),
	}
	backward := []Entry{forward[2], forward[0], forward[1]}

	first := Decide(input(forward, nil, nil))
	second := Decide(input(backward, nil, nil))

	if len(first.Actions) != 3 {
		t.Fatalf("actions = %+v, want three", first.Actions)
	}
	for i := range first.Actions {
		if first.Actions[i] != second.Actions[i] {
			t.Fatalf("action %d = %+v and %+v, want the same order either way",
				i, first.Actions[i], second.Actions[i])
		}
	}
	if first.Actions[0].Path != "a.txt" {
		t.Errorf("first action = %+v, want a.txt", first.Actions[0])
	}
}

func TestSkipsAndHashesAreSortedToo(t *testing.T) {
	t.Parallel()

	in := input(
		[]Entry{file("b.txt", 5000, 100, ""), file("a.txt", 5000, 100, "")},
		[]Entry{file("d.txt", 1, 100, ""), file("c.txt", 1, 100, "")},
		nil,
	)
	in.Local = append(in.Local, file("c.txt", 2, 100, ""), file("d.txt", 2, 100, ""))
	in.Limits = Limits{MaxBytes: 1000}

	plan := Decide(in)

	if len(plan.Skips) != 2 || plan.Skips[0].Path != "a.txt" {
		t.Errorf("skips = %+v, want a.txt first", plan.Skips)
	}
	if len(plan.Hash) != 2 || plan.Hash[0].Local != "c.txt" {
		t.Errorf("hash = %+v, want c.txt first", plan.Hash)
	}
}
