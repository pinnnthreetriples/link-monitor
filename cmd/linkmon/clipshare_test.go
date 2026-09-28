package main

import "testing"

func TestClipboardSharingStartsWithTheApp(t *testing.T) {
	wiring := wireClip(options{}, nil)
	if !wiring.cfg.EnableOnStart {
		t.Fatal("clipboard sharing did not start with the app")
	}
}
