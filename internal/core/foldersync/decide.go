package foldersync

import (
	"cmp"
	"slices"
)

// Decide answers what one pass must do. It is the whole decision: everything
// else in this feature either gathers the facts it is given or carries out what
// it says.
//
// Call it twice. The first call is handed listings with no hashes in them and
// answers with [Plan.Hash] — the files whose content has to be weighed before
// anything can be said about them — and with the actions it can already be
// sure of, which are the files present on only one side. Read those hashes,
// put them into the entries, and call it again: the second answer is complete.
//
// Nothing it returns removes a file. See [Way].
func Decide(in Input) Plan {
	local, badLocal := index(in.Local, in.Limits)
	remote, badRemote := index(in.Remote, in.Limits)

	plan := Plan{Next: Baseline{}, Fallback: Baseline{}}
	// A file skipped on either side is left alone on both. A size cap that
	// applied to one machine's copy and not the other's would be a licence to
	// overwrite the big one with the small one.
	for key, skip := range badLocal {
		plan.Skips = append(plan.Skips, skip)
		delete(local, key)
		delete(remote, key)
	}
	for key, skip := range badRemote {
		if _, both := badLocal[key]; !both {
			plan.Skips = append(plan.Skips, skip)
		}
		delete(local, key)
		delete(remote, key)
	}

	d := &decider{in: in, local: local, remote: remote, plan: &plan}
	d.unread = make(map[string]bool, len(in.Unreadable))
	for _, name := range in.Unreadable {
		d.unread[fold(name)] = true
	}
	d.taken = make(map[string]bool, len(local))
	for key := range local {
		d.taken[key] = true
	}

	for _, key := range union(local, remote) {
		d.one(key)
	}
	order(&plan)
	return plan
}

// listing is one side's files, keyed by the folded relative path.
type listing map[string]Entry

// index keys one side's entries, and reports separately the ones this pass
// must leave alone whatever the other side holds.
func index(entries []Entry, lim Limits) (listing, map[string]Skip) {
	out := make(listing, len(entries))
	bad := make(map[string]Skip)
	limit := maxBytes(lim)

	for _, e := range entries {
		rel, err := SafeRel(e.Path)
		if err != nil {
			// Rule 7: a name from the peer is untrusted input. It is named in
			// the UI rather than dropped, because a rejected name is the one
			// kind of skip that might mean something is wrong.
			bad[fold(e.Path)] = Skip{Path: e.Path, Why: WhyUnsafeName, Size: e.Size}
			continue
		}
		if Excluded(rel, lim) {
			// Deliberately not a Skip: .git and node_modules would drown the
			// list they are meant to make readable.
			continue
		}
		if e.Size > limit {
			bad[fold(rel)] = Skip{Path: rel, Why: WhyTooBig, Size: e.Size}
			continue
		}
		e.Path = rel
		out[fold(rel)] = e
	}
	return out, bad
}

// decider carries the one decision's working state, so the functions below
// read as the rules they are rather than as parameter lists.
type decider struct {
	in     Input
	local  listing
	remote listing
	unread map[string]bool
	// taken is every name the local side already holds, plus the ones this
	// plan's conflict copies have claimed, so no conflict copy can land on an
	// existing file or on another conflict copy.
	taken map[string]bool
	plan  *Plan
}

// one rules on a single file.
func (d *decider) one(key string) {
	l, hasLocal := d.local[key]
	r, hasRemote := d.remote[key]
	rec, hasRec := d.in.Baseline[key]

	if d.unread[key] {
		// Rule 4: a file somebody is writing. No error, no fix card, and the
		// record stays as it was so the next pass tries again.
		shown := l
		if !hasLocal {
			shown = r
		}
		d.plan.Skips = append(d.plan.Skips,
			Skip{Path: shown.Path, Why: WhyBusy, Size: shown.Size})
		d.keep(d.plan.Next, key, rec, hasRec)
		return
	}

	switch {
	case hasLocal && hasRemote:
		d.both(key, l, r, rec, hasRec)
	case hasLocal:
		// Rule 1 from the other end: the peer does not have it, so it goes to
		// the peer. A file missing on one side is never a deletion to repeat.
		d.add(Action{Key: key, Way: ToPeer, Path: l.Path, Dest: l.Path, Size: l.Size}, rec, hasRec)
	default:
		d.add(Action{Key: key, Way: ToUs, Path: r.Path, Dest: r.Path, Size: r.Size}, rec, hasRec)
	}
}

// both rules on a file that exists on both machines.
func (d *decider) both(key string, l, r Entry, rec Record, hasRec bool) {
	if !suspect(l, rec.Local, hasRec) && !suspect(r, rec.Remote, hasRec) {
		// Both sides are exactly as the record left them, so the record still
		// describes them — hashes and all, which is why it is carried forward
		// rather than rebuilt from a listing that carries none. This is the
		// case that makes a pass over a quiet folder cheap.
		d.plan.Next[key] = rec
		d.sayIfDiverged(l, rec)
		return
	}
	if l.Hash == "" || r.Hash == "" {
		d.plan.Hash = append(d.plan.Hash, Need{Local: l.Path, Remote: r.Path})
		d.keep(d.plan.Next, key, rec, hasRec)
		return
	}
	if l.Hash == r.Hash {
		// The same bytes on both machines. A stamp moved, nothing else did.
		d.plan.Next[key] = Record{Local: stateOf(l), Remote: stateOf(r)}
		return
	}

	localChanged := changed(l, rec.Local, hasRec)
	remoteChanged := changed(r, rec.Remote, hasRec)
	switch {
	case localChanged && remoteChanged:
		d.conflict(key, l, r, rec, hasRec)
	case localChanged:
		d.add(Action{Key: key, Way: ToPeer, Path: l.Path, Dest: r.Path, Size: l.Size}, rec, hasRec)
	case remoteChanged:
		d.add(Action{Key: key, Way: ToUs, Path: r.Path, Dest: l.Path, Size: r.Size}, rec, hasRec)
	default:
		// Neither side has moved since the record, and the two contents
		// differ: a conflict already reported and not yet resolved.
		d.sayIfDiverged(l, rec)
		d.plan.Next[key] = rec
	}
}

// sayIfDiverged reports a conflict nobody has resolved yet. There is nothing
// to do about it — neither machine has changed since it was reported, and
// choosing a winner is exactly what rule 2 forbids — but a divergence that
// stops being mentioned is one that quietly becomes permanent, so it is said
// again every pass for as long as it lasts.
func (d *decider) sayIfDiverged(e Entry, rec Record) {
	if rec.Local.Hash != rec.Remote.Hash {
		d.plan.Skips = append(d.plan.Skips,
			Skip{Path: e.Path, Why: WhyUnresolved, Size: e.Size})
	}
}

// conflict is rule 2. Both machines changed the file, so the peer's copy is
// written *beside* the local one and the local one is not touched.
func (d *decider) conflict(key string, l, r Entry, rec Record, hasRec bool) {
	dest := conflictName(l.Path, d.in.PeerName, d.in.Now, d.taken)
	d.taken[fold(dest)] = true
	d.plan.Actions = append(d.plan.Actions, Action{
		Key: key, Way: ToUs, Path: r.Path, Dest: dest, Size: r.Size, Conflict: true,
	})
	// Both sides have now been seen holding this content on purpose. Recording
	// both is what keeps the next pass from reading the divergence as a
	// one-sided change and copying one of them over the other.
	d.plan.Next[key] = Record{Local: stateOf(l), Remote: stateOf(r)}
	d.keep(d.plan.Fallback, key, rec, hasRec)
}

// add queues one copy and remembers what to fall back to if it fails.
func (d *decider) add(a Action, rec Record, hasRec bool) {
	d.plan.Actions = append(d.plan.Actions, a)
	d.keep(d.plan.Fallback, a.Key, rec, hasRec)
}

// keep carries a record forward, when there is one to carry.
func (d *decider) keep(into Baseline, key string, rec Record, hasRec bool) {
	if hasRec {
		into[key] = rec
	}
}

// suspect reports whether one side may have changed since the record: its
// length or its timestamp no longer matches, or there is no record at all.
//
// Timestamps are compared to the second, because a second is all SFTP carries.
// A stamp that differs only in its nanoseconds describes the same moment in
// two protocols, not a file that moved.
func suspect(e Entry, s State, hasRec bool) bool {
	return !hasRec || e.Size != s.Size || e.MTime.Unix() != s.MTime.Unix()
}

// changed reports whether one side's *content* differs from the record. This
// is the only question a copy is allowed to turn on; suspect merely asks
// whether it is worth reading the file to find out.
//
// A record with no hash in it cannot answer, so it answers "changed", which is
// the safe direction: with both sides unanswerable the result is a conflict
// copy, never an overwrite.
func changed(e Entry, s State, hasRec bool) bool {
	return !hasRec || s.Hash == "" || e.Hash != s.Hash
}

// stateOf is one entry as a record remembers it.
func stateOf(e Entry) State { return State{Size: e.Size, MTime: e.MTime, Hash: e.Hash} }

// union lists every key either side holds, sorted, so a pass is deterministic.
func union(local, remote listing) []string {
	keys := make([]string, 0, len(local)+len(remote))
	for key := range local {
		keys = append(keys, key)
	}
	for key := range remote {
		if _, both := local[key]; !both {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

// order sorts a plan so that the same input always produces the same answer,
// and so the UI's lists do not shuffle between passes.
func order(p *Plan) {
	slices.SortFunc(p.Actions, func(a, b Action) int {
		return cmp.Or(cmp.Compare(a.Key, b.Key), cmp.Compare(a.Way, b.Way))
	})
	slices.SortFunc(p.Skips, func(a, b Skip) int {
		return cmp.Or(cmp.Compare(fold(a.Path), fold(b.Path)), cmp.Compare(a.Why, b.Why))
	})
	slices.SortFunc(p.Hash, func(a, b Need) int { return cmp.Compare(fold(a.Local), fold(b.Local)) })
}
