//go:build windows

// Package winpaths resolves user-facing Windows folders, including redirected ones.
package winpaths

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// Downloads returns the real Downloads known folder, even when moved to D:.
// A fallback keeps startup possible if Shell's folder lookup is unavailable.
func Downloads(fallback string) string {
	path, err := windows.KnownFolderPath(windows.FOLDERID_Downloads, windows.KF_FLAG_DEFAULT)
	if err != nil || path == "" {
		return fallback
	}
	return filepath.Clean(path)
}

// Screenshots resolves the user's redirected Screenshots known folder.
func Screenshots(fallback string) string {
	path, err := windows.KnownFolderPath(windows.FOLDERID_Screenshots, windows.KF_FLAG_DEFAULT)
	if err != nil || path == "" {
		return fallback
	}
	return filepath.Clean(path)
}
