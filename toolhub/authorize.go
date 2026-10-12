package toolhub

import "strings"

// Authorize is ToolHub's route policy, not an SDK-wide role matrix.
func Authorize(caller, method, path string) bool {
	if path == "/readyz" || path == "/metrics" {
		return method == "GET" && (caller == "toolhub" || caller == "control")
	}
	get := method == "GET"
	if caller == "control" {
		return strings.HasPrefix(path, "/v1/assets") || path == "/v1/discover"
	}
	if caller == "registry" {
		return get && strings.HasPrefix(path, "/v1/resolve/") || method == "POST" && path == "/v1/assets"
	}
	if caller == "runtime" && method == "POST" && path == "/v1/skill-file" {
		return true
	}
	if caller == "runtime" || caller == "worker" {
		return get && strings.HasPrefix(path, "/v1/resolve/") || caller == "worker" && method == "POST" && path == "/invoke"
	}
	return false
}
