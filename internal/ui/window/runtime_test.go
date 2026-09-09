package window

import (
	"errors"
	"testing"
)

func TestValidRuntimeVersionAcceptsOnlyAnInstalledVersion(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		pv   string
		want bool
	}{
		{name: "the version on the dev machine", pv: "152.0.4191.66", want: true},
		{name: "a three-part version", pv: "120.0.2210", want: true},
		{name: "a two-part version", pv: "120.0", want: true},
		{name: "surrounding whitespace is ignored", pv: "  152.0.4191.66\n", want: true},
		{name: "a zero build is still installed", pv: "152.0.0.0", want: true},
		{name: "an uninstalled runtime leaves 0.0.0.0", pv: "0.0.0.0", want: false},
		{name: "a bare zero is not installed", pv: "0.0", want: false},
		{name: "an empty value is nothing at all", pv: "", want: false},
		{name: "whitespace is nothing at all", pv: "   ", want: false},
		{name: "a single number is not a version", pv: "152", want: false},
		{name: "words are not a version", pv: "installed", want: false},
		{name: "a partly numeric value is not a version", pv: "152.0.x.66", want: false},
		{name: "an empty field is not a version", pv: "152..66", want: false},
		{name: "a trailing dot is not a version", pv: "152.0.", want: false},
		{name: "a leading dot is not a version", pv: ".152.0", want: false},
		{name: "a negative is not a version", pv: "-1.0", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := validRuntimeVersion(tc.pv); got != tc.want {
				t.Errorf("validRuntimeVersion(%q) = %t, want %t", tc.pv, got, tc.want)
			}
		})
	}
}

func TestRuntimeInstalledReadsThroughToTheVersion(t *testing.T) {
	t.Parallel()

	absent := errors.New("the key is not there")

	for _, tc := range []struct {
		name string
		read func() (string, error)
		want bool
	}{
		{
			name: "a version means the runtime is present",
			read: func() (string, error) { return "152.0.4191.66", nil },
			want: true,
		},
		{
			name: "no value at all means it is absent",
			read: func() (string, error) { return "", absent },
			want: false,
		},
		{
			name: "an unreadable hive means we cannot count on it",
			read: func() (string, error) { return "152.0.4191.66", absent },
			want: false,
		},
		{
			name: "a value that is not a version is not an answer",
			read: func() (string, error) { return "yes", nil },
			want: false,
		},
		{
			name: "the leftovers of an uninstall are not a runtime",
			read: func() (string, error) { return "0.0.0.0", nil },
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := runtimeInstalled(tc.read); got != tc.want {
				t.Errorf("runtimeInstalled() = %t, want %t", got, tc.want)
			}
		})
	}
}
