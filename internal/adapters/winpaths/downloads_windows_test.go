//go:build windows

package winpaths

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestDownloadsUsesWindowsKnownFolder(t *testing.T) {
	want, err := windows.KnownFolderPath(windows.FOLDERID_Downloads, windows.KF_FLAG_DEFAULT)
	if err != nil {
		t.Skipf("Windows has no Downloads folder: %v", err)
	}
	if got := Downloads(`C:\fallback\Downloads`); got != filepath.Clean(want) {
		t.Fatalf("Downloads = %q, want %q", got, want)
	}
}
