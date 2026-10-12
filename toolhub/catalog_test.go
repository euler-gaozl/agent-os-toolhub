package toolhub

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/euler-gaozl/agent-os-sdk/contracts"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	db, role := fixtureDatabase(t)
	if err := Migrate(context.Background(), db, role); err != nil {
		t.Fatal(err)
	}
	return &Store{DB: db}
}
func TestSkillRegistrationRequiresCheckAndAuditedActivation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	spec := contracts.JSON(contracts.Skill{Name: "Evidence", Content: "Keep sources and dates."})
	a, e := s.Register(ctx, "skill", "evidence", spec, nil, "test-admin")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Resolve(ctx, a.Kind, a.ID, a.Revision); e == nil {
		t.Fatal("unchecked asset resolved")
	}
	if e = s.Enable(ctx, a.Kind, a.ID, a.Revision, true, "reviewed", "test-admin"); e == nil {
		t.Fatal("unchecked asset enabled")
	}
	if worked, e := s.CheckNext(ctx, "checker-1"); e != nil || !worked {
		t.Fatal(worked, e)
	}
	if e = s.Enable(ctx, a.Kind, a.ID, a.Revision, true, "reviewed content", "test-admin"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Resolve(ctx, a.Kind, a.ID, a.Revision); e != nil {
		t.Fatal(e)
	}
	b, e := s.Register(ctx, a.Kind, a.ID, contracts.JSON(contracts.Skill{Name: "Evidence", Content: "Changed."}), nil, "test-admin")
	if e != nil || b.Revision != 2 {
		t.Fatal(b, e)
	}
	old, e := s.Get(ctx, a.Kind, a.ID, 1)
	if e != nil || canonical(old.Spec) != canonical(spec) {
		t.Fatal("old version mutated", e)
	}
	if e = s.Enable(ctx, a.Kind, a.ID, 1, false, "withdraw", "test-admin"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Resolve(ctx, a.Kind, a.ID, 1); e == nil {
		t.Fatal("disabled asset resolved")
	}
	audit, e := s.Audit(ctx, a.Kind, a.ID, 1, 0)
	if e != nil || len(audit) != 4 {
		t.Fatal(audit, e)
	}
}
func fixture(t *testing.T) (*httptest.Server, *atomic.Bool, *atomic.Int32, []contracts.Tool) {
	t.Helper()
	var drift atomic.Bool
	var calls atomic.Int32
	tools := []contracts.Tool{{Name: "read", Description: "Read fixture", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		w.Header().Set("Content-Type", "application/json")
		var result any
		switch in.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-11-25"}
		case "notifications/initialized":
			w.WriteHeader(202)
			return
		case "tools/list":
			copy := append([]contracts.Tool{}, tools...)
			if drift.Load() {
				copy[0].Description = "Changed tool"
			}
			result = map[string]any{"tools": copy}
		case "tools/call":
			calls.Add(1)
			t.Error("health probe executed tool")
		default:
			t.Error("unexpected method", in.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	t.Cleanup(srv.Close)
	return srv, &drift, &calls, tools
}
func TestMCPHealthDiscoveryDriftAndStaleGate(t *testing.T) {
	s := testStore(t)
	srv, drift, calls, tools := fixture(t)
	u, _ := url.Parse(srv.URL)
	s.Policy.AllowedHosts = []string{u.Host}
	ctx := context.Background()
	a, e := s.Register(ctx, "connector", "fixture", contracts.JSON(contracts.Connector{Endpoint: srv.URL, Protocol: "2025-11-25", Tools: tools}), nil, "test-admin")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.CheckNext(ctx, "checker"); e != nil {
		t.Fatal(e)
	}
	if e = s.Enable(ctx, a.Kind, a.ID, 1, true, "reviewed", "test-admin"); e != nil {
		t.Fatal(e)
	}
	drift.Store(true)
	_ = s.Schedule(ctx, a.Kind, a.ID, 1)
	if _, e = s.CheckNext(ctx, "replacement"); e != nil {
		t.Fatal(e)
	}
	v, e := s.Get(ctx, a.Kind, a.ID, 1)
	if e != nil || v.Health != "drifted" {
		t.Fatal(v, e)
	}
	if _, e = s.Resolve(ctx, a.Kind, a.ID, 1); e == nil {
		t.Fatal("drifted definition allowed")
	}
	drift.Store(false)
	_ = s.Schedule(ctx, a.Kind, a.ID, 1)
	_, _ = s.CheckNext(ctx, "replacement")
	_, e = s.DB.Exec(ctx, "UPDATE asset_state SET checked_at=now()-interval '6 minutes'")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Resolve(ctx, a.Kind, a.ID, 1); e == nil {
		t.Fatal("stale health allowed")
	}
	if calls.Load() != 0 {
		t.Fatal("healthcheck caused effect")
	}
}
func TestCheckerExpiredLeaseIsTakenOver(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, e := s.Register(ctx, "skill", "recovery", contracts.JSON(contracts.Skill{Name: "Recovery", Content: "Sources."}), nil, "test-admin")
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.DB.Exec(ctx, "UPDATE asset_state SET check_owner='dead',check_fence=7,check_until=now()+interval '1 minute'")
	if e != nil {
		t.Fatal(e)
	}
	if work, e := s.CheckNext(ctx, "replacement"); e != nil || work {
		t.Fatal("live lease stolen", work, e)
	}
	_, _ = s.DB.Exec(ctx, "UPDATE asset_state SET check_until=now()-interval '1 second'")
	if work, e := s.CheckNext(ctx, "replacement"); e != nil || !work {
		t.Fatal(work, e)
	}
	var owner string
	var fence int64
	_ = s.DB.QueryRow(ctx, "SELECT check_owner,check_fence FROM asset_state").Scan(&owner, &fence)
	if owner != "replacement" || fence != 8 {
		t.Fatal(owner, fence)
	}
}
func TestEndpointPolicyRejectsMetadataAndRedirects(t *testing.T) {
	p := Policy{}
	for _, endpoint := range []string{"http://public.example/mcp", "https://169.254.169.254/latest", "https://127.0.0.1/mcp", "https://10.0.0.1/mcp", "https://example.com/mcp?token=hidden", "https://user:secret@example.com"} {
		if e := p.Endpoint(endpoint); e == nil {
			t.Fatal("allowed", endpoint)
		}
	}
	var reached atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redirect.Close()
	u, _ := url.Parse(redirect.URL)
	p.AllowedHosts = []string{u.Host}
	client := p.Client()
	defer client.CloseIdleConnections()
	if resp, e := client.Get(redirect.URL); e == nil {
		resp.Body.Close()
		t.Fatal("redirect accepted")
	}
	if reached.Load() != 0 {
		t.Fatal("redirect reached target")
	}
	client = (&Policy{}).Client()
	defer client.CloseIdleConnections()
	if resp, e := client.Get(target.URL); e == nil {
		resp.Body.Close()
		t.Fatal("private dial accepted")
	}
}
func zipFiles(t *testing.T, entries map[string]string, mode fs.FileMode) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, content := range entries {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(mode)
		w, e := z.CreateHeader(h)
		if e != nil {
			t.Fatal(e)
		}
		_, _ = w.Write([]byte(content))
	}
	if e := z.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func TestSkillBundleRejectsTraversalSymlinksBombsAndCredentials(t *testing.T) {
	for name, body := range map[string][]byte{"traversal": zipFiles(t, map[string]string{"SKILL.md": "safe", "../escape": "bad"}, 0644), "symlink": zipFiles(t, map[string]string{"SKILL.md": "target"}, fs.ModeSymlink|0644), "bomb": zipFiles(t, map[string]string{"SKILL.md": strings.Repeat("x", (1<<20)+1)}, 0644), "secret": zipFiles(t, map[string]string{"SKILL.md": "ghp_" + strings.Repeat("A", 30)}, 0644)} {
		t.Run(name, func(t *testing.T) {
			if _, e := Unpack(body); e == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
	files, e := Unpack(zipFiles(t, map[string]string{"SKILL.md": "Keep sources.", "references/method.md": "Method."}, 0644))
	if e != nil || len(files) != 2 {
		t.Fatal(files, e)
	}
}
func TestRejectedDefinitionDoesNotCreateAsset(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, e := s.Register(ctx, "skill", "secret", contracts.JSON(contracts.Skill{Name: "Invalid", Content: "sk-" + strings.Repeat("a", 30)}), nil, "test-admin")
	if e == nil {
		t.Fatal("secret accepted")
	}
	var n int
	_ = s.DB.QueryRow(ctx, "SELECT count(*) FROM assets").Scan(&n)
	if n != 0 {
		t.Fatal("rejected asset persisted")
	}
}
func TestBundleReferencesReadByBoundVersionAndDisabledGate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	files, e := Unpack(zipFiles(t, map[string]string{"SKILL.md": "Use references/method.md", "references/method.md": "Retain original source."}, 0644))
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.Register(ctx, "skill", "bundle", contracts.JSON(contracts.Skill{Name: "Bundle", Content: "Use references/method.md"}), files, "test")
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.CheckNext(ctx, "checker")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Enable(ctx, "skill", "bundle", 1, true, "reviewed", "test"); e != nil {
		t.Fatal(e)
	}
	resource, e := s.Resolve(ctx, "skill", "bundle", 1)
	var skill contracts.Skill
	json.Unmarshal(resource.Spec, &skill)
	if e != nil || len(skill.Files) != 2 {
		t.Fatal(skill, e)
	}
	run := contracts.Run{Snapshot: contracts.Snapshot{Agent: contracts.Agent{Skills: []contracts.Ref{{ID: "bundle", Revision: 1}}}}}
	v, e := s.ReadSkillFile(ctx, run, "bundle", "references/method.md", 0, 100)
	if e != nil || !strings.Contains(string(contracts.JSON(v)), "Retain original") {
		t.Fatal(v, e)
	}
	if _, e = s.ReadSkillFile(ctx, run, "other", "references/method.md", 0, 100); e == nil {
		t.Fatal("unbound skill read")
	}
	if _, e = s.ReadSkillFile(ctx, run, "bundle", "../escape", 0, 100); e == nil {
		t.Fatal("path escape")
	}
	if e = s.Enable(ctx, "skill", "bundle", 1, false, "withdraw", "test"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ReadSkillFile(ctx, run, "bundle", "references/method.md", 0, 100); e == nil {
		t.Fatal("disabled skill read")
	}
}
