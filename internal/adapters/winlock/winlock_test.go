package winlock

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckNameRejectsNamesTheKernelWouldMisread(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		lock    string
		wantErr bool
	}{
		{name: "a plain name is accepted", lock: "link-monitor", wantErr: false},
		{name: "a brace-wrapped GUID is accepted", lock: "{9f1c-4d}", wantErr: false},
		{name: "an empty name is refused", lock: "", wantErr: true},
		{name: "a name of spaces is refused", lock: "   ", wantErr: true},
		{name: "a namespace separator is refused", lock: `Global\link-monitor`, wantErr: true},
		{name: "a trailing separator is refused", lock: `link-monitor\`, wantErr: true},
		{name: "an over-long name is refused", lock: strings.Repeat("a", nameLimit+1), wantErr: true},
		{name: "a name at the limit is accepted", lock: strings.Repeat("a", nameLimit), wantErr: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := checkName(tc.lock)
			if tc.wantErr {
				if !errors.Is(err, errBadName) {
					t.Fatalf("checkName(%q) = %v, want an errBadName", tc.lock, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("checkName(%q) = %v, want no error", tc.lock, err)
			}
		})
	}
}

func TestAcquireRejectsABadNameBeforeTouchingTheKernel(t *testing.T) {
	t.Parallel()

	release, err := Acquire(`Global\anything`)
	if err == nil {
		t.Fatal("Acquire accepted a name containing a namespace separator")
	}
	if release != nil {
		t.Fatal("Acquire returned a release together with an error")
	}
	if errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("a malformed name was reported as a second instance: %v", err)
	}
}
