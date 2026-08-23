package apimart

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientPollNormalizesPendingAndCompletedImages(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/tasks/task-1" {
			t.Fatalf("unexpected poll request: %s %s", r.Method, r.URL.RequestURI())
		}
		if r.URL.Query().Get("language") != "en" {
			t.Fatalf("poll language query missing: %s", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer provider-key" {
			t.Fatal("poll authorization header missing")
		}
		if calls.Add(1) == 1 {
			_, _ = io.WriteString(w, `{"code":200,"data":{"status":"processing","progress":"40%"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"code":200,"data":{"status":"completed","result":{"images":[{"url":["https://example.test/one.png","https://example.test/two.png"]}]}}}`)
	}))
	defer server.Close()

	task, err := NewTaskRef("task-1")
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(Config{MaxPollAttempts: 2, InitialBackoff: time.Nanosecond, MaxBackoff: time.Nanosecond, RequestTimeout: time.Second})
	result, err := client.Poll(context.Background(), PollRequest{BaseURL: server.URL, APIKey: "provider-key", Task: task})
	if err != nil {
		t.Fatalf("Poll returned error: %v", err)
	}
	if result.Status != StatusCompleted || len(result.Images) != 2 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if calls.Load() != 2 {
		t.Fatalf("poll count = %d, want 2", calls.Load())
	}
}

func TestClientPollRetriesTransientGetWithoutReposting(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method %s", r.Method)
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":{"message":"temporary failure"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"status":"completed","result":{"images":[{"url":"https://example.test/image.png"}]}}}`)
	}))
	defer server.Close()

	task, _ := NewTaskRef("task-2")
	client := NewClient(Config{MaxPollAttempts: 2, InitialBackoff: time.Nanosecond, MaxBackoff: time.Nanosecond, RequestTimeout: time.Second})
	result, err := client.Poll(context.Background(), PollRequest{BaseURL: server.URL, APIKey: "provider-key", Task: task})
	if err != nil {
		t.Fatalf("Poll returned error: %v", err)
	}
	if result.Status != StatusCompleted || len(result.Images) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if calls.Load() != 2 {
		t.Fatalf("GET count = %d, want 2", calls.Load())
	}
}

func TestClientSubmitNeverRetriesAfterTransportOrHTTPFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/images/generations" {
			t.Fatalf("unexpected submit request: %s %s", r.Method, r.URL.Path)
		}
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":{"message":"provider task id task-secret"}}`)
	}))
	defer server.Close()

	client := NewClient(Config{RequestTimeout: time.Second})
	_, err := client.Submit(context.Background(), SubmitRequest{
		BaseURL: server.URL,
		APIKey:  "provider-key",
		Body:    []byte(`{"prompt":"a cat","model":"gpt-image-2"}`),
	})
	if err == nil {
		t.Fatal("Submit unexpectedly succeeded")
	}
	if calls.Load() != 1 {
		t.Fatalf("POST count = %d, want 1", calls.Load())
	}
	if strings.Contains(err.Error(), "task-secret") {
		t.Fatalf("provider task id leaked in error: %v", err)
	}
}

func TestTaskRefIsOpaqueToJSON(t *testing.T) {
	task, err := NewTaskRef("provider-task-secret")
	if err != nil {
		t.Fatal(err)
	}
	if task.ID() != "provider-task-secret" {
		t.Fatal("explicit ID extraction failed")
	}
	if strings.Contains(string(mustJSON(task)), "provider-task-secret") {
		t.Fatal("provider task id should not be JSON-marshallable")
	}
}

func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
