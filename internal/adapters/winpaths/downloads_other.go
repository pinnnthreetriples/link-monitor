//go:build !windows

package winpaths

// Downloads has no Windows known folder on other systems.
func Downloads(fallback string) string { return fallback }

// Screenshots has no Windows known folder on other systems.
func Screenshots(fallback string) string { return fallback }
