// Package httpapi is the HTTP face of the application, and the only thing the
// UI window talks to. It serves JSON over loopback and streams status changes
// as Server-Sent Events.
//
// Authentication: there is none, on purpose. The listener binds 127.0.0.1, so
// only a process already running as this user can reach it, and such a process
// could read the user's files and keys anyway — a token kept next to the socket
// it protects would be theatre. What is defended against is the one attacker
// loopback does not exclude: a web page in the user's browser, which can send
// cross-origin requests to 127.0.0.1. Hence [allowedOrigin]: the Host must
// name loopback, and a browser Origin must match that Host and the HTTP scheme.
// Requests with no Origin from local clients and SSH port forwards are allowed.
//
// Every message this package returns is Russian; Go error strings are wrapped
// and dropped here rather than shown.
package httpapi

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/pinnnthreetriples/link-monitor/internal/app"
	"github.com/pinnnthreetriples/link-monitor/internal/core/diagnose"
)

// maxBodyBytes caps a request body. Every request this API takes is a handful
// of fields; anything larger is a mistake or an attack.
const maxBodyBytes = 64 << 10

// StatusSource is the poller, as this package needs it.
type StatusSource interface {
	Latest() (app.Result, bool)
	Check(ctx context.Context) (app.Result, error)
	Subscribe() (<-chan app.Result, func())
	Checking() bool
}

// FixRunner carries out one repair.
type FixRunner interface {
	Apply(ctx context.Context, id diagnose.FixID) app.FixOutcome
}

// HistorySource is the 24-hour uptime strip.
type HistorySource interface {
	Points() []app.Point
}

// PeerSource lists the tailnet.
type PeerSource interface {
	List(ctx context.Context) ([]app.Peer, error)
}

// FileService moves files and remembers the recent ones.
type FileService interface {
	Send(ctx context.Context, path, peer string) error
	Receive(ctx context.Context) ([]string, error)
	List() []app.Transfer
}

// ForwardService opens, lists and closes tunnels, and publishes a port to the
// tailnet.
type ForwardService interface {
	Start(ctx context.Context, localPort int, remoteHost string, remotePort int) (app.Forward, error)
	Stop(id string) error
	List() []app.Forward
	Serve(ctx context.Context, port int) (string, error)
}

// SyncService is the shared folder, as this package needs it. It is asked for
// its state and nudged into a pass; it is never told where the folder is,
// because a path arriving over this API — even on loopback — is a path a page
// in the user's browser could choose.
type SyncService interface {
	On() bool
	Status() app.SyncStatus
	SyncNow()
}

// ClipService is the shared clipboard, as this package needs it.
//
// It is asked for its state, switched on and off, and handed an item that
// arrived from the peer. It is never asked what is *on* the clipboard: no route
// in this package can answer with the content, which is what makes the
// receiving endpoint a write and not a way to read this machine's clipboard
// from the loopback.
type ClipService interface {
	On() bool
	TurnOn() error
	TurnOff()
	Status() app.ClipStatus
	Receive(text []byte) error
}

// LinkController connects and disconnects the tailnet.
type LinkController interface {
	Apply(ctx context.Context, action app.LinkAction) app.LinkOutcome
}

// Deps are the services the handlers stand on. A nil field is allowed: the
// routes that needed it answer 503 with a Russian message instead of panicking.
type Deps struct {
	Status       StatusSource
	Fixes        FixRunner
	History      HistorySource
	Peers        PeerSource
	Files        FileService
	Uploads      UploadService
	Forwards     ForwardService
	Link         LinkController
	Sync         SyncService
	Clip         ClipService
	FolderOpener FolderOpener
	// DefaultPeer is the tailnet name of the machine this install watches: the
	// one a file goes to when the request names none, and the one GET /api/peers
	// reports as configuredPeer so the UI never has to guess it from the list.
	DefaultPeer string
}

// server holds the dependencies for the handlers.
type server struct {
	deps Deps
}

// New builds the API handler. The returned handler is safe for concurrent use
// and does not listen on anything by itself — see [Listen].
func New(deps Deps) http.Handler {
	s := &server{deps: deps}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("POST /api/check", s.handleCheck)
	mux.HandleFunc("POST /api/fix", s.handleFix)
	mux.HandleFunc("GET /api/history", s.handleHistory)
	mux.HandleFunc("GET /api/peers", s.handlePeers)
	mux.HandleFunc("POST /api/files/send", s.handleFilesSend)
	mux.HandleFunc("POST /api/files/upload", s.handleFileUpload)
	mux.HandleFunc("POST /api/files/receive", s.handleFilesReceive)
	mux.HandleFunc("GET /api/files", s.handleFilesList)
	mux.HandleFunc("POST /api/forward", s.handleForwardStart)
	mux.HandleFunc("DELETE /api/forward", s.handleForwardStop)
	mux.HandleFunc("GET /api/forward", s.handleForwardList)
	mux.HandleFunc("POST /api/serve", s.handleServe)
	mux.HandleFunc("POST /api/link", s.handleLink)
	mux.HandleFunc("GET /api/sync", s.handleSyncStatus)
	mux.HandleFunc("POST /api/sync/run", s.handleSyncRun)
	mux.HandleFunc("POST /api/sync/open", s.handleFolderOpen)
	mux.HandleFunc("GET /api/clip", s.handleClipStatus)
	mux.HandleFunc("POST /api/clip/on", s.handleClipOn)
	mux.HandleFunc("POST /api/clip/off", s.handleClipOff)
	// The route the peer's own instance posts an item to, through a port
	// forwarded over the SSH session this program already holds. What protects
	// it is argued where it is handled, in clip.go.
	mux.HandleFunc("POST /api/clip/receive", s.handleClipReceive)
	mux.HandleFunc("POST /api/clip/image", s.handleClipImage)
	mux.HandleFunc("GET /api/whoami", s.handleWhoami)
	mux.HandleFunc("GET /api/events", s.handleEvents)

	// A path that exists but was asked for with the wrong method answers in the
	// API's own shape. These patterns are less specific than the ones above, so
	// they only catch the methods those did not claim.
	for _, path := range apiPaths {
		mux.HandleFunc(path, s.handleMethodNotAllowed)
	}
	mux.HandleFunc("/", s.handleNotFound)

	return guardOrigin(mux)
}

// apiPaths is every path this API answers on, for the wrong-method fallback.
var apiPaths = []string{
	"/api/status", "/api/check", "/api/fix", "/api/history", "/api/peers",
	"/api/files", "/api/files/send", "/api/files/upload", "/api/files/receive",
	"/api/forward", "/api/serve", "/api/link", "/api/events",
	"/api/sync", "/api/sync/run", "/api/sync/open",
	"/api/clip", "/api/clip/on", "/api/clip/off", "/api/clip/receive",
	"/api/clip/image", "/api/whoami",
}

// guardOrigin checks the Host even when Origin is absent, since DNS rebinding
// can make a browser send a same-origin request with no Origin header.
func guardOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedOrigin(r.Header.Get("Origin"), r.Host) {
			writeError(w, http.StatusForbidden, msgBadOrigin)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allowedOrigin accepts local requests, including clients without Origin, and
// requires browser requests to name exactly the requested HTTP origin.
func allowedOrigin(origin, requestHost string) bool {
	host, port, err := net.SplitHostPort(requestHost)
	if err != nil || !isLoopbackHost(host) {
		return false
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return false
	}
	return origin == "" || strings.EqualFold(origin, "http://"+requestHost)
}

// isLoopbackHost reports whether a host names this machine and nothing else.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// handleNotFound answers an unknown path in the API's own shape, so the UI
// never has to parse an HTML error page.
func (s *server) handleNotFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, msgNotFound)
}

// handleMethodNotAllowed answers a known path asked for with a method it does
// not take, again in the API's own shape rather than net/http's plain text.
func (s *server) handleMethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusMethodNotAllowed, msgMethodNotAllowed)
}

// Listen opens the API's listening socket. It refuses anything but a loopback
// address: an API that answered on the LAN would hand this machine's SSH
// tunnels and file transfers to anyone on the network.
//
// Pass port 0 to let the operating system pick one, which is what everything
// but a fixed-port install should do.
func Listen(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("parsing the API address %q: %w", addr, err)
	}
	if !isLoopbackHost(host) {
		return nil, fmt.Errorf("refusing to serve the API on %q: loopback addresses only", addr)
	}
	// context.Background() is the honest argument here, not a placeholder: this
	// socket's lifetime is the process's, and it is closed by the http.Server
	// that serves it rather than by a caller giving up. Taking a context would
	// mean promising to honour a cancellation that arrives after the bind,
	// which a net.Listener does not do. It goes through a ListenConfig anyway,
	// so that the resolve-and-bind step is not the one part of this package
	// with no way to be interrupted.
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listening on %s: %w", addr, err)
	}
	return ln, nil
}
