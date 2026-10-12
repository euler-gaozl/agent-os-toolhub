package toolhub

import (
	"context"
	"github.com/euler-gaozl/agent-os-sdk/catalog"
	"github.com/euler-gaozl/agent-os-sdk/contracts"
)

func (s *Store) Catalog(ctx context.Context, q catalog.Query) (catalog.Page, error) {
	rows, err := s.DB.Query(ctx, `SELECT kind,id,revision,`+catalogProjection+` FROM assets a
 WHERE ($1='' OR kind=$1) AND (kind||':'||id) COLLATE "C">$2
 AND revision=(SELECT max(b.revision) FROM assets b WHERE b.kind=a.kind AND b.id=a.id)
 AND ($3='' OR strpos(lower(id||' '||coalesce(spec->>'name','')),lower($3))>0)
 ORDER BY (kind||':'||id) COLLATE "C" LIMIT $4`, q.Kind, q.After, q.Search, q.Limit+1)
	if err != nil {
		return catalog.Page{}, err
	}
	defer rows.Close()
	items := []contracts.Resource{}
	for rows.Next() {
		r := contracts.Resource{Summary: true}
		if err = rows.Scan(&r.Kind, &r.ID, &r.Revision, &r.Spec); err != nil {
			return catalog.Page{}, err
		}
		items = append(items, r)
	}
	return catalog.Finish(items, q.Limit), rows.Err()
}
