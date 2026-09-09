package sshx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The SFTP subsystem rides the connection this package already keeps, so the
// shared folder inherits the host-key verification and the key handling from
// the rest of it and nothing new is dialled. These tests talk to pkg/sftp's
// own server over the loopback SSH server in testserver_test.go, which is a
// real SFTP conversation rather than a script.

func TestSFTPOpensOverTheExistingConnection(t *testing.T) {
	t.Parallel()

	client, server := newLazyAgainst(t, 0)

	sftpClient, err := client.SFTP(context.Background())
	if err != nil {
		t.Fatalf("SFTP: %v", err)
	}
	defer func() { _ = sftpClient.Close() }()

	// One real round trip, so this asserts a working conversation and not
	// merely a channel that opened.
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}
	info, err := sftpClient.Stat(remoteName(path))
	if err != nil {
		t.Fatalf("Stat over sftp: %v", err)
	}
	if info.Size() != 5 {
		t.Errorf("size = %d, want 5", info.Size())
	}

	// It is one more channel on the connection that was already there.
	if got := server.dials(); got != 1 {
		t.Errorf("the client dialled %d times, want once", got)
	}
}

// A second SFTP client on the same connection is another channel and not
// another dial, which is what "one session per pass" costs.
func TestASecondSFTPClientIsAnotherChannelAndNotAnotherDial(t *testing.T) {
	t.Parallel()

	client, server := newLazyAgainst(t, 0)

	for range 2 {
		sftpClient, err := client.SFTP(context.Background())
		if err != nil {
			t.Fatalf("SFTP: %v", err)
		}
		if err := sftpClient.Close(); err != nil {
			t.Errorf("closing the sftp client: %v", err)
		}
	}
	if got := server.dials(); got != 1 {
		t.Errorf("the client dialled %d times, want once", got)
	}
}

// Closing the SFTP client must not close the SSH connection under everything
// else the program is doing with it.
func TestClosingTheSFTPClientLeavesTheConnectionAlone(t *testing.T) {
	t.Parallel()

	client, server := newLazyAgainst(t, 0)

	sftpClient, err := client.SFTP(context.Background())
	if err != nil {
		t.Fatalf("SFTP: %v", err)
	}
	if err := sftpClient.Close(); err != nil {
		t.Fatalf("closing the sftp client: %v", err)
	}

	if _, _, _, err := client.RunPowerShell(context.Background(), "echo ok"); err != nil {
		t.Errorf("running a command after the sftp client closed: %v", err)
	}
	if got := server.dials(); got != 1 {
		t.Errorf("the client dialled %d times, want once", got)
	}
}

// A peer with no sftp-server refuses the subsystem. The failure has to name
// the host, because "opening the sftp subsystem failed" with no host in it is
// the same message on both machines.
func TestAPeerWithNoSFTPServerIsReportedWithItsAddress(t *testing.T) {
	t.Parallel()

	client, server := newLazyAgainst(t, 0)
	server.setRejectSFTP(true)

	_, err := client.SFTP(context.Background())
	if err == nil {
		t.Fatal("SFTP succeeded against a peer that refused the subsystem")
	}
	if !strings.Contains(err.Error(), server.addr()) {
		t.Errorf("error = %q, want it to name %s", err, server.addr())
	}
}

func TestSFTPOnAPeerThatCannotBeReachedFails(t *testing.T) {
	t.Parallel()

	client, server := newLazyAgainst(t, 0)
	server.closeListener()

	if _, err := client.SFTP(context.Background()); err == nil {
		t.Fatal("SFTP succeeded with nothing listening")
	}
}

// remoteName is the path SFTP addresses a local file by. pkg/sftp's server
// speaks forward slashes and answers to a Windows drive path only with a
// leading slash, which is the same rule the real Windows sftp-server follows —
// see internal/adapters/syncfs/remote.go.
func remoteName(path string) string {
	slashed := strings.ReplaceAll(path, `\`, "/")
	if strings.HasPrefix(slashed, "/") {
		return slashed
	}
	return "/" + slashed
}
