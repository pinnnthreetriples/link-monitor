package window

import (
	"errors"
	"fmt"
	"testing"
)

func TestDeclareDPIReportsWhatWindowsSaid(t *testing.T) {
	t.Parallel()

	refused := errors.New("SetProcessDpiAwarenessContext: some other failure")

	for _, tc := range []struct {
		name string
		set  func() error
		want error
	}{
		{
			name: "the awareness was accepted",
			set:  func() error { return nil },
			want: nil,
		},
		{
			name: "a manifest got there first",
			set:  func() error { return errDPIAlreadyDeclared },
			want: errDPIAlreadyDeclared,
		},
		{
			name: "the manifest's error is recognised through a wrapper",
			set:  func() error { return fmt.Errorf("shcore: %w", errDPIAlreadyDeclared) },
			want: errDPIAlreadyDeclared,
		},
		{
			name: "this Windows has no per-monitor awareness at all",
			set:  func() error { return errNoPerMonitorDPI },
			want: errNoPerMonitorDPI,
		},
		{
			name: "anything else is wrapped with what was being attempted",
			set:  func() error { return refused },
			want: refused,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := declareDPI(tc.set)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("declareDPI() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("declareDPI() = %v, want it to wrap %v", err, tc.want)
			}
		})
	}
}

// TestDeclareDPINamesWhatItWasDoing checks the one thing a log line is for. A
// bare "access is denied" in the log of a program that has just opened a
// window at the wrong size would say nothing about which call refused.
func TestDeclareDPINamesWhatItWasDoing(t *testing.T) {
	t.Parallel()

	err := declareDPI(func() error { return errors.New("access is denied") })
	if err == nil {
		t.Fatal("declareDPI() = nil, want the failure reported")
	}
	if got := err.Error(); got == "access is denied" {
		t.Errorf("declareDPI() = %q, want it to say what it was declaring", got)
	}
}

// TestDeclarePerMonitorDPIOnThisMachine calls the real thing. On Windows it
// must either take — this is the whole fix — or say that something declared an
// awareness first; anywhere else it says there is nothing to declare. It is
// deliberately not parallel: DPI awareness is a property of the process, so
// this test changes what every other test's display queries answer, and it
// must not do so while one is mid-flight.
func TestDeclarePerMonitorDPIOnThisMachine(t *testing.T) {
	err := DeclarePerMonitorDPI()
	switch {
	case err == nil:
		t.Log("this process now scales its own windows, per monitor")
	case errors.Is(err, errDPIAlreadyDeclared):
		t.Log("an awareness was already declared for this process, which is not a failure")
	case errors.Is(err, errNoPerMonitorDPI):
		t.Log("this platform has no per-monitor DPI awareness to declare")
	default:
		t.Fatalf("DeclarePerMonitorDPI() = %v, want it to take or to say why not", err)
	}

	// Whatever happened, asking twice must not turn into a different kind of
	// failure: the second call meets an awareness that is already set.
	if second := DeclarePerMonitorDPI(); second != nil &&
		!errors.Is(second, errDPIAlreadyDeclared) && !errors.Is(second, errNoPerMonitorDPI) {
		t.Errorf("the second DeclarePerMonitorDPI() = %v, want it recognised as already set",
			second)
	}
}
