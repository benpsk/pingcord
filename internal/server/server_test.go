package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/benpsk/pingcord/internal/githubhook"
	"github.com/benpsk/pingcord/internal/notify"
)

var secret = []byte("test-secret-test-secret")

type fakeEvents struct {
	got      []githubhook.Event
	err      error
	panicMsg string
	deadline time.Duration
}

func (f *fakeEvents) Handle(ctx context.Context, ev githubhook.Event) (notify.Result, error) {
	if f.panicMsg != "" {
		panic(f.panicMsg)
	}
	if dl, ok := ctx.Deadline(); ok {
		f.deadline = time.Until(dl)
	}
	f.got = append(f.got, ev)
	return notify.Result{Pinged: []string{"100"}, Unlinked: []string{"erin"}}, f.err
}

func newTestHandler(events *fakeEvents, interactions http.Handler) http.Handler {
	return NewHandler(Options{
		WebhookSecret: secret,
		Org:           "my-org",
		Events:        events,
		Interactions:  interactions,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func signedRequest(event string, body []byte) *http.Request {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	req := httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return req
}

const commentBody = `{"action":"created","issue":{"number":1,"title":"t","user":{"id":1,"login":"alice"}},"comment":{"body":"@carol","user":{"id":2,"login":"bob"}},"sender":{"id":2,"login":"bob"}}`

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGitHubWebhookHappyPath(t *testing.T) {
	events := &fakeEvents{}

	rec := serve(newTestHandler(events, nil), signedRequest("issue_comment", []byte(commentBody)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	if len(events.got) != 1 || events.got[0].Name != "issue_comment" || events.got[0].Mentions.Users[0] != "carol" {
		t.Fatalf("events = %+v", events.got)
	}
	if events.deadline <= 0 || events.deadline > handleTimeout {
		t.Fatalf("handler deadline = %v, want within %v", events.deadline, handleTimeout)
	}
}

func TestGitHubWebhookPing(t *testing.T) {
	events := &fakeEvents{}

	rec := serve(newTestHandler(events, nil), signedRequest("ping", []byte(`{"zen":"Keep it logically awesome."}`)))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "pong") || len(events.got) != 0 {
		t.Fatalf("status=%d body=%q events=%d", rec.Code, rec.Body.String(), len(events.got))
	}
}

func TestGitHubWebhookRejections(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(r *http.Request)
		want   int
	}{
		{name: "bad signature", mutate: func(r *http.Request) { r.Header.Set("X-Hub-Signature-256", "sha256=00") }, want: http.StatusUnauthorized},
		{name: "missing signature", mutate: func(r *http.Request) { r.Header.Del("X-Hub-Signature-256") }, want: http.StatusUnauthorized},
		{name: "missing event header", mutate: func(r *http.Request) { r.Header.Del("X-GitHub-Event") }, want: http.StatusBadRequest},
		{name: "malformed signature header", mutate: func(r *http.Request) { r.Header.Set("X-Hub-Signature-256", "sha256=nothex") }, want: http.StatusUnauthorized},
		{name: "form encoded", mutate: func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }, want: http.StatusUnsupportedMediaType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := &fakeEvents{}
			req := signedRequest("issue_comment", []byte(commentBody))
			tt.mutate(req)

			rec := serve(newTestHandler(events, nil), req)

			if rec.Code != tt.want || len(events.got) != 0 {
				t.Fatalf("status = %d want %d (events %d)", rec.Code, tt.want, len(events.got))
			}
		})
	}
}

func TestGitHubWebhookWrongMethod(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/github/webhook", nil)

	if rec := serve(newTestHandler(&fakeEvents{}, nil), req); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestGitHubWebhookMalformedPayload(t *testing.T) {
	rec := serve(newTestHandler(&fakeEvents{}, nil), signedRequest("issues", []byte(`{"action":5}`)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestGitHubWebhookTooLarge(t *testing.T) {
	body := bytes.Repeat([]byte("a"), maxGitHubBody+1)

	rec := serve(newTestHandler(&fakeEvents{}, nil), signedRequest("issues", body))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestGitHubWebhookNotifyFailureReturns502(t *testing.T) {
	events := &fakeEvents{err: errors.New("discord down: secret detail")}

	rec := serve(newTestHandler(events, nil), signedRequest("issue_comment", []byte(commentBody)))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret detail") {
		t.Fatal("internal error details leaked to client")
	}
}

func TestPanicIsRecovered(t *testing.T) {
	events := &fakeEvents{panicMsg: "boom"}

	rec := serve(newTestHandler(events, nil), signedRequest("issue_comment", []byte(commentBody)))

	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("status = %d body = %q", rec.Code, rec.Body.String())
	}
}

func TestHealthz(t *testing.T) {
	rec := serve(newTestHandler(&fakeEvents{}, nil), httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK || rec.Body.String() != "ok\n" {
		t.Fatalf("status = %d body = %q", rec.Code, rec.Body.String())
	}
}

func TestInteractionsRoute(t *testing.T) {
	called := false
	inter := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	})

	rec := serve(newTestHandler(&fakeEvents{}, inter), httptest.NewRequest(http.MethodPost, "/discord/interactions", nil))
	if !called || rec.Code != http.StatusTeapot {
		t.Fatalf("interactions not routed: %d", rec.Code)
	}

	rec = serve(newTestHandler(&fakeEvents{}, nil), httptest.NewRequest(http.MethodPost, "/discord/interactions", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("without handler status = %d, want 404", rec.Code)
	}
}

func TestUnknownPath(t *testing.T) {
	rec := serve(newTestHandler(&fakeEvents{}, nil), httptest.NewRequest(http.MethodGet, "/admin", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestNewHTTPServerHasTimeouts(t *testing.T) {
	srv := NewHTTPServer(":0", http.NotFoundHandler())

	if srv.ReadHeaderTimeout == 0 || srv.ReadTimeout == 0 || srv.WriteTimeout == 0 || srv.IdleTimeout == 0 {
		t.Fatalf("server missing timeouts: %+v", srv)
	}
}

func TestGitHubWebhookIgnoresOtherOrgs(t *testing.T) {
	events := &fakeEvents{}
	body := strings.Replace(commentBody, `"sender"`, `"organization":{"login":"Other-Org"},"sender"`, 1)

	rec := serve(newTestHandler(events, nil), signedRequest("issue_comment", []byte(body)))

	if rec.Code != http.StatusOK || len(events.got) != 0 {
		t.Fatalf("status=%d events=%d", rec.Code, len(events.got))
	}

	body = strings.Replace(commentBody, `"sender"`, `"organization":{"login":"MY-ORG"},"sender"`, 1)
	serve(newTestHandler(events, nil), signedRequest("issue_comment", []byte(body)))
	if len(events.got) != 1 {
		t.Fatal("org match should be case-insensitive")
	}
}

func TestGitHubWebhookLimitsConcurrentBodies(t *testing.T) {
	h := NewHandler(Options{
		WebhookSecret: secret, Events: &fakeEvents{}, MaxConcurrentWebhooks: 1,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	pr, pw := io.Pipe()
	slow := signedRequest("issue_comment", []byte(commentBody))
	slow.Body = pr
	done := make(chan struct{})
	go func() {
		serve(h, slow)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond) // let the slow request take the slot

	rec := serve(h, signedRequest("issue_comment", []byte(commentBody)))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 while the slot is taken", rec.Code)
	}
	_ = pw.Close()
	<-done
	if rec := serve(h, signedRequest("issue_comment", []byte(commentBody))); rec.Code != http.StatusOK {
		t.Fatalf("slot not released: %d", rec.Code)
	}
}

func TestInvalidSignatureWarningsAreSampledAndTruncated(t *testing.T) {
	var logs bytes.Buffer
	h := NewHandler(Options{
		WebhookSecret: secret, Events: &fakeEvents{},
		Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	for range 5 {
		req := signedRequest("issue_comment", []byte(commentBody))
		req.Header.Set("X-Hub-Signature-256", "sha256="+strings.Repeat("0", 64))
		req.Header.Set("X-GitHub-Delivery", strings.Repeat("x", 5000))
		req.Header.Set("X-Forwarded-For", "203.0.113.7")
		serve(h, req)
	}

	out := logs.String()
	if n := strings.Count(out, "invalid signature"); n != 1 {
		t.Fatalf("warnings logged = %d, want 1 (sampled)\n%s", n, out)
	}
	if strings.Contains(out, strings.Repeat("x", 100)) {
		t.Fatal("attacker-controlled header not truncated in logs")
	}
	if !strings.Contains(out, "203.0.113.7") {
		t.Fatal("client IP from the proxy not logged")
	}
}
