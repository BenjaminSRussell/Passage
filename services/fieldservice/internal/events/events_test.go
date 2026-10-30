package events

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// requiredOf reads the payload "required: [...]" list of a message in events/asyncapi.yaml.
func requiredOf(t *testing.T, message string) []string {
	t.Helper()
	raw, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	at := strings.Index(text, "    "+message+":\n")
	if at < 0 {
		t.Fatalf("message %s not in asyncapi.yaml", message)
	}
	rest := text[at:]
	req := strings.Index(rest, "required: [")
	line := rest[req+len("required: [") : req+strings.Index(rest[req:], "]")]
	var out []string
	for _, f := range strings.Split(line, ",") {
		out = append(out, strings.TrimSpace(f))
	}
	sort.Strings(out)
	return out
}

func keysOf(t *testing.T, v any) []string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The payload structs emit exactly the properties AsyncAPI requires, so a
// consumer validating against the spec accepts every event.
func TestPayloadsMatchAsyncAPI(t *testing.T) {
	if got, want := keysOf(t, CaseOpenedData{}), requiredOf(t, "CaseOpened"); !reflect.DeepEqual(got, want) {
		t.Errorf("CaseOpenedData keys %v, AsyncAPI requires %v", got, want)
	}
	if got, want := keysOf(t, WorkOrderPostedData{}), requiredOf(t, "WorkOrderPosted"); !reflect.DeepEqual(got, want) {
		t.Errorf("WorkOrderPostedData keys %v, AsyncAPI requires %v", got, want)
	}
	if got, want := keysOf(t, SLAWarningData{}), requiredOf(t, "SLAWarning"); !reflect.DeepEqual(got, want) {
		t.Errorf("SLAWarningData keys %v, AsyncAPI requires %v", got, want)
	}
	if got, want := keysOf(t, SLABreachedData{}), requiredOf(t, "SLABreached"); !reflect.DeepEqual(got, want) {
		t.Errorf("SLABreachedData keys %v, AsyncAPI requires %v", got, want)
	}
	if got, want := keysOf(t, ArticlePublishedData{}), requiredOf(t, "ArticlePublished"); !reflect.DeepEqual(got, want) {
		t.Errorf("ArticlePublishedData keys %v, AsyncAPI requires %v", got, want)
	}
	if got, want := keysOf(t, CaseStageChangedData{}), requiredOf(t, "CaseStageChanged"); !reflect.DeepEqual(got, want) {
		t.Errorf("CaseStageChangedData keys %v, AsyncAPI requires %v", got, want)
	}
	if got, want := keysOf(t, SupplierClaimSettledData{}), requiredOf(t, "SupplierClaimSettled"); !reflect.DeepEqual(got, want) {
		t.Errorf("SupplierClaimSettledData keys %v, AsyncAPI requires %v", got, want)
	}
	if SupplierClaimSettled != "dev.passage.service.supplierclaim.settled.v1" {
		t.Errorf("event type %s", SupplierClaimSettled)
	}
	if CaseStageChanged != "dev.passage.case.stage.changed.v1" {
		t.Errorf("event type %s", CaseStageChanged)
	}
	if ArticlePublished != "dev.passage.knowledge.article.published.v1" {
		t.Errorf("event type %s", ArticlePublished)
	}
	if SLAWarning != "dev.passage.service.sla.warning.v1" || SLABreached != "dev.passage.service.sla.breached.v1" {
		t.Errorf("event types %s, %s", SLAWarning, SLABreached)
	}
	if CaseOpened != "dev.passage.service.case.opened.v1" || WorkOrderPosted != "dev.passage.service.workorder.posted.v1" {
		t.Errorf("event types %s, %s", CaseOpened, WorkOrderPosted)
	}
}
