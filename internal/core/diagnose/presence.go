package diagnose

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// localFilterBudget bounds the dial this engine still makes when the peer is
// not going to answer it — whether because the tailnet says the machine is away
// or because the tunnel got no reply from it.
//
// It is short because of what the dial is for. The kill-switch row asks whether
// a local packet filter refuses sockets to the tailnet, and a WFP «block» rule
// — AmneziaVPN's kill switch is one — is enforced inside connect() itself:
// Windows answers WSAEACCES before a packet leaves the machine, in microseconds
// and without anybody at the far end. So the answer this row exists for cannot
// take longer than the local stack takes to say no, and waiting a peer's
// timeout for it would spend fifteen seconds to learn nothing further.
const localFilterBudget = 400 * time.Millisecond

// present asks the local Tailscale daemon what it already knows about the peer,
// before anything is sent anywhere, and reports whether the network is still
// worth asking.
//
// This step is first among the peer questions because it is the cheapest and
// the most authoritative: the control plane tells the daemon which nodes exist
// and which are connected, so «that machine is switched off» — the commonest of
// the three failures this program exists to tell apart — is a local read away.
// It used to be diagnosed by sending three disco pings, waiting out the whole
// probe deadline and then reporting the deadline, with the answer sitting in
// the same status the machine card was already drawn from.
func (d *diagnosis) present(ctx context.Context, p core.Probe) bool {
	presence, err := p.PeerPresence(ctx, d.peer)
	if err != nil {
		// The daemon would not answer. That is no evidence about the peer, so
		// nothing is claimed and the tunnel is asked instead — the run the
		// program made before this step existed. The error is not recorded on
		// a row because every row this step would touch is about to be
		// answered by probes that do work; recording it would name a cause for
		// a failure that has not happened.
		return true
	}
	switch presence {
	case core.PresenceOffline:
		d.peerIsOff(ctx, p, peerOffline(d.peer))
		return false
	case core.PresenceAbsent:
		d.peerIsOff(ctx, p, peerAbsent(d.peer))
		return false
	case core.PresenceOnline:
		// Being connected to the control plane is not a working path, so the
		// ping still has to run — and this flag is exactly the one that goes on
		// saying «connected» for minutes after a machine is switched off. It is
		// remembered rather than reported, so that what the ping finds can be
		// reported beside it instead of against it.
		d.peerListed()
		return true
	default:
		// An answer that settles nothing. The ping is asked, and nothing about
		// the tailnet's view of the peer is claimed.
		return true
	}
}

// absence is one way the tailnet can say the peer is not there, in the words
// the user reads. Offline and absent differ in every sentence and in the advice
// — one machine needs waking, the other is not in this tailnet at all — and
// what they share is only the shape of the run that follows.
type absence struct {
	outbound string
	inbound  string
	pending  string
	summary  string
	detail   string
	fix      Fix
	// state is what the machine card is to say about the peer. It travels with
	// the sentences because it is the same finding in a machine-readable form,
	// and keeping the two in one value is what stops the card and the rows
	// disagreeing about which of the two absences this is.
	state core.PeerState
}

// peerOffline is a node the tailnet knows and does not currently see.
func peerOffline(peer core.Machine) absence {
	return absence{
		outbound: fmt.Sprintf("%s не в сети: тайлнет знает этот узел, но не видит его", name(peer)),
		inbound:  fmt.Sprintf("Встречное подключение невозможно: %s не в сети", name(peer)),
		pending:  notePeerOffline,
		summary:  summaryDown,
		detail: fmt.Sprintf("%s не в сети — так говорит сам тайлнет, а не истёкшее ожидание: "+
			"машина выключена, спит или осталась без сети", name(peer)),
		fix:   fixCheckPeerOnline(peer),
		state: core.PeerStateOffline,
	}
}

// peerAbsent is a node this tailnet has never heard of, which is the more
// serious of the two: nothing broke, the machine we were told to watch is not
// a member of this network.
func peerAbsent(peer core.Machine) absence {
	return absence{
		outbound: fmt.Sprintf("%s нет в tailnet: тайлнет такого узла не знает", name(peer)),
		inbound:  fmt.Sprintf("Встречное подключение невозможно: узла %s в tailnet нет", name(peer)),
		pending:  notePeerAbsent,
		summary:  summaryNotInTailnet,
		detail: fmt.Sprintf("Это не обрыв связи: узла %s в tailnet нет вовсе — "+
			"он вышел из тайлнета, его удалили, или в настройках указано не то имя", name(peer)),
		fix:   fixPeerNotInTailnet(peer),
		state: core.PeerStateAbsent,
	}
}

// peerIsOff records a peer the tailnet itself says is not there, and then asks
// what is still worth asking — which is not nothing.
//
// Two of the four remaining rows are facts about *this* machine and stay
// answerable with the peer switched off: whether our own SSH server is up, which
// the local service manager answers in a millisecond, and whether a local packet
// filter refuses sockets to the tailnet, whose only useful answer arrives before
// a packet leaves. Both used to read «не проверялось» in this state, which was
// weaker than the evidence to hand.
//
// The peer's own sshd is the one row that genuinely cannot be answered: asking
// takes a login to a machine that is not there. It says so, and names why.
func (d *diagnosis) peerIsOff(ctx context.Context, p core.Probe, a absence) {
	d.set(core.CheckSSHOut, core.StateFail, a.outbound)
	d.pending(a.pending, core.CheckSSHD)
	d.summary, d.detail = a.summary, a.detail
	d.peerState = a.state
	d.fixes = append(d.fixes, a.fix)

	d.ourServer(ctx, p, a.inbound)
	d.localFilter(ctx, p)
}

// ourServer settles the inbound row when nothing can arrive from the peer.
//
// The peer being off is already proof that no connection will come in, so this
// row fails whatever the service manager says. It is asked all the same, because
// a server of our own that is stopped or missing is a *second* and independent
// fault — one that will still be there when the peer comes back — and naming
// the fault the user can act on now beats repeating the one they cannot.
func (d *diagnosis) ourServer(ctx context.Context, p core.Probe, offNote string) {
	running, err := p.LocalServiceRunning(ctx, sshdService)
	if err == nil && !running {
		// localServerDown also writes the headline's sentence, and here it is
		// not this row's to write: the peer being off owns the headline.
		d.localServerDown(ctx, p)
		return
	}
	// Either our server is up, or the service manager would not answer.
	// Neither changes this row — the direction is settled by the peer being
	// away — so the error is not reported: it would name a cause for a row
	// that already has one.
	d.set(core.CheckSSHIn, core.StateFail, offNote)
}

// localFilter answers the kill-switch row without the peer's help.
//
// The row asks one thing: does a local packet filter refuse sockets to the
// tailnet? On these machines that filter is AmneziaVPN's kill switch, whose WFP
// rule denies 100.64.0.0/10, and Windows reports it as WSAEACCES on the connect
// itself. That answer needs nobody at the far end, so it survives the peer
// being switched off — see localFilterBudget for why the dial is cut short.
//
// Whether a packet filter on *this* machine refuses a socket is a fact about
// this machine, so both steps that meet an unreachable peer ask it: the one
// where the tailnet says the machine is away, and the one where the tunnel got
// no answer from it. The second used to answer «Не проверялось: вторая машина
// не отвечает», which made the same local fact reportable in one no-answer case
// and skipped in the other for no reason either could name.
//
// What the quiet case may claim is narrower than usual, deliberately. With
// nobody answering, silence is fully explained by the peer, so it is no
// evidence that packets leave this machine: a filter that drops rather than
// blocks would look exactly the same. What was established is that no local
// filter refused the socket — which is what rules out the kill switch — and the
// note says that and nothing more.
func (d *diagnosis) localFilter(ctx context.Context, p core.Probe) {
	budget, cancel := context.WithTimeout(ctx, localFilterBudget)
	defer cancel()

	err := p.TCPReachable(budget, d.peer.Addr, sshPort)

	var blocked *core.BlockedError
	if errors.As(err, &blocked) {
		d.set(core.CheckKillSwitch, core.StateFail, blockedSocketNote(blocked))
		d.fixes = append(d.fixes, fixDisableKillSwitch(), fixSplitTunnelSSH())
		return
	}
	var timedOut *core.TimeoutError
	var closed *core.PortClosedError
	if err == nil || errors.As(err, &timedOut) || errors.As(err, &closed) {
		d.set(core.CheckKillSwitch, core.StateOK, noteNoLocalRefusal)
		return
	}
	// The dial failed in a way that says nothing about a filter — an address
	// this machine cannot use, a probe that was never wired. Calling the kill
	// switch innocent on that evidence would be inventing the answer.
	d.set(core.CheckKillSwitch, core.StateUnknown, unknownNote(fmt.Errorf(
		"dialing %s:%d to see whether a local filter refuses it: %w", d.peer.Addr, sshPort, err)))
}
