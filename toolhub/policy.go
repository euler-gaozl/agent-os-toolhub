package toolhub

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/euler-gaozl/agent-os-sdk/mcpsecurity"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/euler-gaozl/agent-os-sdk/contracts"
	"github.com/euler-gaozl/agent-os-sdk/toolcontract"
)

type Finding struct{ Code, Severity, Message string }
type Report struct {
	Status         string           `json:"status"`
	Findings       []Finding        `json:"findings"`
	Tools          []contracts.Tool `json:"tools,omitempty"`
	LatencyMS      int64            `json:"latency_ms"`
	DefinitionHash string           `json:"definition_hash"`
}
type File struct {
	Path string `json:"path"`
	Body []byte `json:"body"`
}
type Policy = mcpsecurity.Policy

func canonical(v any) string {
	b := contracts.JSON(v)
	if raw, ok := v.([]byte); ok {
		b = raw
	}
	var parsed any
	if json.Unmarshal(b, &parsed) != nil {
		return ""
	}
	return contracts.Hash(contracts.JSON(parsed))
}

var credentialPattern = regexp.MustCompile(`(?i)(gh[pousr]_[a-z0-9]{20,}|github_pat_[a-z0-9_]{25,}|sk-[a-z0-9_-]{24,}|-----BEGIN [A-Z ]*PRIVATE KEY-----)`)

func inspect(kind string, spec json.RawMessage, p Policy) Report {
	r := Report{Status: "valid", Findings: []Finding{}, DefinitionHash: canonical(spec)}
	add := func(code, severity, msg string) {
		r.Findings = append(r.Findings, Finding{code, severity, msg})
		if severity == "error" {
			r.Status = "rejected"
		}
	}
	if len(spec) > 128<<10 || !json.Valid(spec) {
		add("invalid_spec", "error", "definition must be valid JSON of at most 128 KiB")
		return r
	}
	if credentialPattern.Match(spec) {
		add("embedded_credential", "error", "possible credential found; replace with a credential reference")
	}
	switch kind {
	case "skill":
		var s contracts.Skill
		if json.Unmarshal(spec, &s) != nil || strings.TrimSpace(s.Name) == "" || strings.TrimSpace(s.Content) == "" || len(s.Content) > 40000 {
			add("skill_content", "error", "skill requires a name and nonempty content of at most 40 KB")
		}
		lower := strings.ToLower(s.Content)
		for _, marker := range []string{"ignore previous instructions", "ignore all previous", "忽略之前的指令", "忽略所有规则", "curl |", "wget |"} {
			if strings.Contains(lower, marker) {
				add("instruction_review", "warning", "instruction or shell pattern requires human review; static checks do not prove safety")
				break
			}
		}
	case "connector":
		var c contracts.Connector
		if e := json.Unmarshal(spec, &c); e != nil {
			add("invalid_connector", "error", "invalid connector definition")
			break
		}
		if e := p.Endpoint(c.Endpoint); e != nil {
			add("endpoint_policy", "error", e.Error())
		}
		if c.Protocol != "2025-11-25" {
			add("protocol", "error", "supported protocol is 2025-11-25")
		}
		if len(c.Tools) == 0 || len(c.Tools) > 128 {
			add("tools_count", "error", "discover and pin 1–128 tools before registration")
		}
		seen := map[string]bool{}
		for _, t := range c.Tools {
			if !contracts.SafeName.MatchString(t.Name) || seen[t.Name] {
				add("tool_name", "error", "invalid or duplicate tool name")
			}
			seen[t.Name] = true
			if len(t.InputSchema) > 32000 {
				add("tool_schema_size", "error", "tool schema exceeds 32 KB")
				continue
			}
			if _, e := toolcontract.CompileSchema(t.InputSchema); e != nil {
				add("tool_schema", "error", "tool schema is invalid or contains external references")
			}
		}
	default:
		add("kind", "error", "only skill and connector belong to ToolHub")
	}
	return r
}

// Unpack never writes archive paths onto the host. Bounds apply to both declared
// and actual uncompressed bytes, including forged ZIP size headers.
func Unpack(body []byte) ([]File, error) {
	if len(body) == 0 || len(body) > 1<<20 {
		return nil, errors.New("ZIP must be 1 byte to 1 MiB")
	}
	z, e := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if e != nil {
		return nil, e
	}
	if len(z.File) > 64 {
		return nil, errors.New("bundle exceeds 64 entries")
	}
	files := []File{}
	seen := map[string]bool{}
	total := 0
	skill := false
	for _, f := range z.File {
		n := f.Name
		if strings.Contains(n, "\\") || strings.HasPrefix(n, "/") || strings.Contains(n, ":") || path.Clean(n) != strings.TrimSuffix(n, "/") || strings.Contains(n, "../") || n == "." || n == ".." {
			return nil, fmt.Errorf("unsafe bundle entry: %q", n)
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if !f.Mode().IsRegular() || f.Mode()&0111 != 0 || seen[strings.ToLower(n)] {
			return nil, errors.New("nonregular or duplicate bundle file")
		}
		seen[strings.ToLower(n)] = true
		if f.UncompressedSize64 > 1<<20 {
			return nil, errors.New("bundle file exceeds 1 MiB")
		}
		r, e := f.Open()
		if e != nil {
			return nil, e
		}
		b, e := io.ReadAll(io.LimitReader(r, (1<<20)+1))
		r.Close()
		if e != nil {
			return nil, e
		}
		total += len(b)
		if len(b) > 1<<20 || total > 4<<20 {
			return nil, errors.New("bundle uncompressed bytes exceed limit")
		}
		if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
			return nil, errors.New("only UTF-8 source and documentation files are accepted")
		}
		if credentialPattern.Match(b) {
			return nil, errors.New("possible credential in bundle")
		}
		if n == "SKILL.md" {
			skill = true
		}
		files = append(files, File{n, b})
	}
	if !skill {
		return nil, errors.New("bundle requires root SKILL.md")
	}
	return files, nil
}
