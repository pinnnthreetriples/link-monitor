package peerclip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/pinnnthreetriples/link-monitor/internal/adapters/localendpoint"
)

// whoamiPath answers with the peer instance's pid; see internal/app/httpapi.
const whoamiPath = "api/whoami"

// via opens a tunnel to the peer's instance and hands its address to send.
//
// A record confirmed earlier is tried first: forwarding over the session
// already held and asking the far end its pid costs milliseconds, where
// [Peer.Record] starts PowerShell on the peer and costs seconds — the delay a
// user sees as "the first paste is still the old text". Nothing is sent to a
// remembered port until the process listening there has named the pid the
// record was confirmed for, which keeps the guarantee Record gives: an item
// never goes to an unrelated program handed a recycled port.
//
// When the remembered record does not hold up the full check runs, exactly as
// before. A send that fails after the pid matched is not retried, since the
// item may already have arrived.
func (p *Peer) via(ctx context.Context, send func(addr string) error) error {
	if rec, ok := p.remembered(); ok {
		err := p.through(ctx, rec, true, send)
		if !errors.Is(err, errNotThere) {
			if err != nil {
				p.forget()
			}
			return err
		}
		p.forget()
	}
	rec, err := p.Record(ctx)
	if err != nil {
		return err
	}
	if err := p.through(ctx, rec, false, send); err != nil {
		return err
	}
	p.remember(rec)
	return nil
}

// errNotThere marks a remembered record that no longer leads to the instance.
var errNotThere = errors.New("peerclip: the remembered endpoint no longer answers as that instance")

// through forwards rec's port and calls send with the tunnel's address. With
// check set it first asks the far end for its pid and refuses on a mismatch.
func (p *Peer) through(ctx context.Context, rec localendpoint.Record, check bool,
	send func(addr string) error,
) error {
	port, err := rec.Port()
	if err != nil {
		return fmt.Errorf("reading %s's endpoint record: %w", p.name, err)
	}
	tunnel, err := p.fwd.LocalForward(ctx, 0, loopback, port)
	if err != nil {
		if check {
			return fmt.Errorf("forwarding %s's port %d: %w", p.name, port, errNotThere)
		}
		return fmt.Errorf("forwarding %s's port %d: %w", p.name, port, err)
	}
	defer func() {
		// Closing the tunnel cannot lose the item: the request has already
		// been answered, and there is nobody left to tell.
		_ = tunnel.Close()
	}()
	addr, err := tunnelAddr(tunnel)
	if err != nil {
		return err
	}
	if check {
		if err := p.confirmPID(ctx, addr, rec.PID); err != nil {
			return fmt.Errorf("asking %s's instance who it is: %w: %w", p.name, errNotThere, err)
		}
	}
	return send(addr)
}

// confirmPID asks the process behind addr for its pid and compares.
func (p *Peer) confirmPID(ctx context.Context, addr string, want int) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/"+whoamiPath, nil)
	if err != nil {
		return fmt.Errorf("building the request: %w", err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("sending the request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() // read to answerLimit below
	var answer struct {
		PID int `json:"pid"`
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, answerLimit))
	if err != nil {
		return fmt.Errorf("reading the answer: %w", err)
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &answer) != nil {
		return fmt.Errorf("answered %s, which is not this program's whoami", resp.Status)
	}
	if answer.PID != want {
		return fmt.Errorf("pid %d answered where %d was confirmed", answer.PID, want)
	}
	return nil
}

func (p *Peer) remembered() (localendpoint.Record, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.known == nil {
		return localendpoint.Record{}, false
	}
	return *p.known, true
}

func (p *Peer) remember(rec localendpoint.Record) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.known = &rec
}

func (p *Peer) forget() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.known = nil
}
