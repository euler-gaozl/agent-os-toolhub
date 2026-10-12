package toolhub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/euler-gaozl/agent-os-sdk/contracts"
)

func TestHTTPBaselineEditPreservesBundleAndRequiresFreshApproval(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	old := contracts.Skill{Name: "original", Content: "Read references/evidence.md"}
	files := []File{{"SKILL.md", []byte(old.Content)}, {"references/evidence.md", []byte("version one evidence")}}
	if _, e := s.Register(ctx, "skill", "package", contracts.JSON(old), files, "test"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.CheckNext(ctx, "checker"); e != nil {
		t.Fatal(e)
	}
	if e := s.Enable(ctx, "skill", "package", 1, true, "reviewed", "test"); e != nil {
		t.Fatal(e)
	}
	// A newer concurrent version must not change the explicit editing baseline.
	if _, e := s.Register(ctx, "skill", "package", contracts.JSON(old), []File{{"SKILL.md", []byte(old.Content)}, {"references/evidence.md", []byte("version two evidence")}}, "test"); e != nil {
		t.Fatal(e)
	}
	mux := http.NewServeMux()
	(&Server{Store: s}).Routes(mux)
	post := func(body any) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/v1/assets", bytes.NewReader(contracts.JSON(body))))
		return w
	}
	changed := contracts.Skill{Name: "renamed", Content: "Cite sources. Read references/evidence.md"}
	w := post(map[string]any{"kind": "skill", "id": "package", "base_revision": 1, "spec": changed})
	if w.Code != 201 {
		t.Fatalf("registration %d: %s", w.Code, w.Body.String())
	}
	var a Asset
	if e := json.Unmarshal(w.Body.Bytes(), &a); e != nil {
		t.Fatal(e)
	}
	if a.Revision != 3 || a.Enabled || a.Health != "unchecked" {
		t.Fatalf("new version inherited approval: %+v", a)
	}
	if _, e := s.Resolve(ctx, "skill", "package", 3); e == nil {
		t.Fatal("unreviewed version resolved")
	}
	for _, check := range []struct {
		rev           int
		path, content string
	}{
		{1, "SKILL.md", old.Content}, {3, "SKILL.md", changed.Content},
		{1, "references/evidence.md", "version one evidence"}, {2, "references/evidence.md", "version two evidence"}, {3, "references/evidence.md", "version one evidence"},
	} {
		var body []byte
		var hash string
		if e := s.DB.QueryRow(ctx, "SELECT body,hash FROM asset_files WHERE kind='skill' AND id='package' AND revision=$1 AND path=$2", check.rev, check.path).Scan(&body, &hash); e != nil {
			t.Fatal(e)
		}
		if string(body) != check.content || hash != contracts.Hash(body) {
			t.Fatalf("revision %d file %s changed or lost", check.rev, check.path)
		}
	}
	for _, body := range []map[string]any{
		{"kind": "skill", "id": "package", "base_revision": 99, "spec": changed},
		{"kind": "skill", "id": "different-id", "base_revision": 1, "spec": changed},
		{"kind": "skill", "id": "package", "base_revision": -1, "spec": changed},
		{"kind": "connector", "id": "package", "base_revision": 1, "spec": changed},
		{"kind": "skill", "id": "package", "base_revision": 1, "bundle": []byte("zip"), "spec": changed},
	} {
		if w := post(body); w.Code < 400 {
			t.Fatalf("invalid baseline accepted: %d", w.Code)
		}
	}
	// Refuse corrupted historical bytes rather than copying them as a new truth.
	if _, e := s.DB.Exec(ctx, "UPDATE asset_files SET body='corrupted' WHERE kind='skill' AND id='package' AND revision=1 AND path='references/evidence.md'"); e != nil {
		t.Fatal(e)
	}
	if w := post(map[string]any{"kind": "skill", "id": "package", "base_revision": 1, "spec": changed}); w.Code < 400 {
		t.Fatal("corrupt baseline accepted")
	}
	var count int
	if e := s.DB.QueryRow(ctx, "SELECT count(*) FROM assets").Scan(&count); e != nil || count != 3 {
		t.Fatalf("failed edit persisted: count %d error %v", count, e)
	}
}
