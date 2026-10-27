package optimizer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewClient_EmptyURLIsNotConfigured(t *testing.T) {
	if c := NewClient("  "); c != nil {
		t.Fatal("empty URL must yield no client")
	}
	var nilClient *Client
	if _, err := nilClient.CampaignSequence(context.Background(), [][]int{{0}}, time.Second); err != ErrNotConfigured {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
}

func TestCampaignSequence_PostsMatrixAndDecodesAnswer(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("request body: %v", err)
		}
		_, _ = w.Write([]byte(`{"status":"OPTIMAL","sequence":[1,0],"changeoverMinutes":50}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL + "/")
	res, err := c.CampaignSequence(context.Background(), [][]int{{0, 30, 90}, {0, 0, 20}, {0, 60, 0}}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != CampaignPath {
		t.Fatalf("path = %s, want %s", gotPath, CampaignPath)
	}
	if gotBody["timeLimitSeconds"] != 30.0 || len(gotBody["changeover"].([]any)) != 3 {
		t.Fatalf("body = %v", gotBody)
	}
	if res.Status != "OPTIMAL" || len(res.Sequence) != 2 || res.Sequence[0] != 1 || res.ChangeoverMinutes != 50 {
		t.Fatalf("result = %+v", res)
	}
}

func TestCampaignSequence_HTTPErrorIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusUnprocessableEntity)
	}))
	defer srv.Close()
	if _, err := NewClient(srv.URL).CampaignSequence(context.Background(), [][]int{{0, 1}, {0, 0}}, time.Second); err == nil {
		t.Fatal("422 must surface as an error so the planner keeps the heuristic")
	}
}
