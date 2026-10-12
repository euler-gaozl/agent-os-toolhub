package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/euler-gaozl/agent-os-sdk/contracts"
)

// ImportLegacy preserves exact binding versions. Existing catalog rows must have
// identical content; imports never overwrite edited resources or activation.
func (s *Store) ImportLegacy(ctx context.Context, r contracts.Resource) error {
	if r.Kind != "skill" && r.Kind != "connector" {
		return errors.New("not a ToolHub resource")
	}
	report := inspect(r.Kind, r.Spec, s.Policy)
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, "INSERT INTO assets(kind,id,revision,spec,definition_hash) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING", r.Kind, r.ID, r.Revision, r.Spec, report.DefinitionHash)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		var raw json.RawMessage
		if e = tx.QueryRow(ctx, "SELECT spec FROM assets WHERE kind=$1 AND id=$2 AND revision=$3", r.Kind, r.ID, r.Revision).Scan(&raw); e != nil {
			return e
		}
		if canonical(raw) != canonical(r.Spec) {
			return errors.New("legacy and catalog resource differ; migration stopped")
		}
		return tx.Commit(ctx)
	}
	enabled := r.Kind == "skill" && report.Status != "rejected"
	health := "unchecked"
	if enabled {
		health = "healthy"
		report.Status = health
	}
	if _, e = tx.Exec(ctx, "INSERT INTO asset_state(kind,id,revision,enabled,health,report,checked_at) VALUES($1,$2,$3,$4,$5,$6,CASE WHEN $4 THEN now() ELSE NULL END)", r.Kind, r.ID, r.Revision, enabled, health, contracts.JSON(report)); e != nil {
		return e
	}
	if e = audit(ctx, tx, r.Kind, r.ID, r.Revision, "legacy_import", "migration", map[string]any{"preserved_version": true, "enabled": enabled, "report": report}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
