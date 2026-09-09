package ui

import (
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

func TestTooltipShowsTheSummaryAndTheDetail(t *testing.T) {
	got := tooltip(Status{
		Overall: core.StateOK,
		Summary: "Связь установлена",
		Detail:  "Задержка 12 мс",
	})
	want := "Связь установлена\nЗадержка 12 мс"
	if got != want {
		t.Errorf("tooltip = %q, want %q", got, want)
	}
}

func TestTooltipOmitsAnEmptyDetail(t *testing.T) {
	got := tooltip(Status{Overall: core.StateOK, Summary: "Связь установлена", Detail: "   "})
	if got != "Связь установлена" {
		t.Errorf("tooltip = %q, want no second line", got)
	}
}

func TestTooltipFallsBackToTheStateWhenThereIsNoSummary(t *testing.T) {
	tests := map[core.State]string{
		core.StateOK:      "Связь установлена",
		core.StateUnknown: "Состояние связи неизвестно",
		core.StateWarn:    "Связь работает с перебоями",
		core.StateFail:    "Связь потеряна",
	}
	for state, want := range tests {
		if got := tooltip(Status{Overall: state}); got != want {
			t.Errorf("tooltip for %s = %q, want %q", state, got, want)
		}
	}
}

func TestTooltipFitsTheWindowsBuffer(t *testing.T) {
	got := tooltip(Status{
		Overall: core.StateFail,
		Summary: "Связь потеряна",
		Detail:  strings.Repeat("очень длинное объяснение ", 40),
	})

	if n := len(utf16.Encode([]rune(got))); n > tooltipLimit {
		t.Errorf("tooltip is %d UTF-16 units, the buffer holds %d", n, tooltipLimit)
	}
	if !strings.HasPrefix(got, "Связь потеряна\n") {
		t.Errorf("truncation ate the headline: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated tooltip does not end in an ellipsis: %q", got)
	}
}

func TestTruncateUTF16CountsSurrogatePairs(t *testing.T) {
	// Emoji outside the BMP cost two UTF-16 units each, which is the whole
	// reason this counts units instead of runes.
	in := strings.Repeat("😀", 10)
	got := truncateUTF16(in, 9)
	if n := len(utf16.Encode([]rune(got))); n > 9 {
		t.Errorf("truncateUTF16 returned %d units, want at most 9", n)
	}
	if strings.Count(got, "😀") != 4 {
		t.Errorf("got %q, want four whole emoji plus an ellipsis", got)
	}
}

func TestTruncateUTF16LeavesShortStringsAlone(t *testing.T) {
	if got := truncateUTF16("Связь", 127); got != "Связь" {
		t.Errorf("truncateUTF16 = %q, want it untouched", got)
	}
}

func TestNotificationForUsesTheSnapshotsOwnWords(t *testing.T) {
	got := notificationFor(Status{
		Overall: core.StateFail,
		Summary: "Связь потеряна",
		Detail:  "Tailscale не запущен",
	})
	want := Notification{Title: "Связь потеряна", Body: "Tailscale не запущен"}
	if got != want {
		t.Errorf("notificationFor = %+v, want %+v", got, want)
	}
}

func TestNotificationForIsNeverBlank(t *testing.T) {
	got := notificationFor(Status{Overall: core.StateWarn})
	if got.Title != "Связь работает с перебоями" {
		t.Errorf("title = %q, want the Russian fallback", got.Title)
	}
	if got.Body != noDetail {
		t.Errorf("body = %q, want %q", got.Body, noDetail)
	}
}

func TestUserFacingTextIsRussian(t *testing.T) {
	// A guard against an English string sneaking back into the menu.
	for _, s := range []string{
		menuOpen, menuCheck, menuQuit,
		hintOpen, hintCheck, hintQuit,
		startingSummary, noDetail,
	} {
		if !strings.ContainsFunc(s, func(r rune) bool { return r >= 'а' && r <= 'я' }) {
			t.Errorf("%q has no Cyrillic in it", s)
		}
	}
}
