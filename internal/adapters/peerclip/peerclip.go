// Package peerclip carries one clipboard item to the program's own instance on
// the peer.
//
// # Why it cannot be done the way the shared folder does it
//
// The shared folder needs nothing on the peer but sshd: SFTP reads and writes
// files, and a file does not care which Windows session wrote it. A clipboard
// does. It belongs to an interactive session, and everything run over SSH lands
// in the session OpenSSH gives it — not the user's — so a copy of this program
// started over SSH would set a clipboard nobody can see. The peer's *own*
// instance, the one running in the user's session behind the tray icon, is the
// only thing that can put text on the peer's clipboard. This package's whole
// job is to reach it.
//
// # How it reaches it
//
// The instance publishes where it listens — see
// internal/adapters/localendpoint — on 127.0.0.1 with a port the operating
// system handed out, which is unreachable from anywhere but that machine. So:
//
//  1. Read the peer's endpoint record over the SSH session this program already
//     holds, and hand the bytes to [localendpoint.Parse], which applies the
//     same rules a reader of the local file gets.
//  2. Ask the peer's own operating system about the pid in it, and put that
//     answer through [localendpoint.Record.Confirm]. A record that names a
//     process the peer is not running is refused there, exactly as a stale
//     local record is — which is what stops this program from posting a
//     clipboard item to whatever unrelated program has since been handed that
//     recycled loopback port.
//  3. Forward that port over the same SSH session, with sshx's LocalForward,
//     and POST the item to the instance through the tunnel.
//
// No second mechanism, no listening socket on the peer, and nothing of ours
// left running: the forward is opened for one item and closed after it.
//
// # What is never here
//
// The item's bytes go into one JSON body and out again. Nothing in this package
// logs, and no error it builds carries any of the content — not the text, not a
// prefix, not its length. See tools/gates for the check that keeps it that way.
package peerclip

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/pinnnthreetriples/link-monitor/internal/core/clipshare"
)

// receivePath is the route the peer's instance takes an item on. It is a
// constant here and in internal/app/httpapi because the two ends of one wire
// have to agree, and the contract test there pins it.
const receivePath = "api/clip/receive"

// loopback is the address the forward's far end dials, on the peer. The peer's
// API refuses to listen anywhere else.
const loopback = "127.0.0.1"

// answerLimit caps the answer read back from the peer's instance. The API
// answers with two fields and one Russian sentence; anything larger is not
// that API on the far end.
const answerLimit = 64 << 10

// Shell runs a PowerShell script on the peer. *sshx.Lazy implements it, and it
// is the only way this package learns anything about that machine.
type Shell interface {
	RunPowerShell(ctx context.Context, script string) (stdout, stderr string, exitCode int, err error)
}

// Forwarder opens an ssh -L style tunnel over the session already held.
// *sshx.Lazy implements it.
type Forwarder interface {
	LocalForward(ctx context.Context, localPort int, remoteHost string, remotePort int) (io.Closer, error)
}

// Peer is the instance on the other machine, not yet contacted.
type Peer struct {
	shell Shell
	fwd   Forwarder
	name  string
	// client has no timeout of its own: every call takes a context, and the
	// loop that calls Deliver bounds it. Redirects are refused because the
	// address came out of a record on the peer's disk, which is validated as
	// loopback before it is used — a redirect would be a way around that.
	client *http.Client
}

// New describes the peer's instance. name is what the UI calls that machine.
func New(shell Shell, fwd Forwarder, name string) *Peer {
	return &Peer{
		shell: shell,
		fwd:   fwd,
		name:  name,
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("peerclip: refusing to follow a redirect away from the tunnel")
			},
		},
	}
}

// Name is what the UI calls the peer.
func (p *Peer) Name() string { return p.name }

// Deliver puts one item on the peer's clipboard, through the peer's own
// instance.
//
// A peer with no instance running is reported with [localendpoint.ErrNoRecord]
// or [localendpoint.ErrStale] in the chain, so the caller can tell it from a
// failure — [localendpoint.NotRunning] answers both — and say the honest thing:
// the program has to be running on both machines, and that is a normal state
// rather than a fault.
func (p *Peer) Deliver(ctx context.Context, text []byte) error {
	rec, err := p.Record(ctx)
	if err != nil {
		return err
	}
	port, err := rec.Port()
	if err != nil {
		return fmt.Errorf("reading %s's endpoint record: %w", p.name, err)
	}

	// Local port 0: the operating system picks one, the listener is on the
	// loopback only, and it is closed as soon as this one item has gone.
	tunnel, err := p.fwd.LocalForward(ctx, 0, loopback, port)
	if err != nil {
		return fmt.Errorf("forwarding %s's port %d: %w", p.name, port, err)
	}
	defer func() {
		// Closing the tunnel cannot lose the item: the request above has
		// already been answered, and there is nobody left to tell.
		_ = tunnel.Close()
	}()

	addr, err := tunnelAddr(tunnel)
	if err != nil {
		return err
	}
	return p.post(ctx, addr, text)
}

// tunnelAddr is the loopback address the forward is listening on, which is
// where the request goes.
func tunnelAddr(tunnel io.Closer) (string, error) {
	reporter, ok := tunnel.(interface{ Addr() net.Addr })
	if !ok {
		return "", errors.New("peerclip: the port forward will not say what it listens on")
	}
	addr := reporter.Addr()
	if addr == nil {
		return "", errors.New("peerclip: the port forward is listening on nothing")
	}
	return addr.String(), nil
}

// post sends one item through the tunnel to the peer's instance.
func (p *Peer) post(ctx context.Context, addr string, text []byte) error {
	body, err := encodeItem(text)
	if err != nil {
		return err
	}
	defer clipshare.Zero(body)

	url := "http://" + addr + "/" + receivePath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building the request to %s: %w", p.name, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("handing the item to %s's instance: %w", p.name, err)
	}
	defer func() {
		// The body is read to answerLimit below; closing it loses nothing.
		_ = resp.Body.Close()
	}()
	return p.readAnswer(resp)
}

// encodeItem is the wire body: one field, and never anything else.
//
// json.Marshal of a map of strings cannot fail, and the error is still
// checked and wrapped rather than discarded — but it is wrapped without the
// value, which is the one thing a failure here would be tempted to include.
func encodeItem(text []byte) ([]byte, error) {
	body, err := json.Marshal(map[string]string{"text": string(text)})
	if err != nil {
		return nil, errors.New("peerclip: the clipboard item could not be encoded as JSON")
	}
	return body, nil
}

// readAnswer turns the instance's reply into an error or a nil. The sentence it
// carries is the peer instance's own Russian message about what it did with the
// item; it is never the item.
func (p *Peer) readAnswer(resp *http.Response) error {
	var answer struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, answerLimit))
	if err != nil {
		return fmt.Errorf("reading %s's answer: %w", p.name, err)
	}
	if err := json.Unmarshal(data, &answer); err != nil {
		return fmt.Errorf("%s answered %s, which is not this program's API: %w",
			p.name, resp.Status, err)
	}
	if resp.StatusCode != http.StatusOK || !answer.OK {
		return fmt.Errorf("%s refused the item: %s: %s", p.name, resp.Status, answer.Message)
	}
	return nil
}
