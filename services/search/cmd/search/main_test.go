package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func TestRunAndConsumeRefuseWithoutDatabaseURL(t *testing.T) {
	none := func(string) string { return "" }
	if err := run(context.Background(), none); err == nil {
		t.Error("run must require DATABASE_URL")
	}
	env := map[string]string{"KAFKA_BROKERS": "localhost:9092"}
	if err := consume(context.Background(), func(k string) string { return env[k] }); err == nil {
		t.Error("consume must require DATABASE_URL")
	}
}

// The consumer's Kafka user holds literal ACLs: a subscribed topic without a
// read entry fails authorization when the consumer starts.
func TestEverySubscribedTopicHasAReadACL(t *testing.T) {
	cfg, err := kafkaConfig(func(k string) string { return map[string]string{"KAFKA_BROKERS": "localhost:9092"}[k] })
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile("../../../../deploy/kafka/search-consumer.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, topic := range cfg.Topics {
		entry := "- resource: {type: topic, name: " + topic + ", patternType: literal}\n        operations: [Read, Describe]"
		if !strings.Contains(string(manifest), entry) {
			t.Errorf("deploy/kafka/search-consumer.yaml grants no read on %s", topic)
		}
	}
}

func TestSeededRequiresTheIndexWriteDuty(t *testing.T) {
	if !seeded(tenant.ServicePrincipalTable()) {
		t.Error("the shipped service principal table must seed search-index")
	}
	if seeded(map[string][]string{"search-index": {"other.duty"}}) || seeded(nil) {
		t.Error("a table without search.index.write must not count as seeded")
	}
}

func TestReadyReportsDependencyHealth(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		code int
		body string
	}{
		"up":   {nil, http.StatusOK, "ready"},
		"down": {errors.New("db down"), http.StatusServiceUnavailable, "unavailable"},
	} {
		rec := httptest.NewRecorder()
		ready(func(context.Context) error { return tc.err })(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		var got map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if rec.Code != tc.code || got["status"] != tc.body {
			t.Errorf("%s: %d %v", name, rec.Code, got)
		}
	}
}

func TestAddrFallsBack(t *testing.T) {
	if got := addr(func(string) string { return "" }, "HTTP_ADDR", ":8080"); got != ":8080" {
		t.Errorf("fallback %q", got)
	}
	if got := addr(func(string) string { return ":1" }, "HTTP_ADDR", ":8080"); got != ":1" {
		t.Errorf("override %q", got)
	}
}

func TestLoggerNamesTheService(t *testing.T) {
	var buf bytes.Buffer
	newLogger(&buf).Info("hello")
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["service"] != "search" || got["message"] != "hello" || got["timestamp"] == nil {
		t.Errorf("log line %v", got)
	}
}
