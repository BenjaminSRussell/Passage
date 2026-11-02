package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEdgeStatus(t *testing.T) {
	now := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }
	cases := []struct {
		name   string
		last   *time.Time
		active bool
		want   string
	}{
		{"never reported", nil, true, EdgePending},
		{"just now", at(5 * time.Second), true, EdgeOnline},
		{"at the window edge", at(EdgeOnlineWindow), true, EdgeOnline},
		{"one second past", at(EdgeOnlineWindow + time.Second), true, EdgeOffline},
		{"switched off wins", at(time.Second), false, EdgeInactive},
		{"switched off, never reported", nil, false, EdgeInactive},
	}
	for _, c := range cases {
		if got := EdgeStatus(now, c.last, c.active); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

const res1 = "0190a1b2-0000-7000-8000-000000000001"

func TestPrepareEdgeConfig_WritesVersionAndKeepsOtherKeys(t *testing.T) {
	raw := `{"version": 99, "opcua": {"endpoint": "opc.tcp://plc1:4840"},
		"machines": [{"machineId":"M1","resourceId":"` + res1 + `"}],
		"tags": [{"address":"ns=2;s=Count","machineId":"M1","kind":"count"},{"address":"ns=2;s=Temp","machineId":"M1","kind":"measurement","name":"temp"}],
		"writeRules": [{"node":"ns=2;s=Setpoint","min":10,"max":20}]}`
	out, fields := PrepareEdgeConfig(json.RawMessage(raw), 3)
	if len(fields) > 0 {
		t.Fatalf("unexpected failures: %v", fields)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if string(doc["version"]) != "3" {
		t.Errorf("version = %s, want the server's 3", doc["version"])
	}
	if !strings.Contains(string(doc["opcua"]), "plc1") {
		t.Errorf("unknown key dropped: %s", out)
	}
}

func TestPrepareEdgeConfig_Refusals(t *testing.T) {
	big := `{"x":"` + strings.Repeat("a", MaxEdgeConfigBytes) + `"}`
	m := `"machines":[{"machineId":"M1","resourceId":"` + res1 + `"}]`
	cases := map[string]struct{ doc, field string }{
		"empty":               {``, "document"},
		"array":               {`[]`, "document"},
		"null":                {`null`, "document"},
		"too big":             {big, "document"},
		"machines not list":   {`{"machines": 4}`, "document.machines"},
		"machine no id":       {`{"machines":[{"resourceId":"` + res1 + `"}]}`, "document.machines"},
		"bad resource":        {`{"machines":[{"machineId":"M1","resourceId":"x"}]}`, "document.machines"},
		"duplicate machine":   {`{"machines":[{"machineId":"M1","resourceId":"` + res1 + `"},{"machineId":"M1","resourceId":"` + res1 + `"}]}`, "document.machines"},
		"tag no address":      {`{` + m + `,"tags":[{"machineId":"M1","kind":"count"}]}`, "document.tags"},
		"tag unknown machine": {`{` + m + `,"tags":[{"address":"a","machineId":"M2","kind":"count"}]}`, "document.tags"},
		"tag unknown kind":    {`{` + m + `,"tags":[{"address":"a","machineId":"M1","kind":"pressure"}]}`, "document.tags"},
		"tag twice":           {`{` + m + `,"tags":[{"address":"a","machineId":"M1","kind":"count"},{"address":"a","machineId":"M1","kind":"state"}]}`, "document.tags"},
		"measurement noname":  {`{` + m + `,"tags":[{"address":"a","machineId":"M1","kind":"measurement"}]}`, "document.tags"},
		"rule min>max":        {`{"writeRules":[{"node":"n","min":5,"max":1}]}`, "document.writeRules"},
		"rule twice":          {`{"writeRules":[{"node":"n","min":1,"max":2},{"node":"n","min":1,"max":2}]}`, "document.writeRules"},
		"rule no node":        {`{"writeRules":[{"min":1,"max":2}]}`, "document.writeRules"},
	}
	for name, c := range cases {
		_, fields := PrepareEdgeConfig(json.RawMessage(c.doc), 1)
		if _, ok := fields[c.field]; !ok {
			t.Errorf("%s: want failure on %s, got %v", name, c.field, fields)
		}
	}
}

func TestValidateMachineStatuses(t *testing.T) {
	if f := ValidateMachineStatuses([]MachineStatus{{"M1", "RUNNING"}}); f != nil {
		t.Errorf("valid list refused: %v", f)
	}
	if f := ValidateMachineStatuses([]MachineStatus{{"", "RUNNING"}}); f == nil {
		t.Error("blank machine code accepted")
	}
	if f := ValidateMachineStatuses(make([]MachineStatus, MaxMachineStatuses+1)); f == nil {
		t.Error("oversized list accepted")
	}
}
