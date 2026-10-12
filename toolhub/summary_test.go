package toolhub

import (
	"context"
	"github.com/euler-gaozl/agent-os-sdk/catalog"
	"github.com/euler-gaozl/agent-os-sdk/contracts"
	"strings"
	"testing"
)

func TestToolCatalogOmitsLargeBodiesAndProbeReports(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	body := strings.Repeat("方法", 19000) + "SKILL-TAIL-731"
	spec := contracts.JSON(contracts.Skill{Name: "Evidence skill", Content: body})
	_, e := s.DB.Exec(ctx, `INSERT INTO assets(kind,id,revision,spec,definition_hash) SELECT 'skill','large-'||i,1,$1::jsonb,'fixture' FROM generate_series(1,205)i`, spec)
	if e != nil {
		t.Fatal(e)
	}
	// The summary index must also work without fetching mutable health reports.
	p, e := s.Catalog(ctx, catalog.Query{Kind: "skill", Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Items) != 100 || p.Next == "" || len(contracts.JSON(p)) > 120000 || strings.Contains(string(contracts.JSON(p)), "SKILL-TAIL-731") {
		t.Fatal("unbounded/invalid ToolHub index")
	}
	p2, e := s.Catalog(ctx, catalog.Query{Kind: "skill", After: p.Next, Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	seen := map[string]bool{}
	for _, a := range p.Items {
		seen[a.ID] = true
	}
	for _, a := range p2.Items {
		if seen[a.ID] {
			t.Fatal("duplicate across cursor")
		}
	}
	one, e := s.Catalog(ctx, catalog.Query{Kind: "skill", Search: "large-205", Limit: 100})
	if e != nil || len(one.Items) != 1 || one.Items[0].ID != "large-205" {
		t.Fatal("server-side search did not reach later resource")
	}
}
