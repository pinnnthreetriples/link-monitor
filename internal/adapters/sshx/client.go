// Package sshx is the SSH adapter. It talks to the peer with
// golang.org/x/crypto/ssh in this process and never shells out to ssh.exe: on
// these machines a VPN split-tunnel rule once matched the ssh.exe image name
// specifically and broke it silently, which is impossible to diagnose from the
// outside. An in-process client also gives us exit codes, both streams and a
// port forward without parsing anyone's console output.
//
// Typical use, with the real machines as an example:
//
//	c, err := sshx.Dial(ctx, sshx.Config{
//		Addr:    "100.127.188.87", // work PC; the laptop is 100.124.47.73
//		User:    "user",           // the laptop logs in as "pnj"
//		KeyPath: `C:\Users\pnj\.ssh\id_ed25519`,
//	})
//
// The peer's default shell is cmd.exe, so anything that needs quoting or
// Cyrillic goes through RunPowerShell, which passes the script as
// -EncodedCommand and therefore never meets a shell quoting rule.
//
// Nothing in this package logs or wraps key bytes, passphrases or forwarded
// payload data into an error.
package sshx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// DefaultPort is the port OpenSSH listens on for both machines.
const DefaultPort = 22

// DefaultTimeout bounds a Dial that the caller's context does not bound.
const DefaultTimeout = 10 * time.Second

// Config describes one SSH destination. The zero value is not usable: Addr,
// User and KeyPath are required.
type Config struct {
	// Addr is the host to connect to, normally a Tailscale address such as
	// "100.127.188.87" (work PC) or "100.124.47.73" (laptop).
	Addr string
	// Port is the SSH port; zero means DefaultPort.
	Port int
	// User is the account to log in as - "user" on the work PC, "pnj" on the
	// laptop. The work PC keeps our key in
	// C:\ProgramData\ssh\administrators_authorized_keys because that account is
	// an administrator.
	User string
	// KeyPath is the private key file, normally an ed25519 key such as
	// C:\Users\pnj\.ssh\id_ed25519. Its bytes never leave this package.
	KeyPath string
	// KeyPassphrase decrypts KeyPath when it is encrypted. It is never logged
	// and never included in an error.
	KeyPassphrase []byte
	// KnownHostsPath is the file host keys are verified against. Empty means
	// %USERPROFILE%\.ssh\known_hosts.
	KnownHostsPath string
	// TrustOnFirstUse accepts and records the host key of a host that
	// known_hosts does not mention yet. A host that is already mentioned is
	// still verified strictly, so a changed key is rejected either way. This is
	// a deliberate, documented weakening for first contact only; the package
	// never uses ssh.InsecureIgnoreHostKey.
	TrustOnFirstUse bool
	// Timeout bounds the TCP connect and the SSH handshake when the caller's
	// context carries no deadline. Zero means DefaultTimeout.
	Timeout time.Duration
	// PeerProbeTimeout is how long the peer waits for its connection back to
	// this machine in PeerCanReachUs. Zero means DefaultPeerProbeTimeout. It is
	// separate from Timeout because it bounds work happening on the far side.
	PeerProbeTimeout time.Duration
}

// withDefaults returns a copy with the optional fields filled in.
func (c Config) withDefaults() Config {
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	if c.PeerProbeTimeout <= 0 {
		c.PeerProbeTimeout = DefaultPeerProbeTimeout
	}
	return c
}

// hostPort renders the destination as "host:port".
func (c Config) hostPort() string {
	return net.JoinHostPort(c.Addr, strconv.Itoa(c.Port))
}

// validate reports the first missing required field.
func (c Config) validate() error {
	switch {
	case c.Addr == "":
		return errors.New("sshx: Config.Addr is empty")
	case c.User == "":
		return errors.New("sshx: Config.User is empty")
	case c.KeyPath == "":
		return errors.New("sshx: Config.KeyPath is empty")
	default:
		return nil
	}
}

// Client is a live SSH connection to the peer. It is safe for concurrent use:
// Run, RunPowerShell, ServiceRunning and LocalForward may all be in flight at
// once. Close it when done.
type Client struct {
	conn *ssh.Client
	cfg  Config

	closeOnce sync.Once
	closeErr  error
}

// Dial connects, authenticates with the key in cfg.KeyPath and verifies the
// host key against known_hosts. It honours ctx during both the TCP connect and
// the SSH handshake: a cancelled context tears the socket down instead of
// leaving the handshake to run to completion.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()

	signer, err := loadSigner(cfg.KeyPath, cfg.KeyPassphrase)
	if err != nil {
		return nil, err
	}
	hostKeys, err := hostKeyCallback(cfg)
	if err != nil {
		return nil, err
	}

	dialCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	var d net.Dialer
	raw, err := d.DialContext(dialCtx, "tcp", cfg.hostPort())
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", cfg.hostPort(), err)
	}

	clientCfg := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostKeys,
		Timeout:         cfg.Timeout,
	}
	conn, err := handshake(dialCtx, raw, cfg.hostPort(), clientCfg)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, cfg: cfg}, nil
}

// handshake runs the SSH handshake under ctx. x/crypto/ssh takes no context, so
// cancellation works by closing the socket underneath it.
func handshake(ctx context.Context, raw net.Conn, addr string, cc *ssh.ClientConfig) (*ssh.Client, error) {
	type result struct {
		client *ssh.Client
		err    error
	}
	done := make(chan result, 1)
	go func() {
		conn, chans, reqs, err := ssh.NewClientConn(raw, addr, cc)
		if err != nil {
			done <- result{err: err}
			return
		}
		done <- result{client: ssh.NewClient(conn, chans, reqs)}
	}()

	select {
	case <-ctx.Done():
		// Closing the socket makes the handshake goroutine return; we then wait
		// for it so nothing is left running behind us.
		_ = raw.Close() // best effort: the point is only to unblock the handshake
		if r := <-done; r.client != nil {
			_ = r.client.Close() // it beat the cancellation; drop it anyway
		}
		return nil, fmt.Errorf("ssh handshake with %s: %w", addr, ctx.Err())
	case r := <-done:
		if r.err != nil {
			_ = raw.Close() // best effort: the handshake already failed
			return nil, fmt.Errorf("ssh handshake with %s: %w", addr, r.err)
		}
		return r.client, nil
	}
}

// loadSigner reads and parses the private key. Errors name the key's directory
// and never the file, and never carry key bytes or the passphrase.
func loadSigner(path string, passphrase []byte) (ssh.Signer, error) {
	blob, err := os.ReadFile(path) //nolint:gosec // the key path is operator-supplied configuration
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			// Unwrap so the message keeps the directory but drops the file name.
			return nil, fmt.Errorf("reading the private key in %s: %w", filepath.Dir(path), pathErr.Err)
		}
		return nil, fmt.Errorf("reading the private key in %s: %w", filepath.Dir(path), err)
	}
	defer zero(blob)

	if len(passphrase) > 0 {
		signer, perr := ssh.ParsePrivateKeyWithPassphrase(blob, passphrase)
		if perr != nil {
			return nil, fmt.Errorf("parsing the encrypted private key in %s: %w", filepath.Dir(path), perr)
		}
		return signer, nil
	}
	signer, err := ssh.ParsePrivateKey(blob)
	if err != nil {
		return nil, fmt.Errorf("parsing the private key in %s: %w", filepath.Dir(path), err)
	}
	return signer, nil
}

// zero overwrites b so key material does not sit in a reusable buffer longer
// than it must. It is a courtesy, not a guarantee: the runtime may have copied.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// Close shuts the connection down. It is safe to call more than once and from
// several goroutines; every caller sees the same result.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		if err := c.conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			c.closeErr = fmt.Errorf("closing the ssh connection to %s: %w", c.cfg.hostPort(), err)
		}
	})
	return c.closeErr
}
