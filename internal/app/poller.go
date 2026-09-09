package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
	"github.com/pinnnthreetriples/link-monitor/internal/core/diagnose"
)

const (
	// DefaultInterval is how often the link is probed when nobody says otherwise.
	DefaultInterval = 30 * time.Second

	// DefaultProbeTimeout bounds one whole diagnosis. It is shorter than the
	// interval on purpose: a run that has not finished by the next tick would
	// otherwise queue up behind itself.
	DefaultProbeTimeout = 25 * time.Second
)

// ErrPollerStopped means the poller is not running, so there is nobody to ask
// for a fresh check.
var ErrPollerStopped = errors.New("app: the poller is not running")

// Result is one finished diagnosis: what the link looked like and what could be
// done about it.
type Result struct {
	Snapshot core.Snapshot
	Fixes    []diagnose.Fix
}

// PollerConfig describes one poller. Probe, Local and Peer are required; the
// rest fall back to defaults.
type PollerConfig struct {
	Probe    core.Probe
	Local    core.Machine
	Peer     core.Machine
	Interval time.Duration
	Timeout  time.Duration
	// History, when set, gets one point per finished probe.
	History *History
}

// Poller runs the diagnosis on an interval and fans each result out to whoever
// is listening. Exactly one goroutine ever probes, so the adapters below it
// never see two runs at once.
//
// Shutdown is by context: when the context given to Start ends, the loop
// returns, every subscriber channel is closed, and Wait reports when the
// goroutine is gone. Nothing outlives it.
type Poller struct {
	cfg   PollerConfig
	nudge chan struct{}
	wg    sync.WaitGroup

	mu        sync.Mutex
	subs      map[int]chan Result
	nextID    int
	latest    Result
	hasLatest bool
	checking  bool
	running   bool
	stopped   bool
}

// NewPoller prepares a poller. It probes nothing until Start is called.
func NewPoller(cfg PollerConfig) *Poller {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultProbeTimeout
	}
	return &Poller{
		cfg:   cfg,
		nudge: make(chan struct{}, 1),
		subs:  make(map[int]chan Result),
	}
}

// Start launches the polling goroutine. Calling it twice is a no-op, so a
// caller that is unsure cannot accidentally start two loops.
func (p *Poller) Start(ctx context.Context) {
	p.mu.Lock()
	if p.running || p.stopped {
		p.mu.Unlock()
		return
	}
	p.running = true
	p.mu.Unlock()

	p.wg.Add(1)
	go p.loop(ctx)
}

// Wait blocks until the polling goroutine has finished. A test can call it
// after cancelling the context to prove nothing is left running.
func (p *Poller) Wait() { p.wg.Wait() }

// Interval reports how often the poller probes.
func (p *Poller) Interval() time.Duration { return p.cfg.Interval }

// loop is the only goroutine that probes.
func (p *Poller) loop(ctx context.Context) {
	defer p.wg.Done()
	defer p.shutdown()

	ticker := time.NewTicker(p.cfg.Interval)
	defer ticker.Stop()

	p.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.runOnce(ctx)
		case <-p.nudge:
			p.runOnce(ctx)
			// A manual check restarts the clock: the next scheduled probe is a
			// full interval away, not whatever was left of the old one.
			ticker.Reset(p.cfg.Interval)
		}
	}
}

// runOnce performs one diagnosis and publishes it. It returns nothing on
// purpose: publish is the only way a result leaves the loop, and Check waits
// on a subscription rather than on a return value, so that two simultaneous
// requests cost one probe between them.
func (p *Poller) runOnce(ctx context.Context) {
	p.setChecking()

	runCtx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()

	snap, fixes := diagnose.Run(runCtx, p.cfg.Probe, p.cfg.Local, p.cfg.Peer)
	res := Result{Snapshot: snap, Fixes: fixes}
	if p.cfg.History != nil {
		p.cfg.History.Add(Point{At: snap.Taken, State: snap.Overall})
	}
	p.publish(res)
}

// setChecking flags a probe as in flight, so the UI can show a spinner.
func (p *Poller) setChecking() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.checking = true
}

// publish stores the result, clears the in-flight flag and hands the result to
// every subscriber. A subscriber that has not kept up loses its stale value
// rather than blocking the poller: what a slow reader wants is the latest
// state, never a backlog of old ones.
func (p *Poller) publish(res Result) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.latest, p.hasLatest, p.checking = res, true, false
	for _, ch := range p.subs {
		select {
		case ch <- res:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- res:
			default:
			}
		}
	}
}

// shutdown closes every subscriber channel so their readers unblock, and marks
// the poller stopped so a late Subscribe does not wait for a result that will
// never come.
func (p *Poller) shutdown() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.running, p.stopped, p.checking = false, true, false
	for id, ch := range p.subs {
		delete(p.subs, id)
		close(ch)
	}
}

// Subscribe returns a channel of results and the function that closes it. The
// channel holds one result: it is a view of the present, not a queue.
//
// The returned function must be called exactly once, and it is the only thing
// that may close the channel. Subscribing to a poller that has already stopped
// yields an already-closed channel.
func (p *Poller) Subscribe() (<-chan Result, func()) {
	p.mu.Lock()
	defer p.mu.Unlock()

	ch := make(chan Result, 1)
	if p.stopped {
		close(ch)
		return ch, func() {}
	}
	id := p.nextID
	p.nextID++
	p.subs[id] = ch
	return ch, func() { p.unsubscribe(id) }
}

// unsubscribe drops one subscriber. It holds the same mutex publish does, so a
// channel can never be closed while a send to it is in flight.
func (p *Poller) unsubscribe(id int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if ch, ok := p.subs[id]; ok {
		delete(p.subs, id)
		close(ch)
	}
}

// CheckNow asks for a probe as soon as the loop can get to one. It never
// blocks: a nudge that arrives while one is already pending is the same nudge.
func (p *Poller) CheckNow() {
	select {
	case p.nudge <- struct{}{}:
	default:
	}
}

// Check asks for an immediate probe and waits for its result. It is what the
// "проверить сейчас" button is built on.
//
// It waits on a subscription rather than probing inline, so the loop stays the
// only prober and two simultaneous requests cost one probe between them.
func (p *Poller) Check(ctx context.Context) (Result, error) {
	p.mu.Lock()
	running := p.running
	p.mu.Unlock()
	if !running {
		return Result{}, ErrPollerStopped
	}

	ch, cancel := p.Subscribe()
	defer cancel()
	p.CheckNow()

	select {
	case res, ok := <-ch:
		if !ok {
			return Result{}, ErrPollerStopped
		}
		return res, nil
	case <-ctx.Done():
		return Result{}, fmt.Errorf("waiting for a fresh check: %w", ctx.Err())
	}
}

// Latest reports the most recent result, and whether there has been one.
func (p *Poller) Latest() (Result, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.latest, p.hasLatest
}

// Checking reports whether a probe is in flight right now.
func (p *Poller) Checking() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.checking
}
