package toolhub

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/euler-gaozl/agent-os-sdk/contracts"
)

func TestSlowProbeDoesNotBlockOtherAssets(t *testing.T) {
	s := testStore(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer slow.Close()
	defer close(release)
	u, _ := url.Parse(slow.URL)
	s.Policy.AllowedHosts = []string{u.Host}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tools := []contracts.Tool{{Name: "read", InputSchema: contracts.JSON(map[string]any{"type": "object"})}}
	if _, e := s.Register(ctx, "connector", "slow", contracts.JSON(contracts.Connector{Endpoint: slow.URL, Protocol: "2025-11-25", Tools: tools}), nil, "fixture"); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- s.MonitorWorkers(ctx, "fixture", 2) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("slow probe never started")
	}
	if _, e := s.Register(ctx, "skill", "ready", contracts.JSON(contracts.Skill{Name: "Ready", Content: "Controlled fixture."}), nil, "fixture"); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(3 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		a, e := s.Get(ctx, "skill", "ready", 1)
		if e != nil {
			t.Fatal(e)
		}
		if a.Health == "healthy" {
			ready = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("slow MCP blocked independent Skill validation")
	}
	cancel()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("monitor did not stop with in-flight probe")
	}
}

func TestProbeStorageErrorDoesNotExitServiceLoop(t *testing.T) {
	s := testStore(t)
	s.DB.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.MonitorWorkers(ctx, "fixture", 2) }()
	select {
	case e := <-done:
		t.Fatal("background probe failure would terminate the catalog HTTP service", e)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
