package activedns

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestParseRateLimits(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	in := func(seconds int) time.Time { return now.Add(time.Duration(seconds) * time.Second) }

	for _, tc := range []struct {
		name   string
		header http.Header
		want   []RateLimit
	}{
		{"none", http.Header{}, nil},
		{"current draft", http.Header{
			"Ratelimit":        {`"default";r=50;t=30`},
			"Ratelimit-Policy": {`"default";q=100;w=60`},
		}, []RateLimit{{Name: "default", Limit: 100, Remaining: 50, Reset: in(30)}}},
		{"current draft, two quotas", http.Header{
			"Ratelimit":        {`"persec";r=0;t=1, "perhour";r=4321;t=1800`},
			"Ratelimit-Policy": {`"persec";q=5;w=1, "perhour";q=5000;w=3600`},
		}, []RateLimit{{Name: "persec", Limit: 5, Remaining: 0, Reset: in(1)}, {Name: "perhour", Limit: 5000, Remaining: 4321, Reset: in(1800)}}},
		{"current draft on two lines, no policy", http.Header{
			"Ratelimit": {`"persec";r=3;t=1`, `"perday";r=10`},
		}, []RateLimit{{Name: "persec", Limit: -1, Remaining: 3, Reset: in(1)}, {Name: "perday", Limit: -1, Remaining: 10}}},
		{"unquoted name, partition key", http.Header{
			"Ratelimit": {`ip;r=7;t=12;pk="abc,def"`},
		}, []RateLimit{{Name: "ip", Limit: -1, Remaining: 7, Reset: in(12)}}},
		{"earlier draft, one field", http.Header{
			"Ratelimit": {`limit=100, remaining=50, reset=30`},
		}, []RateLimit{{Limit: 100, Remaining: 50, Reset: in(30)}}},
		{"three headers", http.Header{
			"Ratelimit-Limit":     {"100, 100;w=60"},
			"Ratelimit-Remaining": {"0"},
			"Ratelimit-Reset":     {"45"},
		}, []RateLimit{{Limit: 100, Remaining: 0, Reset: in(45)}}},
		{"x- headers, seconds", http.Header{
			"X-Ratelimit-Limit":     {"60"},
			"X-Ratelimit-Remaining": {"59"},
			"X-Ratelimit-Reset":     {"17"},
		}, []RateLimit{{Limit: 60, Remaining: 59, Reset: in(17)}}},
		{"x- headers, unix time", http.Header{
			"X-Ratelimit-Limit":     {"60"},
			"X-Ratelimit-Remaining": {"0"},
			"X-Ratelimit-Reset":     {"1791202500"},
		}, []RateLimit{{Limit: 60, Remaining: 0, Reset: time.Unix(1791202500, 0)}}},
		{"x- headers, unix milliseconds", http.Header{
			"X-Ratelimit-Remaining": {"1"},
			"X-Ratelimit-Reset":     {"1791202500000"},
		}, []RateLimit{{Limit: -1, Remaining: 1, Reset: time.Unix(1791202500, 0)}}},
		{"remaining only", http.Header{
			"Ratelimit-Remaining": {"9"},
		}, []RateLimit{{Limit: -1, Remaining: 9}}},
		{"the standard form wins over x-", http.Header{
			"Ratelimit":             {`"q";r=1;t=2`},
			"X-Ratelimit-Remaining": {"99"},
		}, []RateLimit{{Name: "q", Limit: -1, Remaining: 1, Reset: in(2)}}},
		{"garbage", http.Header{
			"Ratelimit":           {`;;;`},
			"Ratelimit-Remaining": {"lots"},
		}, nil},
		{"the API's budget headers are not rate-limit headers", http.Header{
			"X-Ratelimit-Hour-Remaining": {"0"},
			"X-Ratelimit-Hour-Reset":     {"1791205200"},
		}, nil},
	} {
		got := parseRateLimits(tc.header, now)
		if len(got) != len(tc.want) {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
			continue
		}
		for i := range got {
			if got[i].Name != tc.want[i].Name || got[i].Limit != tc.want[i].Limit || got[i].Remaining != tc.want[i].Remaining || !got[i].Reset.Equal(tc.want[i].Reset) {
				t.Errorf("%s: quota %d = %+v, want %+v", tc.name, i, got[i], tc.want[i])
			}
		}
	}
}

// With nothing left in a short window (queries per second), the next request
// waits for the reset instead of being sent and refused.
func TestExhaustedQuotaDelaysTheNextRequest(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("RateLimit-Policy", `"persec";q=2;w=1`)
		w.Header().Set("RateLimit", `"persec";r=0;t=1`)
		writeStatus(w, http.StatusOK, pageJSON)
	})
	for i := 0; i < 3; i++ {
		if _, err := h.client.Query(context.Background(), "example.com"); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.sleeps(); len(got) != 2 || got[0] != time.Second || got[1] != time.Second {
		t.Errorf("waited %v, want a second before the 2nd and the 3rd request", got)
	}
	if h.hits.Load() != 3 {
		t.Errorf("%d requests, want 3 (none refused)", h.hits.Load())
	}
	limits := h.client.RateLimits()
	if len(limits) != 1 || limits[0].Name != "persec" || limits[0].Limit != 2 || limits[0].Remaining != 0 {
		t.Errorf("RateLimits() = %+v", limits)
	}
}

// With quota left, nothing waits.
func TestQuotaLeftDoesNotDelay(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "60")
		w.Header().Set("X-RateLimit-Remaining", "41")
		w.Header().Set("X-RateLimit-Reset", "30")
		writeStatus(w, http.StatusOK, pageJSON)
	})
	for i := 0; i < 3; i++ {
		if _, err := h.client.Query(context.Background(), "example.com"); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.sleeps(); len(got) != 0 {
		t.Errorf("waited %v with quota left", got)
	}
}

// A quota that resets later than the client is willing to sleep is reported
// instead: the answer in hand is returned, later calls fail at once.
func TestExhaustedLongQuotaFailsLocally(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("RateLimit", `"perhour";r=0;t=1200`)
		writeStatus(w, http.StatusOK, pageJSON)
	})
	start := h.now()
	if _, err := h.client.Query(context.Background(), "example.com"); err != nil {
		t.Fatalf("the answer that carried the header: %v", err)
	}
	_, err := h.client.Query(context.Background(), "example.com")
	var limit *RateLimitError
	if !errors.As(err, &limit) || !limit.Local || limit.Exceeded != "perhour" || !limit.RetryAt.Equal(start.Add(20*time.Minute)) {
		t.Errorf("second call: %v", err)
	}
	if h.hits.Load() != 1 || len(h.sleeps()) != 0 {
		t.Errorf("%d requests, sleeps %v", h.hits.Load(), h.sleeps())
	}
	h.advance(21 * time.Minute)
	if _, err := h.client.Query(context.Background(), "example.com"); err != nil {
		t.Errorf("after the reset: %v", err)
	}
}

// A 429 without Retry-After is retried when the quota's reset says so.
func Test429WithoutRetryAfterUsesTheQuotaReset(t *testing.T) {
	first := true
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if first {
			first = false
			w.Header().Set("RateLimit-Limit", "5")
			w.Header().Set("RateLimit-Remaining", "0")
			w.Header().Set("RateLimit-Reset", "3")
			writeStatus(w, http.StatusTooManyRequests, `{"error":"too many requests","code":429}`)
			return
		}
		writeStatus(w, http.StatusOK, pageJSON)
	})
	if _, err := h.client.Query(context.Background(), "example.com"); err != nil {
		t.Fatal(err)
	}
	if got := h.sleeps(); len(got) != 1 || got[0] != 3*time.Second {
		t.Errorf("waited %v, want [3s]", got)
	}
	if h.hits.Load() != 2 {
		t.Errorf("%d requests, want 2", h.hits.Load())
	}
}

// A 429 that says to come back in a long while stops later calls too, with
// or without a named limit.
func Test429WithLongRetryAfterStopsTheClient(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "600")
		writeStatus(w, http.StatusTooManyRequests, `{"error":"too many requests from your address","code":429}`)
	})
	_, err := h.client.Query(context.Background(), "example.com")
	var limit *RateLimitError
	if !errors.As(err, &limit) || limit.Local {
		t.Fatalf("first call: %v", err)
	}
	if _, err := h.client.Query(context.Background(), "example.com"); !errors.As(err, &limit) || !limit.Local {
		t.Errorf("second call: %v", err)
	}
	if h.hits.Load() != 1 || len(h.sleeps()) != 0 {
		t.Errorf("%d requests, sleeps %v", h.hits.Load(), h.sleeps())
	}
}
