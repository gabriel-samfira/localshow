package sshsrv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabriel-samfira/localshow/params"
)

// recorder is an io.Writer standing in for a terminal session.
type recorder struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (r *recorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

func (r *recorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

// waitFor blocks until the recorder's output contains substr.
func (r *recorder) waitFor(t *testing.T, substr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(r.String(), substr) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in output %q", substr, r.String())
}

const (
	testHTTPURL  = "http://app.localshow.test"
	testHTTPSURL = "https://app.localshow.test"
	bannerMarker = "HTTP tunnel successfully created on"
)

func urlMessage(t *testing.T, httpURL string) params.NotifyMessage {
	t.Helper()
	payload, err := json.Marshal(params.URLs{HTTP: httpURL, HTTPS: testHTTPSURL})
	if err != nil {
		t.Fatalf("failed to marshal urls: %s", err)
	}
	return params.NotifyMessage{MessageType: params.NotifyMessageURL, Payload: payload}
}

func logMessage(text string) params.NotifyMessage {
	return params.NotifyMessage{MessageType: params.NotifyMessageLog, Payload: []byte(text)}
}

type testHandler struct {
	*messageHandler
	msgs chan params.NotifyMessage
	errs chan error
}

func newTestHandler(t *testing.T, format messageFormat) testHandler {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	msgs := make(chan params.NotifyMessage, 10)
	errs := make(chan error, 1)
	return testHandler{
		messageHandler: newMessageHandler(ctx, msgs, errs, format, true),
		msgs:           msgs,
		errs:           errs,
	}
}

// settle registers a session with logging enabled and sends it a log
// line, then waits for that line. Messages are processed in order, so
// once it arrives everything sent before it has been delivered.
func (h testHandler) settle(t *testing.T) {
	t.Helper()
	probe := &recorder{}
	id := h.Register(probe)
	defer h.Unregister(id)
	h.SetLogging(id, true)
	h.msgs <- logMessage("settle-probe")
	probe.waitFor(t, "settle-probe")
}

func TestBannerDeliveredToSessionAttachedFirst(t *testing.T) {
	h := newTestHandler(t, stringFormat)
	term := &recorder{}
	h.Register(term)

	h.msgs <- urlMessage(t, testHTTPURL)
	term.waitFor(t, bannerMarker)
	h.settle(t)

	out := term.String()
	if got := strings.Count(out, bannerMarker); got != 1 {
		t.Fatalf("banner delivered %d times, want 1:\n%s", got, out)
	}
	if !strings.Contains(out, testHTTPURL) || !strings.Contains(out, testHTTPSURL) {
		t.Fatalf("banner is missing the tunnel URLs:\n%s", out)
	}
}

func TestBannerReplayedToSessionAttachedLater(t *testing.T) {
	h := newTestHandler(t, stringFormat)
	h.msgs <- urlMessage(t, testHTTPURL)
	h.settle(t)

	term := &recorder{}
	h.Register(term)
	// Register replays synchronously, so the banner must already be there.
	out := term.String()
	if got := strings.Count(out, bannerMarker); got != 1 {
		t.Fatalf("banner delivered %d times, want 1:\n%s", got, out)
	}
	if !strings.Contains(out, testHTTPURL) {
		t.Fatalf("banner is missing the tunnel URL:\n%s", out)
	}
}

// TestBannerExactlyOnceWhenAttachRacesDelivery pits a session attaching
// against the banner being processed, the way the SSH client's back to
// back forward request and session open do, and checks the banner is
// neither lost nor duplicated.
func TestBannerExactlyOnceWhenAttachRacesDelivery(t *testing.T) {
	for i := 0; i < 300; i++ {
		h := newTestHandler(t, stringFormat)
		h.msgs <- urlMessage(t, testHTTPURL)

		term := &recorder{}
		h.Register(term)
		h.settle(t)

		if got := strings.Count(term.String(), bannerMarker); got != 1 {
			t.Fatalf("iteration %d: banner delivered %d times, want 1:\n%s", i, got, term.String())
		}
		h.Close()
	}
}

func TestEveryBannerIsReplayedInOrder(t *testing.T) {
	h := newTestHandler(t, stringFormat)
	h.msgs <- urlMessage(t, "http://first.localshow.test")
	h.msgs <- urlMessage(t, "http://second.localshow.test")
	h.settle(t)

	term := &recorder{}
	h.Register(term)
	out := term.String()
	first := strings.Index(out, "http://first.localshow.test")
	second := strings.Index(out, "http://second.localshow.test")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("expected both banners in order, got:\n%s", out)
	}
}

func TestErrorShownToSessionAttachedFirst(t *testing.T) {
	h := newTestHandler(t, stringFormat)
	term := &recorder{}
	h.Register(term)

	h.errs <- errors.New("subdomain \"www\" is reserved")
	term.waitFor(t, "subdomain \"www\" is reserved")
	if err := h.Wait(); err == nil {
		t.Fatal("Wait returned nil, want the tunnel error")
	}
}

func TestErrorReplayedToSessionAttachedLater(t *testing.T) {
	h := newTestHandler(t, stringFormat)
	h.errs <- errors.New("subdomain \"www\" is reserved")
	if err := h.Wait(); err == nil {
		t.Fatal("Wait returned nil, want the tunnel error")
	}

	term := &recorder{}
	h.Register(term)
	if out := term.String(); !strings.Contains(out, "subdomain \"www\" is reserved") {
		t.Fatalf("error was not replayed to a late session, got %q", out)
	}
}

func TestLogLinesAreOptInAndNeverReplayed(t *testing.T) {
	h := newTestHandler(t, stringFormat)
	h.msgs <- logMessage("before-attach")
	h.settle(t)

	term := &recorder{}
	id := h.Register(term)
	h.msgs <- logMessage("logging-disabled")
	h.settle(t)
	h.SetLogging(id, true)
	h.msgs <- logMessage("logging-enabled")
	term.waitFor(t, "logging-enabled")

	out := term.String()
	for _, unwanted := range []string{"before-attach", "logging-disabled", "settle-probe"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("log line %q should not have reached the session:\n%s", unwanted, out)
		}
	}
}

func TestJSONFormatBannerIsPassedThrough(t *testing.T) {
	h := newTestHandler(t, jsonFormat)
	term := &recorder{}
	h.Register(term)
	h.msgs <- urlMessage(t, testHTTPURL)
	term.waitFor(t, "\n")

	var got params.URLs
	if err := json.Unmarshal([]byte(strings.TrimSpace(term.String())), &got); err != nil {
		t.Fatalf("api sessions must receive the raw JSON banner, got %q: %s", term.String(), err)
	}
	if got.HTTP != testHTTPURL || got.HTTPS != testHTTPSURL {
		t.Fatalf("unexpected urls %+v", got)
	}
}
