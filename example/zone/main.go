// Zone prints what is known of the zone of a name, and lists a few of the
// zones that share its first nameserver.
//
//	go run ./example/zone www.example.com
//	go run ./example/zone -limit 20 example.com
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
	limit := flag.Int("limit", 10, "zones to list that share the first nameserver")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: zone [-limit n] <domain>")
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
	client, err := activedns.NewClient("example-zone/1.0", opts...)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	info, err := client.Zone(ctx, flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	zone := info.Zone
	if zone == nil {
		fmt.Printf("no zone is known for %s\n", info.Query)
		return
	}

	fmt.Printf("%s is in the zone %s (parent %s), seen %s to %s\n", info.Query, zone.Name, zone.Parent,
		zone.FirstSeen.Format(time.DateOnly), zone.LastSeen.Format(time.DateOnly))
	if info.QueryIsNameserver {
		fmt.Printf("%s is also a nameserver: see Client.Nameservers\n", info.Query)
	}
	if soa := zone.SOA; soa != nil {
		fmt.Printf("SOA: primary %s, contact %s, serial %d\n", soa.Primary, soa.Email, soa.Serial)
	}
	for _, ns := range zone.Nameservers {
		fmt.Printf("nameserver %-30s %s, shared by %s zones\n", ns.Name, ns.Provider, count(ns.Zones))
	}
	for _, key := range zone.DNSKeys {
		fmt.Printf("DNSKEY tag %d, algorithm %d, fingerprint %.16s…, in %s zones\n",
			key.KeyTag, key.Algorithm, key.Fingerprint, count(key.Zones))
	}
	if len(zone.Nameservers) == 0 {
		return
	}

	first := zone.Nameservers[0].Name
	page, err := client.Zones(ctx, activedns.ByNameserver, first, activedns.WithPageSize(*limit))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\n%d of %s zones use %s:\n", len(page.Zones), count(page.Count), first)
	for _, z := range page.Zones {
		fmt.Printf("  %-40s last seen %s\n", z.Name, z.LastSeen.Format(time.DateOnly))
	}
}

// count writes a Count, with a plus when counting stopped.
func count(c activedns.Count) string {
	if c.Capped {
		return fmt.Sprintf("%d+", c.N)
	}
	return fmt.Sprint(c.N)
}
