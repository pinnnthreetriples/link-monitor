package foldersync

// Result is what one action achieved. It is reported by whoever carried the
// action out; this package never learns it any other way.
type Result struct {
	// Key identifies the action, and matches [Action.Key].
	Key string
	// OK is false when nothing moved — the file was busy, the link dropped,
	// the destination refused the write. The record then stays as it was.
	OK bool
	// Read is the source's state once the copy had finished, re-checked so
	// that a file which moved underneath the copy is not recorded as agreed.
	// Its Hash is the hash of the bytes that actually travelled.
	Read State
	// Wrote is the destination's state after the rename. Its Hash is the same
	// hash: the bytes were counted on the way past, and a copy that changed
	// them would have failed.
	Wrote State
}

// Commit answers with the baseline to store after a pass.
//
// It is pure, and it is deliberately the only way a plan's [Plan.Next] reaches
// disk. A copy that failed must never be recorded as synced: the next pass
// would then read the side that still holds the older content as "unchanged"
// and the other as "changed", and would overwrite the newer file with the
// older one. That is the exact shape of the bug this function exists to make
// impossible, so the rule is written once, here, rather than at every call
// site that carries an action out.
//
// A path with no result is treated as a failure, for the same reason.
func Commit(plan Plan, results []Result) Baseline {
	next := make(Baseline, len(plan.Next))
	for key, rec := range plan.Next {
		next[key] = rec
	}

	byKey := make(map[string]Result, len(results))
	for _, r := range results {
		byKey[r.Key] = r
	}

	for _, a := range plan.Actions {
		r, reported := byKey[a.Key]
		switch {
		case !reported || !r.OK:
			revert(next, plan.Fallback, a.Key)
		case a.Conflict:
			// The plan already holds the two-sided record: the copy landed
			// under another name and says nothing about this path's own state.
		case a.Way == ToPeer:
			next[a.Key] = Record{Local: r.Read, Remote: r.Wrote}
		default:
			next[a.Key] = Record{Local: r.Wrote, Remote: r.Read}
		}
	}
	return next
}

// revert puts back what was agreed before the action that failed, or forgets
// the file entirely when nothing was ever agreed about it. Forgetting is the
// safe answer: with no record, the next pass treats a difference as a conflict.
func revert(next, fallback Baseline, key string) {
	if rec, had := fallback[key]; had {
		next[key] = rec
		return
	}
	delete(next, key)
}
