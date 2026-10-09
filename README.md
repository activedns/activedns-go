# activedns-go

Go client for the [ActiveDNS](https://activedns.net) API: Query the DNS database by name, address, network and AS
number. No dependencies outside the standard library; Go 1.23 or later.

```
go get github.com/activedns/activedns-go
```

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/activedns/activedns-go"
)

func main() {
	client, err := activedns.NewClient("mytool/1.0")
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	page, err := client.Query(ctx, "*.example.com")
	if err != nil {
		log.Fatal(err)
	}
	for _, record := range page.Records {
		fmt.Println(record.Domain, record.IPAddress, record.Observed)
	}
}
```

A query is a domain (`example.com`), a wildcard (`*.example.com`), an address, a network (`192.0.2.0/24`) or an
AS number (`AS13335`). `QueryCombined` matches several at once.

`Query` returns one page of the result and tells you whether there is more; see [Paging](#paging).

It works without an account or a key. AS-number and combined searches, wider networks, complete answers and
higher rates come with [a token of your own](#custom-token).

The client talks to `/api/v2/query`. The API has no costs or budgets, only rate limits.

## Paging

A query returns one page: up to 100 records, or as many as `WithPageSize` asks for. The page tells you whether
that was everything:

| field | meaning |
|---|---|
| `page.Count` | how many records match in all (`CountEstimated`: an estimate; `CountCapped`: at least that many) |
| `page.HasMore` | there are records after this page |
| `len(page.Records)` | how many you got |

The client never fetches further pages by itself. Each one is a request against your rate limit, so how much of
a large result to retrieve is your decision, made with `Next`:

```go
page, err := client.Query(ctx, "*.example.com")
for fetched := 0; err == nil; {
	use(page.Records)
	fetched += len(page.Records)
	if !page.HasMore || fetched >= 500 { // enough for this job
		break
	}
	page, err = client.Next(ctx, page)
}
```

`Next` repeats the query from where the page ended. On the last page it returns `ErrNoMore`. Past the depth
your token may page to, it fails with a 403 (`IsForbidden`).

## Limits in SDK default access
activedns-go library does not require a token and provide modest capabilities, but uses a public rate-limiting policy

To provide free API access, the client has and limits to what you can query.

* Limited CIDR range
* Limited number of records per request
* Limited amount of pages
* Limited request rate
* Standard prioritization

## Custom token

activedns-go library does not require a token and provide modest capabilities, but uses a public rate-limiting policy
have and limits to what you can query.

To use a private token, the client is initialized as follows:

```go
client, err := activedns.NewClient("mytool/1.0", activedns.WithToken(os.Getenv("ACTIVEDNS_TOKEN")))
```

### Supported queries using custom token

#### Query ASN

```go
page, err := client.Query(ctx, "AS64496")
```

### Combined queries

**Ask precise questions.** What does this company host at that provider? Which names under a domain sit in
one particular network? A combined search answers in one request what would otherwise be thousands of records
to download and filter yourself:

```go
page, err := client.QueryCombined(ctx, activedns.Combined{Domain: "*.example.com", ASN: 64496})
page, err = client.QueryCombined(ctx, activedns.Combined{Domain: "*.example.com", IP: "192.0.2.0/24"})
```

### Broader CIDR ranges

**Sweep whole networks.** A /16 in one search instead of 256 separate /24s:

```go
page, err := client.Query(ctx, "198.51.0.0/16")
```

**Get the whole answer.** The built-in token shows the first 200 records of a search, which is enough to look
around. Large zones, hosting ranges and CDNs have tens of thousands; with your own token `Next` keeps going:

```go
// every name under a large zone, not only the first 200 records
page, err := client.Query(ctx, "*.example.com")
for err == nil {
	...
	page, err = client.Next(ctx, page)
}
if !errors.Is(err, activedns.ErrNoMore) {
	log.Fatal(err)
}
```

With the built-in token the same loop ends after 200 records with
`403: cursor beyond your token's limit of 100`.

**Run it where your work runs.** The built-in token's allowance belongs to a network address, so an office, a
cloud region or a CI provider's address range shares it with whoever else is there. Your own token's allowance
is yours wherever it is used: pipelines, scheduled jobs and several machines at once. Keep the token out of the
code and hand it to the job as a secret:

## Say who you are

`NewClient` takes the name and version of your program. 

```
User-Agent: mytool/1.0 activedns-go/0.2.0 (go1.25.4; linux/amd64)
```

Everyone using the SDK's token looks the same to the API otherwise. With a name, a tool that misbehaves can be
told apart from the rest, and its author asked about it instead of everyone being limited.

## Examples

Example programs are provided in `example/`, each one file that runs as it is:

```
go run ./example/query example.com                 one page of a search, and the rate limit
go run ./example/query -limit 10 192.0.2.0/24
go run ./example/subdomains -pages 5 example.com   names under a domain, paging with Next
go run ./example/combined -domain '*.example.com' -asn 64496
```

They use the SDK's own token; set `ACTIVEDNS_TOKEN` to use yours. `combined` needs one, and shows the refusal
without it; `query` with your own token also takes AS numbers (`AS64496`) and wider networks.

## A fair client

The API is shared, so the client holds itself back without being asked:

| situation | what the client does |
|---|---|
| several goroutines query at once | one request at a time (`WithMaxConcurrent` to change) |
| 429, 502, 503, 504, connection failure | up to 4 retries (`WithMaxRetries`), waiting 1 s, 2 s, 4 s, 8 s … (at most 30 s) with jitter, and at least `Retry-After` |
| told to slow down | every goroutine using the client waits, not only the one that was told |
| asked to wait longer than 30 s (`WithMaxWait`) | no sleeping: the call returns a `*RateLimitError` with `RetryAt` |
| a rate-limit header says a quota has nothing left | the next request waits for the reset instead of being sent and refused (or fails at once with a `*RateLimitError` if the reset is more than 30 s away) |
| the token is refused (401) | nothing more is sent; `IsUnauthorized(err)` is true |
| the token may not make that query (403) | returned as it is, never retried; `IsForbidden(err)` is true and other queries go on working |
| bad query, server error, request timed out | returned as it is, never retried |
| the server timed out on a search (`Page.TimedOut`) | the page is returned as it is, not retried |
| a result has more pages | nothing: further pages are fetched only when you call `Next` |

Share one `Client` across a program: these limits are kept per client.

```go
page, err := client.Query(ctx, "*.example.com")
var limit *activedns.RateLimitError
if errors.As(err, &limit) {
	// limit.Exceeded: "address", "network" or "token"
	// limit.RetryAt:  when to try again
}
```

### Rate-limit headers

The client reads the rate-limit headers of every answer. The API sends the first form below: the limit closest
to running out, with `r` requests left right now and one more in `t` seconds. The other usual spellings are
understood as well:

```
RateLimit: "address";r=41;t=2                              IETF httpapi draft
RateLimit-Policy: "address";q=60;w=120, "network";q=300;w=120

RateLimit: limit=100, remaining=50, reset=30               the draft's earlier form
RateLimit-Limit / RateLimit-Remaining / RateLimit-Reset    earlier still
X-RateLimit-Limit / X-RateLimit-Remaining / X-RateLimit-Reset
```

and `Retry-After` (seconds or a date) on 429 and 503. `client.RateLimits()` returns the quotas of the latest
answer.

## Options

| option | default |
|---|---|
| `WithToken(token)` | the SDK's own token |
| `WithMaxConcurrent(n)` | 1 |
| `WithMaxRetries(n)` | 4 |
| `WithMaxWait(d)` | 30 s |
| `WithHTTPClient(hc)` | 60 s timeout |
| `WithBaseURL(url)` | `https://activedns.net` |

## Development

```
go test ./...                             unit tests, against a fake server
go vet ./...
golangci-lint run --build-tags e2e
go test -tags e2e -run E2E -count=1 .     about ten requests to the live API, with the SDK's own token
```

The same checks run in GitHub Actions on every push and pull request (`.github/workflows/ci.yml`): gofmt and
golangci-lint, the unit tests on Go 1.23 and the current release, and then the end-to-end tests.

A release is a tag: set the version in `version.go`, commit, and push the tag `v<version>`.
`.github/workflows/release.yml` runs the checks again, creates the GitHub release and fetches the version with
`go run …@v<version>`, which is also what makes the Go module proxy and pkg.go.dev list it. A published version
cannot be changed afterwards: a mistake is fixed with the next version.
