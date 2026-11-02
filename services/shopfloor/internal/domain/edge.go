package domain

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// Edge node platforms and rollout rings (FR-25.19).
const (
	PlatformK3s     = "k3s"
	PlatformWindows = "windows"
	PlatformLinux   = "linux"

	RingCanary = "canary"
	RingOne    = "ring1"
	RingAll    = "all"
)

// Edge node statuses as the fleet screen shows them.
const (
	EdgePending  = "pending"  // never sent a heartbeat
	EdgeOnline   = "online"   // heartbeat within EdgeOnlineWindow
	EdgeOffline  = "offline"  // last heartbeat older than the window
	EdgeInactive = "inactive" // switched off by a supervisor
)

const (
	// HeartbeatInterval is how often a connector reports in (FR-25.19).
	HeartbeatInterval = 30 * time.Second
	// EdgeOnlineWindow is three missed heartbeats: older than this and the node is offline.
	EdgeOnlineWindow = 3 * HeartbeatInterval
	// MaxEdgeConfigBytes bounds one configuration document.
	MaxEdgeConfigBytes = 256 * 1024
	// MaxMachineStatuses bounds the per-machine statuses of one heartbeat.
	MaxMachineStatuses = 500
)

// ValidEdgePlatform reports whether p is a supported connector platform.
func ValidEdgePlatform(p string) bool {
	return p == PlatformK3s || p == PlatformWindows || p == PlatformLinux
}

// ValidRing reports whether r is a rollout ring.
func ValidRing(r string) bool { return r == RingCanary || r == RingOne || r == RingAll }

// EdgeStatus derives the status of a node from its last heartbeat. It is computed when read,
// so a connector that goes quiet turns offline without anything having to run.
func EdgeStatus(now time.Time, last *time.Time, active bool) string {
	switch {
	case !active:
		return EdgeInactive
	case last == nil:
		return EdgePending
	case now.Sub(*last) > EdgeOnlineWindow:
		return EdgeOffline
	}
	return EdgeOnline
}

var edgeKinds = map[string]bool{"count": true, "state": true, "downtime": true, "alarm": true, "measurement": true}

// PrepareEdgeConfig validates a configuration document and returns it with the server's
// version number written into it. The document is the one the connector runs with: machines
// (machineId, resourceId UUID), tags (address, machineId, kind, name for a measurement) and
// writeRules (the write allowlist, node with finite min <= max). Other keys (OPC-UA endpoints,
// MQTT topics) pass through untouched. The returned map names the fields that failed.
func PrepareEdgeConfig(raw json.RawMessage, version int) (json.RawMessage, map[string]string) {
	fail := func(field, msg string) (json.RawMessage, map[string]string) {
		return nil, map[string]string{field: msg}
	}
	if len(raw) == 0 || len(raw) > MaxEdgeConfigBytes {
		return fail("document", fmt.Sprintf("is required and at most %d bytes", MaxEdgeConfigBytes))
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
		return fail("document", "must be a JSON object")
	}
	var machines []struct {
		MachineID  string `json:"machineId"`
		ResourceID string `json:"resourceId"`
	}
	var tags []struct {
		Address   string `json:"address"`
		MachineID string `json:"machineId"`
		Kind      string `json:"kind"`
		Name      string `json:"name"`
	}
	var rules []struct {
		Node string  `json:"node"`
		Min  float64 `json:"min"`
		Max  float64 `json:"max"`
	}
	for key, dst := range map[string]any{"machines": &machines, "tags": &tags, "writeRules": &rules} {
		if v, ok := doc[key]; ok {
			if err := json.Unmarshal(v, dst); err != nil {
				return fail("document."+key, "must be a list of objects with the documented fields")
			}
		}
	}
	known := map[string]bool{}
	for i, m := range machines {
		switch {
		case m.MachineID == "":
			return fail("document.machines", fmt.Sprintf("machine %d needs a machineId", i+1))
		case !ids.Valid(m.ResourceID):
			return fail("document.machines", fmt.Sprintf("resourceId of machine %s must be a UUID", m.MachineID))
		case known[m.MachineID]:
			return fail("document.machines", fmt.Sprintf("machine %s is listed twice", m.MachineID))
		}
		known[m.MachineID] = true
	}
	seen := map[string]bool{}
	for _, t := range tags {
		switch {
		case t.Address == "":
			return fail("document.tags", "tag address is required")
		case !known[t.MachineID]:
			return fail("document.tags", fmt.Sprintf("tag %s points at unknown machine %q", t.Address, t.MachineID))
		case seen[t.Address]:
			return fail("document.tags", fmt.Sprintf("tag address %s is mapped twice", t.Address))
		case !edgeKinds[t.Kind]:
			return fail("document.tags", fmt.Sprintf("tag %s has unknown kind %q", t.Address, t.Kind))
		case t.Kind == "measurement" && t.Name == "":
			return fail("document.tags", fmt.Sprintf("measurement tag %s needs a name", t.Address))
		}
		seen[t.Address] = true
	}
	nodes := map[string]bool{}
	for _, w := range rules {
		switch {
		case w.Node == "":
			return fail("document.writeRules", "write rule node is required")
		case nodes[w.Node]:
			return fail("document.writeRules", fmt.Sprintf("write rule for node %s is listed twice", w.Node))
		case math.IsNaN(w.Min) || math.IsInf(w.Min, 0) || math.IsNaN(w.Max) || math.IsInf(w.Max, 0) || w.Min > w.Max:
			return fail("document.writeRules", fmt.Sprintf("write rule for node %s needs finite limits with min <= max", w.Node))
		}
		nodes[w.Node] = true
	}
	v, _ := json.Marshal(version)
	doc["version"] = v
	out, err := json.Marshal(doc)
	if err != nil {
		return fail("document", "must be a JSON object")
	}
	return out, nil
}

// MachineStatus is one machine's state as a connector reports it in a heartbeat.
type MachineStatus struct {
	MachineCode string `json:"machineCode"`
	State       string `json:"state"`
}

// ValidateMachineStatuses checks the per-machine statuses of a heartbeat.
func ValidateMachineStatuses(list []MachineStatus) map[string]string {
	if len(list) > MaxMachineStatuses {
		return map[string]string{"machines": fmt.Sprintf("at most %d machines per heartbeat", MaxMachineStatuses)}
	}
	for _, m := range list {
		if m.MachineCode == "" || len(m.MachineCode) > 80 || m.State == "" || len(m.State) > 40 {
			return map[string]string{"machines": "each machine needs a machineCode (1 to 80 characters) and a state (1 to 40)"}
		}
	}
	return nil
}

// WriteRuleNodes lists the nodes of a validated document's write allowlist, in document
// order, so the publish event records which machine nodes the version lets the connector write.
func WriteRuleNodes(doc json.RawMessage) []string {
	var d struct {
		WriteRules []struct {
			Node string `json:"node"`
		} `json:"writeRules"`
	}
	out := []string{}
	if json.Unmarshal(doc, &d) == nil {
		for _, w := range d.WriteRules {
			out = append(out, w.Node)
		}
	}
	return out
}
