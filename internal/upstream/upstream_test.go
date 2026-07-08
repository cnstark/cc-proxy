package upstream

import (
	"github.com/cnstark/cc-proxy/internal/config"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClient_Forward(t *testing.T) {
	// Fake upstream: echoes request body
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(200)
		body := make([]byte, r.ContentLength)
		r.Body.Read(body)
		w.Write(body)
	}))
	defer ts.Close()

	cfg := config.Upstream{
		Name:    "test",
		URL:     ts.URL,
		APIKey:  "sk-test",
		Models: []string{"real-model"},
		Timeout: 5 * time.Second,
	}

	client := NewClient()
	rec := httptest.NewRecorder()
	err := client.Forward(cfg, []byte(`{"model":"real-model"}`), http.Header{
		"Content-Type": []string{"application/json"},
	}, rec, nil, nil)
	if err != nil {
		t.Fatalf("unexpected forward error: %v", err)
	}
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestClient_HealthCheck(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer ts.Close()

	cfg := config.Upstream{
		Name:    "test",
		URL:     ts.URL,
		Timeout: 2 * time.Second,
	}

	client := NewClient()
	ok := client.HealthCheck(cfg)
	if !ok {
		t.Fatal("expected health check success")
	}
}

func TestClient_HealthCheck_Unreachable(t *testing.T) {
	cfg := config.Upstream{
		Name:    "bad",
		URL:     "http://127.0.0.1:19999",
		Timeout: 100 * time.Millisecond,
	}
	client := NewClient()
	ok := client.HealthCheck(cfg)
	if ok {
		t.Fatal("expected health check failure for unreachable upstream")
	}
}
