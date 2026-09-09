package main

import (
	"log/slog"

	"github.com/pinnnthreetriples/link-monitor/internal/adapters/localendpoint"
)

// publishEndpoint records where this instance's loopback interface can be
// reached, and returns the function that takes the record back.
//
// This is what lets a second copy of the exe — Explorer's «Отправить на ПК»
// today, the shared clipboard next — find the instance that is already running
// instead of doing the work itself. See internal/adapters/localendpoint for why
// the record can be trusted and why nothing in it is a secret.
//
// A failure is a warning and nothing else. Everything that reads the record
// falls back to doing the job in its own process, which is worse than handing
// it over and far better than refusing to run the tray because a file under
// %LOCALAPPDATA% could not be written.
func publishEndpoint(uiURL string, log *slog.Logger) func() {
	nothing := func() {}

	path, err := localendpoint.Path()
	if err != nil {
		log.Warn("locating the local endpoint record", "err", err)
		return nothing
	}
	remove, err := localendpoint.Publish(uiURL)
	if err != nil {
		log.Warn("publishing the local endpoint record", "file", path, "err", err)
		return nothing
	}

	log.Info("published the local endpoint record", "file", path, "url", uiURL)
	return func() {
		if err := remove(); err != nil {
			log.Warn("removing the local endpoint record", "file", path, "err", err)
		}
	}
}
