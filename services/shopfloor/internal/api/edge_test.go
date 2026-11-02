package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

const (
	edgeID       = "0195f0a0-0000-7000-8000-000000000051"
	edgeAdmin    = "shopfloor.view,shopfloor.edge.configure"
	edgeConnect  = "shopfloor.edge.connect"
	connectorSub = "edge-plant1"
)

// asConnector is a connector call as the identity bound to the node.
var asConnector = map[string]string{"X-Passage-User": connectorSub}

type edgeFake struct {
	nodeIn  *store.EdgeNodeInput
	ring    string
	status  string
	doc     json.RawMessage
	beat    *store.HeartbeatInput
	beatErr error
	subject string
}

var edgeState = map[*fakeStore]*edgeFake{}

func (f *fakeStore) edge() *edgeFake {
	if edgeState[f] == nil {
		edgeState[f] = &edgeFake{}
	}
	return edgeState[f]
}

func (f *fakeStore) CreateEdgeNode(_ context.Context, in store.EdgeNodeInput, by string, _ store.Idem) (store.EdgeNode, error) {
	f.edge().nodeIn = &in
	return store.EdgeNode{ID: edgeID, Name: in.Name, Platform: in.Platform, Ring: in.Ring, Active: in.Active, Version: 1, Status: domain.EdgePending, CreatedBy: by,
		Machines: []domain.MachineStatus{}}, nil
}

func (f *fakeStore) UpdateEdgeNode(_ context.Context, id string, version int, in store.EdgeNodeInput, by string) (store.EdgeNode, error) {
	f.edge().nodeIn, f.updateVersion = &in, version
	return store.EdgeNode{ID: id, Name: in.Name, Ring: in.Ring, Active: in.Active, Version: version + 1, Machines: []domain.MachineStatus{}}, nil
}

func (f *fakeStore) GetEdgeNode(_ context.Context, id string) (store.EdgeNode, error) {
	return store.EdgeNode{ID: id, Name: "Plant 1", Platform: "k3s", Ring: "canary", Active: true, Version: 4, Status: domain.EdgeOnline, Machines: []domain.MachineStatus{}}, nil
}

func (f *fakeStore) ListEdgeNodes(_ context.Context, ring, status string, _ int) ([]store.EdgeNode, error) {
	f.edge().ring, f.edge().status = ring, status
	return []store.EdgeNode{{ID: edgeID, Name: "Plant 1", Platform: "k3s", Ring: "canary", Active: true, Version: 1, Machines: []domain.MachineStatus{}}}, nil
}

func (f *fakeStore) PublishEdgeConfig(_ context.Context, id string, doc json.RawMessage, by string, _ store.Idem) (store.EdgeConfigVersion, error) {
	f.edge().doc = doc
	return store.EdgeConfigVersion{ID: id, EdgeID: id, Version: 2, Document: doc, PublishedBy: by}, nil
}

func (f *fakeStore) ListEdgeConfigs(context.Context, string, int) ([]store.EdgeConfigVersion, error) {
	return []store.EdgeConfigVersion{{ID: edgeID, EdgeID: edgeID, Version: 1, Document: json.RawMessage(`{"version":1}`)}}, nil
}

func (f *fakeStore) CurrentEdgeConfig(_ context.Context, id, subject string) (store.EdgeConfigVersion, error) {
	f.edge().subject = subject
	return store.EdgeConfigVersion{ID: id, EdgeID: id, Version: 3, Document: json.RawMessage(`{"version":3}`)}, nil
}

func (f *fakeStore) Heartbeat(_ context.Context, id, subject string, in store.HeartbeatInput) (store.HeartbeatResult, error) {
	f.edge().beat, f.edge().subject = &in, subject
	if f.edge().beatErr != nil {
		return store.HeartbeatResult{}, f.edge().beatErr
	}
	three := 3
	return store.HeartbeatResult{EdgeID: id, Ring: "canary", AppliedConfigVersion: in.AppliedConfigVersion, LatestConfigVersion: &three, HeartbeatIntervalSeconds: 30}, nil
}

func TestEdgeNode_CreateUpdateAndRules(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	rec := call(t, h, http.MethodPost, "/v1/shopfloor/edge-nodes", `{"name":" Plant 1 gateway ","platform":"k3s"}`, edgeAdmin)
	in := f.edge().nodeIn
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` || in.Name != "Plant 1 gateway" || in.Ring != domain.RingAll || !in.Active || in.ConnectorSubject != nil {
		t.Fatalf("create (PRD default ring all, unbound): %d %s %+v", rec.Code, rec.Body.String(), in)
	}
	rec = call(t, h, http.MethodPost, "/v1/shopfloor/edge-nodes", `{"name":"E2","platform":"linux","connectorSubject":" edge-plant1 "}`, edgeAdmin)
	if rec.Code != http.StatusCreated || f.edge().nodeIn.ConnectorSubject == nil || *f.edge().nodeIn.ConnectorSubject != "edge-plant1" {
		t.Fatalf("create bound: %d %s", rec.Code, rec.Body.String())
	}
	for name, body := range map[string]string{
		"no name":       `{"platform":"k3s"}`,
		"bad platform":  `{"name":"E","platform":"mainframe"}`,
		"no platform":   `{"name":"E"}`,
		"bad ring":      `{"name":"E","platform":"linux","ring":"beta"}`,
		"bad site":      `{"name":"E","platform":"linux","siteId":"x"}`,
		"unknown field": `{"name":"E","platform":"linux","extra":1}`,
		"blank subject": `{"name":"E","platform":"linux","connectorSubject":" "}`,
		"service actor": `{"name":"E","platform":"linux","connectorSubject":"service:ledger"}`,
	} {
		if rec := call(t, h, http.MethodPost, "/v1/shopfloor/edge-nodes", body, edgeAdmin); rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want a validation error", name, rec.Code)
		}
	}
	path := "/v1/shopfloor/edge-nodes/" + edgeID
	if rec := callWith(t, h, http.MethodPut, path, `{"name":"E","ring":"all"}`, edgeAdmin, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	// A PUT replaces the node, so it names the ring rather than falling back to a default.
	if rec := callWith(t, h, http.MethodPut, path, `{"name":"E"}`, edgeAdmin, map[string]string{"If-Match": `"4"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT without a ring: %d", rec.Code)
	}
	rec = callWith(t, h, http.MethodPut, path, `{"name":"E","ring":"ring1","active":false,"connectorSubject":"edge-plant9"}`, edgeAdmin, map[string]string{"If-Match": `"4"`})
	if rec.Code != http.StatusOK || f.updateVersion != 4 || f.edge().nodeIn.Ring != "ring1" || f.edge().nodeIn.Active || rec.Header().Get("ETag") != `"5"` ||
		*f.edge().nodeIn.ConnectorSubject != "edge-plant9" {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if rec := callWith(t, h, http.MethodPut, "/v1/shopfloor/edge-nodes/x", `{"name":"E","ring":"all"}`, edgeAdmin, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusNotFound || problemCode(t, rec) != store.CodeEdgeNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
}

func TestEdgeNode_ReadsFiltersAndDuties(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/edge-nodes?ring=canary&status=offline", "", "shopfloor.view"); rec.Code != http.StatusOK || f.edge().ring != "canary" || f.edge().status != "offline" {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	for _, q := range []string{"ring=beta", "status=sleeping"} {
		if rec := call(t, h, http.MethodGet, "/v1/shopfloor/edge-nodes?"+q, "", "shopfloor.view"); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/edge-nodes/"+edgeID, "", "shopfloor.view"); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("get: %d", rec.Code)
	}
	// The edge writes need the edge duty: machine and pay setup does not grant them.
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/shopfloor/edge-nodes", `{"name":"E","platform":"k3s"}`},
		{http.MethodPut, "/v1/shopfloor/edge-nodes/" + edgeID, `{"name":"E"}`},
		{http.MethodPost, "/v1/shopfloor/edge-nodes/" + edgeID + "/config-versions", `{"document":{}}`},
	} {
		if rec := callWith(t, h, c.method, c.path, c.body, "shopfloor.view,shopfloor.setup.maintain,shopfloor.machine.ingest", map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s without the edge duty: %d", c.method, c.path, rec.Code)
		}
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/edge-nodes", "", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("list without a duty: %d", rec.Code)
	}
	// A connector pulls and reports with the connect duty only; neither the fleet screens nor
	// the machine-event ingest identity can read a node's configuration.
	for _, duties := range []string{"shopfloor.view,shopfloor.edge.configure", "shopfloor.machine.ingest"} {
		if rec := callWith(t, h, http.MethodGet, "/v1/shopfloor/edge-nodes/"+edgeID+"/config", "", duties, asConnector); rec.Code != http.StatusForbidden {
			t.Fatalf("pull with %s: %d", duties, rec.Code)
		}
	}
	if rec := callWith(t, h, http.MethodGet, "/v1/shopfloor/edge-nodes/"+edgeID+"/config", "", edgeConnect, asConnector); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` || f.edge().subject != connectorSub {
		t.Fatalf("pull: %d %s, subject %q", rec.Code, rec.Body.String(), f.edge().subject)
	}
}

func TestEdgeConfig_PublishValidatesDocument(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	path := "/v1/shopfloor/edge-nodes/" + edgeID + "/config-versions"
	good := `{"document":{"machines":[{"machineId":"M1","resourceId":"0195f0a0-0000-7000-8000-000000000061"}],"tags":[{"address":"ns=2;s=C","machineId":"M1","kind":"count"}]}}`
	if rec := call(t, h, http.MethodPost, path, good, edgeAdmin); rec.Code != http.StatusCreated || len(f.edge().doc) == 0 {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body.String())
	}
	for name, body := range map[string]string{
		"no document":     `{}`,
		"not an object":   `{"document":[1]}`,
		"unknown machine": `{"document":{"tags":[{"address":"a","machineId":"M9","kind":"count"}]}}`,
		"bad write rule":  `{"document":{"writeRules":[{"node":"n","min":9,"max":1}]}}`,
	} {
		rec := call(t, h, http.MethodPost, path, body, edgeAdmin)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d %s, want a validation error", name, rec.Code, rec.Body.String())
		}
	}
	if rec := call(t, h, http.MethodPost, path, good, "shopfloor.view"); rec.Code != http.StatusForbidden {
		t.Fatalf("publish without the setup duty: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, path, "", "shopfloor.view"); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
}

func TestEdgeHeartbeat_ValidationAndResult(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	path := "/v1/shopfloor/edge-nodes/" + edgeID + ":heartbeat"
	rec := callWith(t, h, http.MethodPost, path, `{"connectorVersion":"1.4.2","bufferFillPct":"12.5","appliedConfigVersion":2,"machines":[{"machineCode":"M1","state":"RUNNING"}],"certExpiresAt":"2027-01-30T00:00:00Z"}`, edgeConnect, asConnector)
	if rec.Code != http.StatusOK || f.edge().subject != connectorSub {
		t.Fatalf("heartbeat: %d %s, subject %q", rec.Code, rec.Body.String(), f.edge().subject)
	}
	b := f.edge().beat
	if b.BufferFillPct.String() != "12.5" || *b.AppliedConfigVersion != 2 || len(b.Machines) != 1 || b.CertExpiresAt == nil || !b.CertExpiresAt.Equal(time.Date(2027, 1, 30, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("stored = %+v", b)
	}
	var out store.HeartbeatResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.HeartbeatIntervalSeconds != 30 || *out.LatestConfigVersion != 3 {
		t.Fatalf("result = %s", rec.Body.String())
	}
	for name, body := range map[string]string{
		"no fill":         `{}`,
		"negative fill":   `{"bufferFillPct":"-1"}`,
		"over 100":        `{"bufferFillPct":"100.01"}`,
		"three decimals":  `{"bufferFillPct":"1.234"}`,
		"version zero":    `{"bufferFillPct":"1","appliedConfigVersion":0}`,
		"blank machine":   `{"bufferFillPct":"1","machines":[{"machineCode":"","state":"RUNNING"}]}`,
		"empty connector": `{"bufferFillPct":"1","connectorVersion":""}`,
	} {
		if rec := callWith(t, h, http.MethodPost, path, body, edgeConnect, asConnector); rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want a validation error", name, rec.Code)
		}
	}
	for _, duties := range []string{"shopfloor.setup.maintain,shopfloor.edge.configure", "shopfloor.machine.ingest"} {
		if rec := callWith(t, h, http.MethodPost, path, `{"bufferFillPct":"1"}`, duties, asConnector); rec.Code != http.StatusForbidden {
			t.Fatalf("heartbeat with %s: %d", duties, rec.Code)
		}
	}
	// The store's refusal of a caller that is not the bound connector reaches the connector.
	f.edge().beatErr = httpx.NewProblem(http.StatusForbidden, store.CodeEdgeNotConnector, "not this node's connector")
	if rec := callWith(t, h, http.MethodPost, path, `{"bufferFillPct":"1"}`, edgeConnect, map[string]string{"X-Passage-User": "edge-plant2"}); rec.Code != http.StatusForbidden ||
		problemCode(t, rec) != store.CodeEdgeNotConnector || f.edge().subject != "edge-plant2" {
		t.Fatalf("other connector: %d %s", rec.Code, rec.Body.String())
	}
}
