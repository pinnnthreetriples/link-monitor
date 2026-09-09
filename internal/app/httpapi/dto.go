package httpapi

import (
	"fmt"
	"strings"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/app"
	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// The wire types. Field names and JSON tags are the contract the frontend is
// written against: renaming one of them breaks the UI, so none of them changes
// without changing the UI in the same breath.

// Status is one moment of the link, as the dashboard draws it.
//
// PeerState is what the diagnosis established about the peer machine itself —
// [core.PeerState]'s own token — and it is there so the machine card has one
// server-stated fact to draw from instead of two that can disagree. The card
// used to take its word from /api/peers's "online", which is the control
// plane's flag and goes on saying true for minutes after a machine is switched
// off, so the card read «В сети» while the headline beside it read «Связи нет —
// похоже, она выключена». The flag is still reported, because it is true and
// the tailnet list is what it describes; which of the two the card believes is
// settled here rather than in the browser.
//
// It is empty — never "unknown" — when there is no diagnosis to report yet. The
// difference matters to the card: an empty value means nothing has been checked
// and the tailnet list is all there is, while "unknown" means a run finished and
// established nothing about the peer, which is itself worth saying.
type Status struct {
	TakenAt   string  `json:"takenAt"`
	Overall   string  `json:"overall"`
	Summary   string  `json:"summary"`
	Detail    string  `json:"detail"`
	LatencyMs int64   `json:"latencyMs"`
	Checking  bool    `json:"checking"`
	PeerState string  `json:"peerState"`
	Checks    []Check `json:"checks"`
	Fixes     []Fix   `json:"fixes"`
}

// Check is one row of the dashboard. Label and Note are Russian.
type Check struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Label string `json:"label"`
	Note  string `json:"note"`
}

// Fix is one repair on offer. Title and Explanation are Russian.
type Fix struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Explanation string `json:"explanation"`
	NeedsAdmin  bool   `json:"needsAdmin"`
}

// HistoryPoint is one mark on the 24-hour uptime strip.
type HistoryPoint struct {
	At    string `json:"at"`
	State string `json:"state"`
}

// PeerDTO is one member of the tailnet.
type PeerDTO struct {
	Name   string `json:"name"`
	Addr   string `json:"addr"`
	Online bool   `json:"online"`
	Self   bool   `json:"self"`
}

// TransferDTO is one recent file transfer. Size is preformatted for display —
// the UI shows it verbatim, so the units are Russian.
type TransferDTO struct {
	Name  string `json:"name"`
	Size  string `json:"size"`
	At    string `json:"at"`
	Dir   string `json:"dir"`
	State string `json:"state"`
	Pct   int    `json:"pct"`
}

// ForwardDTO is one live tunnel.
type ForwardDTO struct {
	ID         string `json:"id"`
	LocalPort  int    `json:"localPort"`
	RemoteHost string `json:"remoteHost"`
	RemotePort int    `json:"remotePort"`
}

// emptyStatus is what /api/status answers before the first probe has finished:
// unknown, and honest about why.
func emptyStatus(checking bool) Status {
	return Status{
		TakenAt:  time.Time{}.Format(time.RFC3339),
		Overall:  core.StateUnknown.String(),
		Summary:  msgNotCheckedYet,
		Detail:   msgNotCheckedYetDetail,
		Checking: checking,
		// No diagnosis has run, so there is no finding about the peer — not even
		// "unknown", which is a finished run's answer. The card falls back to the
		// tailnet list here, and only here.
		PeerState: "",
		Checks:    []Check{},
		Fixes:     []Fix{},
	}
}

// toStatus renders one poller result for the wire.
func toStatus(res app.Result, checking bool) Status {
	snap := res.Snapshot
	checks := make([]Check, 0, len(snap.Checks))
	for _, c := range snap.Checks {
		checks = append(checks, Check{
			ID: string(c.ID), State: c.State.String(), Label: c.Label, Note: c.Note,
		})
	}
	fixes := make([]Fix, 0, len(res.Fixes))
	for _, f := range res.Fixes {
		fixes = append(fixes, Fix{
			ID: string(f.ID), Title: f.Title, Explanation: f.Explanation, NeedsAdmin: f.NeedsAdmin,
		})
	}
	return Status{
		TakenAt:   snap.Taken.Format(time.RFC3339),
		Overall:   snap.Overall.String(),
		Summary:   snap.Summary,
		Detail:    snap.Detail,
		LatencyMs: snap.Latency.Milliseconds(),
		Checking:  checking,
		PeerState: snap.Peer.String(),
		Checks:    checks,
		Fixes:     fixes,
	}
}

// toPoints renders the uptime strip.
func toPoints(points []app.Point) []HistoryPoint {
	out := make([]HistoryPoint, 0, len(points))
	for _, p := range points {
		out = append(out, HistoryPoint{At: p.At.Format(time.RFC3339), State: p.State.String()})
	}
	return out
}

// toPeers renders the tailnet list.
func toPeers(peers []app.Peer) []PeerDTO {
	out := make([]PeerDTO, 0, len(peers))
	for _, p := range peers {
		out = append(out, PeerDTO{Name: p.Name, Addr: p.Addr, Online: p.Online, Self: p.Self})
	}
	return out
}

// toTransfers renders the recent transfers.
func toTransfers(items []app.Transfer) []TransferDTO {
	out := make([]TransferDTO, 0, len(items))
	for _, t := range items {
		out = append(out, TransferDTO{
			Name:  t.Name,
			Size:  humanSize(t.Size),
			At:    t.At.Format(time.RFC3339),
			Dir:   string(t.Dir),
			State: string(t.State),
			Pct:   t.Pct,
		})
	}
	return out
}

// toForwards renders the live tunnels.
func toForwards(items []app.Forward) []ForwardDTO {
	out := make([]ForwardDTO, 0, len(items))
	for _, f := range items {
		out = append(out, ForwardDTO{
			ID: f.ID, LocalPort: f.LocalPort, RemoteHost: f.RemoteHost, RemotePort: f.RemotePort,
		})
	}
	return out
}

// sizeUnits are the Russian abbreviations, smallest first.
var sizeUnits = []string{"Б", "КБ", "МБ", "ГБ", "ТБ"}

// humanSize formats a byte count for a Russian reader: whole bytes below a
// kilobyte, one decimal above it, and a comma for the decimal point.
func humanSize(n int64) string {
	if n < 0 {
		n = 0
	}
	if n < 1024 {
		return fmt.Sprintf("%d %s", n, sizeUnits[0])
	}
	value := float64(n)
	unit := 0
	for value >= 1024 && unit < len(sizeUnits)-1 {
		value /= 1024
		unit++
	}
	return strings.Replace(fmt.Sprintf("%.1f %s", value, sizeUnits[unit]), ".", ",", 1)
}
