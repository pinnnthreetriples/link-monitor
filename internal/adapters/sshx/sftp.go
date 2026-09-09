package sshx

import (
	"context"
	"fmt"

	"github.com/pkg/sftp"
)

// SFTP opens an SFTP client over the connection this program already keeps to
// the peer.
//
// It is one more channel on the existing transport, so nothing new is dialled,
// nothing new listens, and the shared folder inherits the host-key
// verification and the key handling the rest of this package already does. It
// is also why the peer needs no software of ours: Windows OpenSSH ships the
// sftp-server subsystem and sshd runs as a service whether or not anybody is
// looking at that machine's screen.
//
// The returned client owns a channel on the connection and must be closed.
// Open one per pass and close it at the end of the pass: a client whose
// transport has died does not heal, exactly as a port forward does not, and a
// pass is the natural unit to notice that in. Closing it does not close the
// SSH connection.
func (l *Lazy) SFTP(ctx context.Context) (*sftp.Client, error) {
	return withSession(ctx, l, func(c *Client) (*sftp.Client, error) {
		return c.SFTP()
	})
}

// SFTP opens an SFTP client on this connection. See [Lazy.SFTP].
func (c *Client) SFTP() (*sftp.Client, error) {
	client, err := sftp.NewClient(c.conn)
	if err != nil {
		return nil, fmt.Errorf("opening the sftp subsystem on %s: %w", c.cfg.hostPort(), err)
	}
	return client, nil
}
