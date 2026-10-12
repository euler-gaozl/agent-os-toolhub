package main

import (
	"context"
	"errors"
	"github.com/euler-gaozl/agent-os-sdk/servicehost"
	"github.com/euler-gaozl/agent-os-sdk/servicehttp"
	"github.com/euler-gaozl/agent-os-toolhub/toolhub"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"os"
	"strconv"
	"strings"
)

func main() {
	servicehost.Main(servicehost.Options{Role: "toolhub", Port: "8091", Database: true, Concurrency: 4, Authorize: toolhub.Authorize, Readiness: toolhub.Readiness}, func(ctx context.Context, p *pgxpool.Pool, c servicehttp.Config) (http.Handler, func(context.Context) error, error) {
		if err := applyLocalDefaults(); err != nil {
			return nil, nil, err
		}
		client := servicehost.Client()
		if client.RegistryURL == "" || client.RuntimeURL == "" {
			return nil, nil, errors.New("REGISTRY_URL and RUNTIME_URL required")
		}
		workers, e := strconv.Atoi(servicehost.Env("TOOLHUB_PROBE_WORKERS", "2"))
		if e != nil || workers < 1 || workers > 8 {
			return nil, nil, errors.New("TOOLHUB_PROBE_WORKERS must be 1..8")
		}
		store := &toolhub.Store{DB: p, Policy: toolhub.Policy{AllowedHosts: strings.Split(os.Getenv("MCP_ALLOWED_HOSTS"), ",")}, Secrets: client}
		server := &toolhub.Server{Store: store, HTTP: c, Execution: client}
		return server.Handler(), func(ctx context.Context) error {
			return store.MonitorWorkers(ctx, servicehost.Env("INSTANCE_ID", "toolhub-monitor"), workers)
		}, nil
	})
}
