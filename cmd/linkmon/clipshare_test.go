package main

import "testing"

func TestClipboardSharingStartsOffUntilUserTurnsItOn(t *testing.T) {
	wiring := wireClip(options{}, nil)
	if wiring.cfg.EnableOnStart {
		t.Fatal("clipboard sharing started without a user action")
	}
}
