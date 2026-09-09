package window

import (
	"errors"
	"strings"
	"testing"
)

func TestNewRefusesAConfigItCannotBuildAWindowFrom(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		url  string
	}{
		{name: "no URL at all", url: ""},
		{name: "whitespace instead of a URL", url: "   "},
		{name: "a remote host", url: "http://100.127.188.87:8765/"},
		{name: "a public name", url: "https://example.com/"},
		{name: "a file path", url: `file:///C:/frontend/index.html`},
		{name: "a scheme with no host", url: "about:blank"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			w, err := New(Config{URL: tc.url})
			if !errors.Is(err, errBadConfig) {
				t.Fatalf("New(%q) = %v, want an errBadConfig", tc.url, err)
			}
			if w != nil {
				t.Error("New returned a window together with an error")
			}
		})
	}
}

func TestNewAcceptsEveryWayOfSayingThisMachine(t *testing.T) {
	t.Parallel()

	for _, url := range []string{
		"http://127.0.0.1:8765/",
		"http://localhost:8765/",
		"http://[::1]:8765/",
		"https://127.0.0.1:8765/ui",
		"http://127.0.0.2:8765/",
	} {
		if _, err := New(Config{URL: url}); err != nil {
			t.Errorf("New(%q) = %v, want a window", url, err)
		}
	}
}

func TestNewAppliesTheDesignsDefaults(t *testing.T) {
	t.Parallel()

	w, err := New(Config{URL: testURL})
	if err != nil {
		t.Fatalf("New() = %v, want a window", err)
	}

	got := w.cfg
	if got.Title != defaultTitle {
		t.Errorf("Title = %q, want %q", got.Title, defaultTitle)
	}
	// The sizes must stay zero. Filling them in here would be inventing a
	// number of physical pixels before anything knows the display's DPI, which
	// is exactly the bug this arithmetic replaced; zero is what tells
	// [layoutFor] to derive the size from the design instead.
	if got.Width != 0 || got.Height != 0 {
		t.Errorf("size = %dx%d, want it left at 0x0 to be derived from the design",
			got.Width, got.Height)
	}
	if got.MinWidth != 0 || got.MinHeight != 0 {
		t.Errorf("minimum = %dx%d, want it left at 0x0 to be derived from the design",
			got.MinWidth, got.MinHeight)
	}
	if got.DataDir == "" {
		t.Error("DataDir is empty; WebView2 would scatter its profile next to the exe name")
	}
	if got.Logger == nil {
		t.Error("Logger is nil; every warning the window raises would panic")
	}
	if !w.visible {
		t.Error("a window that was not asked to start hidden should open visible")
	}
}

func TestNewKeepsWhatItWasGiven(t *testing.T) {
	t.Parallel()

	cfg := Config{
		URL:         testURL,
		Title:       "Монитор связи",
		Width:       1280,
		Height:      800,
		MinWidth:    640,
		MinHeight:   600,
		DataDir:     `D:\profiles\webview`,
		StartHidden: true,
		Logger:      quiet(),
	}

	w, err := New(cfg)
	if err != nil {
		t.Fatalf("New() = %v, want a window", err)
	}

	got := w.cfg
	if got.Title != cfg.Title || got.Width != cfg.Width || got.Height != cfg.Height {
		t.Errorf("New rewrote the title or the size: %+v", got)
	}
	if got.MinWidth != cfg.MinWidth || got.MinHeight != cfg.MinHeight {
		t.Errorf("New rewrote the minimum size: %dx%d", got.MinWidth, got.MinHeight)
	}
	if got.DataDir != cfg.DataDir {
		t.Errorf("DataDir = %q, want %q", got.DataDir, cfg.DataDir)
	}
	if w.visible {
		t.Error("StartHidden was ignored")
	}
}

// TestNonsenseSizesReachTheArithmeticUntouched records where the decision
// moved to. New no longer has an opinion about sizes at all: a negative is
// carried through and treated as "not set" by [positiveOr], which is the only
// place that judgement is made. The behaviour it used to guard here — a size
// below the minimum being raised to it — is tested against [layoutFor] in
// sizing_test.go, where the DPI it depends on can be varied.
func TestNonsenseSizesReachTheArithmeticUntouched(t *testing.T) {
	t.Parallel()

	w, err := New(Config{URL: testURL, Width: -1, Height: 0, MinWidth: -100, MinHeight: 0})
	if err != nil {
		t.Fatalf("New() = %v, want a window", err)
	}

	got := w.cfg
	if got.Width != -1 || got.Height != 0 || got.MinWidth != -100 || got.MinHeight != 0 {
		t.Errorf("New rewrote the sizes: %dx%d, minimum %dx%d",
			got.Width, got.Height, got.MinWidth, got.MinHeight)
	}
}

func TestDefaultDataDirNamesTheProgram(t *testing.T) {
	t.Parallel()

	// %LOCALAPPDATA% is set on every Windows session, and the fallbacks exist
	// only so the function has an answer everywhere.
	if dir := defaultDataDir(); dir != "" && !strings.HasSuffix(dir, dataDirName) {
		t.Errorf("defaultDataDir() = %q, want it to end in %q", dir, dataDirName)
	}
}
