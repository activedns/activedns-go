// Subdomains lists the names known under a domain, with the addresses each
// one has resolved to.
//
//	go run ./example/subdomains example.com
//	go run ./example/subdomains -pages 10 example.com
//
// A query returns one page. The program decides how many more to fetch
// (-pages), and says what it left behind.
//
// It uses the token built into the SDK; set ACTIVEDNS_TOKEN to use your own.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	activedns "github.com/activedns/activedns-go"
)

func main() {
	maxPages := flag.Int("pages", 3, "how many pages to fetch at most")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: subdomains [-pages n] <domain>")
		os.Exit(2)
	}
	domain := flag.Arg(0)

	var opts []activedns.Option
	if token := os.Getenv("ACTIVEDNS_TOKEN"); token != "" {
		opts = append(opts, activedns.WithToken(token))
	}
	// the name tells the operators of the API which program is asking
	client, err := activedns.NewClient("example-subdomains/1.0", opts...)
	if err != nil {
		log.Fatal(err)
	}

	// Ctrl-C stops between pages
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	addresses := map[string][]string{}
	note := func(name, address string) {
		for _, known := range addresses[name] {
			if known == address {
				return
			}
		}
		addresses[name] = append(addresses[name], address)
	}

	fetched := 0
	page, err := client.Query(ctx, "*."+domain)
	for pages := 1; err == nil; pages++ {
		for _, record := range page.Records {
			note(record.Domain, record.IPAddress)
			// names that reach the record's owner through a CNAME
			for _, alias := range record.Aliases {
				note(alias.Name, record.IPAddress)
			}
		}
		fetched += len(page.Records)

		// The page says whether there is more. Fetching it is a decision:
		// every page is a request against the rate limit.
		if !page.HasMore {
			break
		}
		if pages == *maxPages {
			fmt.Fprintf(os.Stderr, "stopped at page %d: %d of %d records fetched; -pages fetches more\n", pages, fetched, page.Count)
			break
		}
		page, err = client.Next(ctx, page)
	}
	if err != nil {
		explain(err)
	}

	names := make([]string, 0, len(addresses))
	for name := range addresses {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Printf("%-50s %s\n", name, strings.Join(addresses[name], " "))
	}
	fmt.Fprintf(os.Stderr, "%d names under %s\n", len(names), domain)
}

// explain says why the listing stopped early. What was found so far is
// still printed.
func explain(err error) {
	var limit *activedns.RateLimitError
	switch {
	case errors.As(err, &limit):
		// the client has already waited and retried; this wait was too long
		fmt.Fprintf(os.Stderr, "rate limit %q reached; try again at %s\n", limit.Exceeded, limit.RetryAt.Format(time.TimeOnly))
	case activedns.IsForbidden(err):
		// most often: the token may not page any deeper into the answer
		fmt.Fprintf(os.Stderr, "stopped by the token's policy: %v\n", err)
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(os.Stderr, "interrupted")
	default:
		fmt.Fprintln(os.Stderr, err)
	}
}
