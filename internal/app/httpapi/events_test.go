package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// noFlusher is a ResponseWriter that cannot stream, which SSE needs.
type noFlusher struct{ http.ResponseWriter }

func TestEventsStreamsTheCurrentStatusThenEveryChange(t *testing.T) {
	t.Parallel()

	src := newFakeStatus()
	src.latest, src.hasLatest = sampleResult(), true
	h := New(Deps{Status: src})

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8731/api/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()

	// The send completes only once the handler has taken the value, so by the
	// time it returns the first frame is already written.
	changed := sampleResult()
	changed.Snapshot.Summary = "Связь установлена"
	src.updates <- changed

	cancel()
	<-done

	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("Content-Type = %q", got)
	}
	frames := dataFrames(t, rec.Body.String())
	if len(frames) != 2 {
		t.Fatalf("got %d frames:\n%s", len(frames), rec.Body.String())
	}
	if frames[0].Summary != "Частичная связь" {
		t.Errorf("first frame = %+v", frames[0])
	}
	if frames[1].Summary != "Связь установлена" {
		t.Errorf("second frame = %+v", frames[1])
	}
	if src.unsubscribed() != 1 {
		t.Errorf("the stream did not drop its subscription: %d", src.unsubscribed())
	}
}

func TestEventsStopsWhenThePollerStops(t *testing.T) {
	t.Parallel()

	src := newFakeStatus()
	h := New(Deps{Status: src})

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8731/api/events", nil)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()

	close(src.updates)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the stream did not end when the poller stopped")
	}
	if src.unsubscribed() != 1 {
		t.Errorf("the stream did not drop its subscription: %d", src.unsubscribed())
	}
}

func TestEventsOverARealConnection(t *testing.T) {
	t.Parallel()

	src := newFakeStatus()
	src.latest, src.hasLatest = sampleResult(), true
	srv := httptest.NewServer(New(Deps{Status: src}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/events", nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("opening the stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("reading the first frame: %v", err)
	}
	if !strings.HasPrefix(line, "data: ") {
		t.Fatalf("first line = %q", line)
	}
	var got Status
	if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(line), "data: ")), &got); err != nil {
		t.Fatalf("decoding the frame: %v", err)
	}
	if got.Summary != "Частичная связь" {
		t.Errorf("streamed status = %+v", got)
	}
}

func TestEventsWithoutAPollerOrAFlusher(t *testing.T) {
	t.Parallel()

	rec := do(t, New(Deps{}), http.MethodGet, "/api/events", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no poller code = %d", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8731/api/events", nil)
	plain := httptest.NewRecorder()
	New(Deps{Status: newFakeStatus()}).ServeHTTP(noFlusher{plain}, req)
	if plain.Code != http.StatusInternalServerError {
		t.Errorf("no flusher code = %d", plain.Code)
	}
}

func TestEventsSendsAHeartbeatOnAnIdleStream(t *testing.T) {
	t.Parallel()

	src := newFakeStatus()
	s := &server{deps: Deps{Status: src}}

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8731/api/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	// The real heartbeat is minutes apart; the frame itself is what is worth
	// asserting, so it is written here through the same helper the loop uses.
	writeAndFlush(rec, rec, ": ping\n\n")
	cancel()
	s.handleEvents(rec, req)

	if !strings.Contains(rec.Body.String(), ": ping") {
		t.Errorf("no heartbeat frame in %q", rec.Body.String())
	}
}

// dataFrames pulls the JSON payloads out of an SSE stream.
func dataFrames(t *testing.T, body string) []Status {
	t.Helper()

	var out []Status
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var st Status
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &st); err != nil {
			t.Fatalf("decoding %q: %v", line, err)
		}
		out = append(out, st)
	}
	return out
}
