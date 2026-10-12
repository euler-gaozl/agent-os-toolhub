package toolhub

import (
	"github.com/euler-gaozl/agent-os-sdk/servicehttp"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServiceIdentityBoundary(t *testing.T) {
	tokens := map[string]string{}
	peers := map[string]string{}
	for i, role := range []string{"control", "futures", "worker", "runtime", "registry", "toolhub", "sources"} {
		tokens[role] = strings.Repeat(string(rune('a'+i)), 40)
		peers[role] = servicehttp.TokenHash(tokens[role])
	}
	handler := servicehttp.New(servicehttp.Config{Role: "toolhub", Peers: peers, Authorize: Authorize}, nil, func(m *http.ServeMux) {
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	})
	for _, tc := range []struct {
		role, method, path string
		want               int
	}{{"futures", "POST", "/v1/assets", 403}, {"worker", "POST", "/v1/assets", 403}, {"runtime", "GET", "/v1/assets", 403}, {"control", "POST", "/invoke", 403}, {"registry", "POST", "/v1/assets", 204}, {"runtime", "POST", "/v1/skill-file", 204}, {"worker", "POST", "/invoke", 204}, {"", "GET", "/v1/assets", 401}} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+tokens[tc.role])
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != tc.want {
			t.Fatalf("%+v got %d", tc, res.Code)
		}
	}
}
