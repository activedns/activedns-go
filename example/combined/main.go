// Combined searches for records that match a domain and a network and an AS
// number at once: any two or three of them.
//
//	ACTIVEDNS_TOKEN=… go run ./example/combined -domain '*.example.com' -asn 64496
//	ACTIVEDNS_TOKEN=… go run ./example/combined -domain '*.example.com' -ip 192.0.2.0/24
//
// Combined searches need a token of your own: the one built into the SDK may
// not make them, and without ACTIVEDNS_TOKEN this example shows what that
// refusal looks like.
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
	var q activedns.Combined
	flag.StringVar(&q.Domain, "domain", "", "a domain, or a wildcard such as *.example.com")
	flag.StringVar(&q.IP, "ip", "", "an address or a network")
	flag.IntVar(&q.ASN, "asn", 0, "an AS number, without the AS")
	flag.Parse()

	var opts []activedns.Option
	if token := os.Getenv("ACTIVEDNS_TOKEN"); token != "" {
		opts = append(opts, activedns.WithToken(token))
	}
	// the name tells the operators of the API which program is asking
	client, err := activedns.NewClient("example-combined/1.0", opts...)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	page, err := client.QueryCombined(ctx, q)
	switch {
	case activedns.IsForbidden(err):
		// the token works, but not for this query; other queries still do
		log.Fatalf("this token may not make that search: %v", err)
	case activedns.IsUnauthorized(err):
		log.Fatalf("the token is not accepted: %v", err)
	case err != nil:
		log.Fatal(err)
	}

	for _, r := range page.Records {
		fmt.Printf("%-40s %-39s AS%-8d %s\n", r.Domain, r.IPAddress, r.ASN, r.Observed.Format(time.DateOnly))
	}
	fmt.Printf("\n%s: %d of %d records\n", page.Query, len(page.Records), page.Count)
}
