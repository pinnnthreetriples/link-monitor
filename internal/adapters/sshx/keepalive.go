package sshx

import "time"

// DefaultKeepAlive is how often a held session is asked whether the far end is
// still there, when Config.KeepAlive is unset.
const DefaultKeepAlive = 15 * time.Second

// keepAliveTimeout is how long one keepalive may go unanswered before the
// session is declared dead.
const keepAliveTimeout = 10 * time.Second

// keepAliveRequest is the global request OpenSSH itself sends for the purpose.
// Any SSH server answers it, with "unknown request" if nothing else.
const keepAliveRequest = "keepalive@openssh.com"

// keepAlive watches a session that looks alive and kills it when it is not.
//
// Without it, a connection whose path vanished — Tailscale switching relays, a
// laptop waking from sleep, a NAT forgetting the mapping — stays "alive" as
// far as the library knows, because nothing is ever sent that could fail.
// Every command on it then hangs until its caller's deadline, the probe goes
// Unknown, clipboard items time out, and none of it heals until the operating
// system finally gives up on the socket many minutes later. Killing the
// session here closes dead, which is what makes the next call dial afresh.
func keepAlive(dead <-chan struct{}, interval, timeout time.Duration, ping func() error, kill func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-dead:
			return
		case <-ticker.C:
		}
		if !answered(dead, timeout, ping) {
			kill()
			return
		}
	}
}

// answered sends one ping and reports whether the far end replied in time. A
// session that died meanwhile counts as answered: its own watcher has already
// noticed, and there is nothing left to kill.
func answered(dead <-chan struct{}, timeout time.Duration, ping func() error) bool {
	done := make(chan error, 1)
	go func() { done <- ping() }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err == nil
	case <-dead:
		return true
	case <-timer.C:
		return false
	}
}
