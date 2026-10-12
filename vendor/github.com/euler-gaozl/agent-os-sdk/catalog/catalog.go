// Package catalog defines the bounded, read-only resource index contract.
// Index items must never be written back as resource definitions.
package catalog

import (
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/euler-gaozl/agent-os-sdk/contracts"
)

type Query struct {
	Kind, After, Search string
	Limit               int
}
type Page struct {
	Items []contracts.Resource `json:"items"`
	Next  string               `json:"next_cursor"`
}

func Parse(v url.Values) (Query, error) {
	q := Query{Kind: v.Get("kind"), After: v.Get("after"), Search: strings.TrimSpace(v.Get("q")), Limit: 100}
	if n := v.Get("limit"); n != "" {
		var err error
		q.Limit, err = strconv.Atoi(n)
		if err != nil {
			return q, errors.New("invalid catalog limit")
		}
	}
	if q.Limit < 1 || q.Limit > 100 || len(q.Search) > 200 || len(q.After) > 200 {
		return q, errors.New("catalog query exceeds limits")
	}
	if q.Kind != "" && !contracts.Has([]string{"agent", "knowledge", "prompt", "skill", "connector"}, q.Kind) {
		return q, errors.New("invalid catalog kind")
	}
	if q.After != "" {
		parts := strings.Split(q.After, ":")
		if len(parts) != 2 || !contracts.SafeName.MatchString(parts[0]) || !contracts.SafeName.MatchString(parts[1]) {
			return q, errors.New("invalid catalog cursor")
		}
	}
	return q, nil
}
func Key(r contracts.Resource) string { return r.Kind + ":" + r.ID }
func Finish(items []contracts.Resource, limit int) Page {
	p := Page{Items: items}
	if len(items) > limit {
		p.Items = items[:limit]
		p.Next = Key(p.Items[limit-1])
	}
	return p
}
