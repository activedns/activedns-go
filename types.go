package activedns

import (
	"net/url"
	"time"
)

// Record is one observation: a name that resolved to an address.
type Record struct {
	ID        int64  `json:"id"`
	IPAddress string `json:"ip_address"`
	// Domain is the name that holds the address. For a name that is a
	// CNAME, it is the name at the end of the chain; see Aliases.
	Domain string `json:"domain"`
	// Observed is the most recent observation, FirstSeen the first.
	Observed  time.Time `json:"observed"`
	FirstSeen time.Time `json:"first_seen"`
	// IPVersion is 4 or 6.
	IPVersion int `json:"ip_version"`
	// ASN and Country are those of the address. Zero when unknown.
	ASN     int    `json:"asn,omitempty"`
	Country string `json:"country,omitempty"`
	// Aliases lists the CNAME names that matched a name query and lead to
	// Domain. Empty when Domain itself matched.
	Aliases []Alias `json:"aliases,omitempty"`
	// KnownAs lists some of the CNAME names that point at Domain. It is
	// set on address, network and AS-number queries. KnownAsTotal is how
	// many there are, capped at 1000.
	KnownAs      []Alias `json:"known_as,omitempty"`
	KnownAsTotal int     `json:"known_as_total,omitempty"`
}

// Alias is a CNAME name that resolves to a [Record]'s Domain.
type Alias struct {
	Name string `json:"name"`
	// Chain is the names from Name to Domain, in order.
	Chain []string `json:"chain"`
}

// Page is one page of results. Count and HasMore say whether there is more
// than this page; [Client.Next] fetches the next one.
type Page struct {
	Records []Record `json:"records"`
	// Count is the number of records that match the query in all, not the
	// number in this page. It is an estimate when CountEstimated is set,
	// and a lower bound when CountCapped is set.
	Count          int  `json:"count"`
	CountEstimated bool `json:"count_estimated,omitempty"`
	CountCapped    bool `json:"count_capped,omitempty"`
	// HasMore is true when there are records after this page. NextCursor
	// is then the offset of the first of them.
	HasMore    bool `json:"has_more"`
	NextCursor int  `json:"next_cursor"`
	// TimedOut is true when the server gave up on the search. Records may
	// be incomplete. The client does not retry such a query.
	TimedOut bool `json:"timed_out,omitempty"`
	// Query is the query as the server normalised it. QueryType is how it
	// was classified: "domain", "wildcard_domain", "ip", "cidr", "asn" or
	// "combined".
	QueryType string `json:"query_type"`
	Query     string `json:"query"`

	// request is the query that produced the page, for Client.Next.
	request url.Values
}
