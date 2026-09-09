// Package window is the program's own window: a native Win32 frame hosting the
// approved HTML interface in an embedded Edge WebView2 control.
//
// It exists because a tray utility that opens the user's browser is not a
// Windows program — «я хотел чтобы это была программа для виндовс, а не веб
// интерфейс открывался». The window here has its own HWND, title bar, taskbar
// button and icon, and no browser chrome; the X button hides it to the tray
// instead of ending the program.
//
// The syscalls are not testable, so every decision this package makes is
// lifted out of them: [decideProcAction] is a plain function over a message
// number, [layoutFor] is arithmetic over a DPI and a work area, the lifecycle
// runs against the unexported backend interface, and [RuntimeInstalled]
// parses a version string that a test can supply.
//
// The window is sized from the design rather than from a remembered pair of
// numbers, and the process declares Per-Monitor V2 DPI awareness so that the
// page inside it scales with the display: see sizing.go and
// [DeclarePerMonitorDPI].
package window

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// defaultTitle is the only default that is a name rather than arithmetic. The
// sizes are not constants here: they are derived from the approved design and
// the DPI of the monitor the window opens on, in sizing.go.
const defaultTitle = "Link Monitor"

// dataDirName is the folder WebView2 keeps its profile in, under
// %LOCALAPPDATA%. It is per-user because the cache is per-user.
var dataDirName = filepath.Join("LinkMonitor", "webview")

// errBadConfig reports a configuration the window cannot be built from.
var errBadConfig = errors.New("invalid window configuration")

// errNotLoopback rejects a URL that does not point at this machine. The window
// shows this program's own UI and nothing else; accepting a remote address
// would turn a status string into a way to load a stranger's page inside a
// frame that looks like ours.
var errNotLoopback = errors.New("only a loopback address may be shown")

// Config describes the window. The zero value is not usable: URL is required.
type Config struct {
	// Title is the window's title, shown in the title bar and the taskbar.
	// Defaults to "Link Monitor".
	Title string
	// URL is the loopback address of the app UI. Required.
	URL string
	// Width and Height override the initial window size, in physical pixels.
	//
	// Leave them zero — the normal case, and what cmd/linkmon does. The size
	// is then derived from the approved design and the DPI of the monitor the
	// window opens on, and clamped to that monitor's work area: see
	// [layoutFor]. A value given here is still clamped, and still raised to
	// the minimum, because a window off the edge of the screen or too small
	// for the layout is a mistake however it was asked for.
	Width  int
	Height int
	// MinWidth and MinHeight override the size the user cannot drag below, in
	// physical pixels. Leave them zero: the minimum is a property of the
	// design — the width at which the stacked variant takes over, the height
	// at which the first status card is still whole — and is scaled to the
	// display like everything else.
	MinWidth  int
	MinHeight int
	// DataDir is the WebView2 user data folder. Defaults to
	// %LOCALAPPDATA%\LinkMonitor\webview.
	DataDir string
	// Icon is the .ico bytes for the title bar and the taskbar. Optional; the
	// window keeps the system default icon without it.
	Icon []byte
	// StartHidden keeps the window hidden until the first [Window.Show]. With
	// it false, the window appears as soon as [Window.Run] has built it.
	StartHidden bool
	// OnHide is called after each hide to the tray, from the window thread, so
	// the caller can say «программа осталась в трее» once. Optional.
	OnHide func()
	// OpenExternal hands over a link that must leave the app window — a
	// Tailscale login URL, a `tailscale serve` address — to whatever opens
	// URLs on this machine. The page calls it as window.openExternal.
	//
	// Optional. Without it such a link raises a warning and goes nowhere,
	// which is better than navigating the app window away from its own UI.
	// The URL passed in may be a one-time credential: route it, do not record
	// it. See [externalLinks].
	OpenExternal func(rawURL string) error
	// Logger receives the failures a window cannot report any other way.
	// Defaults to slog.Default().
	Logger *slog.Logger
}

// New validates cfg and applies defaults. It makes no native calls, so it is
// safe to call before deciding whether a window is wanted at all.
func New(cfg Config) (*Window, error) {
	if err := cfg.check(); err != nil {
		return nil, err
	}
	prepared := cfg.withDefaults()
	return &Window{
		cfg:        prepared,
		log:        prepared.Logger,
		newBackend: newNativeBackend,
		// A window that is not asked to start hidden wants to be visible from
		// the moment Run builds it, and Run reads exactly this field.
		visible: !prepared.StartHidden,
	}, nil
}

// check reports whether the configuration describes a window worth building.
func (cfg Config) check() error {
	if strings.TrimSpace(cfg.URL) == "" {
		return fmt.Errorf("%w: no URL was given", errBadConfig)
	}
	if err := checkLoopbackURL(cfg.URL); err != nil {
		return fmt.Errorf("%w: %w", errBadConfig, err)
	}
	return nil
}

// checkLoopbackURL accepts only http(s) URLs whose host is this machine. It
// mirrors the check the browser fallback in the parent package makes: the two
// paths are handed the same address and must agree about what is acceptable.
func checkLoopbackURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parsing the URL %q: %w", rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme %q: %w", u.Scheme, errNotLoopback)
	}

	host := u.Hostname()
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("host %q: %w", host, errNotLoopback)
}

// withDefaults returns cfg with every unset field filled in.
func (cfg Config) withDefaults() Config {
	out := cfg
	if strings.TrimSpace(out.Title) == "" {
		out.Title = defaultTitle
	}
	// The four sizes are deliberately left as they came in, zeros included.
	// Zero means "derive it from the design", and the design cannot be turned
	// into pixels until the DPI of the monitor the window opens on is known —
	// which is only after there is a window. See [layoutFor].
	if strings.TrimSpace(out.DataDir) == "" {
		out.DataDir = defaultDataDir()
	}
	if out.Logger == nil {
		out.Logger = slog.Default()
	}
	return out
}

// defaultDataDir returns the WebView2 profile folder. An empty answer is not a
// failure: the control then picks its own folder next to %AppData%, which is
// worse only in that it is named after the exe.
func defaultDataDir() string {
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		return filepath.Join(local, dataDirName)
	}
	if cache, err := os.UserCacheDir(); err == nil {
		return filepath.Join(cache, dataDirName)
	}
	return ""
}
