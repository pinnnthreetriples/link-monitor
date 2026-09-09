package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/pinnnthreetriples/link-monitor/internal/app"
)

// fakeLink stands in for the link controller.
type fakeLink struct {
	out        app.LinkOutcome
	lastAction app.LinkAction
	calls      int
}

func (f *fakeLink) Apply(_ context.Context, action app.LinkAction) app.LinkOutcome {
	f.calls++
	f.lastAction = action
	return f.out
}

func TestLinkConnectsAndDisconnects(t *testing.T) {
	t.Parallel()

	cases := []struct {
		body string
		want app.LinkAction
	}{
		{`{"action":"up"}`, app.LinkUp},
		{`{"action":"down"}`, app.LinkDown},
	}
	for _, tc := range cases {
		link := &fakeLink{out: app.LinkOutcome{OK: true, Message: "Tailscale подключён."}}
		rec := do(t, New(Deps{Link: link}), http.MethodPost, "/api/link", tc.body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code = %d", tc.body, rec.Code)
		}

		var got linkResponse
		decode(t, rec, &got)
		if !got.OK || !isRussian(got.Message) || got.LoginURL != "" || got.NeedsAdmin {
			t.Errorf("%s: response = %+v", tc.body, got)
		}
		if link.lastAction != tc.want {
			t.Errorf("%s: controller got action %q", tc.body, link.lastAction)
		}
	}
}

func TestLinkPassesTheLoginURLThrough(t *testing.T) {
	t.Parallel()

	const url = "https://login.tailscale.com/a/0123456789abcdef"
	link := &fakeLink{out: app.LinkOutcome{Message: "Нужен вход в браузере.", LoginURL: url}}

	rec := do(t, New(Deps{Link: link}), http.MethodPost, "/api/link", `{"action":"up"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var got linkResponse
	decode(t, rec, &got)
	if got.OK || got.LoginURL != url {
		t.Errorf("response = %+v", got)
	}
}

func TestLinkReportsThatAdminRightsAreNeeded(t *testing.T) {
	t.Parallel()

	link := &fakeLink{out: app.LinkOutcome{Message: "Нужны права администратора.", NeedsAdmin: true}}
	rec := do(t, New(Deps{Link: link}), http.MethodPost, "/api/link", `{"action":"down"}`)

	var got linkResponse
	decode(t, rec, &got)
	if got.OK || !got.NeedsAdmin || !isRussian(got.Message) {
		t.Errorf("response = %+v", got)
	}
}

func TestLinkRejectsAnUnknownAction(t *testing.T) {
	t.Parallel()

	link := &fakeLink{}
	h := New(Deps{Link: link})
	for _, body := range []string{`{"action":"toggle"}`, `{"action":""}`, `{}`} {
		rec := do(t, h, http.MethodPost, "/api/link", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code = %d, want 400", body, rec.Code)
		}
		var got linkResponse
		decode(t, rec, &got)
		if got.OK || !isRussian(got.Message) {
			t.Errorf("%s: response = %+v", body, got)
		}
	}
	if link.calls != 0 {
		t.Errorf("an unknown action still reached the controller %d times", link.calls)
	}
}

func TestLinkRejectsRubbishAndReportsAMissingController(t *testing.T) {
	t.Parallel()

	rec := do(t, New(Deps{Link: &fakeLink{}}), http.MethodPost, "/api/link", `{"action":`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad JSON code = %d", rec.Code)
	}

	// An unknown action is checked before the controller, so a bad request is a
	// bad request whether or not the feature is wired up.
	rec = do(t, New(Deps{}), http.MethodPost, "/api/link", `{"action":"toggle"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown action without a controller = %d", rec.Code)
	}

	rec = do(t, New(Deps{}), http.MethodPost, "/api/link", `{"action":"up"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no controller code = %d", rec.Code)
	}
	var got linkResponse
	decode(t, rec, &got)
	if got.OK || !isRussian(got.Message) {
		t.Errorf("response = %+v", got)
	}
}

func TestLinkRefusesTheWrongMethod(t *testing.T) {
	t.Parallel()

	rec := do(t, New(Deps{Link: &fakeLink{}}), http.MethodGet, "/api/link", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("code = %d", rec.Code)
	}
}
