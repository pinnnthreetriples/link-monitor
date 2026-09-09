package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	linkmonitor "github.com/pinnnthreetriples/link-monitor"
	"github.com/pinnnthreetriples/link-monitor/internal/app"
	"github.com/pinnnthreetriples/link-monitor/internal/app/httpapi"
)

// The loopback server the window and the API live on: what is mounted where,
// how it is started, and how it is stopped.
//
// It is its own file because main.go had outgrown the size limit, and because
// these three functions are one subject — the socket the whole interface is
// reached through, including the route the peer's own instance posts a
// clipboard item to. Everything else about that feature is in clipshare.go.

// serve starts the loopback server the UI and the API live on, and returns the
// channel carrying whatever the server finally had to say.
func serve(listener net.Listener, handler http.Handler) (*http.Server, <-chan error) {
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	errs := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- fmt.Errorf("serving the interface: %w", err)
			return
		}
		errs <- nil
	}()
	return server, errs
}

// finish shuts the server down within the grace period and reports the first
// thing that actually went wrong.
func finish(server *http.Server, serveErr <-chan error, trayErr error) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Warn("shutting the interface's server down", "err", err)
	}
	if err := <-serveErr; err != nil {
		return err
	}
	return trayErr
}

// mount puts the API under /api/ and the UI everywhere else, so the whole
// interface is one origin and needs no cross-origin rules.
func mount(a *app.App, defaultPeer string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", httpapi.New(httpapi.Deps{
		Status:      a.Poller,
		Fixes:       a.Fixer,
		History:     a.History,
		Peers:       a.Peers,
		Files:       a.Transfers,
		Forwards:    a.Forwards,
		Link:        a.Link,
		Sync:        a.Folder,
		Clip:        a.Clip,
		DefaultPeer: defaultPeer,
	}))
	mux.Handle("/", http.FileServerFS(linkmonitor.Assets()))
	return mux
}
