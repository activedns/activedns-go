package activedns

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"
)

const zoneJSON = `{
  "query": "www.example.com", "query_is_nameserver": true,
  "registrable_domains": {"example.com": "example.com", "com": ""},
  "zone": {
    "name": "example.com", "parent": "com",
    "first_seen": "2024-01-02T03:04:05Z", "last_seen": "2026-10-04T00:00:00Z",
    "children": {"n": 3},
    "ns_sets": [{"ns": ["ns1.example.net", "ns2.example.net"], "origin": "parent", "from": "a.gtld-servers.net",
      "server": "192.0.2.53", "first_seen": "2024-01-02T03:04:05Z", "last_seen": "2026-10-04T00:00:00Z",
      "zones": {"n": 1000, "capped": true}}],
    "nameservers": [{"name": "ns1.example.net", "provider": "example.net", "zones": {"n": 7},
      "addresses": [{"ip": "192.0.2.1", "origins": ["glue"], "names": {"n": 2},
        "first_seen": "2024-01-02T03:04:05Z", "last_seen": "2026-10-04T00:00:00Z"}]}],
    "soa": {"primary": "ns1.example.net", "contact": "hostmaster.example.com", "email": "hostmaster@example.com",
      "serial": 2026100401, "refresh": 7200, "retry": 3600, "expire": 1209600, "minimum": 300,
      "since": "2026-01-01T00:00:00Z", "last_seen": "2026-10-04T00:00:00Z",
      "primary_zones": {"n": 5}, "contact_zones": {"n": 9}},
    "ds": [{"key_tag": 12345, "algorithm": 13, "digest_type": 2, "digest": "ab12",
      "first_seen": "2024-01-02T03:04:05Z", "last_seen": "2026-10-04T00:00:00Z"}],
    "dnskeys": [{"key_tag": 12345, "flags": 257, "algorithm": 13, "fingerprint": "ff00",
      "first_seen": "2024-01-02T03:04:05Z", "last_seen": "2026-10-04T00:00:00Z", "zones": {"n": 2}}],
    "refusals": [{"server": "192.0.2.99", "reason": "refused",
      "first_seen": "2024-01-02T03:04:05Z", "last_seen": "2026-10-04T00:00:00Z"}]
  }
}`

func TestZoneSendsAndDecodes(t *testing.T) {
	var got *http.Request
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		writeStatus(w, http.StatusOK, zoneJSON)
	})

	info, err := h.client.Zone(context.Background(), "www.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodGet || got.URL.Path != "/api/v2/zone" || got.URL.Query().Get("name") != "www.example.com" {
		t.Errorf("request: %s %s", got.Method, got.URL)
	}
	if got.Header.Get("Authorization") != "Bearer test-token" {
		t.Errorf("Authorization: %q", got.Header.Get("Authorization"))
	}
	if info.Query != "www.example.com" || !info.QueryIsNameserver || info.RegistrableDomains["example.com"] != "example.com" {
		t.Errorf("info: %+v", info)
	}
	z := info.Zone
	if z == nil || z.Name != "example.com" || z.Parent != "com" || z.Children == nil || z.Children.N != 3 ||
		!z.FirstSeen.Equal(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("zone: %+v", z)
	}
	if len(z.NSSets) != 1 || z.NSSets[0].Origin != "parent" || len(z.NSSets[0].NS) != 2 ||
		z.NSSets[0].Zones.N != 1000 || !z.NSSets[0].Zones.Capped {
		t.Errorf("ns sets: %+v", z.NSSets)
	}
	if len(z.Nameservers) != 1 || z.Nameservers[0].Provider != "example.net" || len(z.Nameservers[0].Addresses) != 1 ||
		z.Nameservers[0].Addresses[0].IP != "192.0.2.1" || z.Nameservers[0].Addresses[0].Names.N != 2 {
		t.Errorf("nameservers: %+v", z.Nameservers)
	}
	if z.SOA == nil || z.SOA.Serial != 2026100401 || z.SOA.Email != "hostmaster@example.com" ||
		z.SOA.PrimaryZones.N != 5 || z.SOA.ContactZones.N != 9 {
		t.Errorf("soa: %+v", z.SOA)
	}
	if len(z.DS) != 1 || z.DS[0].Digest != "ab12" || len(z.DNSKeys) != 1 || z.DNSKeys[0].Fingerprint != "ff00" ||
		len(z.Refusals) != 1 || z.Refusals[0].Reason != "refused" {
		t.Errorf("ds %+v, keys %+v, refusals %+v", z.DS, z.DNSKeys, z.Refusals)
	}
}

func TestZoneUnknown(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		writeStatus(w, http.StatusOK, `{"query": "nope.invalid", "zone": null}`)
	})
	info, err := h.client.Zone(context.Background(), "nope.invalid")
	if err != nil {
		t.Fatal(err)
	}
	if info.Zone != nil || info.Query != "nope.invalid" {
		t.Errorf("info: %+v", info)
	}
}

func TestZonesSendsAndDecodes(t *testing.T) {
	var got *http.Request
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		writeStatus(w, http.StatusOK, `{
		  "by": "soa_contact_of", "value": "example.com", "subject": "hostmaster@example.com",
		  "count": {"n": 10000, "capped": true}, "has_more": true, "next_cursor": 2,
		  "registrable_domains": {"a.example": "a.example"},
		  "zones": [{"name": "a.example", "first_seen": "2024-01-02T03:04:05Z", "last_seen": "2026-10-04T00:00:00Z"},
		            {"name": "b.example", "reason": "servfail", "first_seen": "2024-01-02T03:04:05Z", "last_seen": "2026-10-04T00:00:00Z"}]
		}`)
	})

	page, err := h.client.Zones(context.Background(), BySOAContactOf, "example.com", WithPageSize(2), WithCursor(4))
	if err != nil {
		t.Fatal(err)
	}
	if got.URL.Path != "/api/v2/zones" {
		t.Errorf("path: %s", got.URL.Path)
	}
	if q := got.URL.Query(); q.Get("by") != "soa_contact_of" || q.Get("value") != "example.com" ||
		q.Get("limit") != "2" || q.Get("cursor") != "4" {
		t.Errorf("query string: %s", got.URL.RawQuery)
	}
	if len(page.Zones) != 2 || page.Zones[1].Reason != "servfail" || page.By != BySOAContactOf ||
		page.Subject != "hostmaster@example.com" || page.Count.N != 10000 || !page.Count.Capped ||
		!page.HasMore || page.NextCursor != 2 || page.RegistrableDomains["a.example"] != "a.example" {
		t.Errorf("page: %+v", page)
	}
}

func TestZonesNeedsWhatAndValue(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("request sent")
	})
	if _, err := h.client.Zones(context.Background(), "", "example.com"); err == nil {
		t.Error("no error without by")
	}
	if _, err := h.client.Zones(context.Background(), ByNameserver, ""); err == nil {
		t.Error("no error without value")
	}
	if _, err := h.client.Zone(context.Background(), ""); err == nil {
		t.Error("no error without a name")
	}
	if _, err := h.client.Nameservers(context.Background(), ""); err == nil {
		t.Error("no error without an address")
	}
}

// zonePages serves total zones, as many per page as the request asks for.
func zonePages(total int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		cursor, _ := strconv.Atoi(q.Get("cursor"))
		if limit == 0 {
			limit = 100
		}
		end := min(cursor+limit, total)
		body := `{"by": "` + q.Get("by") + `", "value": "` + q.Get("value") + `", "count": {"n": ` + strconv.Itoa(total) +
			`}, "has_more": ` + strconv.FormatBool(end < total) + `, "next_cursor": ` + strconv.Itoa(end) + `, "zones": [`
		for i := cursor; i < end; i++ {
			if i > cursor {
				body += ","
			}
			body += `{"name": "z` + strconv.Itoa(i) + `.example"}`
		}
		writeStatus(w, http.StatusOK, body+"]}")
	}
}

func TestNextZonesContinuesTheRequest(t *testing.T) {
	h := newHarness(t, zonePages(25))
	page, err := h.client.Zones(context.Background(), ByNameserver, "ns1.example.net", WithPageSize(10))
	if err != nil {
		t.Fatal(err)
	}
	if h.hits.Load() != 1 {
		t.Fatalf("%d requests for one page", h.hits.Load())
	}
	var names []string
	for {
		for _, z := range page.Zones {
			names = append(names, z.Name)
		}
		next, err := h.client.NextZones(context.Background(), page)
		if errors.Is(err, ErrNoMore) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		page = next
	}
	if len(names) != 25 || names[0] != "z0.example" || names[24] != "z24.example" {
		t.Errorf("zones: %v", names)
	}
	if h.hits.Load() != 3 || page.By != ByNameserver || page.Value != "ns1.example.net" {
		t.Errorf("%d requests, page %+v", h.hits.Load(), page)
	}
}

func TestNextZonesNeedsAPageOfThisClient(t *testing.T) {
	h := newHarness(t, zonePages(1))
	if _, err := h.client.NextZones(context.Background(), nil); err == nil {
		t.Error("no error for nil")
	}
	if _, err := h.client.NextZones(context.Background(), &ZonePage{HasMore: true}); err == nil {
		t.Error("no error for a page made by hand")
	}
	if h.hits.Load() != 0 {
		t.Errorf("%d requests", h.hits.Load())
	}
}

func TestNextZonesBeyondTheTokensDepth(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") != "" {
			writeStatus(w, http.StatusForbidden, `{"code": 403, "error": "paging too deep"}`)
			return
		}
		zonePages(300)(w, r)
	})
	page, err := h.client.Zones(context.Background(), ByChildren, "com")
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.client.NextZones(context.Background(), page)
	if !IsForbidden(err) {
		t.Errorf("error: %v", err)
	}
}

func TestNameserversSendsAndDecodes(t *testing.T) {
	var got *http.Request
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		writeStatus(w, http.StatusOK, `{
		  "ip": "192.0.2.1", "names": {"n": 2}, "refused": {"n": 1000, "capped": true},
		  "nameservers": [{"name": "ns1.example.net", "origins": ["glue", "answer"], "zones": {"n": 7},
		    "first_seen": "2024-01-02T03:04:05Z", "last_seen": "2026-10-04T00:00:00Z"}]
		}`)
	})
	addr, err := h.client.Nameservers(context.Background(), "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if got.URL.Path != "/api/v2/nameservers" || got.URL.Query().Get("ip") != "192.0.2.1" {
		t.Errorf("request: %s", got.URL)
	}
	if addr.IP != "192.0.2.1" || addr.Names.N != 2 || !addr.Refused.Capped || len(addr.Nameservers) != 1 ||
		addr.Nameservers[0].Name != "ns1.example.net" || addr.Nameservers[0].Zones.N != 7 ||
		len(addr.Nameservers[0].Origins) != 2 {
		t.Errorf("address: %+v", addr)
	}
}

func TestZoneErrors(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		writeStatus(w, http.StatusBadRequest, `{"code": 400, "error": "not a name"}`)
	})
	_, err := h.client.Zone(context.Background(), "*.example.com")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("error: %v", err)
	}
	if h.hits.Load() != 1 {
		t.Errorf("%d requests; a 400 is not retried", h.hits.Load())
	}
}
