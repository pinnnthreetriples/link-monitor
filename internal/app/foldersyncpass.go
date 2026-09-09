package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"

	"github.com/pinnnthreetriples/link-monitor/internal/core/foldersync"
)

// passResult is what one sweep achieved.
type passResult struct {
	moved   []SyncMoved
	skipped []SyncSkipped
	// failure is one Russian sentence, or empty when the pass finished.
	failure string
	// lost says the history could not be read, so every difference this pass
	// found became a conflict copy rather than a copy.
	lost bool
}

// sweep is one pass: the two trees, the facts they gave, and what came of it.
type sweep struct {
	f     *Folder
	here  Tree
	there Tree
	in    foldersync.Input
	lost  bool
}

// sweep opens the peer's side, runs one pass and closes it again.
func (f *Folder) sweep(ctx context.Context) passResult {
	there, closer, err := f.deps.There.Open(ctx)
	if err != nil {
		f.deps.Log.Warn("opening the shared folder on the peer", "err", err)
		return passResult{failure: msgSyncNoPeer}
	}
	defer func() {
		if err := closer.Close(); err != nil {
			f.deps.Log.Warn("closing the sftp session", "err", err)
		}
	}()

	s := &sweep{f: f, here: f.deps.Here, there: there}
	base, err := f.deps.History.Load(f.cfg.Root, f.cfg.PeerName)
	if err != nil {
		// Not fatal, and deliberately not silent. With no history every
		// difference becomes a conflict copy, and the user has to be told that
		// before they find the copies and wonder where they came from.
		f.deps.Log.Warn("reading the sync state", "err", err)
		s.lost = true
	}
	return s.run(ctx, base)
}

// run is the pass: look, decide, weigh what the decision asked for, decide
// again, carry it out, record it.
func (s *sweep) run(ctx context.Context, base foldersync.Baseline) passResult {
	keep := func(rel string) bool { return !foldersync.Excluded(rel, s.f.cfg.Limits) }

	here, err := s.here.Scan(ctx, keep)
	if err != nil {
		s.f.deps.Log.Warn("listing the shared folder here", "err", err)
		return passResult{failure: msgSyncScanHere, lost: s.lost}
	}
	there, err := s.there.Scan(ctx, keep)
	if err != nil {
		s.f.deps.Log.Warn("listing the shared folder on the peer", "err", err)
		return passResult{failure: msgSyncScanThere, lost: s.lost}
	}

	s.in = foldersync.Input{
		Local: here, Remote: there, Baseline: base,
		Limits: s.f.cfg.Limits, PeerName: s.f.cfg.PeerName, Now: s.f.deps.Now(),
	}
	plan := foldersync.Decide(s.in)
	if len(plan.Hash) > 0 {
		// The cheap listing was not enough to rule on these, so their content
		// is weighed and the same decision is asked again — which is the whole
		// of "content, not clocks".
		s.weighAll(ctx, plan.Hash)
		plan = foldersync.Decide(s.in)
	}
	return s.carryOut(ctx, plan)
}

// weighAll reads the content hash of every file the decision asked about, on
// both machines, and puts the answers back into the listing.
//
// A file that will not give up a hash is not a failure: it is a file somebody
// is writing, and it is named in Unreadable so the decision leaves it for the
// next pass. Both sides are weighed even when one has already refused, because
// stopping early would only hide which of them is busy.
func (s *sweep) weighAll(ctx context.Context, needs []foldersync.Need) {
	hereStates := make(map[string]foldersync.State, len(needs))
	thereStates := make(map[string]foldersync.State, len(needs))

	for _, need := range needs {
		if ctx.Err() != nil {
			break
		}
		hereState, hereErr := weigh(ctx, s.here, need.Local)
		thereState, thereErr := weigh(ctx, s.there, need.Remote)
		if hereErr != nil || thereErr != nil {
			s.f.deps.Log.Debug("a file could not be weighed this pass",
				"file", need.Local, "here", hereErr, "peer", thereErr)
			s.in.Unreadable = append(s.in.Unreadable, need.Local)
			continue
		}
		hereStates[need.Local] = hereState
		thereStates[need.Remote] = thereState
	}
	applyStates(s.in.Local, hereStates)
	applyStates(s.in.Remote, thereStates)
}

// applyStates writes what was weighed back into the listing, stamps and all:
// the weighing is more recent than the scan, and the copy that follows checks
// the file against the freshest facts we have.
func applyStates(entries []foldersync.Entry, states map[string]foldersync.State) {
	for i := range entries {
		if st, weighed := states[entries[i].Path]; weighed {
			entries[i].Size, entries[i].MTime, entries[i].Hash = st.Size, st.MTime, st.Hash
		}
	}
}

// carryOut performs the plan and records what the pass did.
func (s *sweep) carryOut(ctx context.Context, plan foldersync.Plan) passResult {
	res := passResult{lost: s.lost}
	hereBy := byPath(s.in.Local)
	thereBy := byPath(s.in.Remote)

	results := make([]foldersync.Result, 0, len(plan.Actions))
	for _, action := range plan.Actions {
		if ctx.Err() != nil {
			break
		}
		from, to, scanned := s.sides(action, hereBy, thereBy)
		out, outcome := s.carry(ctx, action, from, to, scanned)
		switch outcome {
		case carried:
			results = append(results, out)
			res.moved = append(res.moved, movedOf(action))
		case busy:
			res.skipped = append(res.skipped,
				SyncSkipped{Name: action.Path, Size: action.Size, Why: foldersync.WhyBusy})
		case broke:
			res.failure = msgSyncCopyFailed
		}
	}
	for _, skip := range plan.Skips {
		res.skipped = append(res.skipped,
			SyncSkipped{Name: skip.Path, Size: skip.Size, Why: skip.Why})
	}

	// Commit is what decides which of the plan's records may be believed. A
	// copy that failed must never be recorded as synced, and that rule lives
	// in core rather than here.
	next := foldersync.Commit(plan, results)
	if err := s.f.deps.History.Save(s.f.cfg.Root, s.f.cfg.PeerName, next); err != nil {
		s.f.deps.Log.Warn("writing the sync state", "err", err)
		if res.failure == "" {
			res.failure = msgSyncStateFailed
		}
	}
	trimLists(&res)
	return res
}

// sides says which tree reads, which writes, and what the decision was told
// the source file looked like.
func (s *sweep) sides(
	a foldersync.Action, hereBy, thereBy map[string]foldersync.Entry,
) (from, to Tree, scanned foldersync.Entry) {
	if a.Way == foldersync.ToPeer {
		return s.here, s.there, hereBy[a.Path]
	}
	return s.there, s.here, thereBy[a.Path]
}

// outcome says what became of one copy.
type outcome int

const (
	// carried is a copy that landed.
	carried outcome = iota
	// busy is rule 4: the source file was moving, or would not open. Not a
	// fault, not a fix card — a file left for the next pass.
	busy
	// broke is a copy that failed for a reason worth telling the user about.
	broke
)

// carry performs one copy: read one side, write the other through a temporary
// name, and publish it only if the source held still throughout.
func (s *sweep) carry(
	ctx context.Context, a foldersync.Action, from, to Tree, scanned foldersync.Entry,
) (foldersync.Result, outcome) {
	before, err := from.Stat(ctx, a.Path)
	if err != nil {
		s.f.deps.Log.Debug("a file could not be looked at this pass", "file", a.Path, "err", err)
		return foldersync.Result{}, busy
	}
	if !steady(stateOf(scanned), before) {
		// It moved between the listing and now. Somebody is saving it.
		return foldersync.Result{}, busy
	}

	sum := sha256.New()
	var read foldersync.State
	var sourceMoved error
	writeErr := to.Receive(ctx, a.Dest, before.MTime, func(w io.Writer) error {
		state, err := copyThrough(ctx, from, a.Path, w, sum, before)
		if err != nil {
			sourceMoved = err
			return err
		}
		read = state
		return nil
	})
	switch {
	case sourceMoved != nil:
		// Nothing was published: Receive removed its own temporary file when
		// the closure refused. Rule 4 all the way to the last moment.
		s.f.deps.Log.Debug("a copy was abandoned because the source moved",
			"file", a.Path, "err", sourceMoved)
		return foldersync.Result{}, busy
	case writeErr != nil:
		s.f.deps.Log.Warn("writing a copy", "file", a.Dest, "err", writeErr)
		return foldersync.Result{}, broke
	}
	return s.check(ctx, a, to, read)
}

// check makes sure what landed is as long as what was sent. It is cheap, and
// it is the difference between "the copy finished" and "the copy arrived".
//
// What it does NOT do is read the file back to compare hashes: that would
// double every transfer, and SSH already carries a message authentication code
// over every byte of the channel this rode.
func (s *sweep) check(
	ctx context.Context, a foldersync.Action, to Tree, read foldersync.State,
) (foldersync.Result, outcome) {
	wrote, err := to.Stat(ctx, a.Dest)
	if err != nil {
		s.f.deps.Log.Warn("looking at a copy after writing it", "file", a.Dest, "err", err)
		return foldersync.Result{}, broke
	}
	if wrote.Size != read.Size {
		s.f.deps.Log.Warn("a copy arrived the wrong length",
			"file", a.Dest, "sent", read.Size, "arrived", wrote.Size)
		return foldersync.Result{}, broke
	}
	return foldersync.Result{
		Key:   a.Key,
		OK:    true,
		Read:  read,
		Wrote: foldersync.State{Size: wrote.Size, MTime: wrote.MTime, Hash: read.Hash},
	}, carried
}

// weigh reads one file and reports its content hash together with the length
// and stamp it carried while being read.
func weigh(ctx context.Context, t Tree, rel string) (foldersync.State, error) {
	before, err := t.Stat(ctx, rel)
	if err != nil {
		return foldersync.State{}, fmt.Errorf("%w: %w", errBusy, err)
	}
	return copyThrough(ctx, t, rel, io.Discard, sha256.New(), before)
}

// copyThrough reads one file into w, counting every byte past sum, and refuses
// the whole thing if the file moved while it was being read.
//
// This is rule 4 where it actually bites. A file whose length or stamp changed
// underneath the read has just handed us a mixture of two versions, and both
// hashing it and publishing it would be wrong.
func copyThrough(
	ctx context.Context, from Tree, rel string, w io.Writer, sum hash.Hash, before foldersync.State,
) (foldersync.State, error) {
	body, err := from.Open(ctx, rel)
	if err != nil {
		// Rule 4 names this case: a file that will not open for reading is
		// skipped this round, not reported as a fault.
		return foldersync.State{}, fmt.Errorf("%w: %w", errBusy, err)
	}
	// A read-only handle: closing it cannot lose data, so the error cannot
	// matter — the same reasoning internal/adapters/tailscale states.
	defer func() { _ = body.Close() }()

	if _, err := io.Copy(w, io.TeeReader(body, sum)); err != nil {
		return foldersync.State{}, fmt.Errorf("reading %s: %w", rel, err)
	}
	after, err := from.Stat(ctx, rel)
	if err != nil {
		return foldersync.State{}, fmt.Errorf("%w: %w", errBusy, err)
	}
	if !steady(before, after) {
		return foldersync.State{}, fmt.Errorf("%s moved while it was read: %w", rel, errBusy)
	}
	return foldersync.State{
		Size:  after.Size,
		MTime: after.MTime,
		Hash:  hex.EncodeToString(sum.Sum(nil)),
	}, nil
}

// steady reports whether a file looks exactly as it did a moment ago. Stamps
// are compared to the second, because a second is all SFTP carries.
func steady(a, b foldersync.State) bool {
	return a.Size == b.Size && a.MTime.Unix() == b.MTime.Unix()
}

// stateOf is one listing entry as a state.
func stateOf(e foldersync.Entry) foldersync.State {
	return foldersync.State{Size: e.Size, MTime: e.MTime, Hash: e.Hash}
}

// byPath indexes a listing by the name it was scanned under.
func byPath(entries []foldersync.Entry) map[string]foldersync.Entry {
	out := make(map[string]foldersync.Entry, len(entries))
	for _, e := range entries {
		out[e.Path] = e
	}
	return out
}

// movedOf describes one finished copy for the window.
func movedOf(a foldersync.Action) SyncMoved {
	m := SyncMoved{Name: a.Path, Size: a.Size, Way: a.Way, Conflict: a.Conflict}
	if a.Conflict {
		m.Saved = a.Dest
	}
	return m
}

// trimLists bounds what one pass reports. A first sync of a large folder moves
// thousands of files, and a window — or an API response — carrying a row for
// each of them is a window nobody can read.
func trimLists(res *passResult) {
	if len(res.moved) > defaultSyncKeep {
		res.moved = res.moved[:defaultSyncKeep]
	}
	if len(res.skipped) > defaultSyncKeep {
		res.skipped = res.skipped[:defaultSyncKeep]
	}
}
