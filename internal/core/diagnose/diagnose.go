// Package diagnose turns probe results into one snapshot of the link plus the
// repairs worth suggesting. It is pure: it asks a core.Probe, decides, and
// returns values. It repairs nothing — executing a Fix belongs to the app layer.
//
// The judgement that matters lives in transport(): when the Tailscale daemon
// round-trips the peer but an ordinary socket is refused before it leaves the
// machine, the fault is a local packet filter, not the peer.
package diagnose

import (
	"context"
	"fmt"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

const (
	// sshPort is the only port this program cares about.
	sshPort = 22
	// sshdService is the OpenSSH Server service name on Windows.
	sshdService = "sshd"
)

// checkOrder is the display order of the rows, fixed by ports.go. Every
// snapshot carries all five, Unknown ones included.
var checkOrder = []core.CheckID{
	core.CheckTailscale,
	core.CheckSSHOut,
	core.CheckSSHIn,
	core.CheckSSHD,
	core.CheckKillSwitch,
}

// afterTailscale are the rows that mean nothing until the daemon is up.
var afterTailscale = []core.CheckID{
	core.CheckSSHOut,
	core.CheckSSHIn,
	core.CheckSSHD,
	core.CheckKillSwitch,
}

// Run probes the link between local and peer and reports what it found: a full
// snapshot — all five checks, in the order ports.go declares them — and the
// fixes worth offering, most useful first.
//
// It returns no error on purpose. A probe that cannot answer leaves its row
// Unknown and drags the verdict down with it, because a link is never called
// healthy on missing evidence. The underlying error is wrapped for context and
// consumed here; what the user reads is Russian.
func Run(ctx context.Context, p core.Probe, local, peer core.Machine) (core.Snapshot, []Fix) {
	d := newDiagnosis(local, peer)

	if err := ctx.Err(); err != nil {
		d.giveUp(fmt.Errorf("diagnosis stopped before the first probe: %w", err))
		return d.snapshot(), d.fixes
	}
	if d.tailscale(ctx, p) && d.tunnel(ctx, p) {
		d.transport(ctx, p)
	}
	return d.snapshot(), d.fixes
}

// diagnosis accumulates rows, fixes and the headline while the steps run.
type diagnosis struct {
	local, peer core.Machine
	rows        map[core.CheckID]core.Check
	fixes       []Fix
	summary     string
	detail      string
	latency     time.Duration
}

// newDiagnosis starts from the honest position: nothing has been checked yet.
func newDiagnosis(local, peer core.Machine) *diagnosis {
	d := &diagnosis{
		local:   local,
		peer:    peer,
		rows:    make(map[core.CheckID]core.Check, len(checkOrder)),
		summary: summaryUnknown,
		detail:  detailUnknown,
	}
	for _, id := range checkOrder {
		d.rows[id] = core.Check{
			ID:    id,
			State: core.StateUnknown,
			Label: label(id, local, peer),
			Note:  noteNotChecked,
		}
	}
	return d
}

// set overwrites one row's state and note; the label stays as it was built.
func (d *diagnosis) set(id core.CheckID, s core.State, note string) {
	row := d.rows[id]
	row.State = s
	row.Note = note
	d.rows[id] = row
}

// pending marks rows Unknown with a shared note — for checks that were never
// attempted because a step above them failed. Unknown, never Fail: we did not
// find them broken, we simply never looked.
func (d *diagnosis) pending(note string, ids ...core.CheckID) {
	for _, id := range ids {
		d.set(id, core.StateUnknown, note)
	}
}

// giveUp abandons the whole run, blaming err for every row.
func (d *diagnosis) giveUp(err error) {
	d.pending(unknownNote(err), checkOrder...)
	d.summary, d.detail = summaryUnknown, detailUnknown
}

// snapshot renders the rows in display order and takes the worst state as the
// verdict, which State's ordering (OK < Unknown < Warn < Fail) makes trivial.
func (d *diagnosis) snapshot() core.Snapshot {
	rows := make([]core.Check, 0, len(checkOrder))
	overall := core.StateOK
	for _, id := range checkOrder {
		row := d.rows[id]
		if row.State > overall {
			overall = row.State
		}
		rows = append(rows, row)
	}
	return core.Snapshot{
		Taken:   time.Now(),
		Checks:  rows,
		Overall: overall,
		Summary: d.summary,
		Detail:  d.detail,
		Latency: d.latency,
	}
}
