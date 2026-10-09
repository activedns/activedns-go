package activedns

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RateLimit is a rate limit as the server reported it in the rate-limit
// headers of a response.
type RateLimit struct {
	// Name identifies the limit. The ActiveDNS API uses "address",
	// "network" and "token". Empty if the server gave none.
	Name string
	// Limit is how many requests the limit allows at once, or -1 if not
	// reported.
	Limit int64
	// Remaining is how many requests can be made now.
	Remaining int64
	// Reset is when more requests become available. Zero if not reported.
	Reset time.Time
}

// parseRateLimits reads the rate-limit headers of a response. Servers spell
// them in a few ways, and all the common ones are understood:
//
//	RateLimit: "default";r=50;t=30          (IETF httpapi draft, current form,
//	RateLimit-Policy: "default";q=100;w=60   one member per quota)
//	RateLimit: limit=100, remaining=50, reset=30          (the draft's earlier form)
//	RateLimit-Limit / RateLimit-Remaining / RateLimit-Reset  (earlier still)
//	X-RateLimit-Limit / X-RateLimit-Remaining / X-RateLimit-Reset
//
// Reset is seconds from now, except in the X- form, where a large value is a
// Unix time. The first form present wins.
func parseRateLimits(h http.Header, now time.Time) []RateLimit {
	if values := h.Values("RateLimit"); len(values) > 0 {
		if limits := parseRateLimitField(strings.Join(values, ","), strings.Join(h.Values("RateLimit-Policy"), ","), now); len(limits) > 0 {
			return limits
		}
	}
	for _, prefix := range []string{"RateLimit-", "X-RateLimit-"} {
		remaining, ok := leadingInt(h.Get(prefix + "Remaining"))
		if !ok {
			continue
		}
		limit := RateLimit{Limit: -1, Remaining: remaining}
		if n, ok := leadingInt(h.Get(prefix + "Limit")); ok {
			limit.Limit = n
		}
		if n, ok := leadingInt(h.Get(prefix + "Reset")); ok {
			limit.Reset = resetTime(n, now, prefix == "X-RateLimit-")
		}
		return []RateLimit{limit}
	}
	return nil
}

// parseRateLimitField reads the RateLimit header (and the quota sizes from
// RateLimit-Policy) in either of the draft's forms.
func parseRateLimitField(field, policy string, now time.Time) []RateLimit {
	members := splitList(field)

	// earlier form: one quota as a dictionary, limit=100, remaining=50, reset=30
	if len(members) > 0 && !strings.Contains(members[0], ";") && strings.Contains(members[0], "=") {
		limit := RateLimit{Limit: -1, Remaining: -1}
		for _, member := range members {
			key, value, _ := strings.Cut(member, "=")
			n, ok := leadingInt(value)
			if !ok {
				continue
			}
			switch strings.TrimSpace(key) {
			case "limit":
				limit.Limit = n
			case "remaining":
				limit.Remaining = n
			case "reset":
				limit.Reset = resetTime(n, now, false)
			}
		}
		if limit.Remaining < 0 {
			return nil
		}
		return []RateLimit{limit}
	}

	// current form: a list of quotas, "name";r=<remaining>;t=<seconds to reset>
	sizes := map[string]int64{}
	for _, member := range splitList(policy) {
		name, params := itemParams(member)
		if q, ok := params["q"]; ok {
			sizes[name] = q
		}
	}
	var limits []RateLimit
	for _, member := range members {
		name, params := itemParams(member)
		remaining, ok := params["r"]
		if !ok {
			continue
		}
		limit := RateLimit{Name: name, Limit: -1, Remaining: remaining}
		if q, ok := sizes[name]; ok {
			limit.Limit = q
		}
		if t, ok := params["t"]; ok {
			limit.Reset = resetTime(t, now, false)
		}
		limits = append(limits, limit)
	}
	return limits
}

// resetTime turns a reset value into a time. Seconds from now, unless the
// header may carry a Unix time (the X- form) and the value is too large to
// be anything else; then seconds or milliseconds since 1970.
func resetTime(n int64, now time.Time, mayBeUnix bool) time.Time {
	switch {
	case n < 0:
		return time.Time{}
	case mayBeUnix && n > 1e11:
		return time.UnixMilli(n)
	case mayBeUnix && n > 1e9:
		return time.Unix(n, 0)
	}
	return now.Add(time.Duration(n) * time.Second)
}

// splitList splits a header value on the commas outside quoted strings.
func splitList(s string) []string {
	var out []string
	quoted := false
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			quoted = !quoted
		case ',':
			if !quoted {
				if part := strings.TrimSpace(s[start:i]); part != "" {
					out = append(out, part)
				}
				start = i + 1
			}
		}
	}
	if part := strings.TrimSpace(s[start:]); part != "" {
		out = append(out, part)
	}
	return out
}

// itemParams splits `"name";a=1;b=2` into the name and its integer
// parameters.
func itemParams(member string) (string, map[string]int64) {
	parts := strings.Split(member, ";")
	name := strings.Trim(strings.TrimSpace(parts[0]), `"`)
	params := map[string]int64{}
	for _, part := range parts[1:] {
		key, value, _ := strings.Cut(part, "=")
		if n, ok := leadingInt(value); ok {
			params[strings.TrimSpace(key)] = n
		}
	}
	return name, params
}

// leadingInt reads the integer a value starts with ("100", "100, 100;w=60",
// "12.5").
func leadingInt(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	end := 0
	for end < len(s) && (s[end] >= '0' && s[end] <= '9' || end == 0 && s[end] == '-') {
		end++
	}
	n, err := strconv.ParseInt(s[:end], 10, 64)
	return n, err == nil
}

// exhaustedUntil is the latest reset among the quotas with nothing left: the
// moment before which another request would be refused.
func exhaustedUntil(limits []RateLimit, now time.Time) (until time.Time, name string) {
	for _, limit := range limits {
		if limit.Remaining <= 0 && limit.Reset.After(now) && limit.Reset.After(until) {
			until, name = limit.Reset, limit.Name
		}
	}
	return until, name
}
