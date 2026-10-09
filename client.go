// Package activedns is a client for the ActiveDNS API (https://activedns.net):
// DNS search by domain, address, network, and AS number.
//
//	client, err := activedns.NewClient("mytool/1.0")
//	if err != nil {
//		return err
//	}
//	page, err := client.Query(ctx, "*.example.com")
//	if err != nil {
//		return err
//	}
//	for _, record := range page.Records {
//		fmt.Println(record.Domain, record.IPAddress)
//	}
//
// # Queries
//
// A query is one of:
//
//	example.com      records of a domain
//	*.example.com    records of a domain and of every name under it
//	192.0.2.1        names that resolve to an address
//	192.0.2.0/24     names that resolve into a network
//	AS64496          names that resolve into an AS
//
// [Client.Query] runs a query and [Client.QueryCombined] matches a domain, a
// network and an AS number at once.
//
// # Paging
//
// A query returns one [Page]: up to 100 records, or as many as [WithPageSize]
// asks for. The page says whether that was everything:
//
//   - Page.Count is how many records match in all.
//   - Page.HasMore is true when there are records after this page.
//
// [Client.Next] fetches the page after one. The client never fetches further
// pages by itself: how much of a large result to retrieve is the caller's
// decision.
//
//	page, err := client.Query(ctx, "*.example.com")
//	for err == nil {
//		use(page.Records)
//		if !page.HasMore || enough() {
//			break
//		}
//		page, err = client.Next(ctx, page)
//	}
//
// # Tokens
//
// Requests are authenticated with a token. The SDK has one built in, shared
// by all its users, so no account is needed. It allows domain, wildcard,
// address and network queries, small pages and the first records of a result.
// Its rate limit is counted per client network address.
//
// [WithToken] sets a token issued to you. Such a token adds AS-number and
// combined queries, wider networks, larger pages, deeper paging and a higher
// rate limit that is counted per token, not per address. Request one at
// https://activedns.net/contact/.
//
// A query that the token does not allow fails with a 403; see [IsForbidden].
//
// # Errors
//
//   - [*RateLimitError]: a rate limit is reached and the client did not wait
//     it out.
//   - [*APIError]: any other error status. [IsUnauthorized] and [IsForbidden]
//     classify it.
//   - [ErrNoMore]: returned by [Client.Next] for the last page of a result.
//
// # Retries and rate limits
//
// A [Client] is safe for concurrent use and paces its own requests:
//
//   - One request is in flight at a time ([WithMaxConcurrent]).
//   - 429, 502, 503, 504 and connection failures are retried up to four
//     times ([WithMaxRetries]) with exponential backoff, and no sooner than
//     Retry-After. The pause applies to every goroutine using the client.
//   - A wait longer than 30 seconds ([WithMaxWait]) is not slept through.
//     The call returns a [*RateLimitError], and later calls fail without
//     being sent until its RetryAt.
//   - When a response reports that no requests are left, the next request
//     waits for the limit to reset.
//   - After a 401 no further requests are sent.
//   - Other errors, timed-out requests and timed-out searches are not
//     retried.
package activedns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is the server used unless [WithBaseURL] is given.
const DefaultBaseURL = "https://activedns.net"

const (
	defaultMaxConcurrent = 1
	defaultMaxRetries    = 4
	defaultMaxWait       = 30 * time.Second
	defaultHTTPTimeout   = 60 * time.Second

	backoffBase = time.Second
	backoffCap  = 30 * time.Second

	maxResponseBytes = 64 << 20
)

// Client is an ActiveDNS API client. It is safe for concurrent use.
//
// Use one Client per program: request pacing and rate-limit state are kept
// per client.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
	userAgent  string
	maxRetries int
	maxWait    time.Duration
	slots      chan struct{} // one per request allowed in flight

	mu        sync.Mutex
	notBefore time.Time       // nothing is sent before this (after a "slow down")
	limited   *RateLimitError // a limit is reached: nothing is sent until its RetryAt
	limits    []RateLimit     // from the rate-limit headers of the latest answer
	refused   error           // the token was refused: nothing is sent again

	// replaced in tests
	now    func() time.Time
	sleep  func(ctx context.Context, d time.Duration) error
	jitter func() float64 // in [0, 1)
}

// Option configures a [Client] in [NewClient].
type Option func(*config)

type config struct {
	baseURL       string
	token         string
	httpClient    *http.Client
	maxConcurrent int
	maxRetries    int
	maxWait       time.Duration
}

// WithToken sets the API token, replacing the one built into the SDK. See the
// package documentation for what a token of your own allows.
func WithToken(token string) Option { return func(c *config) { c.token = token } }

// WithBaseURL sets the server, as scheme and host: "http://localhost:8080".
// The default is [DefaultBaseURL].
func WithBaseURL(baseURL string) Option { return func(c *config) { c.baseURL = baseURL } }

// WithHTTPClient sets the HTTP client. The default has a 60-second timeout.
func WithHTTPClient(hc *http.Client) Option { return func(c *config) { c.httpClient = hc } }

// WithMaxConcurrent sets how many requests may be in flight at once. The
// default is 1. It does not raise the server's rate limit.
func WithMaxConcurrent(n int) Option { return func(c *config) { c.maxConcurrent = n } }

// WithMaxRetries sets how many times a request is retried after a 429, 502,
// 503, 504 or connection failure. The default is 4; 0 disables retries.
func WithMaxRetries(n int) Option { return func(c *config) { c.maxRetries = n } }

// WithMaxWait sets the longest pause the client sleeps through before a
// retry. The default is 30 seconds. When the server asks for a longer wait,
// the call returns a [*RateLimitError] instead.
func WithMaxWait(d time.Duration) Option { return func(c *config) { c.maxWait = d } }

// NewClient returns a [Client].
//
// app names the calling program and its version, such as "mytool/1.2". It is
// sent in the User-Agent of every request and must be printable ASCII.
//
// NewClient returns [ErrNoAppName] if app is empty, [ErrNoToken] if there is
// no token to use, and an error if an option is invalid.
func NewClient(app string, opts ...Option) (*Client, error) {
	app = strings.TrimSpace(app)
	if app == "" {
		return nil, ErrNoAppName
	}
	for _, c := range app {
		if c < ' ' || c > '~' {
			return nil, fmt.Errorf("activedns: app name %q: only printable ASCII fits in a User-Agent", app)
		}
	}
	cfg := config{
		baseURL:       DefaultBaseURL,
		token:         embeddedToken,
		maxConcurrent: defaultMaxConcurrent,
		maxRetries:    defaultMaxRetries,
		maxWait:       defaultMaxWait,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	cfg.token = strings.TrimSpace(cfg.token)
	if cfg.token == "" {
		return nil, ErrNoToken
	}
	base, err := url.Parse(cfg.baseURL)
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") {
		return nil, fmt.Errorf("activedns: invalid base URL %q", cfg.baseURL)
	}
	if cfg.maxConcurrent < 1 {
		return nil, fmt.Errorf("activedns: max concurrent must be at least 1, not %d", cfg.maxConcurrent)
	}
	if cfg.maxRetries < 0 || cfg.maxWait < 0 {
		return nil, errors.New("activedns: max retries and max wait must not be negative")
	}
	if cfg.httpClient == nil {
		cfg.httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	userAgent := fmt.Sprintf("%s activedns-go/%s (%s; %s/%s)", app, Version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	return &Client{
		baseURL:    strings.TrimRight(base.Scheme+"://"+base.Host+base.Path, "/"),
		token:      cfg.token,
		httpClient: cfg.httpClient,
		userAgent:  userAgent,
		maxRetries: cfg.maxRetries,
		maxWait:    cfg.maxWait,
		slots:      make(chan struct{}, cfg.maxConcurrent),
		now:        time.Now,
		sleep:      sleepContext,
		jitter:     rand.Float64,
	}, nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// QueryOption adjusts a single query.
type QueryOption func(url.Values)

// WithPageSize sets the most records returned per page. The default is 100;
// the token's policy caps it, without an error: a larger request returns the
// token's maximum.
func WithPageSize(n int) QueryOption {
	return func(v url.Values) { v.Set("limit", strconv.Itoa(n)) }
}

// WithCursor sets the offset of the first record returned. To continue from a
// [Page], [Client.Next] is simpler.
func WithCursor(n int) QueryOption {
	return func(v url.Values) { v.Set("cursor", strconv.Itoa(n)) }
}

// Query runs a query and returns one page of results. The package
// documentation lists the forms a query can take.
func (c *Client) Query(ctx context.Context, query string, opts ...QueryOption) (*Page, error) {
	return c.search(ctx, url.Values{"q": {query}}, opts)
}

// Combined is a query for records that match every field that is set.
type Combined struct {
	Domain string // a domain, or a leading wildcard such as "*.example.com"
	IP     string // an address, or a network in CIDR notation
	ASN    int    // an AS number; 0 for none
}

// QueryCombined runs a combined query and returns one page of results. At
// least one field of q must be set. The token must allow combined queries.
func (c *Client) QueryCombined(ctx context.Context, q Combined, opts ...QueryOption) (*Page, error) {
	v := url.Values{}
	if q.Domain != "" {
		v.Set("domain", q.Domain)
	}
	if q.IP != "" {
		v.Set("ip", q.IP)
	}
	if q.ASN != 0 {
		v.Set("asn", "AS"+strconv.Itoa(q.ASN))
	}
	if len(v) == 0 {
		return nil, errors.New("activedns: a combined search needs at least one of Domain, IP and ASN")
	}
	return c.search(ctx, v, opts)
}

func (c *Client) search(ctx context.Context, v url.Values, opts []QueryOption) (*Page, error) {
	for _, opt := range opts {
		opt(v)
	}
	var page Page
	if err := c.get(ctx, "/api/v2/query", v, &page); err != nil {
		return nil, err
	}
	page.request = v
	return &page, nil
}

// Next returns the page after page: the same query, continued where page
// ended. It returns [ErrNoMore] when page is the last one (Page.HasMore is
// false), and fails with a 403 ([IsForbidden]) past the depth the token may
// page to.
func (c *Client) Next(ctx context.Context, page *Page) (*Page, error) {
	if page == nil || page.request == nil {
		return nil, errors.New("activedns: Next needs a page returned by this package")
	}
	if !page.HasMore {
		return nil, ErrNoMore
	}
	v := url.Values{}
	for key, values := range page.request {
		v[key] = values
	}
	v.Set("cursor", strconv.Itoa(page.NextCursor))
	return c.search(ctx, v, nil)
}

// get sends a GET and decodes the answer into out, waiting and retrying as
// the package documentation describes.
func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.slots }()

	for attempt := 0; ; attempt++ {
		if err := c.ready(ctx); err != nil {
			return err
		}
		retryAfter, retryable, err := c.send(ctx, path, query, out)
		if err == nil {
			return nil
		}
		if !retryable {
			return err
		}
		if attempt >= c.maxRetries || retryAfter > c.maxWait {
			c.giveUp(err)
			return err
		}
		wait := min(c.backoff(attempt), c.maxWait)
		if retryAfter > 0 {
			// up to a second later than asked, so that the clients sharing a
			// limit do not all come back in the same instant
			wait = max(wait, retryAfter+time.Duration(c.jitter()*float64(time.Second)))
		}
		c.pause(wait)
	}
}

// ready blocks until the client may send, or says why it will not.
func (c *Client) ready(ctx context.Context) error {
	c.mu.Lock()
	now := c.now()
	if c.refused != nil {
		c.mu.Unlock()
		return c.refused
	}
	if c.limited != nil {
		if c.limited.RetryAt.IsZero() || now.Before(c.limited.RetryAt) {
			local := *c.limited
			local.Local = true
			c.mu.Unlock()
			return &local
		}
		c.limited = nil
	}
	wait := c.notBefore.Sub(now)
	c.mu.Unlock()
	if wait > 0 {
		return c.sleep(ctx, wait)
	}
	return nil
}

// pause holds every request of the client back for d.
func (c *Client) pause(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if until := c.now().Add(d); until.After(c.notBefore) {
		c.notBefore = until
	}
}

// giveUp is called when a retryable error is returned to the caller. If the
// server said when it will accept requests again, the client keeps to that
// for later calls too.
func (c *Client) giveUp(err error) {
	var limit *RateLimitError
	if errors.As(err, &limit) && !limit.RetryAt.IsZero() {
		c.holdUntil(limit.RetryAt, limit)
	}
}

// holdUntil keeps the client from sending before t: by making requests wait
// when that is at most the longest wait it sleeps through, and otherwise by
// failing them at once with limit.
func (c *Client) holdUntil(t time.Time, limit *RateLimitError) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t.Sub(c.now()) > c.maxWait {
		c.limited = limit
		return
	}
	if t.After(c.notBefore) {
		c.notBefore = t
	}
}

// noteRateLimits remembers the quotas of the latest answer and, when one of
// them has nothing left, holds the client back until it resets, so that the
// request that would have been refused is not sent.
func (c *Client) noteRateLimits(limits []RateLimit) {
	if len(limits) == 0 {
		return
	}
	c.mu.Lock()
	c.limits = limits
	now := c.now()
	c.mu.Unlock()
	if until, name := exhaustedUntil(limits, now); !until.IsZero() {
		c.holdUntil(until, &RateLimitError{Exceeded: name, RetryAt: until, Message: "rate limit reached"})
	}
}

// RateLimits returns the rate limits reported by the most recent response, or
// nil if it reported none.
func (c *Client) RateLimits() []RateLimit {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]RateLimit(nil), c.limits...)
}

// backoff is the pause before retry number attempt+1: 1 s, 2 s, 4 s, … up
// to 30 s, with the upper half randomised so clients do not retry in step.
func (c *Client) backoff(attempt int) time.Duration {
	d := backoffCap
	if attempt < 5 {
		d = min(backoffBase<<attempt, backoffCap)
	}
	return d/2 + time.Duration(c.jitter()*float64(d/2))
}

// errorBody is what the API sends with an error status.
type errorBody struct {
	Error    string `json:"error"`
	Exceeded string `json:"exceeded"`
}

// send makes one attempt. For a failed attempt it reports whether trying
// again can help and how long the server asked to wait first.
func (c *Client) send(ctx context.Context, path string, query url.Values, out any) (retryAfter time.Duration, retryable bool, err error) {
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, false, fmt.Errorf("activedns: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, false, ctx.Err()
		}
		// A request that timed out was probably an expensive search still
		// running on the server: sending it again would double the load.
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return 0, false, fmt.Errorf("activedns: %w", err)
		}
		return 0, true, fmt.Errorf("activedns: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		if ctx.Err() != nil {
			return 0, false, ctx.Err()
		}
		return 0, false, fmt.Errorf("activedns: reading the response: %w", err)
	}

	limits := parseRateLimits(resp.Header, c.now())
	c.noteRateLimits(limits)

	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, out); err != nil {
			return 0, false, fmt.Errorf("activedns: decoding the response: %w", err)
		}
		return 0, false, nil
	}

	var problem errorBody
	if json.Unmarshal(body, &problem) != nil || problem.Error == "" {
		problem.Error = http.StatusText(resp.StatusCode)
	}
	retryAfter = c.retryAfter(resp.Header.Get("Retry-After"))
	if retryAfter == 0 && resp.StatusCode == http.StatusTooManyRequests {
		// no Retry-After: the reset of the quota that ran out says the same
		if until, _ := exhaustedUntil(limits, c.now()); !until.IsZero() {
			retryAfter = until.Sub(c.now())
		}
	}
	apiErr := &APIError{StatusCode: resp.StatusCode, Message: problem.Error}

	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		limit := &RateLimitError{Exceeded: problem.Exceeded, Message: problem.Error}
		if retryAfter > 0 {
			limit.RetryAt = c.now().Add(retryAfter)
		}
		return retryAfter, true, limit
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return retryAfter, true, apiErr
	case http.StatusUnauthorized:
		c.mu.Lock()
		c.refused = apiErr
		c.mu.Unlock()
		return 0, false, apiErr
	}
	return 0, false, apiErr
}

// retryAfter reads a Retry-After header: seconds, or an HTTP date.
func (c *Client) retryAfter(header string) time.Duration {
	if header == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(header); err == nil {
		return max(time.Duration(seconds)*time.Second, 0)
	}
	if at, err := http.ParseTime(header); err == nil {
		return max(at.Sub(c.now()), 0)
	}
	return 0
}
