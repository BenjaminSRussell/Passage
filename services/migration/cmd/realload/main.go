// Command realload is the timed AC1 run with the real loader (#475): the seeded 50k customers, 20k vendors, 100k
// items and 10k BOMs go through the pipeline and target.Loader over HTTP into running party, product and engineering
// services, are read back through their detail routes (target.Reader, the *.import.read duties) and reconciled, then
// re-run with no change. scripts/real-load.sh starts the services on a local Postgres and runs it; the services'
// addresses, the tenant and the seeded reference ids come from flags, the signing key from PASSAGE_SERVICE_SIGNING_KEY.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/migration/internal/ac1load"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
	"github.com/BenjaminSRussell/passage/services/migration/internal/target"
	"github.com/BenjaminSRussell/passage/services/migration/migrations"
)

const sourceSystem = "ax2012"

type config struct {
	partyURL, productURL, engineeringURL   string
	tenantID, legalEntityID                string
	paymentTerm, postingProfile, custGroup string
	vendorGroup                            string
	xrefURL, xrefOwner, report, markdown   string
	commit, note                           string
	scale                                  float64
	concurrency                            int
}

func main() {
	var c config
	flag.StringVar(&c.partyURL, "party", "", "party service base URL")
	flag.StringVar(&c.productURL, "product", "", "product service base URL")
	flag.StringVar(&c.engineeringURL, "engineering", "", "engineering service base URL")
	flag.StringVar(&c.tenantID, "tenant", "00000000-0000-0000-0000-000000000001", "tenant id")
	flag.StringVar(&c.legalEntityID, "legal-entity", "", "legal entity the run loads into")
	flag.StringVar(&c.paymentTerm, "payment-term", "", "seeded payment term id")
	flag.StringVar(&c.postingProfile, "posting-profile", "", "seeded posting profile id")
	flag.StringVar(&c.custGroup, "customer-group", "", "customer group id")
	flag.StringVar(&c.vendorGroup, "vendor-group", "", "vendor group id")
	flag.StringVar(&c.xrefURL, "xref-db", "", "migration database URL for migration.xref (the role must not bypass RLS); empty keeps the cross-reference in memory")
	flag.StringVar(&c.xrefOwner, "xref-owner-db", "", "schema owner URL of the same database, to apply the migrations (default -xref-db)")
	flag.StringVar(&c.report, "report", "", "JSON report path")
	flag.StringVar(&c.markdown, "markdown", "", "Markdown report path")
	flag.StringVar(&c.commit, "commit", "", "commit the measured tree is at")
	flag.StringVar(&c.note, "note", "", "how the services and database were run")
	flag.Float64Var(&c.scale, "scale", 1, "fraction of the AC1 volumes to load")
	flag.IntVar(&c.concurrency, "concurrency", 8, "loader calls in flight")
	flag.Parse()
	if err := run(context.Background(), c); err != nil {
		fmt.Fprintf(os.Stderr, "realload: %v\n", err)
		os.Exit(1)
	}
}

type entityRun struct {
	Entity    string                  `json:"entity"`
	Rows      int                     `json:"rows"`
	ElapsedMs int64                   `json:"elapsedMs"`
	Status    string                  `json:"status"`
	Created   int                     `json:"created"`
	Rejected  int                     `json:"rejected"`
	FirstRej  string                  `json:"firstReject,omitempty"`
	Recon     *domain.ReconcileReport `json:"reconciliation"`
	RerunMs   int64                   `json:"rerunElapsedMs"`
	RerunSame int                     `json:"rerunUnchanged"`
	RerunOK   bool                    `json:"rerunReconciled"`
}

type report struct {
	Criterion   string          `json:"criterion"`
	Issue       int             `json:"issue"`
	Commit      string          `json:"commit"`
	Host        string          `json:"host"`
	Note        string          `json:"note"`
	Volumes     ac1load.Volumes `json:"volumes"`
	Concurrency int             `json:"concurrency"`
	Budget      string          `json:"budget"`
	LoadMs      int64           `json:"loadElapsedMs"`
	TotalMs     int64           `json:"totalElapsedMs"`
	OK          bool            `json:"ok"`
	Entities    []entityRun     `json:"entities"`
	Xref        string          `json:"xref"`
	Started     time.Time       `json:"started"`
}

type rows []domain.Record

func (r rows) Extract(context.Context, string) ([]domain.Record, error) { return r, nil }

// dataset is the AC1 volumes in the loader's field names: a party per customer and per vendor, the roles on them,
// the items, and BOMs whose parent and two components are items.
func dataset(c config, v ac1load.Volumes) []struct {
	entity string
	rows   rows
} {
	parties := make(rows, 0, v.Customers+v.Vendors)
	customers := make(rows, v.Customers)
	vendors := make(rows, v.Vendors)
	for i := range customers {
		key := fmt.Sprintf("C%06d", i)
		parties = append(parties, domain.Record{Key: key, Fields: map[string]any{"legalName": fmt.Sprintf("Customer %d", i), "partyType": "organization"}})
		customers[i] = domain.Record{Key: key, Fields: map[string]any{"partyKey": key, "accountNumber": key, "customerGroupId": c.custGroup, "paymentTermId": c.paymentTerm,
			"postingProfileId": c.postingProfile, "currencyCode": "USD", "creditLimit": fmt.Sprintf("%d.50", 1000+i)}}
	}
	for i := range vendors {
		key := fmt.Sprintf("V%06d", i)
		parties = append(parties, domain.Record{Key: key, Fields: map[string]any{"legalName": fmt.Sprintf("Vendor %d", i), "partyType": "organization"}})
		vendors[i] = domain.Record{Key: key, Fields: map[string]any{"partyKey": key, "accountNumber": key, "vendorGroupId": c.vendorGroup, "currencyCode": "USD"}}
	}
	items := make(rows, v.Items)
	for i := range items {
		key := fmt.Sprintf("I%06d", i)
		items[i] = domain.Record{Key: key, Fields: map[string]any{"productNumber": key, "name": fmt.Sprintf("Item %d", i), "productType": "item", "subtype": "product", "baseUom": "ea"}}
	}
	boms := make(rows, v.BOMs)
	n := max(v.Items, 3)
	for i := range boms {
		parent := i % n
		lines := fmt.Sprintf(`[{"lineNo":10,"componentKey":"I%06d","quantity":"2.5"},{"lineNo":20,"componentKey":"I%06d","quantity":"1"}]`, (parent+1)%n, (parent+2)%n)
		boms[i] = domain.Record{Key: fmt.Sprintf("B%06d", i), Fields: map[string]any{"productKey": fmt.Sprintf("I%06d", parent), "siteId": "S1", "effectiveFrom": "2026-01-01", "lines": lines}}
	}
	return []struct {
		entity string
		rows   rows
	}{{"party", parties}, {"customer", customers}, {"vendor", vendors}, {"item", items}, {"bom", boms}}
}

func run(ctx context.Context, c config) error {
	signer, err := tenant.ServiceSignerFromEnv(os.Getenv)
	if err != nil || signer == nil {
		return fmt.Errorf("PASSAGE_SERVICE_SIGNING_KEY with PASSAGE_SERVICE_NAME=migration is required (%v)", err)
	}
	v := ac1load.FullSize
	if c.scale != 1 {
		v = ac1load.Volumes{Customers: int(float64(v.Customers) * c.scale), Vendors: int(float64(v.Vendors) * c.scale), Items: int(float64(v.Items) * c.scale), BOMs: int(float64(v.BOMs) * c.scale)}
	}
	ctx = tenant.With(ctx, tenant.Principal{TenantID: c.tenantID, Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	var xref domain.XrefStore = domain.NewMemXref()
	where := "memory"
	if c.xrefURL != "" {
		ownerURL := c.xrefOwner
		if ownerURL == "" {
			ownerURL = c.xrefURL
		}
		owner, err := dbx.Connect(ctx, ownerURL)
		if err != nil {
			return err
		}
		err = dbx.Migrate(ctx, owner, migrations.FS)
		owner.Close()
		if err != nil {
			return err
		}
		pool, err := dbx.Connect(ctx, c.xrefURL)
		if err != nil {
			return err
		}
		defer pool.Close()
		xref, where = store.New(pool).Xref(), "postgres migration.xref"
	}
	cfg := target.Config{PartyURL: c.partyURL, ProductURL: c.productURL, EngineeringURL: c.engineeringURL, Signer: signer, Concurrency: c.concurrency}
	loader := target.New(cfg, target.Run{SourceSystem: sourceSystem, LegalEntityID: c.legalEntityID, Xref: xref})
	rep := report{Criterion: "AC1 (real loader)", Issue: 475, Commit: c.commit, Note: c.note, Volumes: v, Concurrency: c.concurrency, Budget: ac1load.Budget.String(), Xref: where, Started: time.Now().UTC()}
	rep.Host, _ = os.Hostname()
	start := time.Now()
	var loadTotal time.Duration
	rep.OK = true
	for _, e := range dataset(c, v) {
		p := &domain.Pipeline{Extractor: e.rows, Loader: loader, Reader: loader.Reader(), Xref: xref, IDGen: ids.New}
		spec := domain.RunSpec{SourceSystem: sourceSystem, Entity: e.entity, Passthrough: true}
		began := time.Now()
		got, err := p.Run(ctx, spec)
		if err != nil {
			return fmt.Errorf("%s: %w", e.entity, err)
		}
		took := time.Since(began)
		loadTotal += took
		er := entityRun{Entity: e.entity, Rows: len(e.rows), ElapsedMs: took.Milliseconds(), Status: got.Status, Created: got.Created, Rejected: len(got.Rejects), Recon: got.Reconciliation}
		if len(got.Rejects) > 0 {
			er.FirstRej = fmt.Sprintf("%+v", got.Rejects[0])
		}
		began = time.Now()
		again, err := p.Run(ctx, spec)
		if err != nil {
			return fmt.Errorf("%s re-run: %w", e.entity, err)
		}
		er.RerunMs, er.RerunSame = time.Since(began).Milliseconds(), again.Unchanged
		er.RerunOK = again.Reconciliation != nil && again.Reconciliation.OK()
		fmt.Fprintf(os.Stderr, "%s: %d rows %s status %s created %d rejected %d reconciled %v; re-run unchanged %d in %dms\n", e.entity, er.Rows, took.Round(time.Millisecond), er.Status, er.Created, er.Rejected, er.Recon != nil && er.Recon.OK(), er.RerunSame, er.RerunMs)
		if got.Status != domain.StatusSucceeded || got.Created != len(e.rows) || er.Recon == nil || !er.Recon.OK() || er.RerunSame != len(e.rows) || !er.RerunOK {
			rep.OK = false
		}
		rep.Entities = append(rep.Entities, er)
	}
	rep.LoadMs, rep.TotalMs = loadTotal.Milliseconds(), time.Since(start).Milliseconds()
	if time.Since(start) >= ac1load.Budget {
		rep.OK = false
	}
	raw, _ := json.MarshalIndent(rep, "", "  ")
	md := markdown(rep)
	if c.report != "" {
		if err := os.WriteFile(c.report, append(raw, '\n'), 0o644); err != nil {
			return err
		}
	}
	if c.markdown != "" {
		if err := os.WriteFile(c.markdown, []byte(md), 0o644); err != nil {
			return err
		}
	}
	fmt.Print(md)
	if !rep.OK {
		return fmt.Errorf("the real-loader run missed the %s budget, had rejects or failed reconciliation", ac1load.Budget)
	}
	return nil
}

func markdown(r report) string {
	var b strings.Builder
	b.WriteString("# Wave 10 AC1 with the real loader\n\n")
	b.WriteString("The AC1 volumes loaded by `target.Loader` over HTTP into running party, product and engineering services, read back through ")
	b.WriteString("their detail routes as `service:migration-loader` (`*.import.read`) and reconciled by the pipeline, then re-run with no change. ")
	b.WriteString("Produced by `scripts/real-load.sh` (`cmd/realload`).\n\n")
	fmt.Fprintf(&b, "**Result: %s**\n\n", map[bool]string{true: "PASS", false: "FAIL"}[r.OK])
	fmt.Fprintf(&b, "- Commit %s, host %s, started %s\n- %s\n", r.Commit, r.Host, r.Started.Format(time.RFC3339), r.Note)
	fmt.Fprintf(&b, "- Cross-reference: %s; loader concurrency %d\n", r.Xref, r.Concurrency)
	fmt.Fprintf(&b, "- Volumes: %d customers, %d vendors, %d items, %d BOMs (%d parties under the roles)\n", r.Volumes.Customers, r.Volumes.Vendors, r.Volumes.Items, r.Volumes.BOMs, r.Volumes.Customers+r.Volumes.Vendors)
	fmt.Fprintf(&b, "- First loads with read-back reconciliation: %s; whole run with the re-runs: %s (budget %s)\n\n", time.Duration(r.LoadMs)*time.Millisecond, time.Duration(r.TotalMs)*time.Millisecond, r.Budget)
	b.WriteString("| entity | rows | load + reconcile | status | created | rejected | reconciliation | re-run unchanged | re-run |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, e := range r.Entities {
		ok := "missing"
		if e.Recon != nil {
			ok = map[bool]string{true: "OK", false: "FAIL"}[e.Recon.OK()]
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %d | %d | %s | %d | %s |\n", e.Entity, e.Rows, time.Duration(e.ElapsedMs)*time.Millisecond, e.Status, e.Created, e.Rejected, ok, e.RerunSame, time.Duration(e.RerunMs)*time.Millisecond)
	}
	for _, e := range r.Entities {
		if e.FirstRej != "" {
			fmt.Fprintf(&b, "\nFirst %s reject: %s\n", e.Entity, e.FirstRej)
		}
	}
	return b.String()
}
