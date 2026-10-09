// Query runs one search and prints the first page of the answer.
//
//	go run ./example/query example.com
//	go run ./example/query -limit 10 '*.example.com'
//	go run ./example/query 192.0.2.0/24
//
// It uses the token built into the SDK; set ACTIVEDNS_TOKEN to use your own.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	activedns "github.com/activedns/activedns-go"
)

func main() {
	limit := flag.Int("limit", 25, "records to fetch")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: query [-limit n] <domain | *.domain | address | network | AS number>")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	var opts []activedns.Option
	if token := os.Getenv("ACTIVEDNS_TOKEN"); token != "" {
		opts = append(opts, activedns.WithToken(token))
	}
	// the name tells the operators of the API which program is asking
	client, err := activedns.NewClient("example-query/1.0", opts...)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	page, err := client.Query(ctx, flag.Arg(0), activedns.WithPageSize(*limit))
	if err != nil {
		log.Fatal(err)
	}

	for _, r := range page.Records {
		fmt.Printf("%-40s %-39s first seen %s, last %s\n",
			r.Domain, r.IPAddress, r.FirstSeen.Format(time.DateOnly), r.Observed.Format(time.DateOnly))
	}

	count := fmt.Sprint(page.Count)
	switch {
	case page.CountEstimated:
		count = "about " + count
	case page.CountCapped:
		count = "at least " + count
	}
	fmt.Printf("\n%s search for %s: %d of %s records", page.QueryType, page.Query, len(page.Records), count)
	if page.HasMore {
		fmt.Printf(" (more from cursor %d)", page.NextCursor)
	}
	fmt.Println()
	if page.TimedOut {
		fmt.Println("the server gave up on the search: the answer may be incomplete")
	}

	// what the server said about the rate limit with this answer
	for _, l := range client.RateLimits() {
		fmt.Printf("rate limit %q: %d requests left of %d\n", l.Name, l.Remaining, l.Limit)
	}
}
