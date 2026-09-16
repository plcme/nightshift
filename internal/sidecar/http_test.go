package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPICreateTaskAndQueueReply(t *testing.T) {
	store := newTestStore(t)
	controller := NewController(store, fakeQuotaSource{}, &fakeConversationExecutor{}, DefaultHarvestPolicy())
	server := httptest.NewServer(NewAPI(store, controller))
	defer server.Close()

	created := postJSON[SideTask](t, server.URL+"/api/tasks", map[string]any{
		"title": "API task", "prompt": "first turn", "provider_preference": "auto", "label_id": "side", "priority": 50,
	})
	if created.Status != StatusQueued {
		t.Fatalf("created status = %q", created.Status)
	}
	postJSON[Message](t, server.URL+"/api/tasks/"+created.ID+"/replies", map[string]any{"content": "second turn"})

	response, err := http.Get(server.URL + "/api/tasks/" + created.ID + "/messages")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var body struct {
		Messages []Message `json:"messages"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(body.Messages))
	}
}

func postJSON[T any](t *testing.T, url string, value any) T {
	t.Helper()
	data, _ := json.Marshal(value)
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 300 {
		t.Fatalf("unexpected status %d", response.StatusCode)
	}
	var result T
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}
