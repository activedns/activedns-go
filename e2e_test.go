//go:build e2e

// End-to-end tests: the client against the live API, with the token built
// into the SDK. They make about ten requests.
//
//	go test -tags e2e -run E2E -count=1 .
package activedns_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	activedns "github.com/activedns/activedns-go"
)

// e2eClient returns a client with the built-in token, and a context that
// bounds the test.
func e2eClient(t *testing.T, opts ...activedns.Option) (*activedns.Client, context.Context) {
	t.Helper()
	// the name tells the operators of the API that these are the SDK's tests
	client, err := activedns.NewClient("activedns-go-e2e/1.0", opts...)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return client, ctx
}

// checkRecords fails the test for a record that cannot be right.
func checkRecords(t *testing.T, page *activedns.Page) {
	t.Helper()
	if len(page.Records) == 0 {
		t.Fatalf("%s: no records", page.Query)
	}
	if page.Count < len(page.Records) {
		t.Errorf("%s: count %d is less than the %d records of the page", page.Query, page.Count, len(page.Records))
	}
	for _, r := range page.Records {
		if r.ID == 0 || r.Domain == "" || r.IPAddress == "" {
			t.Errorf("incomplete record: %+v", r)
		}
		version := 4
		if strings.Contains(r.IPAddress, ":") {
			version = 6
		}
		if r.IPVersion != version {
			t.Errorf("%s: IP version %d", r.IPAddress, r.IPVersion)
		}
		if r.FirstSeen.Year() < 2020 || r.Observed.Before(r.FirstSeen) || r.Observed.After(time.Now().Add(time.Hour)) {
			t.Errorf("%s: first seen %s, observed %s", r.Domain, r.FirstSeen, r.Observed)
		}
	}
}

func TestE2EDomain(t *testing.T) {
	client, ctx := e2eClient(t)
	page, err := client.Query(ctx, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if page.QueryType != "domain" || page.Query != "example.com" {
		t.Errorf("query %q of type %q", page.Query, page.QueryType)
	}
	checkRecords(t, page)
	for _, r := range page.Records {
		if r.Domain != "example.com" {
			t.Errorf("record of %s in a search for example.com", r.Domain)
		}
	}

	// the server reports the limit of the built-in token: per address
	var found bool
	for _, limit := range client.RateLimits() {
		if limit.Name == "address" {
			found = true
			if limit.Limit <= 0 || limit.Remaining < 0 || limit.Remaining > limit.Limit {
				t.Errorf("rate limit: %+v", limit)
			}
		}
	}
	if !found {
		t.Errorf(`no "address" rate limit in %+v`, client.RateLimits())
	}
}

func TestE2EPaging(t *testing.T) {
	client, ctx := e2eClient(t)
	first, err := client.Query(ctx, "*.example.com", activedns.WithPageSize(1))
	if err != nil {
		t.Fatal(err)
	}
	checkRecords(t, first)
	if first.QueryType != "wildcard_domain" || len(first.Records) != 1 || !first.HasMore || first.NextCursor != 1 {
		t.Fatalf("first page: type %q, %d records, more %v, next cursor %d",
			first.QueryType, len(first.Records), first.HasMore, first.NextCursor)
	}

	second, err := client.Next(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	checkRecords(t, second)
	if len(second.Records) != 1 || second.NextCursor != 2 || second.Records[0].ID == first.Records[0].ID {
		t.Errorf("second page: %d records, next cursor %d, first record %d after %d",
			len(second.Records), second.NextCursor, second.Records[0].ID, first.Records[0].ID)
	}
}

func TestE2EAddressAndNetwork(t *testing.T) {
	client, ctx := e2eClient(t)
	page, err := client.Query(ctx, "1.1.1.1", activedns.WithPageSize(2))
	if err != nil {
		t.Fatal(err)
	}
	checkRecords(t, page)
	if page.QueryType != "ip" || len(page.Records) != 2 || !page.HasMore {
		t.Errorf("address: type %q, %d records, more %v", page.QueryType, len(page.Records), page.HasMore)
	}
	for _, r := range page.Records {
		if r.IPAddress != "1.1.1.1" {
			t.Errorf("record of %s in a search for 1.1.1.1", r.IPAddress)
		}
	}

	page, err = client.Query(ctx, "1.1.1.0/24", activedns.WithPageSize(2))
	if err != nil {
		t.Fatal(err)
	}
	checkRecords(t, page)
	if page.QueryType != "cidr" {
		t.Errorf("network: type %q", page.QueryType)
	}
	for _, r := range page.Records {
		if !strings.HasPrefix(r.IPAddress, "1.1.1.") {
			t.Errorf("record of %s in a search for 1.1.1.0/24", r.IPAddress)
		}
	}
}

// What the built-in token may not do is refused with a 403, and the client
// goes on working.
func TestE2EForbidden(t *testing.T) {
	client, ctx := e2eClient(t)
	refused := map[string]func() error{
		"AS number": func() error {
			_, err := client.Query(ctx, "AS13335")
			return err
		},
		"combined": func() error {
			_, err := client.QueryCombined(ctx, activedns.Combined{Domain: "*.example.com", ASN: 13335})
			return err
		},
		"wide network": func() error {
			_, err := client.Query(ctx, "1.0.0.0/8")
			return err
		},
		"deep cursor": func() error {
			_, err := client.Query(ctx, "*.example.com", activedns.WithCursor(100000))
			return err
		},
	}
	for name, query := range refused {
		if err := query(); !activedns.IsForbidden(err) {
			t.Errorf("%s: want a 403, got %v", name, err)
		}
	}
	if _, err := client.Query(ctx, "example.com"); err != nil {
		t.Errorf("after the refusals: %v", err)
	}
}

func TestE2EBadQuery(t *testing.T) {
	client, ctx := e2eClient(t)
	_, err := client.Query(ctx, "not a query")
	var apiErr *activedns.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 || apiErr.Message == "" {
		t.Errorf("want a 400 with a message, got %v", err)
	}
}

func TestE2EUnknownToken(t *testing.T) {
	client, ctx := e2eClient(t, activedns.WithToken("00000000-0000-4000-8000-000000000000"))
	if _, err := client.Query(ctx, "example.com"); !activedns.IsUnauthorized(err) {
		t.Errorf("want a 401, got %v", err)
	}
}
