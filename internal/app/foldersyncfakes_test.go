package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core/foldersync"
)

// The shared folder's fakes. Both sides of it are one interface, so one fake
// stands in for a Windows filesystem and for a folder on the far end of an
// SFTP session — which is the point of the interface, and is why a pass can be
// tested without a peer, a network or a disk.

// errFake is what a fake fails with when a test asks it to.
var errFake = errors.New("fake: as arranged")

// quietLog throws the pass's technical detail away. The tests assert on what
// the window is told, in Russian, not on what the log says — and a test suite
// that printed a warning per arranged failure would bury the real ones.
func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// fakeFile is one file in a fake side.
type fakeFile struct {
	body  string
	stamp time.Time
}

// fakeSide is one machine's shared folder, held in memory. Every method takes
// the mutex: the pass runs on its own goroutine and the test reads from
// another, and a data race here is a real defect rather than test noise.
type fakeSide struct {
	name string

	mu    sync.Mutex
	files map[string]fakeFile
	// scanErr fails the whole listing.
	scanErr error
	// openErr, statErr and receiveErr fail one named path, so a test can put
	// exactly one thing wrong and see what the pass makes of it.
	openErr    map[string]error
	statErr    map[string]error
	receiveErr map[string]error
	// afterOpen runs once the body has been taken but before the pass
	// re-checks the file, which is how a test makes a file move underneath a
	// copy that is already reading it.
	afterOpen func(side *fakeSide, rel string)
	// readErr fails one named path partway through the read, which is a link
	// that dropped with the file half copied.
	readErr map[string]error
	// shrink makes what Receive stores one byte shorter than what it was
	// handed, which is a copy that arrived the wrong length.
	shrink bool
	// received names every path Receive was asked to write, in order.
	received []string
}

// newSide builds one machine's folder from a body per path.
func newSide(name string, files map[string]string) *fakeSide {
	side := &fakeSide{
		name:       name,
		files:      make(map[string]fakeFile, len(files)),
		openErr:    map[string]error{},
		statErr:    map[string]error{},
		receiveErr: map[string]error{},
		readErr:    map[string]error{},
	}
	for rel, body := range files {
		side.files[rel] = fakeFile{body: body, stamp: time.Unix(1000, 0)}
	}
	return side
}

// put writes one file, as a program on that machine would.
func (s *fakeSide) put(rel, body string, unixSec int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.files[rel] = fakeFile{body: body, stamp: time.Unix(unixSec, 0)}
}

// drop removes one file, as a person deleting it would. Nothing in the feature
// itself may do this; the tests are the only caller.
func (s *fakeSide) drop(rel string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.files, rel)
}

// body is one file's content, and whether it is there at all.
func (s *fakeSide) body(rel string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	file, found := s.files[rel]
	return file.body, found
}

// paths lists what the side holds, sorted, for an assertion that reads well.
func (s *fakeSide) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]string, 0, len(s.files))
	for rel := range s.files {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

// writes lists what Receive was asked for.
func (s *fakeSide) writes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.received...)
}

// Name is what the UI calls this side.
func (s *fakeSide) Name() string { return s.name }

// Scan lists the files, honouring keep and the context.
func (s *fakeSide) Scan(ctx context.Context, keep func(rel string) bool) ([]foldersync.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.scanErr != nil {
		return nil, s.scanErr
	}
	var out []foldersync.Entry
	for rel, file := range s.files {
		if !keep(rel) {
			continue
		}
		out = append(out, foldersync.Entry{
			Path: rel, Size: int64(len(file.body)), MTime: file.stamp,
		})
	}
	return out, nil
}

// Stat reports one file's length and stamp.
func (s *fakeSide) Stat(ctx context.Context, rel string) (foldersync.State, error) {
	if err := ctx.Err(); err != nil {
		return foldersync.State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.statErr[rel]; err != nil {
		return foldersync.State{}, err
	}
	file, found := s.files[rel]
	if !found {
		return foldersync.State{}, errFake
	}
	return foldersync.State{Size: int64(len(file.body)), MTime: file.stamp}, nil
}

// Open hands back the file's bytes as they are at this moment, and then lets a
// test change the file behind the reader's back.
func (s *fakeSide) Open(ctx context.Context, rel string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	err := s.openErr[rel]
	// The mid-read failure is looked up by presence rather than compared
	// against nil: it is not a failure to report from here, it is what the
	// reader this returns will hand back partway through, and asking "is it
	// nil" about it reads — to a person and to nilerr alike — as an error
	// being swallowed.
	midRead, breaksMidway := s.readErr[rel]
	file, found := s.files[rel]
	after := s.afterOpen
	s.mu.Unlock()

	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errFake
	}
	if after != nil {
		after(s, rel)
	}
	if breaksMidway {
		return io.NopCloser(brokenReader{err: midRead}), nil
	}
	return io.NopCloser(strings.NewReader(file.body)), nil
}

// brokenReader gives up partway, the way a link that dropped mid-copy does.
type brokenReader struct{ err error }

func (b brokenReader) Read(p []byte) (int, error) {
	if len(p) > 0 {
		p[0] = 'x'
		return 1, b.err
	}
	return 0, b.err
}

// Receive collects the bytes and only stores them if the pass did not refuse
// the copy — the fake's own small version of write-to-temp-then-rename.
func (s *fakeSide) Receive(
	ctx context.Context, rel string, stamp time.Time, write func(io.Writer) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var buf bytes.Buffer
	// write runs first, so a source that moved is reported as the source's
	// problem and not as this side's.
	if err := write(&buf); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.received = append(s.received, rel)
	if err := s.receiveErr[rel]; err != nil {
		return err
	}
	body := buf.String()
	if s.shrink && body != "" {
		body = body[:len(body)-1]
	}
	s.files[rel] = fakeFile{body: body, stamp: stamp}
	return nil
}

// fakePeerSide is the peer's tree, opened once per pass.
type fakePeerSide struct {
	side     *fakeSide
	openErr  error
	closeErr error

	mu     sync.Mutex
	opens  int
	closes int
}

// Open hands over the peer's tree and the closer that ends the session.
func (p *fakePeerSide) Open(context.Context) (Tree, io.Closer, error) {
	p.mu.Lock()
	p.opens++
	p.mu.Unlock()

	if p.openErr != nil {
		return nil, nil, p.openErr
	}
	return p.side, closerFunc(func() error {
		p.mu.Lock()
		p.closes++
		p.mu.Unlock()
		return p.closeErr
	}), nil
}

// counts reports how many sessions were opened and how many closed, so a test
// can prove a pass does not leave one behind.
func (p *fakePeerSide) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.opens, p.closes
}

// closerFunc adapts a function to io.Closer.
type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// fakeState is the history, held in memory.
type fakeState struct {
	mu      sync.Mutex
	base    foldersync.Baseline
	loadErr error
	saveErr error
	saves   int
	folder  string
	peer    string
}

// newState is an empty history, as a first run has.
func newState() *fakeState { return &fakeState{base: foldersync.Baseline{}} }

// Load reads what was last synced.
func (s *fakeState) Load(folder, peer string) (foldersync.Baseline, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.folder, s.peer = folder, peer
	if s.loadErr != nil {
		return foldersync.Baseline{}, s.loadErr
	}
	out := make(foldersync.Baseline, len(s.base))
	for key, rec := range s.base {
		out[key] = rec
	}
	return out, nil
}

// Save records what is now synced.
func (s *fakeState) Save(folder, peer string, base foldersync.Baseline) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.folder, s.peer = folder, peer
	if s.saveErr != nil {
		return s.saveErr
	}
	s.saves++
	s.base = base
	return nil
}

// rows is how many files the history remembers.
func (s *fakeState) rows() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.base)
}

// record is what the history remembers about one file.
func (s *fakeState) record(key string) (foldersync.Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, found := s.base[key]
	return rec, found
}
