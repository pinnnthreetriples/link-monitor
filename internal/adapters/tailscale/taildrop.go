package tailscale

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"tailscale.com/tailcfg"
)

// errUnsafeName guards against an inbox entry that tries to escape the target
// directory. The name is chosen by the sending peer, so it is not trusted.
var errUnsafeName = errors.New("unsafe file name")

// SendFile pushes one file to a peer over Taildrop. peer may be a MagicDNS
// name, a hostname or a Tailscale address; the receiving side sees the file
// under its base name. Contents are streamed, never held or logged.
func (c *Client) SendFile(ctx context.Context, path string, peer string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("taildrop send: %w", err)
	}

	name := filepath.Base(path)
	f, err := os.Open(path) // #nosec G304 -- the operator chooses which file to send.
	if err != nil {
		return fmt.Errorf("opening %s: %w", name, err)
	}
	// A read-only handle: closing it cannot lose data, so the error cannot matter.
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("reading %s: %w", name, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory: Taildrop sends files", name)
	}

	target, err := c.fileTarget(ctx, peer)
	if err != nil {
		return err
	}
	if err := c.daemon.PushFile(ctx, target, info.Size(), name, f); err != nil {
		return fmt.Errorf("taildrop send %s to %s: %w", name, peer, err)
	}
	return nil
}

// ReceiveFiles drains the Taildrop inbox into targetDir and returns the paths
// written, in the order they were taken. A file that fails midway stops the
// drain: the paths already written are returned alongside the error.
func (c *Client) ReceiveFiles(ctx context.Context, targetDir string) ([]string, error) {
	waiting, err := c.daemon.WaitingFiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the Taildrop inbox: %w", err)
	}
	if len(waiting) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(targetDir, 0o750); err != nil {
		return nil, fmt.Errorf("creating %s: %w", targetDir, err)
	}

	written := make([]string, 0, len(waiting))
	for _, wf := range waiting {
		if err := ctx.Err(); err != nil {
			return written, fmt.Errorf("taildrop receive: %w", err)
		}
		path, err := c.receiveOne(ctx, targetDir, wf.Name)
		if path != "" {
			written = append(written, path)
		}
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

// receiveOne writes a single waiting file and clears it from the inbox. It may
// return a path together with an error when the file landed but the inbox
// entry could not be removed.
func (c *Client) receiveOne(ctx context.Context, dir, waitingName string) (string, error) {
	base, err := safeBase(waitingName)
	if err != nil {
		return "", err
	}

	// The announced size is advisory; the copy below writes whatever arrives.
	rc, _, err := c.daemon.GetWaitingFile(ctx, waitingName)
	if err != nil {
		return "", fmt.Errorf("fetching %s from the Taildrop inbox: %w", base, err)
	}
	defer func() { _ = rc.Close() }() // read side only: nothing to flush.

	dst, err := freePath(dir, base)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("creating %s: %w", base, err)
	}
	if _, err := io.Copy(f, rc); err != nil {
		_ = f.Close() // the copy error is the one worth reporting.
		return "", fmt.Errorf("writing %s: %w", base, err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("closing %s: %w", base, err)
	}

	if err := c.daemon.DeleteWaitingFile(ctx, waitingName); err != nil {
		return dst, fmt.Errorf("clearing %s from the Taildrop inbox: %w", base, err)
	}
	return dst, nil
}

// fileTarget finds the node id Taildrop should push to.
func (c *Client) fileTarget(ctx context.Context, peer string) (tailcfg.StableNodeID, error) {
	targets, err := c.daemon.FileTargets(ctx)
	if err != nil {
		return "", fmt.Errorf("listing Taildrop targets: %w", err)
	}
	want := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(peer), "."))
	if want == "" {
		return "", fmt.Errorf("taildrop target: empty peer name: %w", ErrPeerNotFound)
	}
	for _, t := range targets {
		if t.Node != nil && nodeMatches(t.Node, want) {
			return t.Node.StableID, nil
		}
	}
	return "", fmt.Errorf("taildrop target %q: %w", peer, ErrPeerNotFound)
}

// nodeMatches accepts the FQDN, the MagicDNS label, the computed name or any
// of the node's Tailscale addresses. want is already lowercased and unrooted.
func nodeMatches(n *tailcfg.Node, want string) bool {
	full := strings.ToLower(strings.TrimSuffix(n.Name, "."))
	if full == want || baseName(full, "") == want {
		return true
	}
	if n.ComputedName != "" && strings.EqualFold(n.ComputedName, want) {
		return true
	}
	for _, p := range n.Addresses {
		if p.Addr().String() == want {
			return true
		}
	}
	return false
}

// safeBase reduces an inbox name to a plain file name inside the target
// directory, refusing anything that reaches outside it.
func safeBase(name string) (string, error) {
	clean := strings.TrimSpace(name)
	if clean == "" || clean == "." || clean == ".." {
		return "", fmt.Errorf("taildrop inbox entry %q: %w", name, errUnsafeName)
	}
	if strings.ContainsAny(clean, `/\`) || filepath.VolumeName(clean) != "" {
		return "", fmt.Errorf("taildrop inbox entry %q: %w", name, errUnsafeName)
	}
	if base := filepath.Base(clean); base != clean {
		return "", fmt.Errorf("taildrop inbox entry %q: %w", name, errUnsafeName)
	}
	return clean, nil
}

// freePath keeps an existing file: "notes.txt" arriving twice becomes
// "notes (1).txt", the way the Tailscale clients name it.
func freePath(dir, base string) (string, error) {
	candidate := filepath.Join(dir, base)
	if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
		return candidate, nil
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 1; i <= 100; i++ {
		candidate = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no free name for %s in %s", base, dir)
}
