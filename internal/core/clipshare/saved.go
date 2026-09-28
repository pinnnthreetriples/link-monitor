package clipshare

import "time"

// SavedScreenshot identifies a newly saved screenshot without carrying its
// contents or exposing its name to the user-facing event stream.
type SavedScreenshot struct {
	ID       string
	Modified time.Time
	Size     int64
	Names    []string
}
