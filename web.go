// Package linkmonitor exists for one reason: `go:embed` can only reach files at
// or below the directory of the file that declares it, and the window's assets
// live at the repository root. Declaring the embed here lets `cmd/linkmon` ship
// the whole UI inside the executable, so the program is one file with nothing
// to install beside it.
package linkmonitor

import (
	"embed"
	"io/fs"
	"mime"
)

// Windows registers no MIME type for .woff2, so mime.TypeByExtension returns
// "" and http.FileServer falls back to sniffing, which labels a font
// application/octet-stream. Chromium loads it anyway, but a browser entitled
// to refuse a mislabelled font would leave the window in fallback faces with
// nothing in the log to explain it. Registering the type costs one line.
//
// AddExtensionType only fails on an extension not starting with a dot, which
// is a compile-time constant here, so the error cannot occur; it is checked
// rather than discarded because this project does not drop errors silently.
func init() {
	if err := mime.AddExtensionType(".woff2", "font/woff2"); err != nil {
		panic("link-monitor: registering the woff2 MIME type: " + err.Error())
	}
}

// assets holds the window: HTML, CSS, JS and the favicon. The `all:` prefix
// keeps files whose names begin with `_` or `.`, which the default rules drop.
//
//go:embed all:frontend
var assets embed.FS

// Assets returns the window's files rooted at the frontend directory, ready to
// hand to http.FileServer. It panics only if the embed is missing entirely,
// which is a build-time mistake rather than a runtime condition.
func Assets() fs.FS {
	sub, err := fs.Sub(assets, "frontend")
	if err != nil {
		panic("link-monitor: the embedded frontend is missing: " + err.Error())
	}
	return sub
}
