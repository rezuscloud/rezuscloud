package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_Get(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tenants/prod" {
			t.Errorf("expected path /api/v1/tenants/prod, got %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("expected Bearer test-token, got %s", got)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Resource{
			Kind:     "Cluster",
			Metadata: &ObjectMeta{Name: "prod"},
		})
	}))
	defer srv.Close()

	client := New(srv.URL, "test-token")
	resource, err := client.Get(t.Context(), "api/v1/tenants", "prod")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resource.Metadata.Name != "prod" {
		t.Errorf("expected name prod, got %s", resource.Metadata.Name)
	}
}

func TestClient_List(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tenants" {
			t.Errorf("expected path /api/v1/tenants, got %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("limit"); got != "50" {
			t.Errorf("expected limit=50, got %s", got)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ListResponse{
			Items: []Resource{
				{Kind: "Cluster", Metadata: &ObjectMeta{Name: "prod"}},
				{Kind: "Cluster", Metadata: &ObjectMeta{Name: "staging"}},
			},
			Total: 2,
		})
	}))
	defer srv.Close()

	client := New(srv.URL, "token")
	list, err := client.List(t.Context(), "api/v1/tenants", ListOptions{Limit: 50, LabelSelector: "env=prod"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if list.Total != 2 {
		t.Errorf("expected total 2, got %d", list.Total)
	}
	if len(list.Items) != 2 {
		t.Errorf("expected 2 items, got %d", len(list.Items))
	}
}

func TestClient_Create(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		var resource Resource
		if err := json.NewDecoder(r.Body).Decode(&resource); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if resource.Metadata.Name != "prod" {
			t.Errorf("expected name prod, got %s", resource.Metadata.Name)
		}

		resource.Metadata.ResourceVersion = 1
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resource)
	}))
	defer srv.Close()

	client := New(srv.URL, "token")
	created, err := client.Create(t.Context(), "api/v1/tenants", &Resource{
		Kind:     "Cluster",
		Metadata: &ObjectMeta{Name: "prod"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Metadata.ResourceVersion != 1 {
		t.Errorf("expected resourceVersion 1, got %d", created.Metadata.ResourceVersion)
	}
}

func TestClient_Delete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}

		now := "2026-06-02T00:00:00Z"
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Resource{
			Kind:     "Cluster",
			Metadata: &ObjectMeta{Name: "prod", DeletionTimestamp: &now},
		})
	}))
	defer srv.Close()

	client := New(srv.URL, "token")
	deleted, err := client.Delete(t.Context(), "api/v1/tenants", "prod")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if deleted.Metadata.DeletionTimestamp == nil {
		t.Error("expected deletionTimestamp to be set")
	}
}

func TestClient_ErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ErrorResponse{
			Status:  "failure",
			Message: "tenant \"prod\" not found",
			Reason:  "NotFound",
			Code:    404,
		})
	}))
	defer srv.Close()

	client := New(srv.URL, "token")
	_, err := client.Get(t.Context(), "api/v1/tenants", "prod")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var apiErr *ErrorResponse
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *ErrorResponse, got %T: %v", err, err)
	}
	if apiErr.Reason != "NotFound" {
		t.Errorf("expected reason NotFound, got %s", apiErr.Reason)
	}
	if apiErr.Code != 404 {
		t.Errorf("expected code 404, got %d", apiErr.Code)
	}
}

func TestClient_NoToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("expected no Authorization header, got %s", got)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "")
	_, _ = client.Get(t.Context(), "api/v1/tenants", "prod")
}

func TestParseResource(t *testing.T) {
	data := []byte(`{"kind":"Cluster","metadata":{"name":"prod"},"spec":{"kubernetesVersion":"1.35.0"}}`)
	resource, err := ParseResource(data)
	if err != nil {
		t.Fatalf("ParseResource: %v", err)
	}
	if resource.Kind != "Cluster" {
		t.Errorf("expected kind Cluster, got %s", resource.Kind)
	}
	if resource.Metadata.Name != "prod" {
		t.Errorf("expected name prod, got %s", resource.Metadata.Name)
	}
}

func TestClient_URLBuilding(t *testing.T) {
	client := New("http://localhost:8080", "")
	if client.url("api/v1/tenants") != "http://localhost:8080/api/v1/tenants" {
		t.Errorf("unexpected url: %s", client.url("api/v1/tenants"))
	}
	if client.url("api/v1/tenants", "prod") != "http://localhost:8080/api/v1/tenants/prod" {
		t.Errorf("unexpected url: %s", client.url("api/v1/tenants", "prod"))
	}
}

func TestClient_TrailingSlash(t *testing.T) {
	client := New("http://localhost:8080/", "")
	if client.url("api/v1/tenants") != "http://localhost:8080/api/v1/tenants" {
		t.Errorf("unexpected url with trailing slash: %s", client.url("api/v1/tenants"))
	}
}

func TestClient_RawPost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing auth header")
		}

		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["action"] != "shutdown" {
			t.Errorf("body action = %q, want shutdown", body["action"])
		}

		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status": "ok"}`)
	}))
	defer server.Close()

	client := New(server.URL, "test-token")
	resp, err := client.RawPost(context.Background(), "/api/v1/machines/x/shutdown", map[string]string{"action": "shutdown"})
	if err != nil {
		t.Fatalf("RawPost: %v", err)
	}
	if string(resp) != `{"status": "ok"}` {
		t.Errorf("response = %q, want status ok", string(resp))
	}
}

func TestClient_RawPost_NoBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > 0 {
			t.Errorf("expected no body, got %d bytes", r.ContentLength)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := New(server.URL, "test-token")
	resp, err := client.RawPost(context.Background(), "/api/v1/action", nil)
	if err != nil {
		t.Fatalf("RawPost: %v", err)
	}
	if len(resp) != 0 {
		t.Errorf("expected empty response, got %q", string(resp))
	}
}

func TestClient_StreamGet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("accept = %q, want text/event-stream", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"message\": \"hello\"}\n\n")
	}))
	defer server.Close()

	client := New(server.URL, "test-token")
	body, err := client.StreamGet(context.Background(), "/api/v1/tenants/x/machines/y/logs")
	if err != nil {
		t.Fatalf("StreamGet: %v", err)
	}
	defer func() { _ = body.Close() }()

	data, _ := io.ReadAll(body)
	if !bytes.Contains(data, []byte("data:")) {
		t.Errorf("expected SSE data, got %q", string(data))
	}
}

func TestClient_StreamGet_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"status":"failure","message":"machine disconnected","reason":"MachineDisconnected","code":503}`)
	}))
	defer server.Close()

	client := New(server.URL, "test-token")
	_, err := client.StreamGet(context.Background(), "/api/v1/tenants/x/machines/y/logs")

	var apiErr *ErrorResponse
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *ErrorResponse, got %T: %v", err, err)
	}
	if apiErr.Code != 503 {
		t.Errorf("code = %d, want 503", apiErr.Code)
	}
}
