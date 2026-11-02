package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/migration/internal/api"
	"github.com/BenjaminSRussell/passage/services/migration/internal/connector"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/egress"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
	"github.com/BenjaminSRussell/passage/services/migration/internal/target"
	"github.com/BenjaminSRussell/passage/services/migration/internal/target/targettest"
)

type liveRig struct {
	t       *testing.T
	r       http.Handler
	fakes   *targettest.Fakes
	store   *store.Mem
	drop    string
	profile connBody
	project projectBody
}

const liveDuties = "migration.run,migration.connection.write,migration.project.write,tenant.administer"

func newLiveRig(t *testing.T) *liveRig {
	t.Helper()
	drop := t.TempDir()
	return newLiveRigWith(t, connector.Env{DropRoot: drop}, drop, acmeFile)
}

// newLiveRigWith builds a live API over env whose one connection profile is profile, and a migrate project on it.
func newLiveRigWith(t *testing.T, env connector.Env, drop, profile string) *liveRig {
	t.Helper()
	l := &liveRig{t: t, fakes: targettest.Start(t), store: store.NewMem(), drop: drop}
	if err := os.MkdirAll(filepath.Join(l.drop, "acme"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := target.Config{PartyURL: l.fakes.PartyURL, ProductURL: l.fakes.ProductURL, ReceivablesURL: l.fakes.ReceivablesURL, Signer: l.fakes.Signer, Concurrency: 1, Sleep: func(context.Context, time.Duration) error { return nil }}
	router := httpx.NewRouter()
	api.New(l.store, api.NotConfigured).
		WithConnections(testKeys(t), &recordingProber{}).
		WithExtraction(env).
		WithTargets(func(_ context.Context, run api.TargetRun) (domain.TargetLoader, domain.TargetReader, error) {
			return target.New(cfg, target.Run{SourceSystem: run.SourceSystem, LegalEntityID: run.LegalEntityID, Xref: l.store.Xref()}), nil, nil
		}).Register(router)
	l.r = router
	l.profile = createConn(t, l.r, profile)
	rec := do(l.r, http.MethodPost, projectsPath, `{"name":"Acme cutover","projectType":"migrate","sourceSystem":"custom_acme","targetLegalEntityId":"`+leTarget+`","connectionProfileId":"`+l.profile.ID+`"}`, liveDuties, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("project = %d %s", rec.Code, rec.Body)
	}
	l.project = decodeProject(t, rec)
	return l
}

func (l *liveRig) file(name, content string) {
	l.t.Helper()
	if err := os.WriteFile(filepath.Join(l.drop, "acme", name), []byte(content), 0o644); err != nil {
		l.t.Fatal(err)
	}
}

func (l *liveRig) bind(entity, file string, keys string) {
	l.t.Helper()
	rec := do(l.r, http.MethodPost, bindPath, `{"connectionProfileId":"`+l.profile.ID+`","entity":"`+entity+`","keyFields":[`+keys+`],"source":{"file":"`+file+`"}}`, liveDuties, true)
	if rec.Code != http.StatusCreated {
		l.t.Fatalf("binding %s = %d %s", entity, rec.Code, rec.Body)
	}
}

type liveRun struct {
	Run struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Counts  map[string]int
		Rejects []struct {
			SourceKey string
			Rejects   []struct{ Code, Detail string }
		}
	}
}

func (l *liveRig) run(mapping string, withProject bool) (*liveRun, *httptest.ResponseRecorder) {
	l.t.Helper()
	payload := `{"sourceSystem":"custom_acme","mode":"cutover","mapping":` + mapping + `}`
	if withProject {
		payload = `{"sourceSystem":"custom_acme","mode":"cutover","projectId":"` + l.project.ID + `","mapping":` + mapping + `}`
	}
	rec := do(l.r, http.MethodPost, runsPath, payload, liveDuties, true)
	var out liveRun
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return &out, rec
}

const (
	partyMap = `{"entity":"party","version":1,"fields":[{"source":"Name","target":"legalName","type":"string","required":true}]}`
	vendMap  = `{"entity":"vendor","version":1,"fields":[
		{"source":"Party","target":"partyKey","type":"string","required":true},
		{"source":"Acct","target":"accountNumber","type":"string","required":true},
		{"rule":"const","value":"EUR","target":"currencyCode","type":"string"},
		{"rule":"const","value":"00000000-0000-7000-8000-00000000d001","target":"vendorGroupId","type":"string"}]}`
	bomMap = `{"entity":"bom","version":1,"fields":[{"source":"Name","target":"name","type":"string"}]}`
)

func TestLive_FileBindingRunsThroughThePipelineIntoTheOwningServicesAndRerunsIdempotently(t *testing.T) {
	l := newLiveRig(t)
	l.file("parties.csv", "id,Name\nP1,Acme GmbH\nP2,Beta AG\nP3,\n")
	l.file("vendors.csv", "id,Party,Acct\nV1,P1,V-100\nV2,P2,V-200\nV3,P9,V-300\n")
	l.file("boms.csv", "id,Name\nB1,Table\n")
	l.bind("party", "parties.csv", `"id"`)
	l.bind("vendor", "vendors.csv", `"id"`)
	l.bind("bom", "boms.csv", `"id"`)

	// Parties: two load, the nameless row is refused by the mapping before it reaches the target.
	got, rec := l.run(partyMap, true)
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("party run = %d %s", rec.Code, rec.Body)
	}
	if got.Run.Status != "partial" || got.Run.Counts["created"] != 2 || got.Run.Counts["rejected"] != 1 || len(l.fakes.Parties) != 2 {
		t.Fatalf("party run = %+v parties %d (%s)", got.Run, len(l.fakes.Parties), rec.Body)
	}
	call := l.fakes.Calls("party")[0]
	if call.Claims.Le != leTarget || call.Claims.Act != "service:migration-loader" {
		t.Fatalf("project legal entity / principal not used: %+v", call.Claims)
	}
	x, _, _ := l.store.Xref().Get(context.Background(), domain.XrefKey{SourceSystem: "custom_acme", Entity: "party", SourceKey: "P1"})
	if x.TargetVersion != 1 || l.fakes.Parties[x.TargetID] == nil || x.ContentHash == "" {
		t.Fatalf("xref = %+v: legacy key must map to the party the service holds, with its version", x)
	}

	// Vendors resolve their party through the cross-reference; the unknown party is a row reject with a reason.
	got, rec = l.run(vendMap, true)
	if got.Run.Counts["created"] != 2 || got.Run.Counts["rejected"] != 1 || len(l.fakes.Vendors) != 2 {
		t.Fatalf("vendor run = %+v (%s)", got.Run, rec.Body)
	}
	if rej := got.Run.Rejects[0]; rej.SourceKey != "V3" || rej.Rejects[0].Code != target.CodeRefUnresolved {
		t.Fatalf("vendor reject = %+v", rej)
	}

	// An entity with no target API is rejected row by row, never dropped.
	got, rec = l.run(bomMap, true)
	if got.Run.Counts["created"] != 0 || got.Run.Counts["rejected"] != 1 || got.Run.Rejects[0].Rejects[0].Code != target.CodeNotAvailable {
		t.Fatalf("bom run = %+v (%s)", got.Run, rec.Body)
	}

	// The same extract again changes nothing in the targets.
	before := len(l.fakes.Calls(""))
	got, _ = l.run(partyMap, true)
	if got.Run.Counts["unchanged"] != 2 || got.Run.Counts["created"] != 0 || len(l.fakes.Parties) != 2 {
		t.Fatalf("rerun = %+v", got.Run)
	}
	if after := len(l.fakes.Calls("")); after != before {
		t.Fatalf("an unchanged rerun made %d calls", after-before)
	}

	// A changed row updates the same party under its recorded version; it does not create another.
	l.file("parties.csv", "id,Name\nP1,Acme Holding GmbH\nP2,Beta AG\nP3,Gamma\n")
	got, rec = l.run(partyMap, true)
	if got.Run.Status != "succeeded" || got.Run.Counts["updated"] != 1 || got.Run.Counts["created"] != 1 || got.Run.Counts["unchanged"] != 1 || len(l.fakes.Parties) != 3 {
		t.Fatalf("update run = %+v (%s)", got.Run, rec.Body)
	}
	if x2, _, _ := l.store.Xref().Get(context.Background(), domain.XrefKey{SourceSystem: "custom_acme", Entity: "party", SourceKey: "P1"}); x2.TargetID != x.TargetID || x2.TargetVersion != 2 || l.fakes.Parties[x.TargetID].Body["legalName"] != "Acme Holding GmbH" {
		t.Fatalf("P1 after update: %+v", x2)
	}
}

func TestLive_RunNeedsAnActiveBindingAndAConnection(t *testing.T) {
	l := newLiveRig(t)
	l.file("parties.csv", "id,Name\nP1,Acme\n")
	if _, rec := l.run(partyMap, true); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), api.CodeNoBinding) {
		t.Fatalf("no binding = %d %s", rec.Code, rec.Body)
	}
	// Without a project the one active profile of the source system is used; none, or two, is a clear 422.
	other := newLiveRig(t)
	other.file("parties.csv", "id,Name\nP1,Acme\n")
	other.bind("party", "parties.csv", `"id"`)
	withEntity := strings.Replace(partyMap, `"fields":[`, `"fields":[{"rule":"const","value":"`+leTarget+`","target":"legalEntityId","type":"string"},`, 1)
	if got, rec := other.run(withEntity, false); got.Run.Counts["created"] != 1 {
		t.Fatalf("by source system = %d %s", rec.Code, rec.Body)
	}
	createConn(t, other.r, strings.Replace(acmeFile, "Acme drop", "Second drop", 1))
	if _, rec := other.run(withEntity, false); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), api.CodeConnectionAmbiguous) {
		t.Fatalf("two profiles = %d %s", rec.Code, rec.Body)
	}
	if rec := do(other.r, http.MethodPost, runsPath, `{"sourceSystem":"custom_nobody","mode":"cutover","mapping":`+partyMap+`}`, liveDuties, true); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), api.CodeNoConnection) {
		t.Fatalf("no profile = %d %s", rec.Code, rec.Body)
	}
}

func TestLive_TargetOutageFailsTheRunAndRerunCompletesIt(t *testing.T) {
	l := newLiveRig(t)
	l.file("parties.csv", "id,Name\nP1,Acme\nP2,Beta\n")
	l.bind("party", "parties.csv", `"id"`)
	l.fakes.Fail503["POST /v1/party/parties"] = 100
	got, _ := l.run(partyMap, true)
	if got.Run.Counts["created"] != 0 || got.Run.Counts["rejected"] != 2 || got.Run.Rejects[0].Rejects[0].Code != target.CodeUnavailable {
		t.Fatalf("outage run = %+v", got.Run)
	}
	delete(l.fakes.Fail503, "POST /v1/party/parties")
	rec := do(l.r, http.MethodPost, runsPath+"/"+got.Run.ID+":rerun", "", liveDuties, true)
	var again liveRun
	_ = json.Unmarshal(rec.Body.Bytes(), &again)
	if again.Run.Status != "succeeded" || again.Run.Counts["created"] != 2 || len(l.fakes.Parties) != 2 {
		t.Fatalf("rerun = %d %s", rec.Code, rec.Body)
	}
}

func TestLive_CustomerWithoutALegalEntityIsRefusedPerRow(t *testing.T) {
	l := newLiveRig(t)
	l.file("parties.csv", "id,Name\nP1,Acme\n")
	l.bind("party", "parties.csv", `"id"`)
	// A run outside the project has no legal entity unless the row names one.
	got, rec := l.run(partyMap, false)
	if got.Run.Counts["rejected"] != 1 || got.Run.Rejects[0].Rejects[0].Code != target.CodeLegalEntity {
		t.Fatalf("no legal entity = %d %s", rec.Code, rec.Body)
	}
}

func TestLive_AMappingTargetNoLoaderSendsIsRefusedBeforeAnyCall(t *testing.T) {
	l := newLiveRig(t)
	l.file("parties.csv", "id,Name,Changed\nP1,Acme,2020-01-01\n")
	l.bind("party", "parties.csv", `"id"`)
	typo := `{"entity":"party","version":1,"fields":[{"source":"Name","target":"legalName","type":"string"},{"source":"Name","target":"tradeNmae","type":"string"}]}`
	_, rec := l.run(typo, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), api.CodeTargetUnknown) || !strings.Contains(rec.Body.String(), "tradeNmae") {
		t.Fatalf("misspelt target = %d %s", rec.Code, rec.Body)
	}
	if n := len(l.fakes.Calls("")); n != 0 {
		t.Fatalf("a refused mapping reached the targets %d times", n)
	}
	// A field kept for rules and change detection only is named with a leading underscore and never sent.
	kept := `{"entity":"party","version":1,"fields":[{"source":"Name","target":"legalName","type":"string"},{"source":"Changed","target":"_changedAt","type":"date"}]}`
	got, rec := l.run(kept, true)
	if got.Run.Counts["created"] != 1 {
		t.Fatalf("kept field = %d %s", rec.Code, rec.Body)
	}
	if body := l.fakes.Calls("party")[0].Body; body["_changedAt"] != nil {
		t.Fatalf("a local field was sent: %+v", body)
	}
}

// TestLive_SQLBindingLoadsFromAPostgresLegacyDatabase reads the legacy rows from a real Postgres table through the SQL
// connector (driver registry, egress guard, keyset paging, sealed password) and loads them into the target fakes.
func TestLive_SQLBindingLoadsFromAPostgresLegacyDatabase(t *testing.T) {
	legacy := testkit.DB(t)
	ctx := context.Background()
	var schema string
	if err := legacy.QueryRow(ctx, "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`create table legacy_party (party_no text primary key, legal_name text, reg_no text)`,
		`insert into legacy_party values ('P1', 'Acme GmbH', 'HRB 1'), ('P2', 'Beta AG', null), ('P3', null, null)`,
	} {
		if _, err := legacy.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	cc, err := pgconn.ParseConfig(os.Getenv(testkit.EnvURL))
	if err != nil {
		t.Fatal(err)
	}
	// The profile names a private host the operator allowed; the test dialer carries the connection to the test server.
	pol, _ := egress.ParsePolicy("10.77.0.0/24")
	guard := &egress.Guard{Policy: pol, Timeout: 5 * time.Second,
		Resolver: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("10.77.0.5")}, nil
		},
		TestDialFunc: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(cc.Host, strconv.Itoa(int(cc.Port))))
		}}
	profile := fmt.Sprintf(`{"name":"Acme ERP","kind":"sql","sourceSystem":"custom_acme","config":{"driver":"postgres","host":"erp.acme.example","port":%d,"database":%q,"user":%q,"sslmode":"disable"}}`, cc.Port, cc.Database, cc.User)
	l := newLiveRigWith(t, connector.Env{Guard: guard}, t.TempDir(), profile)
	password := cc.Password
	if password == "" {
		password = "unused-by-trust-auth"
	}
	if rec := do(l.r, http.MethodPut, connPath+"/"+l.profile.ID+"/secret", `{"secret":`+strconv.Quote(password)+`}`, liveDuties, true); rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("secret = %d %s", rec.Code, rec.Body)
	}
	rec := do(l.r, http.MethodPost, bindPath, `{"connectionProfileId":"`+l.profile.ID+`","entity":"party","keyFields":["party_no"],"source":{"schema":"`+schema+`","table":"legacy_party"}}`, liveDuties, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("binding = %d %s", rec.Code, rec.Body)
	}
	sqlMap := `{"entity":"party","version":1,"fields":[{"source":"legal_name","target":"legalName","type":"string","required":true},{"source":"reg_no","target":"registrationNumber","type":"string"}]}`
	got, rec := l.run(sqlMap, true)
	if got.Run.Status != "partial" || got.Run.Counts["created"] != 2 || got.Run.Counts["rejected"] != 1 || len(l.fakes.Parties) != 2 {
		t.Fatalf("sql run = %d %s", rec.Code, rec.Body)
	}
	x, found, _ := l.store.Xref().Get(ctx, domain.XrefKey{SourceSystem: "custom_acme", Entity: "party", SourceKey: "P1"})
	if !found || l.fakes.Parties[x.TargetID] == nil || l.fakes.Parties[x.TargetID].Body["registrationNumber"] != "HRB 1" {
		t.Fatalf("P1 = %+v found %v", x, found)
	}
	for _, c := range l.fakes.Calls("party") {
		if c.Claims.Le != leTarget {
			t.Fatalf("loaded outside the project's legal entity: %+v", c.Claims)
		}
	}

	// A change in the legacy table updates the same party on the next run; the rest is unchanged.
	if _, err := legacy.Exec(ctx, `update legacy_party set reg_no = 'HRB 2' where party_no = 'P1'`); err != nil {
		t.Fatal(err)
	}
	got, rec = l.run(sqlMap, true)
	if got.Run.Counts["updated"] != 1 || got.Run.Counts["unchanged"] != 1 || len(l.fakes.Parties) != 2 || l.fakes.Parties[x.TargetID].Body["registrationNumber"] != "HRB 2" {
		t.Fatalf("sql rerun = %d %s", rec.Code, rec.Body)
	}
}
