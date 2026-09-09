package sshx

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// This file stands up a real SSH server inside the test process, on a loopback
// listener, using the server half of golang.org/x/crypto/ssh. Every test in the
// package talks to it. Nothing here reaches a real host, and no test needs one.

// execResult is what the fake peer answers an "exec" request with.
type execResult struct {
	stdout string
	stderr string
	code   int
	// noExitStatus makes the peer close the channel without sending a status,
	// which is what a killed process looks like.
	noExitStatus bool
}

// execHandler decides the answer for one command line.
type execHandler func(cmd string) execResult

// sshServer is a minimal but real OpenSSH-speaking peer.
type sshServer struct {
	t        *testing.T
	listener net.Listener
	hostKey  ssh.Signer
	cfg      *ssh.ServerConfig

	// accepted counts every TCP connection the server took, which is how a test
	// asserts how many times the client dialled - including attempts whose SSH
	// handshake then failed.
	accepted atomic.Int64

	mu             sync.Mutex
	handler        execHandler
	rejectForwards bool
	// rejectSFTP makes the peer refuse the sftp subsystem, the way a host with
	// no sftp-server installed does.
	rejectSFTP  bool
	lastForward string
	// live holds the connections handshaken so far, so a test can drop them and
	// see what the client does about it.
	live []*ssh.ServerConn

	wg sync.WaitGroup
}

// newSSHServer starts a peer that accepts exactly the given public key. It
// offers one ed25519 host key, the type both real machines are pinned under.
func newSSHServer(t *testing.T, clientPub ssh.PublicKey) *sshServer {
	t.Helper()
	return newSSHServerWithHostKeys(t, clientPub, newSigner(t))
}

// newSSHServerWithHostKeys starts a peer that offers every one of hostKeys,
// which is what Windows OpenSSH does: sshd generates ed25519, ecdsa and rsa
// host keys when it is installed and advertises all of them, leaving the
// client's preference list to pick one. s.hostKey is the first of them.
func newSSHServerWithHostKeys(t *testing.T, clientPub ssh.PublicKey, hostKeys ...ssh.Signer) *sshServer {
	t.Helper()
	if len(hostKeys) == 0 {
		t.Fatal("the test server needs at least one host key")
	}

	hostKey := hostKeys[0]
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), clientPub.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("unauthorized key")
		},
	}
	for _, k := range hostKeys {
		cfg.AddHostKey(k)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for the test ssh server: %v", err)
	}

	s := &sshServer{
		t:        t,
		listener: listener,
		hostKey:  hostKey,
		cfg:      cfg,
		handler:  func(string) execResult { return execResult{stdout: "ok\n"} },
	}
	s.wg.Add(1)
	go s.acceptLoop()
	t.Cleanup(s.stop)
	return s
}

func (s *sshServer) addr() string { return s.listener.Addr().String() }

func (s *sshServer) port() int {
	_, p, err := net.SplitHostPort(s.addr())
	if err != nil {
		s.t.Fatalf("splitting the test server address: %v", err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		s.t.Fatalf("parsing the test server port: %v", err)
	}
	return n
}

func (s *sshServer) setHandler(h execHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler = h
}

func (s *sshServer) setRejectForwards(reject bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejectForwards = reject
}

func (s *sshServer) forwardTarget() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastForward
}

// dials reports how many TCP connections the client has opened to this server.
func (s *sshServer) dials() int64 { return s.accepted.Load() }

// dropConnections hangs up on every session handshaken so far, the way a peer
// that reboots or loses its tailnet address does. The listener stays up, so a
// client that reconnects will get through.
func (s *sshServer) dropConnections() {
	s.mu.Lock()
	conns := s.live
	s.live = nil
	s.mu.Unlock()

	for _, conn := range conns {
		_ = conn.Close() // best effort: the point is only to break the transport
	}
}

// closeListener stops the peer answering new connections, without waiting for
// the sessions already running - which is what makes it usable from inside a
// handler, where stop would deadlock on its own goroutine.
func (s *sshServer) closeListener() {
	_ = s.listener.Close() // best effort: stop will report nothing new
}

func (s *sshServer) stop() {
	_ = s.listener.Close() // best effort: the accept loop only needs unblocking
	s.wg.Wait()
}

func (s *sshServer) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return // the listener was closed by stop
		}
		s.accepted.Add(1)
		s.wg.Add(1)
		go s.handshake(conn)
	}
}

func (s *sshServer) handshake(raw net.Conn) {
	defer s.wg.Done()
	defer func() { _ = raw.Close() }()

	conn, chans, reqs, err := ssh.NewServerConn(raw, s.cfg)
	if err != nil {
		return // a rejected client, or a probe that never spoke SSH
	}
	defer func() { _ = conn.Close() }()

	s.mu.Lock()
	s.live = append(s.live, conn)
	s.mu.Unlock()

	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		switch nc.ChannelType() {
		case "session":
			s.wg.Add(1)
			go s.session(nc)
		case "direct-tcpip":
			s.wg.Add(1)
			go s.directTCPIP(nc)
		default:
			_ = nc.Reject(ssh.UnknownChannelType, "unsupported") // nothing to recover
		}
	}
}

// session answers one "exec" request the way sshd does: both streams, then an
// exit-status request, then close.
func (s *sshServer) session(nc ssh.NewChannel) {
	defer s.wg.Done()

	ch, reqs, err := nc.Accept()
	if err != nil {
		return
	}
	defer func() { _ = ch.Close() }()

	for req := range reqs {
		switch req.Type {
		case "exec":
			var payload struct{ Command string }
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
				_ = req.Reply(false, nil)
				return
			}
			_ = req.Reply(true, nil)
			s.runExec(ch, payload.Command)
			return
		case "subsystem":
			s.runSubsystem(ch, req)
			return
		default:
			_ = req.Reply(false, nil) // we implement exec and one subsystem
		}
	}
}

// runSubsystem answers a "subsystem" request the way Windows OpenSSH does for
// the one subsystem this program asks for: sftp. The server on the other side
// of the channel is pkg/sftp's own, serving this process's real filesystem
// under whatever directory the test points it at, so an SFTP conversation here
// is a real one rather than a script of canned frames.
func (s *sshServer) runSubsystem(ch ssh.Channel, req *ssh.Request) {
	var payload struct{ Name string }
	if err := ssh.Unmarshal(req.Payload, &payload); err != nil || payload.Name != "sftp" {
		_ = req.Reply(false, nil)
		return
	}
	s.mu.Lock()
	refuse := s.rejectSFTP
	s.mu.Unlock()
	if refuse {
		_ = req.Reply(false, nil)
		return
	}
	_ = req.Reply(true, nil)

	server, err := sftp.NewServer(sftpChannel{ch})
	if err != nil {
		return
	}
	// Serve returns when the client closes its end of the channel, which is
	// what *sftp.Client.Close does.
	_ = server.Serve()
	_ = server.Close()
}

// sftpChannel makes an ssh.Channel the io.ReadWriteCloser pkg/sftp's server
// wants. A channel is already all three; the wrapper only names it.
type sftpChannel struct{ ssh.Channel }

// setRejectSFTP makes the peer refuse the sftp subsystem, the way a host with
// no sftp-server installed does.
func (s *sshServer) setRejectSFTP(reject bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.rejectSFTP = reject
}

func (s *sshServer) runExec(ch ssh.Channel, cmd string) {
	s.mu.Lock()
	h := s.handler
	s.mu.Unlock()

	res := h(cmd)
	_, _ = io.WriteString(ch, res.stdout)
	_, _ = io.WriteString(ch.Stderr(), res.stderr)
	_ = ch.CloseWrite()
	if res.noExitStatus {
		return
	}
	status := struct{ Status uint32 }{uint32(res.code)} //nolint:gosec // test codes are small
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(&status))
}

// directTCPIP is the server side of ssh -L: it connects where the client asked
// and splices the two streams.
func (s *sshServer) directTCPIP(nc ssh.NewChannel) {
	defer s.wg.Done()

	var payload struct {
		Host       string
		Port       uint32
		OriginHost string
		OriginPort uint32
	}
	if err := ssh.Unmarshal(nc.ExtraData(), &payload); err != nil {
		_ = nc.Reject(ssh.ConnectionFailed, "bad payload")
		return
	}
	target := net.JoinHostPort(payload.Host, strconv.Itoa(int(payload.Port)))

	s.mu.Lock()
	s.lastForward = target
	reject := s.rejectForwards
	s.mu.Unlock()

	if reject {
		_ = nc.Reject(ssh.ConnectionFailed, "refused by the test peer")
		return
	}
	upstream, err := net.Dial("tcp", target)
	if err != nil {
		_ = nc.Reject(ssh.ConnectionFailed, "dial failed")
		return
	}
	defer func() { _ = upstream.Close() }()

	ch, reqs, err := nc.Accept()
	if err != nil {
		return
	}
	defer func() { _ = ch.Close() }()
	go ssh.DiscardRequests(reqs)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(ch, upstream); _ = ch.CloseWrite() }()
	go func() { defer wg.Done(); _, _ = io.Copy(upstream, ch); _ = upstream.Close() }()
	wg.Wait()
}

// clixmlNoise is what PowerShell really printed on a cold run when started
// from cmd.exe on a Windows host, captured verbatim during development. It
// comes from the host before the first line of the script executes, so nothing
// inside a script can suppress it. It was seen on stderr, but every parser in
// this package is expected to survive finding it on stdout too - that is the
// whole reason the answers are token lines.
const clixmlNoise = "#< CLIXML\r\n" +
	`<Objs Version="1.1.0.1" xmlns="http://schemas.microsoft.com/powershell/2004/04">` +
	`<Obj S="progress" RefId="0"><TN RefId="0">` +
	`<T>System.Management.Automation.PSCustomObject</T><T>System.Object</T></TN>` +
	`<MS><I64 N="SourceId">1</I64><PR N="Record">` +
	`<AV>Preparing modules for first use.</AV><AI>0</AI><Nil /><PI>-1</PI><PC>-1</PC>` +
	`<T>Completed</T><SR>-1</SR><SD> </SD></PR></MS></Obj></Objs>` + "\r\n"

// buriedInNoise wraps one verdict line the way a cold peer would deliver it.
func buriedInNoise(verdict string) string {
	return clixmlNoise + verdict + "\r\n" + "trailing noise from something else\r\n"
}

// --- key and file helpers ---------------------------------------------------

// newSigner makes a fresh ed25519 signer, the key type both machines use.
func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating an ed25519 key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("building a signer: %v", err)
	}
	return signer
}

// writeKeyFile writes a fresh ed25519 key in OpenSSH format and returns its
// path along with the matching public key.
func writeKeyFile(t *testing.T, dir, name string) (string, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating an ed25519 key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "link-monitor test")
	if err != nil {
		t.Fatalf("marshalling the private key: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("writing the key file: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("building a signer: %v", err)
	}
	return path, signer.PublicKey()
}

// writeEncryptedKeyFile writes a passphrase-protected ed25519 key and returns
// its path and public key.
func writeEncryptedKeyFile(t *testing.T, dir, name string, passphrase []byte) (string, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating an ed25519 key: %v", err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "link-monitor test", passphrase)
	if err != nil {
		t.Fatalf("marshalling the encrypted private key: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("writing the key file: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("building a signer: %v", err)
	}
	return path, signer.PublicKey()
}

// newECDSASigner makes a fresh ecdsa P-256 signer. Windows OpenSSH generates
// one of these next to its ed25519 key, and x/crypto/ssh prefers it: that pair
// of facts is what the host key type tests are about.
func newECDSASigner(t *testing.T) ssh.Signer {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating an ecdsa key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("building an ecdsa signer: %v", err)
	}
	return signer
}

// writeKnownHosts writes a known_hosts naming host with key.
func writeKnownHosts(t *testing.T, dir, host string, key ssh.PublicKey) string {
	t.Helper()
	path := filepath.Join(dir, "known_hosts")
	line := knownhosts.Line([]string{knownhosts.Normalize(host)}, key) + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatalf("writing known_hosts: %v", err)
	}
	return path
}

// newTestClient starts a server and returns a client connected to it, with the
// host key already trusted and both cleaned up by t.
func newTestClient(t *testing.T) (*Client, *sshServer) {
	t.Helper()
	dir := t.TempDir()
	keyPath, clientPub := writeKeyFile(t, dir, "id_ed25519")
	server := newSSHServer(t, clientPub)
	knownHosts := writeKnownHosts(t, dir, server.addr(), server.hostKey.PublicKey())

	client, err := Dial(t.Context(), Config{
		Addr:           "127.0.0.1",
		Port:           server.port(),
		User:           "pnj",
		KeyPath:        keyPath,
		KnownHostsPath: knownHosts,
		Timeout:        5 * time.Second,
	})
	if err != nil {
		t.Fatalf("dialling the test server: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, server
}

// --- goroutine leak detection -----------------------------------------------

// assertNoLeaks fails the test if goroutines outlive the body by more than the
// count present when it was called. CI runs with -race, and a forward that
// leaks a pump would otherwise pass unnoticed.
func assertNoLeaks(t *testing.T) {
	t.Helper()
	before := runtime.NumGoroutine()
	t.Cleanup(func() {
		deadline := time.Now().Add(2 * time.Second)
		for {
			runtime.Gosched()
			now := runtime.NumGoroutine()
			if now <= before {
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("goroutines leaked: %d before the test, %d after", before, now)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

// mustDial is a small assertion used by several tests.
func mustDial(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("connecting to %s: %v", addr, err)
	}
	return conn
}
