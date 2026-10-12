CREATE TABLE IF NOT EXISTS assets(
 kind text NOT NULL CHECK(kind IN ('skill','connector')), id text NOT NULL, revision integer NOT NULL,
 spec jsonb NOT NULL, definition_hash text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(kind,id,revision));
CREATE TABLE IF NOT EXISTS asset_state(
 kind text NOT NULL,id text NOT NULL,revision integer NOT NULL,enabled boolean NOT NULL DEFAULT false,
 health text NOT NULL DEFAULT 'unchecked',report jsonb NOT NULL DEFAULT '{}', checked_at timestamptz,
 next_check_at timestamptz NOT NULL DEFAULT now(),check_owner text NOT NULL DEFAULT '',check_fence bigint NOT NULL DEFAULT 0,
 check_until timestamptz,failures integer NOT NULL DEFAULT 0,
 PRIMARY KEY(kind,id,revision),FOREIGN KEY(kind,id,revision) REFERENCES assets(kind,id,revision));
CREATE INDEX IF NOT EXISTS asset_checks_due ON asset_state(next_check_at) WHERE enabled OR health='unchecked';
CREATE TABLE IF NOT EXISTS asset_audit(
 seq bigserial PRIMARY KEY,kind text NOT NULL,id text NOT NULL,revision integer NOT NULL,
 action text NOT NULL,actor text NOT NULL,details jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS asset_audit_resource ON asset_audit(kind,id,revision,seq);
CREATE TABLE IF NOT EXISTS asset_files(
 kind text NOT NULL,id text NOT NULL,revision integer NOT NULL,path text NOT NULL,
 hash text NOT NULL,body bytea NOT NULL,PRIMARY KEY(kind,id,revision,path),
 FOREIGN KEY(kind,id,revision) REFERENCES assets(kind,id,revision));
CREATE INDEX IF NOT EXISTS assets_catalog_key ON assets (((kind||':'||id) COLLATE "C"), revision DESC);
