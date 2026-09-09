package syncfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core/foldersync"
)

// stateVersion is the shape of the file on disk. A file written by a newer
// version of this program is refused rather than guessed at, and refusing it
// costs the user conflict copies rather than files.
const stateVersion = 1

// statePerm keeps the file to this user. It holds the names of everything in
// the shared folder — never any content — and that is nobody else's business.
const statePerm = 0o600

// ErrNoHistory means the state file exists but cannot be used: it is corrupt,
// it was written by another version, or it belongs to a different folder or a
// different peer.
//
// It is not a failure to recover from. It means the next pass has no history,
// and with no history a difference between the two machines is a conflict —
// so what a lost state file costs the user is a conflict copy beside each
// file that differs, and never a file.
var ErrNoHistory = errors.New("syncfs: the sync state cannot be used")

// Store keeps the baseline — what was last synced — in one small file.
//
// It is written the same way a transferred file is: to a temporary name beside
// it, then renamed into place. A process killed mid-write therefore leaves
// either the old state or the new one, never half of either, and half a state
// file is precisely the input that would make the next pass believe a file had
// been agreed when it had not.
type Store struct {
	path string
}

// NewStore keeps the state in one named file.
func NewStore(path string) *Store { return &Store{path: path} }

// DefaultStatePath is where the state lives when nobody says otherwise:
// %LOCALAPPDATA%\LinkMonitor\sync-state.json. It is per-user and per-machine,
// which is what it describes.
func DefaultStatePath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("finding the local application data folder: %w", err)
	}
	return filepath.Join(dir, "LinkMonitor", "sync-state.json"), nil
}

// Path is the file the state is kept in.
func (s *Store) Path() string { return s.path }

// stateFile is the file's shape. Stamps are whole seconds, which is all SFTP
// carries and therefore all the decision compares.
type stateFile struct {
	Version int                   `json:"version"`
	Folder  string                `json:"folder"`
	Peer    string                `json:"peer"`
	Files   map[string]fileRecord `json:"files"`
}

// fileRecord is one file's last agreed state on each machine.
type fileRecord struct {
	HereSize int64  `json:"hereSize"`
	HereTime int64  `json:"hereTime"`
	HereHash string `json:"hereHash"`
	PeerSize int64  `json:"peerSize"`
	PeerTime int64  `json:"peerTime"`
	PeerHash string `json:"peerHash"`
	Diverged bool   `json:"diverged,omitempty"`
}

// Load reads what was last synced for this folder and this peer.
//
// A file that is not there yet is not an error: it is the first run, and the
// answer is an empty baseline. A file that is there and cannot be used comes
// back as [ErrNoHistory], with an empty baseline alongside it, so a caller
// that ignores the error still gets the safe behaviour rather than the wrong
// one.
func (s *Store) Load(folder, peer string) (foldersync.Baseline, error) {
	blob, err := os.ReadFile(s.path) //nolint:gosec // the path is this program's own configuration
	if errors.Is(err, os.ErrNotExist) {
		return foldersync.Baseline{}, nil
	}
	if err != nil {
		return foldersync.Baseline{}, fmt.Errorf("reading the sync state: %w: %w", ErrNoHistory, err)
	}

	var file stateFile
	if err := json.Unmarshal(blob, &file); err != nil {
		return foldersync.Baseline{}, fmt.Errorf("parsing the sync state: %w: %w", ErrNoHistory, err)
	}
	if file.Version != stateVersion {
		return foldersync.Baseline{}, fmt.Errorf(
			"the sync state is version %d, not %d: %w", file.Version, stateVersion, ErrNoHistory)
	}
	// A state file that describes another folder or another machine is not
	// history for this pairing, and treating it as if it were is exactly how a
	// wrong "unchanged" gets believed.
	if !sameSetting(file.Folder, folder) || !sameSetting(file.Peer, peer) {
		return foldersync.Baseline{}, fmt.Errorf(
			"the sync state belongs to another folder or machine: %w", ErrNoHistory)
	}

	out := make(foldersync.Baseline, len(file.Files))
	for key, rec := range file.Files {
		out[key] = foldersync.Record{
			Local:  foldersync.State{Size: rec.HereSize, MTime: unix(rec.HereTime), Hash: rec.HereHash},
			Remote: foldersync.State{Size: rec.PeerSize, MTime: unix(rec.PeerTime), Hash: rec.PeerHash},
		}
	}
	return out, nil
}

// Save records what is now synced, crash-safely.
func (s *Store) Save(folder, peer string, base foldersync.Baseline) error {
	file := stateFile{
		Version: stateVersion,
		Folder:  folder,
		Peer:    peer,
		Files:   make(map[string]fileRecord, len(base)),
	}
	for key, rec := range base {
		file.Files[key] = fileRecord{
			HereSize: rec.Local.Size, HereTime: rec.Local.MTime.Unix(), HereHash: rec.Local.Hash,
			PeerSize: rec.Remote.Size, PeerTime: rec.Remote.MTime.Unix(), PeerHash: rec.Remote.Hash,
			// Written for a person reading the file, not read back: it says
			// which entries are a conflict the user has still to resolve.
			Diverged: rec.Local.Hash != rec.Remote.Hash,
		}
	}

	blob, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the sync state: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp := s.path + foldersync.TempSuffix
	if err := os.WriteFile(tmp, blob, statePerm); err != nil {
		return fmt.Errorf("writing the sync state: %w", err)
	}
	// The same rule as every other write in this feature: the real name is
	// only ever reached by a rename, so a kill mid-write leaves the previous
	// state intact rather than a file that parses as "everything is agreed".
	if err := os.Rename(tmp, s.path); err != nil {
		// The temporary file is left where it is. Removing it would be the
		// only removal in this whole feature that was not of something this
		// package had just created and could still see, and the guard that
		// forbids deletions is worth more than one tidy directory.
		return fmt.Errorf("putting the sync state into place: %w", err)
	}
	return nil
}

// sameSetting compares a folder or a machine name the way Windows would: case
// does not distinguish two of them there.
func sameSetting(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// unix turns whole seconds back into a time. The zero row of a hand-edited
// file becomes the epoch rather than the zero time, which is harmless: it
// makes the entry look changed, and a changed entry is re-checked.
func unix(sec int64) time.Time { return time.Unix(sec, 0) }
