package activedns

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"time"
)

// Count is how many zones share a value. N stops at 1000 for the values of a
// zone and at 10,000 for the size of a list; Capped is then true and N is a
// lower bound. A few zones is a lead, thousands is a provider.
type Count struct {
	N      int  `json:"n"`
	Capped bool `json:"capped,omitempty"`
}

// ZoneInfo is the answer of [Client.Zone].
type ZoneInfo struct {
	// Query is the name asked for, as the server normalised it.
	Query string `json:"query"`
	// QueryIsNameserver is true when the name is itself a nameserver: in an
	// NS set, named as SOA primary by a zone, or with an address. The zone
	// says nothing about it (the zone of ns1.example.net is example.net), so
	// [Client.Nameservers] may have an answer for it too.
	QueryIsNameserver bool `json:"query_is_nameserver,omitempty"`
	// Zone is the zone the name is in. Nil when none is known.
	Zone *Zone `json:"zone"`
	// RegistrableDomains maps the zone and its parent to their registrable
	// domains. A zone that is a public suffix (pl, co.uk) has none.
	RegistrableDomains map[string]string `json:"registrable_domains,omitempty"`
}

// Zone is what is known of a DNS zone.
type Zone struct {
	Name      string    `json:"name"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	// Parent is the zone that gave the newest delegation of this one.
	Parent string `json:"parent,omitempty"`
	// Children counts the zones this one delegates.
	Children *Count `json:"children,omitempty"`
	// NSSets are the NS sets seen for the zone, from its parent and from its
	// own servers, newest first.
	NSSets []NSSet `json:"ns_sets,omitempty"`
	// Nameservers are the names in NSSets, each once, with their addresses.
	Nameservers []Nameserver `json:"nameservers,omitempty"`
	SOA         *SOA         `json:"soa,omitempty"`
	DS          []DS         `json:"ds,omitempty"`
	DNSKeys     []DNSKey     `json:"dnskeys,omitempty"`
	// Refusals are the servers that would not answer for the zone.
	Refusals []Refusal `json:"refusals,omitempty"`
}

// NSSet is a set of nameservers seen for a zone.
type NSSet struct {
	NS []string `json:"ns"`
	// Origin is "parent" (the delegation, from a server of From) or "apex"
	// (from the zone's own servers).
	Origin string `json:"origin"`
	From   string `json:"from,omitempty"`
	// Server is the address of the server that gave it last.
	Server    string    `json:"server,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	// Zones counts the zones with exactly this set.
	Zones Count `json:"zones"`
}

// Nameserver is a nameserver of a zone.
type Nameserver struct {
	Name string `json:"name"`
	// Provider is the registrable domain of the name: whose nameserver it is.
	Provider  string              `json:"provider,omitempty"`
	Addresses []NameserverAddress `json:"addresses,omitempty"`
	// Zones counts the zones that have it in an NS set.
	Zones Count `json:"zones"`
}

// NameserverAddress is an address of a [Nameserver].
type NameserverAddress struct {
	IP        string    `json:"ip"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	// Origins are where it was seen: "glue" or "additional" (in a referral)
	// and "answer" (the resolver's lookup of the name).
	Origins []string `json:"origins,omitempty"`
	// Names counts the nameserver names at this address.
	Names Count `json:"names"`
}

// SOA is the start-of-authority record of a zone.
type SOA struct {
	Primary string `json:"primary"`
	// Contact is the RNAME as it is in DNS (hostmaster.example.com), Email
	// the address it stands for (hostmaster@example.com).
	Contact string `json:"contact"`
	Email   string `json:"email,omitempty"`
	Serial  int64  `json:"serial"`
	Refresh int    `json:"refresh"`
	Retry   int    `json:"retry"`
	Expire  int    `json:"expire"`
	Minimum int    `json:"minimum"`
	// Since is when these values were first seen without others between.
	Since    time.Time `json:"since"`
	LastSeen time.Time `json:"last_seen"`
	// PrimaryZones counts the zones with this primary, ContactZones those
	// with this contact.
	PrimaryZones Count `json:"primary_zones"`
	ContactZones Count `json:"contact_zones"`
}

// DS is a delegation signer record of a zone.
type DS struct {
	KeyTag     int `json:"key_tag"`
	Algorithm  int `json:"algorithm"`
	DigestType int `json:"digest_type"`
	// Digest is in hex.
	Digest    string    `json:"digest"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// DNSKey is a DNSSEC key of a zone.
type DNSKey struct {
	KeyTag    int `json:"key_tag"`
	Flags     int `json:"flags"`
	Algorithm int `json:"algorithm"`
	// Fingerprint is the SHA-256 of the algorithm and the key, in hex. It is
	// the value that finds the zones with the key: see [ByDNSKey].
	Fingerprint string    `json:"fingerprint"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	// Zones counts the zones with this key.
	Zones Count `json:"zones"`
}

// Refusal is a server that would not answer for a zone.
type Refusal struct {
	Server string `json:"server"`
	// Reason is refused, servfail, notauth or upward.
	Reason    string    `json:"reason"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// Zone returns what is known of the zone that name is in: its NS sets,
// nameservers and their addresses, SOA, DS and DNSKEY records, and the
// servers that refused it. Beside each value is the number of zones that share
// it; [Client.Zones] lists them.
//
// name is a domain name, not a wildcard: the zone of *.example.com is the
// zone of example.com. ZoneInfo.Zone is nil when no zone is known for it.
func (c *Client) Zone(ctx context.Context, name string) (*ZoneInfo, error) {
	if name == "" {
		return nil, errors.New("activedns: Zone needs a name")
	}
	var info ZoneInfo
	if err := c.get(ctx, "/api/v2/zone", url.Values{"name": {name}}, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// ZonesBy is what the zones of a [Client.Zones] list share.
type ZonesBy string

const (
	// ByNameserver lists the zones with a nameserver in an NS set. The value
	// is the name of the nameserver.
	ByNameserver ZonesBy = "nameserver"
	// ByNSSet lists the zones with exactly an NS set. The value is its
	// names joined by commas.
	ByNSSet ZonesBy = "ns_set"
	// BySOAContactOf lists the zones with the SOA contact of a zone. The
	// value is that zone, not the address.
	BySOAContactOf ZonesBy = "soa_contact_of"
	// BySOAPrimary lists the zones with an SOA primary. The value is its
	// name.
	BySOAPrimary ZonesBy = "soa_primary"
	// ByDNSKey lists the zones with a DNSKEY. The value is its fingerprint:
	// 64 hex digits, see [DNSKey].
	ByDNSKey ZonesBy = "dnskey"
	// ByChildren lists the zones that a zone delegates. The value is that
	// zone.
	ByChildren ZonesBy = "children"
	// ByRefusedBy lists the zones that a server would not answer for. The
	// value is the address of the server.
	ByRefusedBy ZonesBy = "refused_by"
)

// ZonePage is one page of the zones that share a value. ZonePage.HasMore and
// [Client.NextZones] work as Page.HasMore and [Client.Next] do.
type ZonePage struct {
	Zones []ListedZone `json:"zones"`
	// By and Value are the request as the server understood it. Subject is
	// the value shared when it is not Value itself: the SOA contact, as an
	// address, for [BySOAContactOf].
	By      ZonesBy `json:"by"`
	Value   string  `json:"value"`
	Subject string  `json:"subject,omitempty"`
	// Count is the number of zones in all, not in this page. It stops at
	// 10,000 (Count.Capped).
	Count Count `json:"count"`
	// RegistrableDomains maps zones to their registrable domains.
	RegistrableDomains map[string]string `json:"registrable_domains,omitempty"`
	// HasMore is true when there are zones after this page. NextCursor is
	// then the offset of the first of them.
	HasMore    bool `json:"has_more"`
	NextCursor int  `json:"next_cursor"`

	// request is the request that produced the page, for Client.NextZones.
	request url.Values
}

// ListedZone is a zone in a [ZonePage], the most recently seen first.
type ListedZone struct {
	Name      string    `json:"name"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	// Reason is why the server would not answer, for [ByRefusedBy].
	Reason string `json:"reason,omitempty"`
}

// Zones returns one page of the zones that share a value: a nameserver, an NS
// set, an SOA contact or primary, a DNSKEY, the parent they are delegated by,
// or a server that refused them. [WithPageSize] and [WithCursor] apply as they
// do to [Client.Query]; [Client.NextZones] continues from a page.
func (c *Client) Zones(ctx context.Context, by ZonesBy, value string, opts ...QueryOption) (*ZonePage, error) {
	if by == "" || value == "" {
		return nil, errors.New("activedns: Zones needs both what the zones share and a value")
	}
	return c.zones(ctx, url.Values{"by": {string(by)}, "value": {value}}, opts)
}

func (c *Client) zones(ctx context.Context, v url.Values, opts []QueryOption) (*ZonePage, error) {
	for _, opt := range opts {
		opt(v)
	}
	var page ZonePage
	if err := c.get(ctx, "/api/v2/zones", v, &page); err != nil {
		return nil, err
	}
	page.request = v
	return &page, nil
}

// NextZones returns the page after page: the same request, continued where
// page ended. It returns [ErrNoMore] when page is the last one, and fails
// with a 403 ([IsForbidden]) past the depth the token may page to.
func (c *Client) NextZones(ctx context.Context, page *ZonePage) (*ZonePage, error) {
	if page == nil || page.request == nil {
		return nil, errors.New("activedns: NextZones needs a page returned by this package")
	}
	if !page.HasMore {
		return nil, ErrNoMore
	}
	v := url.Values{}
	for key, values := range page.request {
		v[key] = values
	}
	v.Set("cursor", strconv.Itoa(page.NextCursor))
	return c.zones(ctx, v, nil)
}

// Address is the answer of [Client.Nameservers].
type Address struct {
	IP string `json:"ip"`
	// Nameservers are the names at the address, the most recently seen first,
	// up to 100. Names counts them all.
	Nameservers []AddressNameserver `json:"nameservers,omitempty"`
	Names       Count               `json:"names"`
	// Refused counts the zones the address would not answer for. They are
	// listed by [ByRefusedBy].
	Refused Count `json:"refused"`
}

// AddressNameserver is a nameserver name seen at an [Address].
type AddressNameserver struct {
	Name      string    `json:"name"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	// Origins are where it was seen, as NameserverAddress.Origins.
	Origins []string `json:"origins,omitempty"`
	// Zones counts the zones that have it in an NS set.
	Zones Count `json:"zones"`
}

// Nameservers returns the nameserver names seen at an IPv4 or IPv6 address,
// each with the number of zones that use it, and the number of zones the
// address refused to answer for.
func (c *Client) Nameservers(ctx context.Context, ip string) (*Address, error) {
	if ip == "" {
		return nil, errors.New("activedns: Nameservers needs an address")
	}
	var addr Address
	if err := c.get(ctx, "/api/v2/nameservers", url.Values{"ip": {ip}}, &addr); err != nil {
		return nil, err
	}
	return &addr, nil
}
