package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/euler-gaozl/agent-os-sdk/contracts"
	"github.com/euler-gaozl/agent-os-sdk/mcp"
	"github.com/euler-gaozl/agent-os-sdk/servicehttp"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SecretReader interface {
	Secret(context.Context, string) (string, error)
}
type Store struct {
	DB          *pgxpool.Pool
	Policy      Policy
	Secrets     SecretReader
	ProbeClient *http.Client
}
type Asset struct {
	contracts.Resource
	Enabled   bool       `json:"enabled"`
	Health    string     `json:"health"`
	Report    Report     `json:"report"`
	CheckedAt *time.Time `json:"checked_at"`
}

func audit(ctx context.Context, tx pgx.Tx, kind, id string, rev int, action, actor string, detail any) error {
	_, e := tx.Exec(ctx, "INSERT INTO asset_audit(kind,id,revision,action,actor,details) VALUES($1,$2,$3,$4,$5,$6)", kind, id, rev, action, actor, contracts.JSON(detail))
	return e
}
func (s *Store) Register(ctx context.Context, kind, id string, spec json.RawMessage, files []File, actor string) (Asset, error) {
	if !contracts.SafeName.MatchString(id) {
		return Asset{}, errors.New("invalid resource id")
	}
	report := inspect(kind, spec, s.Policy)
	if report.Status == "rejected" {
		return Asset{}, &ValidationError{report}
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Asset{}, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,73))", kind+":"+id); e != nil {
		return Asset{}, e
	}
	var rev int
	if e = tx.QueryRow(ctx, "SELECT coalesce(max(revision),0)+1 FROM assets WHERE kind=$1 AND id=$2", kind, id).Scan(&rev); e != nil {
		return Asset{}, e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO assets(kind,id,revision,spec,definition_hash) VALUES($1,$2,$3,$4,$5)", kind, id, rev, spec, report.DefinitionHash); e != nil {
		return Asset{}, e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO asset_state(kind,id,revision,report) VALUES($1,$2,$3,$4)", kind, id, rev, contracts.JSON(report)); e != nil {
		return Asset{}, e
	}
	for _, f := range files {
		if _, e = tx.Exec(ctx, "INSERT INTO asset_files(kind,id,revision,path,hash,body) VALUES($1,$2,$3,$4,$5,$6)", kind, id, rev, f.Path, contracts.Hash(f.Body), f.Body); e != nil {
			return Asset{}, e
		}
	}
	if e = audit(ctx, tx, kind, id, rev, "registered", actor, report); e != nil {
		return Asset{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return Asset{}, e
	}
	return s.Get(ctx, kind, id, rev)
}

type ValidationError struct{ Report Report }

func (e *ValidationError) Error() string { return "ToolHub policy rejected resource" }

const columns = "a.kind,a.id,a.revision,a.spec,s.enabled,s.health,s.report,s.checked_at"

func scan(row pgx.Row) (Asset, error) {
	var a Asset
	var report []byte
	e := row.Scan(&a.Kind, &a.ID, &a.Revision, &a.Spec, &a.Enabled, &a.Health, &report, &a.CheckedAt)
	if e == nil {
		e = json.Unmarshal(report, &a.Report)
	}
	return a, e
}
func (s *Store) Get(ctx context.Context, kind, id string, rev int) (Asset, error) {
	return scan(s.DB.QueryRow(ctx, "SELECT "+columns+" FROM assets a JOIN asset_state s USING(kind,id,revision) WHERE a.kind=$1 AND a.id=$2 AND a.revision=$3", kind, id, rev))
}
func (s *Store) List(ctx context.Context, kind, after string, limit int) ([]Asset, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, e := s.DB.Query(ctx, "SELECT "+columns+` FROM assets a JOIN asset_state s USING(kind,id,revision) WHERE ($1='' OR a.kind=$1) AND (a.kind||':'||a.id) COLLATE "C">$2 AND a.revision=(SELECT max(b.revision) FROM assets b WHERE b.kind=a.kind AND b.id=a.id) ORDER BY (a.kind||':'||a.id) COLLATE "C" LIMIT $3`, kind, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Asset{}
	size := 0
	for rows.Next() {
		a, e := scan(rows)
		if e != nil {
			return nil, e
		}
		n := len(contracts.JSON(a))
		if len(out) > 0 && size+n > 2<<20 {
			break
		}
		size += n
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) Enable(ctx context.Context, kind, id string, rev int, enabled bool, reason, actor string) error {
	if reason == "" || len(reason) > 2000 {
		return errors.New("bounded change reason required")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	a, e := scan(tx.QueryRow(ctx, "SELECT "+columns+" FROM assets a JOIN asset_state s USING(kind,id,revision) WHERE a.kind=$1 AND a.id=$2 AND a.revision=$3 FOR UPDATE OF s", kind, id, rev))
	if e != nil {
		return e
	}
	if enabled && (a.CheckedAt == nil || a.Health != "healthy" || (kind == "connector" && time.Since(*a.CheckedAt) > 5*time.Minute)) {
		return errors.New("successful current checks required before activation")
	}
	if _, e = tx.Exec(ctx, "UPDATE asset_state SET enabled=$4,next_check_at=least(next_check_at,now()+interval '60 seconds') WHERE kind=$1 AND id=$2 AND revision=$3", kind, id, rev, enabled); e != nil {
		return e
	}
	if e = audit(ctx, tx, kind, id, rev, "activation_changed", actor, map[string]any{"enabled": enabled, "reason": reason}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// Resolve gates new snapshots and execution. A pinned version stays immutable;
// disabling it deliberately prevents new effects by existing runs too.
func (s *Store) Resolve(ctx context.Context, kind, id string, rev int) (contracts.Resource, error) {
	a, e := s.Get(ctx, kind, id, rev)
	if e != nil {
		return a.Resource, e
	}
	if !a.Enabled || a.Health != "healthy" || (kind == "connector" && (a.CheckedAt == nil || time.Since(*a.CheckedAt) > 5*time.Minute)) {
		return a.Resource, errors.New("resource disabled, unhealthy, unchecked or stale")
	}
	if kind == "skill" {
		var skill contracts.Skill
		if e = json.Unmarshal(a.Spec, &skill); e != nil {
			return a.Resource, e
		}
		skill.Files = nil
		rows, err := s.DB.Query(ctx, "SELECT path FROM asset_files WHERE kind=$1 AND id=$2 AND revision=$3 ORDER BY path", kind, id, rev)
		if err != nil {
			return a.Resource, err
		}
		defer rows.Close()
		for rows.Next() {
			var path string
			if err = rows.Scan(&path); err != nil {
				return a.Resource, err
			}
			skill.Files = append(skill.Files, path)
		}
		if err = rows.Err(); err != nil {
			return a.Resource, err
		}
		a.Spec = contracts.JSON(skill)
	}
	return a.Resource, nil
}
func (s *Store) Schedule(ctx context.Context, kind, id string, rev int) error {
	tag, e := s.DB.Exec(ctx, "UPDATE asset_state SET next_check_at=now(),health=CASE WHEN enabled THEN health ELSE 'unchecked' END WHERE kind=$1 AND id=$2 AND revision=$3", kind, id, rev)
	if e == nil && tag.RowsAffected() != 1 {
		return errors.New("asset not found")
	}
	return e
}
func toolHash(ts []contracts.Tool) string {
	copy := append([]contracts.Tool{}, ts...)
	sort.Slice(copy, func(i, j int) bool { return copy[i].Name < copy[j].Name })
	return canonical(copy)
}
func (s *Store) probe(ctx context.Context, a Asset) Report {
	start := time.Now()
	report := inspect(a.Kind, a.Spec, s.Policy)
	if report.Status == "rejected" {
		return report
	}
	report.Status = "healthy"
	if a.Kind == "connector" {
		var c contracts.Connector
		_ = json.Unmarshal(a.Spec, &c)
		if c.KeyEnv != "" {
			if s.Secrets == nil {
				report.Status = "unhealthy"
				report.Findings = append(report.Findings, Finding{"credential", "error", "credential resolver unavailable"})
				return report
			}
			key, e := s.Secrets.Secret(ctx, c.KeyEnv)
			if e != nil {
				report.Status = "unhealthy"
				report.Findings = append(report.Findings, Finding{"credential", "error", "credential unavailable"})
				return report
			}
			c.AuthToken = key
		}
		client := s.ProbeClient
		if client == nil {
			client = s.Policy.Client()
		}
		defer client.CloseIdleConnections()
		rpc := mcp.MCPClient{Connector: c, HTTP: client}
		e := rpc.Initialize(ctx)
		var tools []contracts.Tool
		if e == nil {
			tools, e = discover(ctx, &rpc)
		}
		if e != nil {
			report.Status = "unhealthy"
			report.Findings = append(report.Findings, Finding{"mcp_probe", "error", "MCP initialize/tools/list failed; check endpoint, protocol and credential"})
		} else {
			report.Tools = tools
			if toolHash(c.Tools) != toolHash(tools) {
				report.Status = "drifted"
				report.Findings = append(report.Findings, Finding{"definition_drift", "error", "remote tool definitions changed; register and review a new version"})
			}
		}
	}
	report.LatencyMS = time.Since(start).Milliseconds()
	return report
}
func discover(ctx context.Context, rpc *mcp.MCPClient) ([]contracts.Tool, error) {
	out := []contracts.Tool{}
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 8; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, e := rpc.RPC(ctx, "tools/list", params, false)
		if e != nil {
			return nil, e
		}
		var v struct {
			Tools []contracts.Tool `json:"tools"`
			Next  string           `json:"nextCursor"`
		}
		if e = json.Unmarshal(raw, &v); e != nil {
			return nil, e
		}
		out = append(out, v.Tools...)
		if len(out) > 128 {
			return nil, errors.New("MCP exceeds 128 tools")
		}
		if v.Next == "" {
			return out, nil
		}
		if seen[v.Next] {
			return nil, errors.New("MCP pagination loop")
		}
		seen[v.Next] = true
		cursor = v.Next
	}
	return nil, errors.New("MCP discovery exceeds page limit")
}

// A short bounded read-only probe can be retried after its lease expires. Result
// writes are fenced; a late checker cannot overwrite a replacement's result.
func (s *Store) CheckNext(ctx context.Context, owner string) (bool, error) {
	var kind, id string
	var rev int
	var fence int64
	e := s.DB.QueryRow(ctx, `UPDATE asset_state SET check_owner=$1,check_fence=check_fence+1,check_until=now()+interval '30 seconds' WHERE (kind,id,revision)=(SELECT kind,id,revision FROM asset_state WHERE (enabled OR health='unchecked') AND next_check_at<=now() AND (check_until IS NULL OR check_until<now()) ORDER BY next_check_at FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING kind,id,revision,check_fence`, owner).Scan(&kind, &id, &rev, &fence)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	a, e := s.Get(ctx, kind, id, rev)
	if e != nil {
		return true, e
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	report := s.probe(probeCtx, a)
	cancel()
	if ctx.Err() != nil {
		return true, ctx.Err()
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return true, e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, `UPDATE asset_state SET health=$6,report=$7,checked_at=now(),check_until=NULL,failures=CASE WHEN $6='healthy' THEN 0 ELSE failures+1 END,next_check_at=CASE WHEN kind='skill' THEN 'infinity'::timestamptz ELSE now()+CASE WHEN $6='healthy' THEN interval '60 seconds' ELSE least(300,power(2,least(failures,8))::int*5)*interval '1 second' END END WHERE kind=$1 AND id=$2 AND revision=$3 AND check_owner=$4 AND check_fence=$5 AND check_until>now()`, kind, id, rev, owner, fence, report.Status, contracts.JSON(report))
	if e != nil {
		return true, e
	}
	if tag.RowsAffected() != 1 {
		return true, errors.New("check lease superseded")
	}
	if a.Health != report.Status {
		if e = audit(ctx, tx, kind, id, rev, "checked", owner, report); e != nil {
			return true, e
		}
	}
	return true, tx.Commit(ctx)
}
func (s *Store) Monitor(ctx context.Context, owner string) error {
	return s.MonitorWorkers(ctx, owner, 2)
}

// Probe work must not take down the catalog API after a transient database
// error. Bounded workers also prevent one slow MCP from blocking all checks.
func (s *Store) MonitorWorkers(ctx context.Context, owner string, workers int) error {
	if workers < 1 || workers > 8 {
		return errors.New("ToolHub probe workers must be 1..8")
	}
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			failures := 0
			for ctx.Err() == nil {
				worked, e := s.CheckNext(ctx, fmt.Sprintf("%s/probe-%d", owner, worker))
				delay := time.Second
				if e != nil {
					failures = min(failures+1, 5)
					delay = time.Duration(1<<failures) * time.Second
					if ctx.Err() == nil {
						slog.Warn("toolhub probe deferred", "worker", worker, "retry_seconds", delay.Seconds())
					}
				} else {
					failures = 0
					if worked {
						continue
					}
				}
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}()
	}
	wg.Wait()
	return ctx.Err()
}
func (s *Store) Audit(ctx context.Context, kind, id string, rev int, after int64) ([]map[string]any, error) {
	rows, e := s.DB.Query(ctx, "SELECT seq,action,actor,details,created_at FROM asset_audit WHERE kind=$1 AND id=$2 AND revision=$3 AND seq>$4 ORDER BY seq LIMIT 100", kind, id, rev, after)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var seq int64
		var action, actor string
		var details json.RawMessage
		var at time.Time
		if e = rows.Scan(&seq, &action, &actor, &details, &at); e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"seq": seq, "action": action, "actor": actor, "details": details, "created_at": at})
	}
	return out, rows.Err()
}
func writeError(w http.ResponseWriter, e error) {
	var v *ValidationError
	if errors.As(e, &v) {
		servicehttp.JSONResponse(w, 422, v.Report)
		return
	}
	servicehttp.JSONResponse(w, 400, map[string]string{"error": e.Error()})
}
