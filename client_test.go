package activedns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// harness is a fake API and a client whose clock and sleeps are the test's:
// sleeping advances the clock and is recorded instead of taking time.
type harness struct {
	t      *testing.T
	client *Client
	mu     sync.Mutex
	clock  time.Time
	slept  []time.Duration
	hits   atomic.Int64
}

const testApp = "mytool/1.2"

func newHarness(t *testing.T, handler http.HandlerFunc, opts ...Option) *harness {
	t.Helper()
	h := &harness{t: t, clock: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.hits.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	client, err := NewClient(testApp, append([]Option{WithToken("test-token"), WithBaseURL(srv.URL)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	client.now = h.now
	client.jitter = func() float64 { return 0 }
	client.sleep = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		h.mu.Lock()
		h.slept = append(h.slept, d)
		h.clock = h.clock.Add(d)
		h.mu.Unlock()
		return nil
	}
	h.client = client
	return h
}

func (h *harness) now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clock
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	h.clock = h.clock.Add(d)
	h.mu.Unlock()
}

func (h *harness) sleeps() []time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]time.Duration(nil), h.slept...)
}

const pageJSON = `{
  "records": [
    {"id": 918273645, "ip_address": "192.0.2.10", "domain": "cdn.example.net",
     "observed": "2026-10-04T22:15:03Z", "first_seen": "2025-03-11T08:40:12Z", "ip_version": 4,
     "asn": 64500, "country": "NO",
     "aliases": [{"name": "www.example.com", "chain": ["www.example.com", "cdn.example.net"]}]},
    {"id": 2, "ip_address": "2001:db8::1", "domain": "example.org",
     "observed": "2026-10-01T00:00:00Z", "first_seen": "2026-10-01T00:00:00Z", "ip_version": 6,
     "known_as": [{"name": "a.example.org", "chain": ["a.example.org", "example.org"]}], "known_as_total": 1000}
  ],
  "count": 4321, "count_estimated": true, "next_cursor": 2, "has_more": true,
  "query": "*.example.com", "query_type": "wildcard_domain"
}`

func writeStatus(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprint(w, body)
}

func TestNewClient(t *testing.T) {
	// the SDK carries a token of its own
	if c, err := NewClient(testApp); err != nil || c.token != embeddedToken || embeddedToken == "" {
		t.Errorf("NewClient without options: %v", err)
	}
	// the program using it has to be named
	for _, app := range []string{"", "  "} {
		if _, err := NewClient(app); !errors.Is(err, ErrNoAppName) {
			t.Errorf("NewClient(%q): %v, want ErrNoAppName", app, err)
		}
	}
	if _, err := NewClient("my\ntool"); err == nil {
		t.Error("NewClient accepted an app name with a line break")
	}
	if _, err := NewClient(testApp, WithToken("")); !errors.Is(err, ErrNoToken) {
		t.Errorf("NewClient with an empty token: %v, want ErrNoToken", err)
	}
	for _, opts := range [][]Option{
		{WithToken("  ")},
		{WithToken("t"), WithBaseURL("activedns.net")},
		{WithToken("t"), WithBaseURL("ftp://activedns.net")},
		{WithToken("t"), WithMaxConcurrent(0)},
		{WithToken("t"), WithMaxRetries(-1)},
	} {
		if _, err := NewClient(testApp, opts...); err == nil {
			t.Errorf("NewClient accepted invalid options")
		}
	}
	c, err := NewClient(testApp, WithToken("t"))
	if err != nil {
		t.Fatal(err)
	}
	if c.baseURL != "https://activedns.net" || cap(c.slots) != 1 {
		t.Errorf("defaults: base %s, concurrency %d", c.baseURL, cap(c.slots))
	}
}

func TestQuerySendsAndDecodes(t *testing.T) {
	var got *http.Request
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		writeStatus(w, http.StatusOK, pageJSON)
	})

	page, err := h.client.Query(context.Background(), "*.example.com", WithPageSize(50), WithCursor(100))
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodGet || got.URL.Path != "/api/v2/query" {
		t.Errorf("request: %s %s", got.Method, got.URL.Path)
	}
	if q := got.URL.Query(); q.Get("q") != "*.example.com" || q.Get("limit") != "50" || q.Get("cursor") != "100" {
		t.Errorf("query string: %s", got.URL.RawQuery)
	}
	if auth := got.Header.Get("Authorization"); auth != "Bearer test-token" {
		t.Errorf("Authorization: %q", auth)
	}
	ua := got.Header.Get("User-Agent")
	if !strings.HasPrefix(ua, "mytool/1.2 activedns-go/"+Version+" (go") {
		t.Errorf("User-Agent: %q", ua)
	}

	if len(page.Records) != 2 || page.Count != 4321 || !page.CountEstimated || page.NextCursor != 2 || !page.HasMore ||
		page.QueryType != "wildcard_domain" || page.Query != "*.example.com" {
		t.Errorf("page: %+v", page)
	}
	first := page.Records[0]
	if first.ID != 918273645 || first.IPAddress != "192.0.2.10" || first.Domain != "cdn.example.net" || first.ASN != 64500 ||
		first.Country != "NO" || first.IPVersion != 4 || !first.Observed.Equal(time.Date(2026, 10, 4, 22, 15, 3, 0, time.UTC)) ||
		!first.FirstSeen.Equal(time.Date(2025, 3, 11, 8, 40, 12, 0, time.UTC)) ||
		len(first.Aliases) != 1 || first.Aliases[0].Name != "www.example.com" || len(first.Aliases[0].Chain) != 2 {
		t.Errorf("first record: %+v", first)
	}
	second := page.Records[1]
	if second.ASN != 0 || second.Country != "" || second.KnownAsTotal != 1000 || len(second.KnownAs) != 1 {
		t.Errorf("second record: %+v", second)
	}
}

func TestQueryCombined(t *testing.T) {
	var raw string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		raw = r.URL.RawQuery
		writeStatus(w, http.StatusOK, pageJSON)
	})
	if _, err := h.client.QueryCombined(context.Background(), Combined{Domain: "*.example.com", ASN: 13335}); err != nil {
		t.Fatal(err)
	}
	if raw != "asn=AS13335&domain=%2A.example.com" {
		t.Errorf("query string: %s", raw)
	}
	if _, err := h.client.QueryCombined(context.Background(), Combined{}); err == nil {
		t.Error("an empty combined search was accepted")
	}
	if h.hits.Load() != 1 {
		t.Errorf("%d requests, want 1", h.hits.Load())
	}
}

// pages serves a search of n records in pages, like the API.
func pages(n int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var cursor, limit int
		fmt.Sscan(r.URL.Query().Get("cursor"), &cursor)
		fmt.Sscan(r.URL.Query().Get("limit"), &limit)
		if limit == 0 {
			limit = 100
		}
		page := Page{Records: []Record{}, Count: n}
		for i := cursor; i < n && i < cursor+limit; i++ {
			page.Records = append(page.Records, Record{ID: int64(i), Domain: fmt.Sprintf("host%d.example.com", i)})
		}
		page.NextCursor = cursor + len(page.Records)
		page.HasMore = page.NextCursor < n
		json.NewEncoder(w).Encode(page)
	}
}

func TestNextContinuesTheQuery(t *testing.T) {
	h := newHarness(t, pages(25))
	page, err := h.client.Query(context.Background(), "*.example.com", WithPageSize(10))
	if err != nil {
		t.Fatal(err)
	}
	if page.Count != 25 || !page.HasMore || len(page.Records) != 10 {
		t.Fatalf("first page: %d records of %d, more %v", len(page.Records), page.Count, page.HasMore)
	}
	if h.hits.Load() != 1 {
		t.Fatalf("%d requests for one page; nothing else is fetched unasked", h.hits.Load())
	}

	var ids []int64
	for {
		for _, record := range page.Records {
			ids = append(ids, record.ID)
		}
		next, err := h.client.Next(context.Background(), page)
		if errors.Is(err, ErrNoMore) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		page = next
	}
	if len(ids) != 25 || ids[0] != 0 || ids[24] != 24 {
		t.Errorf("records: %v", ids)
	}
	if h.hits.Load() != 3 || page.HasMore {
		t.Errorf("%d requests, want 3 pages of 10", h.hits.Load())
	}
}

// Next keeps the page size and the parts of a combined query.
func TestNextRepeatsTheRequest(t *testing.T) {
	var queries []string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		writeStatus(w, http.StatusOK, pageJSON)
	})
	page, err := h.client.QueryCombined(context.Background(), Combined{Domain: "*.example.com", ASN: 13335}, WithPageSize(2))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.Next(context.Background(), page); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 || queries[1] != "asn=AS13335&cursor=2&domain=%2A.example.com&limit=2" {
		t.Errorf("requests: %q", queries)
	}
	if queries[0] != "asn=AS13335&domain=%2A.example.com&limit=2" {
		t.Errorf("Next changed the first page's request: %q", queries[0])
	}
}

func TestNextNeedsAPageOfThisClient(t *testing.T) {
	h := newHarness(t, pages(5))
	if _, err := h.client.Next(context.Background(), nil); err == nil || errors.Is(err, ErrNoMore) {
		t.Errorf("Next(nil): %v", err)
	}
	if _, err := h.client.Next(context.Background(), &Page{HasMore: true}); err == nil || errors.Is(err, ErrNoMore) {
		t.Errorf("Next of a made-up page: %v", err)
	}
	if h.hits.Load() != 0 {
		t.Errorf("%d requests, want none", h.hits.Load())
	}
}

// Past the depth the token may page to, Next says so.
func TestNextBeyondTheTokensDepth(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") != "" {
			writeStatus(w, http.StatusForbidden, `{"error":"cursor beyond your token's limit of 100; narrow the query instead","code":403}`)
			return
		}
		writeStatus(w, http.StatusOK, pageJSON)
	})
	page, err := h.client.Query(context.Background(), "*.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.Next(context.Background(), page); !IsForbidden(err) {
		t.Errorf("Next: %v, want the API's 403", err)
	}
}

// A timed-out page is handed to the caller, not retried.
func TestTimedOutSearchIsNotRetried(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		writeStatus(w, http.StatusOK, `{"records":[],"count":0,"next_cursor":0,"has_more":false,"timed_out":true,"message":"The search query timed out"}`)
	})
	page, err := h.client.Query(context.Background(), "*.example.com")
	if err != nil || !page.TimedOut || h.hits.Load() != 1 {
		t.Errorf("page %+v, error %v, requests %d", page, err, h.hits.Load())
	}
}

func TestErrorsThatRetryingCannotFixAreNotRetried(t *testing.T) {
	for _, status := range []int{400, 404, 405, 500} {
		h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
			writeStatus(w, status, `{"error":"no","code":0}`)
		})
		_, err := h.client.Query(context.Background(), "example.com")
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != status || apiErr.Message != "no" {
			t.Errorf("%d: error %v", status, err)
		}
		if h.hits.Load() != 1 || len(h.sleeps()) != 0 {
			t.Errorf("%d: %d requests, sleeps %v; want one request and no waiting", status, h.hits.Load(), h.sleeps())
		}
	}
}

// 502/503/504 are retried with exponential backoff, then given up on.
func TestBackoffOnServerTrouble(t *testing.T) {
	failures := 3
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if failures > 0 {
			failures--
			writeStatus(w, http.StatusServiceUnavailable, `{"error":"token service unavailable, try again shortly","code":503}`)
			return
		}
		writeStatus(w, http.StatusOK, pageJSON)
	})
	if _, err := h.client.Query(context.Background(), "example.com"); err != nil {
		t.Fatal(err)
	}
	// jitter is 0 in the harness: the lower bound of 1 s, 2 s, 4 s
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second}
	if got := h.sleeps(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("waited %v, want %v", got, want)
	}

	h = newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway) // a proxy's bare 502
	})
	_, err := h.client.Query(context.Background(), "example.com")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 502 || apiErr.Message != "Bad Gateway" {
		t.Errorf("error %v, want the 502", err)
	}
	if h.hits.Load() != 5 {
		t.Errorf("%d requests, want 1 + 4 retries", h.hits.Load())
	}
}

func TestBackoffIsJitteredAndCapped(t *testing.T) {
	c, err := NewClient(testApp, WithToken("t"))
	if err != nil {
		t.Fatal(err)
	}
	for attempt, upper := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second} {
		c.jitter = func() float64 { return 0 }
		if got := c.backoff(attempt); got != upper/2 {
			t.Errorf("backoff(%d) with no jitter = %s, want %s", attempt, got, upper/2)
		}
		c.jitter = func() float64 { return 0.999999 }
		if got := c.backoff(attempt); got <= upper/2 || got > upper {
			t.Errorf("backoff(%d) with full jitter = %s, want just under %s", attempt, got, upper)
		}
	}
	if got := c.backoff(1000); got > 30*time.Second {
		t.Errorf("backoff(1000) = %s", got)
	}
}

// A short Retry-After is waited out and the request is sent again.
func TestRetryAfterIsHonoured(t *testing.T) {
	first := true
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if first {
			first = false
			w.Header().Set("Retry-After", "7")
			writeStatus(w, http.StatusTooManyRequests, `{"error":"too many concurrent requests: your policy allows 1","code":429}`)
			return
		}
		writeStatus(w, http.StatusOK, pageJSON)
	})
	if _, err := h.client.Query(context.Background(), "example.com"); err != nil {
		t.Fatal(err)
	}
	if got := h.sleeps(); len(got) != 1 || got[0] != 7*time.Second {
		t.Errorf("waited %v, want [7s]", got)
	}
}

// A limit that resets far away is not slept through: the caller is told
// when to come back, and until then nothing more is sent.
func TestDistantLimitStopsTheClient(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1800")
		writeStatus(w, http.StatusTooManyRequests, `{"error":"rate limit reached for this token; see Retry-After","code":429,"exceeded":"token"}`)
	})
	start := h.now()
	_, err := h.client.Query(context.Background(), "example.com")
	var limit *RateLimitError
	if !errors.As(err, &limit) {
		t.Fatalf("error %v, want a *RateLimitError", err)
	}
	if limit.Exceeded != "token" || limit.Local || !limit.RetryAt.Equal(start.Add(30*time.Minute)) {
		t.Errorf("limit: %+v", limit)
	}
	if h.hits.Load() != 1 || len(h.sleeps()) != 0 {
		t.Errorf("%d requests, sleeps %v; want one request and no waiting", h.hits.Load(), h.sleeps())
	}

	// further calls do not reach the server
	for i := 0; i < 5; i++ {
		_, err = h.client.Query(context.Background(), "example.org")
		if !errors.As(err, &limit) || !limit.Local || limit.Exceeded != "token" {
			t.Fatalf("call %d while limited: %v", i, err)
		}
	}
	if h.hits.Load() != 1 {
		t.Errorf("%d requests while limited, want 1", h.hits.Load())
	}

	// once it has reset, the client sends again
	h.advance(31 * time.Minute)
	h.client.Query(context.Background(), "example.com")
	if h.hits.Load() != 2 {
		t.Errorf("%d requests after the reset, want 2", h.hits.Load())
	}
}

// The API as it answers: the burst is spent with the first request, the
// second is refused for two seconds, and the client waits them out. What it
// is told names the limit.
func TestTheAPIsRateLimitAnswers(t *testing.T) {
	var requests atomic.Int64
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("RateLimit-Policy", `"address";q=60;w=120, "network";q=300;w=120`)
		if requests.Add(1) == 2 {
			w.Header().Set("RateLimit", `"address";r=0;t=2`)
			w.Header().Set("Retry-After", "2")
			writeStatus(w, http.StatusTooManyRequests, `{"error":"rate limit reached for your address; see Retry-After","code":429,"exceeded":"address"}`)
			return
		}
		w.Header().Set("RateLimit", `"address";r=41;t=2`)
		writeStatus(w, http.StatusOK, pageJSON)
	})
	if _, err := h.client.Query(context.Background(), "example.com"); err != nil {
		t.Fatal(err)
	}
	if limits := h.client.RateLimits(); len(limits) != 1 || limits[0].Name != "address" || limits[0].Limit != 60 || limits[0].Remaining != 41 {
		t.Errorf("rate limits: %+v", limits)
	}
	if _, err := h.client.Query(context.Background(), "example.com"); err != nil {
		t.Fatalf("the refused request was not retried: %v", err)
	}
	if h.hits.Load() != 3 {
		t.Errorf("%d requests, want 3", h.hits.Load())
	}
	var waited time.Duration
	for _, d := range h.sleeps() {
		waited += d
	}
	if waited < 2*time.Second || waited > 4*time.Second {
		t.Errorf("waited %v, want the two seconds asked for (and at most a little more)", waited)
	}
}

// A refused token is not tried again.
func TestRefusedTokenStopsTheClient(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		writeStatus(w, http.StatusUnauthorized, `{"error":"missing or unknown token","code":401}`)
	})
	for i := 0; i < 3; i++ {
		if _, err := h.client.Query(context.Background(), "example.com"); !IsUnauthorized(err) {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if h.hits.Load() != 1 {
		t.Errorf("%d requests, want 1", h.hits.Load())
	}
	if IsUnauthorized(&APIError{StatusCode: 403}) || IsUnauthorized(errors.New("x")) {
		t.Error("IsUnauthorized is too generous")
	}
}

// A query the token may not make is refused on its own: the token still
// works for others.
func TestForbiddenQueryDoesNotStopTheClient(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "AS64496" {
			writeStatus(w, http.StatusForbidden, `{"error":"query type asn is not allowed for your token","code":403}`)
			return
		}
		writeStatus(w, http.StatusOK, pageJSON)
	})
	if _, err := h.client.Query(context.Background(), "AS64496"); !IsForbidden(err) || IsUnauthorized(err) {
		t.Fatalf("forbidden query: %v", err)
	}
	if _, err := h.client.Query(context.Background(), "example.com"); err != nil {
		t.Errorf("query after a forbidden one: %v", err)
	}
	if h.hits.Load() != 2 || len(h.sleeps()) != 0 {
		t.Errorf("%d requests, sleeps %v; want two requests and no retries", h.hits.Load(), h.sleeps())
	}
}

// One goroutine being told to slow down slows the others too.
func TestPauseIsSharedByTheClient(t *testing.T) {
	var told atomic.Bool
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "first.example" && told.CompareAndSwap(false, true) {
			w.Header().Set("Retry-After", "10")
			writeStatus(w, http.StatusServiceUnavailable, `{"error":"busy","code":503}`)
			return
		}
		writeStatus(w, http.StatusOK, pageJSON)
	}, WithMaxConcurrent(2), WithMaxRetries(0))

	if _, err := h.client.Query(context.Background(), "first.example"); err == nil {
		t.Fatal("first call succeeded")
	}
	// retries are off, so nothing has slept yet; pause the client by hand as
	// a retrying call would have
	h.client.pause(10 * time.Second)
	if _, err := h.client.Query(context.Background(), "second.example"); err != nil {
		t.Fatal(err)
	}
	if got := h.sleeps(); len(got) != 1 || got[0] != 10*time.Second {
		t.Errorf("the second call waited %v, want [10s]", got)
	}
}

func TestMaxConcurrent(t *testing.T) {
	for _, limit := range []int{1, 3} {
		var inFlight, peak atomic.Int64
		release := make(chan struct{})
		h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
			n := inFlight.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			<-release
			inFlight.Add(-1)
			writeStatus(w, http.StatusOK, pageJSON)
		}, WithMaxConcurrent(limit))

		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := h.client.Query(context.Background(), "example.com"); err != nil {
					t.Error(err)
				}
			}()
		}
		for inFlight.Load() < int64(limit) {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(30 * time.Millisecond)
		close(release)
		wg.Wait()
		if peak.Load() != int64(limit) {
			t.Errorf("limit %d: %d requests in flight at once", limit, peak.Load())
		}
	}
}

func TestContextCancelsWaiting(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		writeStatus(w, http.StatusServiceUnavailable, `{"error":"busy","code":503}`)
	})
	h.client.sleep = sleepContext // really wait
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := h.client.Query(ctx, "example.com")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error %v, want the context's", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("returned after %s", time.Since(start))
	}

	// waiting for a slot also ends with the context
	h = newHarness(t, func(w http.ResponseWriter, r *http.Request) { writeStatus(w, http.StatusOK, pageJSON) })
	h.client.slots <- struct{}{}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := h.client.Query(ctx, "example.com"); !errors.Is(err, context.DeadlineExceeded) || h.hits.Load() != 0 {
		t.Errorf("error %v, %d requests", err, h.hits.Load())
	}
}

// A connection that fails is retried; a request that timed out is not, since
// the search may still be running on the server.
func TestConnectionFailuresAndTimeouts(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close() // nothing listens here any more
	c, err := NewClient(testApp, WithToken("t"), WithBaseURL(addr), WithMaxRetries(2))
	if err != nil {
		t.Fatal(err)
	}
	var slept int
	c.sleep = func(context.Context, time.Duration) error { slept++; return nil }
	if _, err := c.Query(context.Background(), "example.com"); err == nil {
		t.Error("query against a closed port succeeded")
	}
	if slept != 2 {
		t.Errorf("%d retries of a refused connection, want 2", slept)
	}

	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		writeStatus(w, http.StatusOK, pageJSON)
	}, WithHTTPClient(&http.Client{Timeout: 20 * time.Millisecond}))
	if _, err := h.client.Query(context.Background(), "example.com"); err == nil {
		t.Error("slow query succeeded")
	}
	if h.hits.Load() != 1 || len(h.sleeps()) != 0 {
		t.Errorf("%d requests, sleeps %v; a timed-out search must not be repeated", h.hits.Load(), h.sleeps())
	}
}

func TestRetryAfterParsing(t *testing.T) {
	c, err := NewClient(testApp, WithToken("t"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	for header, want := range map[string]time.Duration{
		"":                              0,
		"5":                             5 * time.Second,
		"-3":                            0,
		"soon":                          0,
		"Mon, 05 Oct 2026 12:01:00 GMT": time.Minute,
		"Mon, 05 Oct 2026 11:00:00 GMT": 0,
	} {
		if got := c.retryAfter(header); got != want {
			t.Errorf("retryAfter(%q) = %s, want %s", header, got, want)
		}
	}
}
