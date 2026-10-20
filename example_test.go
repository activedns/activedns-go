package activedns_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	activedns "github.com/activedns/activedns-go"
)

func Example() {
	client, err := activedns.NewClient("mytool/1.0")
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	page, err := client.Query(ctx, "example.com")
	if err != nil {
		log.Fatal(err)
	}
	for _, record := range page.Records {
		fmt.Println(record.Domain, record.IPAddress, record.Observed.Format(time.DateOnly))
	}
}

// A query returns one page. Next fetches the one after it, for as long as
// the caller wants more.
func ExampleClient_Next() {
	client, err := activedns.NewClient("mytool/1.0")
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	page, err := client.Query(ctx, "*.example.com")
	for fetched := 0; err == nil; {
		for _, record := range page.Records {
			fmt.Println(record.Domain, record.IPAddress)
		}
		fetched += len(page.Records)
		if !page.HasMore || fetched >= 500 {
			fmt.Printf("%d of %d records\n", fetched, page.Count)
			break
		}
		page, err = client.Next(ctx, page)
	}

	var limit *activedns.RateLimitError
	switch {
	case errors.As(err, &limit):
		log.Fatalf("rate limit %q reached, back at %s", limit.Exceeded, limit.RetryAt)
	case err != nil:
		log.Fatal(err)
	}
}

// Zone returns what is known of the zone of a name. Zones lists the zones
// that share one of its values, a page at a time.
func ExampleClient_Zones() {
	client, err := activedns.NewClient("mytool/1.0")
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	info, err := client.Zone(ctx, "www.example.com")
	if err != nil {
		log.Fatal(err)
	}
	if info.Zone == nil || len(info.Zone.Nameservers) == 0 {
		return
	}
	ns := info.Zone.Nameservers[0]
	fmt.Println(ns.Name, "is in", ns.Zones.N, "zones")

	page, err := client.Zones(ctx, activedns.ByNameserver, ns.Name, activedns.WithPageSize(20))
	for err == nil {
		for _, zone := range page.Zones {
			fmt.Println(zone.Name, zone.LastSeen.Format(time.DateOnly))
		}
		if !page.HasMore {
			break
		}
		page, err = client.NextZones(ctx, page)
	}
	if err != nil {
		log.Fatal(err)
	}
}
