package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/euler-gaozl/agent-os-sdk/contracts"
	"strings"
	"unicode/utf8"
)

// RevisionFiles copies an explicitly selected immutable baseline. Editing the
// instructions replaces only SKILL.md; references are retained and revalidated.
// Registration creates a disabled version with fresh checks, never inherited
// activation. No fallback to "latest" is allowed.
func (s *Store) RevisionFiles(ctx context.Context, id string, revision int, spec json.RawMessage) ([]File, error) {
	if revision < 1 {
		return nil, errors.New("positive skill baseline revision required")
	}
	if _, e := s.Get(ctx, "skill", id, revision); e != nil {
		return nil, e
	}
	var skill contracts.Skill
	if e := json.Unmarshal(spec, &skill); e != nil {
		return nil, e
	}
	rows, e := s.DB.Query(ctx, "SELECT path,hash,body FROM asset_files WHERE kind='skill' AND id=$1 AND revision=$2 ORDER BY path", id, revision)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	files := []File{}
	total, hasInstructions := 0, false
	for rows.Next() {
		var f File
		var hash string
		if e = rows.Scan(&f.Path, &hash, &f.Body); e != nil {
			return nil, e
		}
		if contracts.Hash(f.Body) != hash {
			return nil, errors.New("baseline file hash mismatch")
		}
		if f.Path == "SKILL.md" {
			f.Body = []byte(skill.Content)
			hasInstructions = true
		}
		total += len(f.Body)
		if len(files) >= 64 || len(f.Body) > 1<<20 || total > 4<<20 {
			return nil, errors.New("baseline bundle exceeds limits")
		}
		if !utf8.Valid(f.Body) || strings.IndexByte(string(f.Body), 0) >= 0 || credentialPattern.Match(f.Body) {
			return nil, errors.New("invalid source or possible credential in baseline bundle")
		}
		files = append(files, f)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if len(files) > 0 && !hasInstructions {
		return nil, errors.New("baseline bundle requires root SKILL.md")
	}
	return files, nil
}

func (s *Store) ReadSkillFile(ctx context.Context, run contracts.Run, id, path string, offset, limit int) (any, error) {
	if offset < 0 || limit < 0 || limit > 1200 {
		return nil, errors.New("invalid file window")
	}
	if limit == 0 {
		limit = 1200
	}
	var ref contracts.Ref
	for _, candidate := range run.Snapshot.Agent.Skills {
		if candidate.ID == id {
			ref = candidate
			break
		}
	}
	if ref.ID == "" {
		return nil, errors.New("skill not bound to run")
	}
	if _, e := s.Resolve(ctx, "skill", ref.ID, ref.Revision); e != nil {
		return nil, e
	}
	var hash string
	var body []byte
	if e := s.DB.QueryRow(ctx, "SELECT hash,body FROM asset_files WHERE kind='skill' AND id=$1 AND revision=$2 AND path=$3", id, ref.Revision, path).Scan(&hash, &body); e != nil {
		return nil, e
	}
	if hash != contracts.Hash(body) {
		return nil, errors.New("skill file hash mismatch")
	}
	chars := []rune(string(body))
	if offset > len(chars) {
		return nil, errors.New("offset exceeds file length")
	}
	end := min(offset+limit, len(chars))
	return map[string]any{"skill": ref, "path": path, "hash": hash, "content": string(chars[offset:end]), "next_offset": end, "has_more": end < len(chars)}, nil
}
