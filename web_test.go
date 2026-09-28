package linkmonitor_test

import (
	"io/fs"
	"mime"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"

	linkmonitor "github.com/pinnnthreetriples/link-monitor"
)

// The window is embedded rather than read from disk, and an embed that
// silently loses a file produces a program that starts, serves a page, and
// draws it wrong — with nothing in the log. These names are therefore asserted
// rather than assumed. A file renamed on purpose fails here, which is the
// point: renaming it is a decision, not an accident.
func TestAssetsHoldTheWholeWindow(t *testing.T) {
	t.Parallel()

	want := []string{
		"index.html",
		// One stylesheet in three plus the fonts, and the order matters: a
		// rule moved between these files would move in the cascade too.
		"fonts.css",
		"tokens.css",
		"shell.css",
		"panels.css",
		// Loaded in this order: the shared helpers and the API client come
		// before app.js, and link.js's three halves before link.js itself.
		// LM is one namespace object built up at load time, so a wrong order
		// breaks the window silently.
		"helpers.js",
		"api.js",
		"app.js",
		"link-log.js",
		"link-history.js",
		"link-actions.js",
		"link.js",
		"files.js",
		"ports.js",
		"settings.js",
		"sync.js",
		"clip.js",
		"menu.js",
		"desktop.js",
		"shared-folder.js",
		"favicon.svg",
	}
	assets := linkmonitor.Assets()
	for _, name := range want {
		if _, err := fs.Stat(assets, name); err != nil {
			t.Errorf("the embedded window is missing %s: %v", name, err)
		}
	}
}

// The embed directive drops directories whose names begin with "." or "_"
// unless the pattern says otherwise, and frontend/fonts arrived after the
// embed was written. If it were ever dropped, every face would fall back and
// the window would look almost right, which is the worst kind of wrong.
func TestFontsAreEmbedded(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(linkmonitor.Assets(), "fonts")
	if err != nil {
		t.Fatalf("reading the embedded fonts directory: %v", err)
	}

	var woff2 int
	for _, e := range entries {
		if path.Ext(e.Name()) != ".woff2" {
			continue
		}
		woff2++
		info, err := e.Info()
		if err != nil {
			t.Fatalf("stat %s: %v", e.Name(), err)
		}
		// A truncated font is a file that exists and does not work. The
		// smallest subset shipped is ~8 KB.
		if info.Size() < 4096 {
			t.Errorf("%s is %d bytes — too small to be a real font", e.Name(), info.Size())
		}
	}
	if woff2 == 0 {
		t.Error("no woff2 files are embedded — fonts.css will resolve to nothing")
	}
}

// Every @font-face src in fonts.css must name a file that is actually
// embedded. A mismatch is invisible at build time and shows up as a window
// drawn in fallback faces.
func TestFontCSSReferencesOnlyEmbeddedFiles(t *testing.T) {
	t.Parallel()

	assets := linkmonitor.Assets()
	css, err := fs.ReadFile(assets, "fonts.css")
	if err != nil {
		t.Fatalf("reading fonts.css: %v", err)
	}

	refs := urlRefs(string(css))
	if len(refs) == 0 {
		t.Fatal("fonts.css declares no url() sources")
	}
	for _, ref := range refs {
		if _, err := fs.Stat(assets, ref); err != nil {
			t.Errorf("fonts.css points at %s, which is not embedded: %v", ref, err)
		}
	}
}

// The window must not reach the network for anything it draws. This program is
// started precisely when the network is broken, so a remote asset means the UI
// renders differently in the one situation it exists for.
func TestNoAssetReachesTheNetwork(t *testing.T) {
	t.Parallel()

	assets := linkmonitor.Assets()
	err := fs.WalkDir(assets, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch path.Ext(p) {
		case ".html", ".css":
		default:
			return nil
		}
		body, err := fs.ReadFile(assets, p)
		if err != nil {
			return err
		}
		// Only markup that pulls a resource matters. A URL inside a comment
		// or a JS string is documentation, not a request, so the check is
		// scoped to the two attributes and the one CSS function that fetch.
		text := string(body)
		for _, attr := range []string{`href="http`, `src="http`, `url(http`} {
			if strings.Contains(text, attr) {
				t.Errorf("%s fetches a remote resource (%s...) — assets must ship in the exe", p, attr)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the embedded window: %v", err)
	}
}

// Serving a font as application/octet-stream is the default on Windows,
// because the registry has no entry for .woff2. This asserts the fix in init
// is in force and that the server actually applies it.
func TestFontsAreServedWithTheirOwnType(t *testing.T) {
	t.Parallel()

	if got := mime.TypeByExtension(".woff2"); !strings.HasPrefix(got, "font/woff2") {
		t.Fatalf("mime.TypeByExtension(.woff2) = %q, want font/woff2", got)
	}

	srv := httptest.NewServer(http.FileServerFS(linkmonitor.Assets()))
	defer srv.Close()

	entries, err := fs.ReadDir(linkmonitor.Assets(), "fonts")
	if err != nil {
		t.Fatalf("reading the embedded fonts directory: %v", err)
	}
	var name string
	for _, e := range entries {
		if path.Ext(e.Name()) == ".woff2" {
			name = e.Name()
			break
		}
	}
	if name == "" {
		t.Skip("no woff2 embedded; TestFontsAreEmbedded reports that")
	}

	//nolint:noctx // httptest server on loopback in a test; no cancellation to honour.
	resp, err := http.Get(srv.URL + "/fonts/" + name)
	if err != nil {
		t.Fatalf("fetching /fonts/%s: %v", name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /fonts/%s = %d, want 200", name, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "font/woff2") {
		t.Errorf("Content-Type for %s = %q, want font/woff2", name, ct)
	}
}

// urlRefs pulls the argument of every url(...) in a stylesheet, unquoted.
func urlRefs(css string) []string {
	var out []string
	rest := css
	for {
		i := strings.Index(rest, "url(")
		if i < 0 {
			return out
		}
		rest = rest[i+len("url("):]
		j := strings.IndexByte(rest, ')')
		if j < 0 {
			return out
		}
		out = append(out, strings.Trim(rest[:j], `'" `))
		rest = rest[j+1:]
	}
}
