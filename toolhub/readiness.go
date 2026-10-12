package toolhub

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
)

const readinessQuery = "SELECT a.definition_hash,s.check_fence,s.check_until,f.body,u.seq FROM assets a,asset_state s,asset_files f,asset_audit u WHERE false"

// Readiness checks only this service's schema and permissions without reading user data.
func Readiness(ctx context.Context, db *pgxpool.Pool) error {
	if db == nil {
		return errors.New("database required")
	}
	_, err := db.Exec(ctx, readinessQuery)
	return err
}
