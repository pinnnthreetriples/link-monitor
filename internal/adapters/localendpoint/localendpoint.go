// Package localendpoint publishes where this program's loopback interface can
// be reached, so that a second copy of the exe can find the instance that is
// already running.
//
// The UI and the API listen on 127.0.0.1 with whatever port the operating
// system handed out (`-listen 127.0.0.1:0`), so nothing outside the process
// knows the address. Two features need it — the Explorer verb «Отправить на
// ПК», which hands a file to the running instance instead of starting a rival
// transfer, and the shared clipboard that comes next — so rather than let each
// invent its own rendezvous, the instance writes one small record and
// everything else reads it.
//
// # Why a reader may not believe the record on sight
//
// A file outlives the process that wrote it. The instance can be killed, the
// machine can lose power, and by the time somebody reads the record the port
// named in it may belong to an entirely unrelated program: loopback ports are
// recycled constantly. Handing a local file path to whatever answers there
// would be a disclosure and a silent no-op at once.
//
// So the record names the process as well as the port, and a reader believes it
// only once the operating system confirms that exact process is still running:
// the process id, the full path of its image, and its creation time, which
// Windows reports to the 100-nanosecond tick. A pid on its own proves nothing —
// Windows reuses pids within minutes — but a pid whose live process was created
// at the recorded instant is the process that wrote the record, and that
// process has held the listener since it published. A confirmed record
// therefore cannot point at a recycled port: for the port to have been
// recycled, the instance holding it must have gone, and then there is no
// process left for the confirmation to match.
//
// This is proof of process identity rather than a token the reader presents,
// for two reasons. The API deliberately has no authentication — see
// internal/app/httpapi, whose argument is that a token kept beside the socket
// it protects is theatre against an attacker who is already this user — and a
// token would have to be checked by the API to mean anything. And a token would
// only prove that the reader had read the file; what the reader actually needs
// to know is that the listener is this program, which is what the operating
// system is asked here.
//
// # Why the contents are safe to write down
//
// The record holds a loopback URL, this process's own pid, the path of the exe
// that is running, and the moment it started. None of that is a secret: it
// describes a process that any program running as this user can already see in
// the process list, and the address it names is reachable from this machine
// only. Nothing that would matter if it leaked — no key material, no key path,
// no auth key, no file contents — is in here, and nothing ever should be: this
// file is a signpost, not a keyring. It is written under %LOCALAPPDATA%, whose
// ACL Windows grants to this user, SYSTEM and the Administrators group and to
// nobody else, and it is created with mode 0600 so that a build on another
// operating system cannot quietly widen it.
package localendpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Version is the record's schema version. A reader refuses a version it does
// not know rather than guessing at fields that may have moved.
const Version = 1

// dirName and fileName are where the record lives under %LOCALAPPDATA%.
const (
	dirName  = "LinkMonitor"
	fileName = "endpoint.json"
)

// Record is what one running instance says about itself.
//
// Every field is part of the proof rather than decoration. BaseURL is the
// address to talk to; PID, Exe and StartedAt together identify the process
// answering there, which is what makes a leftover record detectable. See the
// package comment for why none of it is sensitive.
type Record struct {
	Version int    `json:"version"`
	BaseURL string `json:"baseURL"`
	PID     int    `json:"pid"`
	// Exe is the full path of the running image, as Windows reports it.
	Exe string `json:"exe"`
	// StartedAt is the process creation time the operating system recorded, not
	// the moment this record was written. It is the field that makes a reused
	// pid detectable, so it must come from the same source the reader will ask.
	StartedAt time.Time `json:"startedAt"`
}

var (
	// ErrNoRecord means nothing has been published: no instance is running, or
	// one is running that is too old to publish.
	ErrNoRecord = errors.New("no endpoint record was published")

	// ErrStale means a record was found and refused. Something wrote it, but
	// the process it names is not the process running now — it exited, or its
	// pid has since been handed to something else.
	ErrStale = errors.New("the endpoint record does not describe a live instance")

	// errNoLocalAppData reports that the directory the record lives in cannot
	// be named, which on Windows means the environment has been tampered with.
	errNoLocalAppData = errors.New("%LOCALAPPDATA% is not set")
)

// NotRunning reports whether err means there is no instance to hand work to.
// It is the one question most callers have: an absent record and a refused one
// both mean "do the job yourself".
func NotRunning(err error) bool {
	return errors.Is(err, ErrNoRecord) || errors.Is(err, ErrStale)
}

// Path reports the file the record is published to. It is exported so the
// wiring can name the file in a log line, and so a person can go and read it.
func Path() (string, error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return "", fmt.Errorf("locating the endpoint record: %w", errNoLocalAppData)
	}
	return filepath.Join(base, dirName, fileName), nil
}

// process is what the operating system knows about a process, as the staleness
// check needs it. It is a seam so the decision can be tested against a process
// that never existed, one that has exited and one whose pid was reused, none of
// which can be arranged for real inside a test.
type process interface {
	// info returns the full image path and the creation time of pid. Any error
	// means the same thing to this package: there is no such process for a
	// record to be confirmed against.
	info(pid int) (exe string, startedAt time.Time, err error)
}

// deps are the two seams this package stands on.
type deps struct {
	files   files
	process process
}

func realDeps() deps { return deps{files: osFiles{}, process: osProcess{}} }

// Publish writes this process's endpoint record and returns the function that
// removes it, which the caller must run on the way out. Removing twice is
// harmless, and a record left behind by a killed process is refused by [Find]
// rather than believed — but leaving one behind still costs the next reader a
// pointless check, so shutdown should tidy up.
func Publish(baseURL string) (remove func() error, err error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	return publish(realDeps(), path, os.Getpid(), baseURL)
}

// publish is [Publish] with the seams and the location injected.
//
// The process's own identity is read back from the operating system rather than
// assembled from os.Executable() and time.Now(): the reader will ask the
// operating system, so the writer has to record what the operating system says,
// or the two would be comparing a path Go resolved against a path Windows
// reports.
func publish(d deps, path string, pid int, baseURL string) (func() error, error) {
	exe, startedAt, err := d.process.info(pid)
	if err != nil {
		return nil, fmt.Errorf("publishing the endpoint record: %w", err)
	}

	rec := Record{Version: Version, BaseURL: baseURL, PID: pid, Exe: exe, StartedAt: startedAt.UTC()}
	if err := validate(rec); err != nil {
		return nil, fmt.Errorf("publishing the endpoint record: %w", err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("publishing the endpoint record: encoding it: %w", err)
	}

	if err := d.files.mkdirAll(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("publishing the endpoint record: %w", err)
	}
	if err := d.files.writeFile(path, append(data, '\n')); err != nil {
		return nil, fmt.Errorf("publishing the endpoint record: %w", err)
	}
	return remover(d, path), nil
}

// remover deletes the record exactly once and gives every later call the same
// answer, so a shutdown path that runs twice neither fails nor reports twice.
func remover(d deps, path string) func() error {
	var (
		once sync.Once
		err  error
	)
	return func() error {
		once.Do(func() {
			if removeErr := d.files.remove(path); removeErr != nil {
				err = fmt.Errorf("removing the endpoint record: %w", removeErr)
			}
		})
		return err
	}
}

// Find returns the record of the instance running now.
//
// It returns [ErrNoRecord] when nothing has been published and [ErrStale] when
// a record was found and refused; [NotRunning] answers both at once. Any other
// error is a real failure — an unreadable directory, a tampered environment —
// and is worth reporting rather than treating as "nothing is running".
func Find() (Record, error) {
	path, err := Path()
	if err != nil {
		return Record{}, err
	}
	return find(realDeps(), path)
}

// find is [Find] with the seams and the location injected.
func find(d deps, path string) (Record, error) {
	data, err := d.files.readFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Record{}, fmt.Errorf("looking for a running instance in %s: %w", path, ErrNoRecord)
	case err != nil:
		return Record{}, fmt.Errorf("looking for a running instance: %w", err)
	}

	// Parse and Confirm are the same two steps a reader of the *peer's* record
	// takes over SSH — see remote.go. They are shared rather than repeated so
	// that the local reader and the remote one cannot come to hold the record
	// to different standards.
	rec, err := Parse(data)
	if err != nil {
		return Record{}, fmt.Errorf("reading %s: %w", path, err)
	}

	exe, startedAt, err := d.process.info(rec.PID)
	if err != nil {
		return Record{}, fmt.Errorf("confirming %s: %w: %w", path, err, ErrStale)
	}
	if err := rec.Confirm(exe, startedAt); err != nil {
		return Record{}, fmt.Errorf("confirming %s: %w", path, err)
	}
	return rec, nil
}
