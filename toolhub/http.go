package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/euler-gaozl/agent-os-sdk/catalog"
	"net/http"
	"strconv"

	"github.com/euler-gaozl/agent-os-sdk/contracts"
	"github.com/euler-gaozl/agent-os-sdk/mcp"
	"github.com/euler-gaozl/agent-os-sdk/servicehttp"
)

type Execution interface {
	Run(context.Context, string) (contracts.Run, error)
	Invoke(context.Context, contracts.Invocation) ([]byte, error)
}
type Server struct {
	Store     *Store
	HTTP      servicehttp.Config
	Execution Execution
}

func newProbe(p Policy, c contracts.Connector) *mcp.MCPClient {
	return &mcp.MCPClient{Connector: c, HTTP: p.Client()}
}
func (s *Server) Handler() http.Handler { return servicehttp.New(s.HTTP, s.Store.DB.Ping, s.Routes) }
func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/assets/catalog", servicehttp.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		q, e := catalog.Parse(r.URL.Query())
		if e != nil {
			return e
		}
		page, e := s.Store.Catalog(r.Context(), q)
		if e != nil {
			return e
		}
		servicehttp.JSONResponse(w, 200, page)
		return nil
	}))
	mux.HandleFunc("POST /v1/skill-file", servicehttp.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		var in struct {
			RunID   string `json:"run_id"`
			SkillID string `json:"skill_id"`
			Path    string `json:"path"`
			Offset  int    `json:"offset"`
			Limit   int    `json:"limit"`
		}
		if e := servicehttp.Decode(w, r, &in); e != nil {
			return e
		}
		if s.Execution == nil {
			return errors.New("runtime unavailable")
		}
		run, e := s.Execution.Run(r.Context(), in.RunID)
		if e != nil {
			return e
		}
		out, e := s.Store.ReadSkillFile(r.Context(), run, in.SkillID, in.Path, in.Offset, in.Limit)
		if e != nil {
			return e
		}
		servicehttp.JSONResponse(w, 200, out)
		return nil
	}))
	mux.HandleFunc("GET /v1/assets/{kind}/{id}/{revision}/files", servicehttp.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		rev, _ := strconv.Atoi(r.PathValue("revision"))
		rows, e := s.Store.DB.Query(r.Context(), "SELECT path,hash FROM asset_files WHERE kind=$1 AND id=$2 AND revision=$3 ORDER BY path", r.PathValue("kind"), r.PathValue("id"), rev)
		if e != nil {
			return e
		}
		defer rows.Close()
		out := []map[string]string{}
		for rows.Next() {
			var path, hash string
			if e = rows.Scan(&path, &hash); e != nil {
				return e
			}
			out = append(out, map[string]string{"path": path, "hash": hash})
		}
		if e = rows.Err(); e != nil {
			return e
		}
		servicehttp.JSONResponse(w, 200, out)
		return nil
	}))
	mux.HandleFunc("GET /v1/assets/{kind}/{id}/{revision}/file", servicehttp.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		rev, e := strconv.Atoi(r.PathValue("revision"))
		if e != nil {
			return errors.New("invalid revision")
		}
		offset := 0
		if v := r.URL.Query().Get("offset"); v != "" {
			offset, e = strconv.Atoi(v)
			if e != nil || offset < 0 {
				return errors.New("invalid offset")
			}
		}
		var body []byte
		var hash string
		if e = s.Store.DB.QueryRow(r.Context(), "SELECT body,hash FROM asset_files WHERE kind=$1 AND id=$2 AND revision=$3 AND path=$4", r.PathValue("kind"), r.PathValue("id"), rev, r.URL.Query().Get("path")).Scan(&body, &hash); e != nil {
			return e
		}
		if contracts.Hash(body) != hash {
			return errors.New("file hash mismatch")
		}
		chars := []rune(string(body))
		if offset > len(chars) {
			return errors.New("offset exceeds file size")
		}
		end := min(offset+4000, len(chars))
		servicehttp.JSONResponse(w, 200, map[string]any{"content": string(chars[offset:end]), "hash": hash, "next_offset": end, "has_more": end < len(chars)})
		return nil
	}))
	mux.HandleFunc("GET /v1/assets", servicehttp.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		list, e := s.Store.List(r.Context(), r.URL.Query().Get("kind"), r.URL.Query().Get("after"), limit)
		if e != nil {
			return e
		}
		servicehttp.JSONResponse(w, 200, list)
		return nil
	}))
	mux.HandleFunc("POST /v1/assets", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Kind         string          `json:"kind"`
			ID           string          `json:"id"`
			Spec         json.RawMessage `json:"spec"`
			Bundle       []byte          `json:"bundle,omitempty"`
			BaseRevision int             `json:"base_revision,omitempty"`
		}
		if e := servicehttp.Decode(w, r, &in); e != nil {
			writeError(w, e)
			return
		}
		var files []File
		if in.BaseRevision < 0 || (in.BaseRevision > 0 && (in.Kind != "skill" || len(in.Bundle) > 0)) {
			writeError(w, errors.New("skill baseline revision cannot be combined with a bundle or another kind"))
			return
		}
		if in.BaseRevision > 0 {
			var e error
			files, e = s.Store.RevisionFiles(r.Context(), in.ID, in.BaseRevision, in.Spec)
			if e != nil {
				writeError(w, e)
				return
			}
		}
		if len(in.Bundle) > 0 {
			if in.Kind != "skill" {
				writeError(w, errors.New("bundles are only supported for skills"))
				return
			}
			var e error
			files, e = Unpack(in.Bundle)
			if e != nil {
				writeError(w, e)
				return
			}
			var skill contracts.Skill
			if e = json.Unmarshal(in.Spec, &skill); e != nil {
				writeError(w, e)
				return
			}
			for _, f := range files {
				if f.Path == "SKILL.md" {
					skill.Content = string(f.Body)
				}
			}
			in.Spec = contracts.JSON(skill)
		}
		v, e := s.Store.Register(r.Context(), in.Kind, in.ID, in.Spec, files, "administrator")
		if e != nil {
			writeError(w, e)
			return
		}
		servicehttp.JSONResponse(w, 201, v)
	})
	mux.HandleFunc("GET /v1/assets/{kind}/{id}/{revision}", servicehttp.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		rev, _ := strconv.Atoi(r.PathValue("revision"))
		a, e := s.Store.Get(r.Context(), r.PathValue("kind"), r.PathValue("id"), rev)
		if e != nil {
			return e
		}
		servicehttp.JSONResponse(w, 200, a)
		return nil
	}))
	mux.HandleFunc("GET /v1/resolve/{kind}/{id}/{revision}", servicehttp.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		rev, _ := strconv.Atoi(r.PathValue("revision"))
		a, e := s.Store.Resolve(r.Context(), r.PathValue("kind"), r.PathValue("id"), rev)
		if e != nil {
			return e
		}
		servicehttp.JSONResponse(w, 200, a)
		return nil
	}))
	mux.HandleFunc("POST /v1/assets/{kind}/{id}/{revision}/check", servicehttp.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		rev, _ := strconv.Atoi(r.PathValue("revision"))
		if e := s.Store.Schedule(r.Context(), r.PathValue("kind"), r.PathValue("id"), rev); e != nil {
			return e
		}
		servicehttp.JSONResponse(w, 202, map[string]bool{"scheduled": true})
		return nil
	}))
	mux.HandleFunc("POST /v1/assets/{kind}/{id}/{revision}/activation", servicehttp.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		var in struct {
			Enabled bool   `json:"enabled"`
			Reason  string `json:"reason"`
		}
		if e := servicehttp.Decode(w, r, &in); e != nil {
			return e
		}
		rev, _ := strconv.Atoi(r.PathValue("revision"))
		if e := s.Store.Enable(r.Context(), r.PathValue("kind"), r.PathValue("id"), rev, in.Enabled, in.Reason, "administrator"); e != nil {
			return e
		}
		servicehttp.JSONResponse(w, 200, map[string]bool{"enabled": in.Enabled})
		return nil
	}))
	mux.HandleFunc("GET /v1/assets/{kind}/{id}/{revision}/audit", servicehttp.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		rev, _ := strconv.Atoi(r.PathValue("revision"))
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		v, e := s.Store.Audit(r.Context(), r.PathValue("kind"), r.PathValue("id"), rev, after)
		if e != nil {
			return e
		}
		servicehttp.JSONResponse(w, 200, v)
		return nil
	}))
	mux.HandleFunc("POST /v1/discover", servicehttp.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		var c contracts.Connector
		if e := servicehttp.Decode(w, r, &c); e != nil {
			return e
		}
		if e := s.Store.Policy.Endpoint(c.Endpoint); e != nil {
			return e
		}
		if c.KeyEnv != "" {
			if s.Store.Secrets == nil {
				return errors.New("credential resolver unavailable")
			}
			key, e := s.Store.Secrets.Secret(r.Context(), c.KeyEnv)
			if e != nil {
				return errors.New("credential unavailable")
			}
			c.AuthToken = key
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10e9)
		defer cancel()
		rpc := newProbe(s.Store.Policy, c)
		defer rpc.HTTP.CloseIdleConnections()
		if e := rpc.Initialize(ctx); e != nil {
			return errors.New("MCP handshake failed")
		}
		v, e := discover(ctx, rpc)
		if e != nil {
			return e
		}
		servicehttp.JSONResponse(w, 200, v)
		return nil
	}))
	mux.HandleFunc("POST /invoke", servicehttp.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		var inv contracts.Invocation
		if e := servicehttp.Decode(w, r, &inv); e != nil {
			return e
		}
		if s.Execution == nil {
			return &servicehttp.StatusError{Status: 503, Message: "execution service unavailable"}
		}
		run, e := s.Execution.Run(r.Context(), inv.RunID)
		if e != nil {
			return e
		}
		for _, ref := range run.Snapshot.Agent.Skills {
			if _, e = s.Store.Resolve(r.Context(), "skill", ref.ID, ref.Revision); e != nil {
				return e
			}
		}
		for _, ref := range run.Snapshot.Agent.Connectors {
			if _, e = s.Store.Resolve(r.Context(), "connector", ref.ID, ref.Revision); e != nil {
				return e
			}
		}
		raw, e := s.Execution.Invoke(r.Context(), inv)
		if e != nil {
			return e
		}
		w.Header().Set("Content-Type", "application/json")
		_, e = w.Write(raw)
		return e
	}))
}
