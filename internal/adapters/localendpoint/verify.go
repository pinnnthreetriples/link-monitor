package localendpoint

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// tick is the resolution Windows reports a process creation time at: FILETIME
// counts 100-nanosecond intervals. Anything finer than one tick is noise from
// the JSON round trip rather than a different process.
const tick = 100 * time.Nanosecond

var (
	// errBadRecord rejects a record that is wrong on its face, before any
	// process is looked up: a version this build does not understand, a
	// missing field, an address that is not this machine's own loopback.
	errBadRecord = errors.New("the endpoint record is malformed")

	// errWrongProcess rejects a record whose process is not the one running
	// under that pid now. This is the staleness case: the instance was killed
	// or the machine rebooted, the file survived, and the pid has since been
	// handed to something else.
	errWrongProcess = errors.New("the process named in the record is not the one running now")
)

// validate is everything that can be decided from the record alone, with no
// syscall and no filesystem. It is deliberately strict about the address: a
// record is an ordinary file this user can edit, and a reader that accepted
// any URL from it would be a way to make this program POST a local file path
// to an address of somebody else's choosing.
func validate(rec Record) error {
	switch {
	case rec.Version != Version:
		return fmt.Errorf("%w: version %d, this build writes and reads %d",
			errBadRecord, rec.Version, Version)
	case rec.PID <= 0:
		return fmt.Errorf("%w: process id %d", errBadRecord, rec.PID)
	case strings.TrimSpace(rec.Exe) == "":
		return fmt.Errorf("%w: it names no executable", errBadRecord)
	case rec.StartedAt.IsZero():
		return fmt.Errorf("%w: it carries no process start time", errBadRecord)
	}
	if err := checkBaseURL(rec.BaseURL); err != nil {
		return fmt.Errorf("%w: %w", errBadRecord, err)
	}
	return nil
}

// checkBaseURL accepts only a plain http URL on this machine's own loopback
// interface, with a port and with nothing but a root path.
//
// http rather than https because that is what the program serves, loopback
// because the API refuses to listen anywhere else (see httpapi.Listen), and a
// root path because the base URL is a prefix the caller appends routes to — a
// path, a query or a fragment here would end up somewhere unintended once
// "api/files/send" was joined onto it.
func checkBaseURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("the address %q cannot be parsed: %w", raw, err)
	}
	switch {
	case u.Scheme != "http":
		return fmt.Errorf("the address %q is not http", raw)
	case !isLoopback(u.Hostname()):
		return fmt.Errorf("the address %q is not on the loopback interface", raw)
	case u.Port() == "":
		return fmt.Errorf("the address %q names no port", raw)
	case u.Path != "" && u.Path != "/":
		return fmt.Errorf("the address %q is not a bare base URL", raw)
	case u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		return fmt.Errorf("the address %q is not a bare base URL", raw)
	default:
		return nil
	}
}

// isLoopback reports whether a host names this machine and nothing else.
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// confirm is the staleness decision: given what the operating system says
// about the pid in the record, is this still the process that published it?
//
// Both halves are needed. The image path alone would accept a second copy of
// this exe that happened to inherit the pid; the creation time alone would
// accept an unrelated program in the vanishing case of a tick collision. Taken
// together they identify one process on this machine exactly, which is what
// lets a caller trust the port without asking who owns the socket.
func confirm(rec Record, exe string, startedAt time.Time) error {
	if !sameFile(rec.Exe, exe) {
		return fmt.Errorf("%w: process %d is now %s", errWrongProcess, rec.PID, filepath.Base(exe))
	}
	if !sameInstant(rec.StartedAt, startedAt) {
		return fmt.Errorf("%w: process %d started at %s, the record says %s", errWrongProcess,
			rec.PID, format(startedAt), format(rec.StartedAt))
	}
	return nil
}

// sameFile compares two Windows paths. The filesystem is case-insensitive and
// tolerates either separator, so a byte comparison would report a difference
// the operating system does not see and would call a live instance stale.
func sameFile(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// sameInstant compares two process creation times at the resolution Windows
// reports them.
func sameInstant(a, b time.Time) bool {
	return a.Truncate(tick).Equal(b.Truncate(tick))
}

// format renders an instant for an error message, in UTC so that two times
// from different sources can be read against each other.
func format(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
