package httpapi

import (
	"net/http"
	"os"
)

// handleWhoami answers with this process's id. The peer's instance asks it
// through the tunnel before posting a clipboard item to a port it remembered,
// so that a port recycled by some other program since the record was confirmed
// gets a GET with no content in it, never the item. It reveals nothing a local
// process could not learn from the endpoint record on disk.
func (s *server) handleWhoami(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		PID int `json:"pid"`
	}{os.Getpid()})
}
