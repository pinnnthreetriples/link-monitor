package sshx

import (
	"strings"
	"testing"
)

func TestTokenLineFieldsFindsItsLine(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		stdout string
		want   int
	}{
		"alone":               {"T 4", 4},
		"trailing newline":    {"T 4\n", 4},
		"windows line ending": {"T 4\r\n", 4},
		"leading whitespace":  {"   T 4  \r\n", 4},
		"after noise":         {"noise\r\nmore noise\r\nT 4\r\n", 4},
		"before noise":        {"T 4\r\nnoise\r\n", 4},
		"buried in CLIXML":    {buriedInNoise("T 4"), 4},
		"negative value":      {"T -1", -1},
		"first match wins":    {"T 4\r\nT 1\r\n", 4},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := tokenLineFields(c.stdout, "T", 1)
			if err != nil {
				t.Fatalf("tokenLineFields: %v", err)
			}
			if len(got) != 1 || got[0] != c.want {
				t.Errorf("got %v, want [%d]", got, c.want)
			}
		})
	}
}

func TestTokenLineFieldsReadsSeveralValues(t *testing.T) {
	t.Parallel()
	got, err := tokenLineFields(buriedInNoise("T 2 10013"), "T", 2)
	if err != nil {
		t.Fatalf("tokenLineFields: %v", err)
	}
	if len(got) != 2 || got[0] != 2 || got[1] != 10013 {
		t.Errorf("got %v, want [2 10013]", got)
	}
}

func TestTokenLineFieldsRejectsWhatItCannotRead(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		stdout string
		want   int
	}{
		"empty stream":         {"", 1},
		"no token at all":      {"4\r\n", 1},
		"token only":           {"T\r\n", 1},
		"too few values":       {"T\r\n", 2},
		"too many values":      {"T 1 2 3\r\n", 2},
		"value is not numeric": {"T Выполняется\r\n", 1},
		// A token that is merely mentioned inside a line does not count; only a
		// line that starts with it does.
		"token not first": {"progress: T 4\r\n", 1},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := tokenLineFields(c.stdout, "T", c.want); err == nil {
				t.Fatalf("expected an error for %q", c.stdout)
			}
		})
	}
}

// TestTokenLineFieldsNamesTheTokenItWanted keeps the error useful in a log,
// where the reader has no idea which script was running.
func TestTokenLineFieldsNamesTheTokenItWanted(t *testing.T) {
	t.Parallel()
	_, err := tokenLineFields("nothing useful", peerProbeToken, 2)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), peerProbeToken) {
		t.Errorf("the error should name the token, got %v", err)
	}
}
