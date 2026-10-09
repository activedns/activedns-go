package activedns

import (
	"errors"
	"fmt"
	"time"
)

// ErrNoAppName is returned by [NewClient] when app is empty.
var ErrNoAppName = errors.New(`activedns: app name is required, as in NewClient("mytool/1.0")`)

// ErrNoToken is returned by [NewClient] when there is no token to use: the
// build has none built in, and [WithToken] was not given or was empty.
var ErrNoToken = errors.New("activedns: no token: none is built in and WithToken was not given")

// ErrNoMore is returned by [Client.Next] when the page it is given is the
// last one of the result.
var ErrNoMore = errors.New("activedns: no more pages")

// APIError is an error response from the API.
type APIError struct {
	StatusCode int    // HTTP status
	Message    string // the server's error message
}

func (e *APIError) Error() string {
	return fmt.Sprintf("activedns: %d: %s", e.StatusCode, e.Message)
}

// IsUnauthorized reports whether err is a 401: the token is unknown or
// revoked. After a 401 the [Client] sends no further requests, and every
// later call returns the same error.
func IsUnauthorized(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == 401
}

// IsForbidden reports whether err is a 403: the token does not allow the
// query, because of its type, the width of a network, a wildcard or the
// cursor. Other queries are unaffected.
func IsForbidden(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == 403
}

// RateLimitError is returned when a rate limit is reached and the client did
// not wait it out: the wait was longer than [WithMaxWait] allows, or the
// retries were used up.
type RateLimitError struct {
	// Exceeded names the limit: "address" (the client's network address),
	// "network" (the network around it) or "token". Empty if the server
	// did not say.
	Exceeded string
	// RetryAt is when the limit allows another request. Zero if unknown.
	RetryAt time.Time
	// Message is the server's error message.
	Message string
	// Local is true when the request was not sent, because an earlier
	// response had already reported the limit.
	Local bool
}

func (e *RateLimitError) Error() string {
	msg := "activedns: rate limited"
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if e.RetryAt.IsZero() {
		return msg
	}
	return msg + " (retry at " + e.RetryAt.UTC().Format(time.RFC3339) + ")"
}
