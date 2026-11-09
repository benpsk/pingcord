package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeWebhook struct {
	srv       *httptest.Server
	calls     atomic.Int64
	responses []func(w http.ResponseWriter) // one per call; last one repeats
	lastBody  atomic.Value                  // string
}

func newFakeWebhook(t *testing.T, responses ...func(w http.ResponseWriter)) *fakeWebhook {
	f := &fakeWebhook{responses: responses}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(f.calls.Add(1)) - 1
		b, _ := io.ReadAll(r.Body)
		f.lastBody.Store(string(b))
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.responses[min(n, len(f.responses)-1)](w)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func ok(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

func rateLimited(seconds float64) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprintf(w, `{"message":"You are being rate limited.","retry_after":%g,"global":false}`, seconds)
	}
}

func newTestClient(f *fakeWebhook, slept *[]time.Duration) *WebhookClient {
	c := NewWebhookClient(f.srv.URL+"/api/webhooks/1/secret-token", f.srv.Client())
	c.sleep = func(_ context.Context, d time.Duration) error {
		*slept = append(*slept, d)
		return nil
	}
	return c
}

func testMessage() Message {
	return Message{
		Content:         "<@100> review requested",
		Embeds:          []Embed{{Title: "PR #1", URL: "https://github.com/x/y/pull/1"}},
		AllowedMentions: AllowedMentions{Parse: []string{}, Users: []string{"100"}},
	}
}

func TestSendSuccessSerializesMessage(t *testing.T) {
	f := newFakeWebhook(t, ok)
	var slept []time.Duration

	if err := newTestClient(f, &slept).Send(context.Background(), testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(f.lastBody.Load().(string)), &got); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	am := got["allowed_mentions"].(map[string]any)
	if parse, ok := am["parse"].([]any); !ok || len(parse) != 0 {
		t.Errorf(`allowed_mentions.parse must be [] (never null), got %v`, am["parse"])
	}
	if users := am["users"].([]any); len(users) != 1 || users[0] != "100" {
		t.Errorf("allowed_mentions.users = %v", users)
	}
	if len(slept) != 0 {
		t.Errorf("unexpected sleep %v", slept)
	}
}

func TestSendNilParseIsSentAsEmptyArray(t *testing.T) {
	f := newFakeWebhook(t, ok)
	var slept []time.Duration
	msg := testMessage()
	msg.AllowedMentions.Parse = nil

	_ = newTestClient(f, &slept).Send(context.Background(), msg)

	if body := f.lastBody.Load().(string); !strings.Contains(body, `"parse":[]`) {
		t.Fatalf("body = %s", body)
	}
}

func TestSendRetriesOnceAfterRateLimit(t *testing.T) {
	f := newFakeWebhook(t, rateLimited(1.5), ok)
	var slept []time.Duration

	err := newTestClient(f, &slept).Send(context.Background(), testMessage())

	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if f.calls.Load() != 2 || len(slept) != 1 || slept[0] != 1500*time.Millisecond {
		t.Fatalf("calls=%d slept=%v", f.calls.Load(), slept)
	}
}

func TestSendGivesUpAfterSecondRateLimit(t *testing.T) {
	f := newFakeWebhook(t, rateLimited(0.1))
	var slept []time.Duration

	err := newTestClient(f, &slept).Send(context.Background(), testMessage())

	if err == nil || !strings.Contains(err.Error(), "429") || f.calls.Load() != 2 {
		t.Fatalf("err=%v calls=%d", err, f.calls.Load())
	}
}

func TestSendDoesNotWaitForLongRateLimit(t *testing.T) {
	f := newFakeWebhook(t, rateLimited(60))
	var slept []time.Duration

	err := newTestClient(f, &slept).Send(context.Background(), testMessage())

	if err == nil || len(slept) != 0 || f.calls.Load() != 1 {
		t.Fatalf("err=%v slept=%v calls=%d", err, slept, f.calls.Load())
	}
}

func TestSendRateLimitUsesRetryAfterHeaderFallback(t *testing.T) {
	f := newFakeWebhook(t, func(w http.ResponseWriter) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
	}, ok)
	var slept []time.Duration

	if err := newTestClient(f, &slept).Send(context.Background(), testMessage()); err != nil {
		t.Fatal(err)
	}
	if len(slept) != 1 || slept[0] != 2*time.Second {
		t.Fatalf("slept=%v", slept)
	}
}

func TestSendErrorsNeverLeakWebhookToken(t *testing.T) {
	t.Run("http error", func(t *testing.T) {
		f := newFakeWebhook(t, func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"message":"boom"}`)
		})
		var slept []time.Duration
		err := newTestClient(f, &slept).Send(context.Background(), testMessage())
		if err == nil || !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(err.Error(), "secret-token") {
			t.Fatalf("error leaks token: %v", err)
		}
	})

	t.Run("network error", func(t *testing.T) {
		c := NewWebhookClient("http://127.0.0.1:1/api/webhooks/1/secret-token", nil)
		err := c.Send(context.Background(), testMessage())
		if err == nil || strings.Contains(err.Error(), "secret-token") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestSendHonoursCancelledContextWhileWaiting(t *testing.T) {
	f := newFakeWebhook(t, rateLimited(1), ok)
	c := NewWebhookClient(f.srv.URL, f.srv.Client())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := c.Send(ctx, testMessage()); err == nil {
		t.Fatal("expected context error")
	}
}

func TestSleepCtx(t *testing.T) {
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("sleepCtx: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestSendPlainTextErrorBody(t *testing.T) {
	f := newFakeWebhook(t, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, "  upstream exploded  ")
	})
	var slept []time.Duration

	err := newTestClient(f, &slept).Send(context.Background(), testMessage())

	if err == nil || !strings.HasSuffix(err.Error(), ": upstream exploded") {
		t.Fatalf("err = %v", err)
	}
}

func TestSendInvalidURL(t *testing.T) {
	err := NewWebhookClient("://bad\x7f", nil).Send(context.Background(), testMessage())
	if err == nil || strings.Contains(err.Error(), "bad") {
		t.Fatalf("err = %v", err)
	}
}

func TestSendSerializesConcurrentCalls(t *testing.T) {
	var inFlight, maxInFlight atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := inFlight.Add(1)
		for {
			m := maxInFlight.Load()
			if n <= m || maxInFlight.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		inFlight.Add(-1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := NewWebhookClient(srv.URL, srv.Client())

	errs := make(chan error, 10)
	for range 10 {
		go func() { errs <- c.Send(context.Background(), testMessage()) }()
	}
	for range 10 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if maxInFlight.Load() != 1 {
		t.Fatalf("max concurrent requests = %d, want 1", maxInFlight.Load())
	}
}

func TestSendWaitsWhenBucketIsExhausted(t *testing.T) {
	f := newFakeWebhook(t, func(w http.ResponseWriter) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset-After", "1.25")
		w.WriteHeader(http.StatusNoContent)
	})
	var slept []time.Duration
	c := newTestClient(f, &slept)
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }

	if err := c.Send(context.Background(), testMessage()); err != nil {
		t.Fatal(err)
	}
	if len(slept) != 0 {
		t.Fatalf("first send should not wait, slept %v", slept)
	}
	if err := c.Send(context.Background(), testMessage()); err != nil {
		t.Fatal(err)
	}
	if len(slept) != 1 || slept[0] != 1250*time.Millisecond {
		t.Fatalf("second send should wait for the bucket reset, slept %v", slept)
	}
}

func TestSendDoesNotWaitWhenBucketHasRoom(t *testing.T) {
	f := newFakeWebhook(t, func(w http.ResponseWriter) {
		w.Header().Set("X-RateLimit-Remaining", "3")
		w.Header().Set("X-RateLimit-Reset-After", "1.25")
		w.WriteHeader(http.StatusNoContent)
	})
	var slept []time.Duration
	c := newTestClient(f, &slept)

	_ = c.Send(context.Background(), testMessage())
	_ = c.Send(context.Background(), testMessage())

	if len(slept) != 0 {
		t.Fatalf("slept %v", slept)
	}
}
