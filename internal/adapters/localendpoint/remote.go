package localendpoint

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// This file is the same record read by somebody who did not open the file.
//
// The shared clipboard needs the *peer's* record, and the peer's record is on
// the peer: the bytes arrive over SSH and the process behind the pid is one
// only the peer's operating system can be asked about. Everything about the
// record that does not depend on being the machine that wrote it therefore
// lives here, exported, and [find] uses exactly these functions — so a reader
// on the far side of an SSH session is held to the same rules as a reader of
// the local file, rather than to a second implementation that could drift.
//
// What a remote reader must still do for itself is ask the *right* operating
// system about the pid. There is no way to do that from here, and pretending
// otherwise — accepting a record because the pid happens to be alive on this
// machine — would be the staleness check inverted into a lie.

// RelativePath is where the record sits under %LOCALAPPDATA%, spelled the way
// Windows spells it.
//
// It exists for the reader that has to build the path on another machine:
// Join-Path $env:LOCALAPPDATA with this, on the peer, and the peer's own
// %LOCALAPPDATA% is expanded by the peer rather than guessed from a user name
// here.
func RelativePath() string { return dirName + `\` + fileName }

// Parse decodes a published record and checks everything that can be decided
// from the record alone: the schema version, the fields, and that the address
// is a bare loopback http URL with a port.
//
// A record that fails any of that is [ErrStale] rather than a malformed-input
// error, because to a caller the two mean the same thing — there is no
// instance here to hand work to — and one sentinel is easier to get right at
// the call site than two.
func Parse(data []byte) (Record, error) {
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, fmt.Errorf("%w: %w", err, ErrStale)
	}
	if err := validate(rec); err != nil {
		return Record{}, fmt.Errorf("%w: %w", err, ErrStale)
	}
	return rec, nil
}

// Confirm is the staleness decision, given what an operating system says about
// the process id in the record: the full image path and the creation time it
// reports for that pid.
//
// It must be the operating system of the machine the record came from. Both
// halves are needed and neither is enough alone — see [confirm] for why — and
// a mismatch is [ErrStale]: something wrote this record, and the process it
// names is not the process running under that pid now.
func (rec Record) Confirm(exe string, startedAt time.Time) error {
	if err := confirm(rec, exe, startedAt); err != nil {
		return fmt.Errorf("%w: %w", err, ErrStale)
	}
	return nil
}

// Port is the loopback port the instance named in the record is listening on.
//
// It is read from BaseURL rather than stored separately so that there is one
// address in the record and no way for two fields to disagree. A record that
// reached here through [Parse] or [Find] always has one; the error is for a
// Record a caller assembled itself.
func (rec Record) Port() (int, error) {
	u, err := url.Parse(rec.BaseURL)
	if err != nil {
		return 0, fmt.Errorf("reading the port out of %q: %w: %w", rec.BaseURL, err, ErrStale)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("the address %q names no usable port: %w", rec.BaseURL, ErrStale)
	}
	return port, nil
}
