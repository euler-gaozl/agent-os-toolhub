package toolhub

import (
	"context"
	"github.com/euler-gaozl/agent-os-sdk/contracts"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
	"time"
)

func TestApplicationRoleCanRegisterCheckActivateAndAudit(t *testing.T) {
	db, role := fixtureDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := Migrate(ctx, db, role); err != nil {
		t.Fatal(err)
	}
	cfg := db.Config().Copy()
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET ROLE "+pgx.Identifier{role}.Sanitize())
		return err
	}
	app, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	store := &Store{DB: app}
	asset, err := store.Register(ctx, "skill", "ownership-fixture", contracts.JSON(contracts.Skill{Name: "Retain sources", Content: "Keep source identifiers."}), nil, "fixture-admin")
	if err != nil {
		t.Fatal("scoped registration/audit failed", err)
	}
	if checked, err := store.CheckNext(ctx, "fixture-monitor"); err != nil || !checked {
		t.Fatal("scoped probe failed", checked, err)
	}
	if err = store.Enable(ctx, asset.Kind, asset.ID, asset.Revision, true, "fixture reviewed", "fixture-admin"); err != nil {
		t.Fatal("scoped activation failed", err)
	}
	if _, err = store.Resolve(ctx, asset.Kind, asset.ID, asset.Revision); err != nil {
		t.Fatal("scoped resolve failed", err)
	}
	var count int
	if err = app.QueryRow(ctx, "SELECT count(*) FROM asset_audit WHERE id=$1", asset.ID).Scan(&count); err != nil || count < 3 {
		t.Fatal("registration/check/activation audit missing", count, err)
	}
}
