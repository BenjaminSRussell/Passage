package store_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
	"github.com/BenjaminSRussell/passage/services/engineering/migrations"
)

// Legal entities are canonical UUIDs, the only form a platform principal's scope carries.
const (
	leENG   = "00000000-0000-0000-00e1-000000000001"
	leDEMF  = "00000000-0000-0000-00de-000000000002"
	leUSMF  = "00000000-0000-0000-00a5-000000000003"
	leOTHER = "00000000-0000-0000-00ff-000000000004"
)

func leContext(n int, user, legalEntities string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), UserID: user, LegalEntityID: legalEntities})
}

func newAppStore(t *testing.T, n int) (*store.Postgres, *pgxpool.Pool, *pgxpool.Pool, context.Context) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	ctx := principalContext(n)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return store.New(app), admin, app, ctx
}

func rowCount(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

type releaseFixture struct {
	policyID string
	bomID    string
}

func seedRelease(t *testing.T, s *store.Postgres, ctx context.Context, engineeringSite string, policy domain.ReleasePolicy) releaseFixture {
	t.Helper()
	created, err := s.CreateReleasePolicy(ctx, policy, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []domain.SiteMapping{
		{EngineeringLEID: leENG, OperationalLEID: leDEMF, EngineeringSite: engineeringSite, OperationalSite: "DEMF-S"},
		{EngineeringLEID: leENG, OperationalLEID: leUSMF, EngineeringSite: engineeringSite, OperationalSite: "USMF-S"},
	} {
		if err := s.PutSiteMapping(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	bom := version("P", domain.StatusActive, releaseDay, "WHEEL")
	bom.SiteID = "ENG-S"
	if _, err := s.CreateBomVersion(ctx, bom, store.Idem{}, noCheck); err != nil {
		t.Fatal(err)
	}
	route := newRoute("P", "ENG-S", 1, releaseDay, routeOp(10, 0))
	if _, err := s.CreateRouteVersion(ctx, route, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateRouteVersion(ctx, route.ID); err != nil {
		t.Fatal(err)
	}
	return releaseFixture{policyID: created.ID, bomID: bom.ID}
}

func fullPolicy(code string, acceptance map[string]bool) domain.ReleasePolicy {
	return domain.ReleasePolicy{Code: code, CopyBOM: true, ActivateBOM: true, CopyRoute: true, ActivateRoute: true, RequireAcceptance: acceptance}
}

func releaseInput(number, policyID string, legalEntities ...string) store.ReleaseSessionInput {
	return store.ReleaseSessionInput{Number: number, CreatedBy: "rita", PolicyID: policyID, EngineeringLEID: leENG, ProductID: "P", VersionCode: "V-02", LegalEntities: legalEntities}
}

func TestPostgres_AC911_ReleaseAcceptanceSplit(t *testing.T) {
	s, _, setup := newStore(t, 1)
	fx := seedRelease(t, s, setup, "ENG-S", fullPolicy("RP-1", map[string]bool{leUSMF: true, leDEMF: false}))
	creator := leContext(1, "rita", leENG+","+leDEMF)

	rec, err := s.CreateReleaseSession(creator, releaseInput("REL-2026-00001", fx.policyID, leDEMF, leUSMF), store.Idem{Key: "rel", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Session.EngineeringLEID != leENG {
		t.Fatalf("session = %+v; want the engineering entity stored", rec.Session)
	}
	byLE := map[string]domain.ReleaseLine{}
	for _, l := range rec.Lines {
		byLE[l.LegalEntityID] = l
	}
	if byLE[leDEMF].Status != domain.LineAccepted || byLE[leDEMF].CopiedBOMVersionID == "" || byLE[leDEMF].CopiedRouteVersionID == "" {
		t.Fatalf("DEMF = %+v; want accepted with copied BOM and route", byLE[leDEMF])
	}
	copied, err := s.GetBomVersion(creator, byLE[leDEMF].CopiedBOMVersionID)
	if err != nil || copied.Status != domain.StatusActive || copied.SiteID != "DEMF-S" || copied.CopiedFrom != fx.bomID {
		t.Fatalf("DEMF BOM = %+v, %v; want active on DEMF-S copied from the engineering BOM", copied, err)
	}
	if byLE[leUSMF].Status != domain.LineSent {
		t.Fatalf("USMF = %s; want sent (open product releases)", byLE[leUSMF].Status)
	}
	open, err := s.ListReleaseLines(creator, store.ReleaseLineFilter{Status: domain.LineSent}, "", store.MaxPageSize)
	if err != nil || len(open.Items) != 1 || open.Items[0].LegalEntityID != leUSMF {
		t.Fatalf("open releases = %+v, %v", open, err)
	}

	accepted, err := s.DecideReleaseLine(leContext(1, "bob", leUSMF), byLE[leUSMF].ID, "bob", "", true)
	if err != nil || accepted.Status != domain.LineAccepted || accepted.CopiedBOMVersionID == "" {
		t.Fatalf("USMF accept = %+v, %v", accepted, err)
	}
	usmf, err := s.GetBomVersion(creator, accepted.CopiedBOMVersionID)
	if err != nil || usmf.SiteID != "USMF-S" || usmf.Status != domain.StatusActive {
		t.Fatalf("USMF BOM = %+v, %v; want the mapped site USMF-S, not the legal entity id", usmf, err)
	}
	products, err := s.ListReleasedProducts(creator, leUSMF, "", store.MaxPageSize)
	if err != nil || len(products.Items) != 1 || products.Items[0].ProductID != "P" {
		t.Fatalf("USMF released products = %+v, %v", products, err)
	}
}

func TestPostgres_ReleaseIsScopedToTheCallersLegalEntities(t *testing.T) {
	s, pool, setup := newStore(t, 1)
	fx := seedRelease(t, s, setup, "", fullPolicy("RP-S", map[string]bool{leUSMF: true}))

	if _, err := s.CreateReleaseSession(leContext(1, "rita", leENG), releaseInput("REL-A", fx.policyID, leDEMF), store.Idem{}); !domain.HasCode(err, domain.CodeReleaseScope) {
		t.Fatalf("auto-accepting into an entity outside the caller's scope = %v; want %s", err, domain.CodeReleaseScope)
	}
	if _, err := s.CreateReleaseSession(leContext(1, "rita", leOTHER), releaseInput("REL-B", fx.policyID, leUSMF), store.Idem{}); !domain.HasCode(err, domain.CodeReleaseScope) {
		t.Fatalf("releasing from an engineering entity outside the caller's scope = %v; want %s", err, domain.CodeReleaseScope)
	}
	if n := rowCount(t, pool, `select count(*) from release_session`); n != 0 {
		t.Fatalf("%d sessions persisted by refused requests", n)
	}
	rec, err := s.CreateReleaseSession(leContext(1, "rita", leENG), releaseInput("REL-C", fx.policyID, leUSMF), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	line := rec.Lines[0].ID
	for _, accept := range []bool{true, false} {
		if _, err := s.DecideReleaseLine(leContext(1, "mallory", leDEMF), line, "mallory", "no", accept); !domain.HasCode(err, domain.CodeReleaseScope) {
			t.Fatalf("accept=%v from another entity = %v; want %s", accept, err, domain.CodeReleaseScope)
		}
	}
	if _, err := s.DecideReleaseLine(principalContext(1), line, "nobody", "", true); !domain.HasCode(err, domain.CodeReleaseScope) {
		t.Fatalf("a caller with no entity claim and no tenant administration = %v; want %s", err, domain.CodeReleaseScope)
	}
	if got := rowCount(t, pool, `select count(*) from release_line where status = 'sent'`); got != 1 {
		t.Fatalf("the line must stay sent, %d are", got)
	}
}

func TestPostgres_ReReleaseExpiresThePreviousCopy(t *testing.T) {
	s, pool, setup := newStore(t, 1)
	fx := seedRelease(t, s, setup, "", fullPolicy("RP-R", map[string]bool{}))
	ctx := leContext(1, "rita", leENG+","+leDEMF)
	first, err := s.CreateReleaseSession(ctx, releaseInput("REL-1", fx.policyID, leDEMF), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateReleaseSession(ctx, releaseInput("REL-2", fx.policyID, leDEMF), store.Idem{})
	if err != nil {
		t.Fatalf("re-release = %v; want the previous operational copy expired, not ENG-BOM-OVERLAP", err)
	}
	for _, q := range []string{
		`select count(*) from bom_version where product_id = 'P' and site_id = 'DEMF-S' and status = 'active'`,
		`select count(*) from route_version where product_id = 'P' and site_id = 'DEMF-S' and status = 'active'`,
	} {
		if n := rowCount(t, pool, q); n != 1 {
			t.Fatalf("%s = %d; want exactly one active copy", q, n)
		}
	}
	if n := rowCount(t, pool, `select count(*) from bom_version where id = $1::uuid and status = 'expired'`, first.Lines[0].CopiedBOMVersionID); n != 1 {
		t.Fatal("the first copy must be expired")
	}
	newest, err := s.GetBomVersion(ctx, second.Lines[0].CopiedBOMVersionID)
	if err != nil || newest.CopiedFrom != fx.bomID || newest.Status != domain.StatusActive {
		t.Fatalf("second copy = %+v, %v; want active and sourced from the engineering BOM, not the first copy", newest, err)
	}
}

func TestPostgres_DraftCopiesDoNotPassReadiness(t *testing.T) {
	s, _, setup := newStore(t, 1)
	rdy, err := s.CreateReadinessPolicy(setup, domain.ReadinessPolicy{Code: "RDY-A", Checks: []domain.ReadinessCheck{{Key: "active_bom", Kind: domain.CheckAuto, Mandatory: true}}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	policy := fullPolicy("RP-D", map[string]bool{})
	policy.ActivateBOM, policy.CopyRoute = false, false
	fx := seedRelease(t, s, setup, "", policy)
	in := releaseInput("REL-D", fx.policyID, leDEMF)
	in.ReadinessPolicyID = rdy.ID
	rec, err := s.CreateReleaseSession(leContext(1, "rita", leENG+","+leDEMF), in, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := s.GetBomVersion(setup, rec.Lines[0].CopiedBOMVersionID)
	if err != nil || draft.Status != domain.StatusDraft {
		t.Fatalf("copy = %+v, %v; want a draft", draft, err)
	}
	page, err := s.ListReleasedProducts(setup, leDEMF, "", store.MaxPageSize)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("released = %+v, %v", page, err)
	}
	got, err := s.GetReleasedProduct(setup, page.Items[0].ID)
	if err != nil || got.State != domain.ReadinessPending || len(got.Checks) != 1 || got.Checks[0].Status != domain.CheckPending {
		t.Fatalf("released product = %+v, %v; a draft BOM must not satisfy active_bom", got, err)
	}
}

func TestPostgres_RouteActivationFailureRefusesTheRelease(t *testing.T) {
	s, pool, setup := newStore(t, 1)
	fx := seedRelease(t, s, setup, "", fullPolicy("RP-F", map[string]bool{}))
	blocker := newRoute("P", "DEMF-S", 1, releaseDay, routeOp(10, 0))
	if _, err := s.CreateRouteVersion(setup, blocker, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateRouteVersion(setup, blocker.ID); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateReleaseSession(leContext(1, "rita", leENG+","+leDEMF), releaseInput("REL-F", fx.policyID, leDEMF), store.Idem{})
	if !domain.HasCode(err, domain.CodeRouteOverlap) {
		t.Fatalf("release onto a site with its own active route = %v; want %s, not a silent draft", err, domain.CodeRouteOverlap)
	}
	if n := rowCount(t, pool, `select count(*) from release_session`) + rowCount(t, pool, `select count(*) from released_product`); n != 0 {
		t.Fatalf("%d rows persisted by a refused release", n)
	}
}

func TestPostgres_AC912_ReadinessBlocksThenActivates(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	policy := domain.ReadinessPolicy{Code: "RDY", TargetState: domain.ReleasedActive, Checks: []domain.ReadinessCheck{
		{Key: "active_bom", Kind: domain.CheckAuto, Mandatory: true},
		{Key: "tax_group", Kind: domain.CheckAuto, Mandatory: true},
		{Key: "warehouse_setup", Kind: domain.CheckAuto, Mandatory: true},
		{Key: "quality_signoff", Kind: domain.CheckManual, Mandatory: true},
		{Key: "safety_signoff", Kind: domain.CheckManual, Mandatory: true},
	}}
	rdy, err := s.CreateReadinessPolicy(ctx, policy, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	rel, err := s.CreateReleasePolicy(ctx, domain.ReleasePolicy{Code: "RP", CopyBOM: true, ActivateBOM: true, RequireAcceptance: map[string]bool{}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	bom := version("P", domain.StatusActive, releaseDay, "WHEEL")
	if _, err := s.CreateBomVersion(ctx, bom, store.Idem{}, noCheck); err != nil {
		t.Fatal(err)
	}
	in := store.ReleaseSessionInput{Number: "REL-1", CreatedBy: "rita", PolicyID: rel.ID, EngineeringLEID: leENG, ProductID: "P", VersionCode: "V-01", LegalEntities: []string{leUSMF}, ReadinessPolicyID: rdy.ID}
	session, err := s.CreateReleaseSession(leContext(1, "rita", leENG+","+leUSMF), in, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if session.Session.ReadinessPolicyID != rdy.ID {
		t.Fatalf("session = %+v; want the readiness policy stored", session.Session)
	}
	page, err := s.ListReleasedProducts(ctx, leUSMF, "", store.MaxPageSize)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("released = %+v, %v", page, err)
	}
	got, err := s.GetReleasedProduct(ctx, page.Items[0].ID)
	if err != nil || got.State != domain.ReadinessPending {
		t.Fatalf("state = %+v, %v; want %s", got, err, domain.ReadinessPending)
	}
	if code := domain.ProcessPolicy(got.State, "sales_order"); code != domain.CodeLifecycleBlocked {
		t.Fatalf("sales order policy = %q; want %s", code, domain.CodeLifecycleBlocked)
	}
	if _, err := s.SignReadinessCheck(leContext(1, "carol", leDEMF), got.ID, "quality_signoff", "carol"); !domain.HasCode(err, domain.CodeReleaseScope) {
		t.Fatalf("sign from another legal entity = %v; want %s", err, domain.CodeReleaseScope)
	}
	receiver := leContext(1, "carol", leUSMF)
	if _, err := s.SignReadinessCheck(receiver, got.ID, "quality_signoff", "rita"); !domain.HasCode(err, domain.CodeReadinessSelfSign) {
		t.Fatalf("creator sign = %v; want %s", err, domain.CodeReadinessSelfSign)
	}
	half, err := s.SignReadinessCheck(receiver, got.ID, "quality_signoff", "carol")
	if err != nil || half.State != domain.ReadinessPending {
		t.Fatalf("after one of two signatures = %+v, %v; the session's readiness policy must keep it pending", half, err)
	}
	signed, err := s.SignReadinessCheck(receiver, got.ID, "safety_signoff", "carol")
	if err != nil || signed.State != domain.ReleasedActive {
		t.Fatalf("after both signatures = %+v, %v; want Active", signed, err)
	}
	if code := domain.ProcessPolicy(signed.State, "sales_order"); code != "" {
		t.Fatalf("active product still blocked: %s", code)
	}
	// The AsyncAPI payloads (camelCase), not Go structs; the move to Active is published too.
	if n := rowCount(t, pool, `select count(*) from outbox where type = 'dev.passage.engineering.releasedproduct.changed.v1' and payload->'data'->>'releasedProductId' = $1 and payload->'data'->>'state' = 'Active' and payload->'data'->>'legalEntityId' = $2`, got.ID, leUSMF); n != 1 {
		t.Fatalf("releasedproduct.changed to Active = %d; want one contract-shaped event", n)
	}
	if n := rowCount(t, pool, `select count(*) from outbox where type = 'dev.passage.engineering.releasesession.created.v1' and payload->'data'->>'sessionId' = $1 and payload->'data'->>'number' = 'REL-1'`, session.Session.ID); n != 1 {
		t.Fatalf("releasesession.created = %d; want one contract-shaped event", n)
	}
}

func TestPostgres_ReleaseNumbersComeFromASequence(t *testing.T) {
	s, _, setup := newStore(t, 1)
	fx := seedRelease(t, s, setup, "", fullPolicy("RP-N", map[string]bool{leUSMF: true}))
	ctx := leContext(1, "rita", leENG)
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		in := releaseInput("", fx.policyID, leUSMF)
		rec, err := s.CreateReleaseSession(ctx, in, store.Idem{})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(rec.Session.Number, "REL-") || seen[rec.Session.Number] {
			t.Fatalf("number %q repeated or malformed", rec.Session.Number)
		}
		seen[rec.Session.Number] = true
	}
}

func TestPostgres_EngineeringVersionsBelongToOneOrganization(t *testing.T) {
	s, _, _ := newStore(t, 1)
	if _, err := s.CreateEngineeringVersion(leContext(1, "rita", leUSMF), domain.EngineeringVersion{ProductID: "P", VersionCode: "V-01", EngineeringLEID: leENG}, store.Idem{}); !domain.HasCode(err, domain.CodeReleaseScope) {
		t.Fatalf("version claimed for an organization outside the caller's scope = %v; want %s", err, domain.CodeReleaseScope)
	}
	if _, err := s.CreateOrganization(leContext(1, "rita", leUSMF), domain.EngineeringOrganization{LegalEntityID: leENG, Code: "ENG", Name: "Eng"}, store.Idem{}); !domain.HasCode(err, domain.CodeReleaseScope) {
		t.Fatalf("organization for an entity outside the caller's scope = %v; want %s", err, domain.CodeReleaseScope)
	}
	ctx := leContext(1, "rita", leENG+","+leUSMF)
	if _, err := s.CreateEngineeringVersion(ctx, domain.EngineeringVersion{ProductID: "P", VersionCode: "V-01", EngineeringLEID: leENG}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateEngineeringVersion(ctx, domain.EngineeringVersion{ProductID: "P", VersionCode: "V-02", EngineeringLEID: leUSMF}, store.Idem{}); !domain.HasCode(err, domain.CodeOwnedField) {
		t.Fatalf("version from another organization = %v; want %s", err, domain.CodeOwnedField)
	}
	if _, err := s.CreateEngineeringVersion(ctx, domain.EngineeringVersion{ProductID: "P", VersionCode: "V-02", EngineeringLEID: leENG}, store.Idem{}); err != nil {
		t.Fatalf("version from the owner = %v", err)
	}
}

func TestPostgres_AC913_NomenclatureOverflowAndLetters(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	n, err := s.CreateNomenclature(ctx, domain.Nomenclature{Code: "VNN", Separator: "-", Segments: []domain.NomenSegment{{Type: domain.SegConstant, Text: "V"}, {Type: domain.SegNumber, Width: 2}}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetNomenclature(ctx, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next, err := loaded.Next("V-99", time.Time{}); err == nil || !domain.HasCode(err, domain.CodeNomenOverflow) {
		t.Fatalf("V-99 next = %s, %v; want %s", next, err, domain.CodeNomenOverflow)
	}
	letters, err := s.CreateNomenclature(ctx, domain.Nomenclature{Code: "LET", Segments: []domain.NomenSegment{{Type: domain.SegLetter}}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := letters.Next("Z", time.Time{})
	if err != nil || got != "AA" {
		t.Fatalf("Z next = %s, %v; want AA", got, err)
	}
}

var signedAt = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

func newDrawingType(t *testing.T, s *store.Postgres, ctx context.Context) domain.DocumentType {
	t.Helper()
	if _, err := s.CreateDocumentType(ctx, domain.DocumentType{Code: "DRW", Name: "Drawing", ChangeControlled: true, SignatureOnRelease: true, IndependentReview: true, SignatureMeaning: "Approved for release"}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	typ, err := s.GetDocumentType(ctx, "DRW")
	if err != nil {
		t.Fatal(err)
	}
	return typ
}

func inReview(t *testing.T, s *store.Postgres, ctx context.Context, typ domain.DocumentType, number, version string) domain.Document {
	t.Helper()
	doc, err := s.CreateDocument(ctx, domain.Document{TypeCode: typ.Code, Number: number, Version: version, LastEditor: "rita", Originals: []domain.DocumentOriginal{{StorageKey: "k/" + version, SHA256: strings.Repeat("a", 64)}}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionDocument(ctx, doc.ID, domain.DocInReview, "rita", nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetDocument(ctx, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func sign(t *testing.T, typ domain.DocumentType, doc domain.Document) domain.Signature {
	t.Helper()
	sig, err := domain.SignDocument(typ, doc, domain.DocReleased, domain.AuthPassword, "carol", "Carol Kane", signedAt)
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func releasedDocument(t *testing.T, s *store.Postgres, ctx context.Context, typ domain.DocumentType, number, version string) domain.Document {
	t.Helper()
	doc := inReview(t, s, ctx, typ, number, version)
	sig := sign(t, typ, doc)
	released, err := s.TransitionDocument(ctx, doc.ID, domain.DocReleased, "carol", &sig)
	if err != nil || released.Status != domain.DocReleased {
		t.Fatalf("first release = %+v, %v", released, err)
	}
	return released
}

func submitDocumentEco(t *testing.T, s *store.Postgres, ctx context.Context, pool *pgxpool.Pool, number, state string, pairs ...domain.DocumentReplacement) domain.Eco {
	t.Helper()
	eco := domain.Eco{ID: ids.New(), Number: number, State: domain.EcoSubmitted, CreatedBy: "rita", Documents: pairs}
	if _, err := s.CreateEco(ctx, eco, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `update eco set state = $2 where id = $1::uuid`, eco.ID, state); err != nil {
		t.Fatal(err)
	}
	eco.State = domain.EcoState(state)
	return eco
}

func releaseEco(ctx context.Context, s *store.Postgres, eco domain.Eco) error {
	eco.State = domain.EcoApproved
	plan, err := eco.Release(nil, releaseDay)
	if err != nil {
		return err
	}
	eco.State, eco.DecidedBy = domain.EcoReleased, "carol"
	return s.ReleaseEco(ctx, eco, domain.EcoApproved, plan)
}

func TestPostgres_AC916_DocumentSignatureAndECO(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	typ := newDrawingType(t, s, ctx)
	first := releasedDocument(t, s, ctx, typ, "DRW-1", "01")
	next := inReview(t, s, ctx, typ, "DRW-1", "02")
	sig := sign(t, typ, next)

	if _, err := s.TransitionDocument(ctx, next.ID, domain.DocReleased, "carol", &sig); !domain.HasCode(err, domain.CodeDocEcoRequired) {
		t.Fatalf("release without ECO = %v; want %s", err, domain.CodeDocEcoRequired)
	}
	submitted := submitDocumentEco(t, s, ctx, pool, "ECO-DOC", "submitted", domain.DocumentReplacement{OldDocumentID: first.ID, NewDocumentID: next.ID})
	withEco, err := s.GetDocument(ctx, next.ID)
	if err != nil || withEco.EcoID != submitted.ID {
		t.Fatalf("new DIR should name the ECO, got %+v, %v", withEco, err)
	}
	if _, err := s.TransitionDocument(ctx, next.ID, domain.DocReleased, "carol", &sig); !domain.HasCode(err, domain.CodeDocEcoRequired) {
		t.Fatalf("release under a submitted ECO = %v; want %s", err, domain.CodeDocEcoRequired)
	}
	if n := rowCount(t, pool, `select count(*) from document_signature`); n != 1 {
		t.Fatalf("%d signatures persisted; want only the first version's", n)
	}

	if _, err := pool.Exec(context.Background(), `update eco set state = 'approved' where id = $1::uuid`, submitted.ID); err != nil {
		t.Fatal(err)
	}
	signed, err := s.TransitionDocument(ctx, next.ID, domain.DocReleased, "carol", &sig)
	if err != nil || signed.Status != domain.DocInReview || signed.SignedAt.IsZero() {
		t.Fatalf("signature under an approved ECO = %+v, %v; want signed and still in review until the ECO releases", signed, err)
	}
	if _, err := s.TransitionDocument(ctx, next.ID, domain.DocReleased, "carol", &sig); !domain.HasCode(err, domain.CodeDocState) {
		t.Fatalf("second signature = %v; want %s", err, domain.CodeDocState)
	}
	sigs, err := s.ListSignatures(ctx, next.ID)
	if err != nil || len(sigs) != 1 || sigs[0].Meaning != "Approved for release" || sigs[0].SignerID != "carol" || sigs[0].RecordID != next.ID || sigs[0].OriginalsHash != domain.HashRecord(next) {
		t.Fatalf("signatures = %+v, %v", sigs, err)
	}

	if err := releaseEco(ctx, s, submitted); err != nil {
		t.Fatal(err)
	}
	if n := rowCount(t, pool, `select count(*) from outbox where type = 'dev.passage.engineering.document.released.v1' and payload->'data'->>'documentId' = $1 and payload->'data'->>'version' = '02'`, next.ID); n != 1 {
		t.Fatalf("document.released events for the replacement = %d; want 1 shaped as the AsyncAPI payload", n)
	}
	if n := rowCount(t, pool, `select count(*) from outbox where type = 'dev.passage.engineering.document.signed.v1' and payload->'data'->>'signerId' = 'carol' and payload->'data' ? 'signedAt' and not payload->'data' ? 'OriginalsHash'`); n != 2 {
		t.Fatalf("document.signed events = %d; want 2 shaped as the AsyncAPI payload", n)
	}
	old, err := s.GetDocument(ctx, first.ID)
	if err != nil || old.Status != domain.DocObsolete {
		t.Fatalf("previous = %+v, %v; want obsolete", old, err)
	}
	now, err := s.GetDocument(ctx, next.ID)
	if err != nil || now.Status != domain.DocReleased {
		t.Fatalf("replacement = %+v, %v; want released", now, err)
	}

	tamper := func(query string) error {
		return dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, query, sigs[0].ID)
			return err
		})
	}
	if err := tamper(`update document_signature set meaning = 'tamper' where id = $1::uuid`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("update error = %v; want append-only", err)
	}
	if err := tamper(`delete from document_signature where id = $1::uuid`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("delete error = %v; want append-only", err)
	}
}

func TestPostgres_EcoDocumentReleaseValidatesThePair(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	typ := newDrawingType(t, s, ctx)

	oldA := releasedDocument(t, s, ctx, typ, "DRW-A", "01")
	newA := inReview(t, s, ctx, typ, "DRW-A", "02")
	unsigned := submitDocumentEco(t, s, ctx, pool, "ECO-UNSIGNED", "approved", domain.DocumentReplacement{OldDocumentID: oldA.ID, NewDocumentID: newA.ID})
	if err := releaseEco(ctx, s, unsigned); !domain.HasCode(err, domain.CodeDocUnsigned) {
		t.Fatalf("ECO release of an unsigned document = %v; want %s", err, domain.CodeDocUnsigned)
	}
	if kept, err := s.GetDocument(ctx, oldA.ID); err != nil || kept.Status != domain.DocReleased {
		t.Fatalf("a refused ECO release must leave the old version released: %+v, %v", kept, err)
	}

	oldB := releasedDocument(t, s, ctx, typ, "DRW-B", "01")
	otherNumber := inReview(t, s, ctx, typ, "DRW-C", "02")
	mismatch := submitDocumentEco(t, s, ctx, pool, "ECO-MISMATCH", "approved", domain.DocumentReplacement{OldDocumentID: oldB.ID, NewDocumentID: otherNumber.ID})
	if err := releaseEco(ctx, s, mismatch); !domain.HasCode(err, domain.CodeDocState) {
		t.Fatalf("ECO pairing different documents = %v; want %s", err, domain.CodeDocState)
	}

	inWork, err := s.CreateDocument(ctx, domain.Document{TypeCode: typ.Code, Number: "DRW-B", Version: "02", LastEditor: "rita"}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	notReviewed := submitDocumentEco(t, s, ctx, pool, "ECO-INWORK", "approved", domain.DocumentReplacement{OldDocumentID: oldB.ID, NewDocumentID: inWork.ID})
	if err := releaseEco(ctx, s, notReviewed); !domain.HasCode(err, domain.CodeDocState) {
		t.Fatalf("ECO release of a document still in work = %v; want %s", err, domain.CodeDocState)
	}
}

func TestPostgres_AC917_EbomImportUnknownParts(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	if err := s.PutIdentifier(ctx, "product_number", "BIKE", "BIKE"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIdentifier(ctx, "cad_part", "WHEEL-CAD", "WHEEL"); err != nil {
		t.Fatal(err)
	}
	payload := domain.EbomPayload{PartNumber: "BIKE", Revision: "A", Lines: make([]domain.EbomLine, 0, 120)}
	for i := 1; i <= 115; i++ {
		payload.Lines = append(payload.Lines, domain.EbomLine{FindNumber: i * 10, PartNumber: "WHEEL-CAD", Quantity: decimal.NewFromInt(1)})
	}
	for i := 1; i <= 5; i++ {
		payload.Lines = append(payload.Lines, domain.EbomLine{FindNumber: 9000 + i, PartNumber: "UNKNOWN-" + strconv.Itoa(i), Quantity: decimal.NewFromInt(1)})
	}
	payload.Lines = append(payload.Lines, domain.EbomLine{FindNumber: 9100, PartNumber: "UNKNOWN-1", Quantity: decimal.NewFromInt(2)})
	rec, err := s.ImportEbom(ctx, store.EbomImportInput{Source: "cad", Payload: payload, Actor: "rita"}, store.Idem{Key: "ebom", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.BOMVersionID == "" || rec.EcrID == "" || len(rec.Exceptions) != 5 || rec.Comparison.Added != 115 {
		t.Fatalf("import = %+v; want draft BOM, ECR, 5 exceptions (a repeated unknown part once), 115 added components", rec)
	}
	draft, err := s.GetBomVersion(ctx, rec.BOMVersionID)
	if err != nil || draft.Status != domain.StatusDraft || len(draft.Lines) != 115 {
		t.Fatalf("draft = %+v, %v; want 115 lines", draft, err)
	}
	for _, ex := range rec.Exceptions {
		if ex.Code != domain.CodeEbomPartUnknown {
			t.Fatalf("exception = %+v", ex)
		}
	}
	ecr, err := s.GetEcr(ctx, rec.EcrID)
	if err != nil || ecr.State != domain.EcrDraft {
		t.Fatalf("ecr = %+v, %v", ecr, err)
	}
	replay, err := s.ImportEbom(ctx, store.EbomImportInput{Source: "cad", Payload: payload}, store.Idem{Key: "ebom", Hash: "h"})
	if err != nil || replay.ID != rec.ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
}

func TestPostgres_EbomImportFlagsUnknownHeaderAndRefusesCycles(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	if err := s.PutIdentifier(ctx, "cad_part", "BIKE-CAD", "BIKE"); err != nil {
		t.Fatal(err)
	}
	one := decimal.NewFromInt(1)

	unknownHeader, err := s.ImportEbom(ctx, store.EbomImportInput{Source: "cad", Payload: domain.EbomPayload{PartNumber: "GHOST", Revision: "A", Lines: []domain.EbomLine{{PartNumber: "GHOST-PART", Quantity: one}}}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	flagged := map[string]bool{}
	for _, ex := range unknownHeader.Exceptions {
		flagged[ex.PartNumber] = true
	}
	if !flagged["GHOST"] || !flagged["GHOST-PART"] {
		t.Fatalf("exceptions = %+v; want the unknown header flagged with its unknown line", unknownHeader.Exceptions)
	}

	alias := domain.EbomPayload{PartNumber: "BIKE", Revision: "A", Lines: []domain.EbomLine{{PartNumber: "BIKE-CAD", Quantity: one}}}
	if _, err := s.ImportEbom(ctx, store.EbomImportInput{Source: "cad", Payload: alias}, store.Idem{}); !domain.HasCode(err, domain.CodeBomCycle) {
		t.Fatalf("assembly listed under its own CAD alias = %v; want %s", err, domain.CodeBomCycle)
	}

	wheel := version("WHEEL", domain.StatusActive, releaseDay, "BIKE")
	if _, err := s.CreateBomVersion(ctx, wheel, store.Idem{}, noCheck); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIdentifier(ctx, "product_number", "WHEEL", "WHEEL"); err != nil {
		t.Fatal(err)
	}
	indirect := domain.EbomPayload{PartNumber: "BIKE", Revision: "B", Lines: []domain.EbomLine{{PartNumber: "WHEEL", Quantity: one}}}
	if _, err := s.ImportEbom(ctx, store.EbomImportInput{Source: "cad", Payload: indirect}, store.Idem{}); !domain.HasCode(err, domain.CodeBomCycle) {
		t.Fatalf("indirect cycle through an existing BOM = %v; want %s", err, domain.CodeBomCycle)
	}
	if n := rowCount(t, pool, `select count(*) from ebom_import where part_number = 'BIKE'`); n != 0 {
		t.Fatalf("%d imports persisted by refused payloads", n)
	}
}

func TestPostgres_DocumentStructureRejectsCycle(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	a, err := s.CreateDocument(ctx, domain.Document{TypeCode: "DRW", Number: "A"}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateDocument(ctx, domain.Document{TypeCode: "DRW", Number: "B"}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDocumentStructure(ctx, a.ID, []string{b.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDocumentStructure(ctx, b.ID, []string{a.ID}); !domain.HasCode(err, domain.CodeDocCycle) {
		t.Fatalf("cycle = %v; want %s", err, domain.CodeDocCycle)
	}
	if err := s.SetDocumentStructure(ctx, a.ID, []string{ids.New()}); problemCode(err) != "ENG-DOC-NOTFOUND" {
		t.Fatalf("unknown child = %v; want ENG-DOC-NOTFOUND", err)
	}
	used, err := s.DocumentWhereUsed(ctx, b.ID)
	if err != nil || len(used) != 1 || used[0].ParentID != a.ID {
		t.Fatalf("where-used = %+v, %v", used, err)
	}
}

func TestPostgres_DocumentStructureRefusesSignedAndReleasedDocuments(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	typ := newDrawingType(t, s, ctx)
	released := releasedDocument(t, s, ctx, typ, "DRW-R", "01")
	child, err := s.CreateDocument(ctx, domain.Document{TypeCode: typ.Code, Number: "DRW-CH"}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDocumentStructure(ctx, released.ID, []string{child.ID}); !domain.HasCode(err, domain.CodeDocSigned) {
		t.Fatalf("structure of a released document = %v; want %s", err, domain.CodeDocSigned)
	}
}

func TestPostgres_ReleaseLineStateMachine(t *testing.T) {
	s, _, setup := newStore(t, 1)
	policy, err := s.CreateReleasePolicy(setup, domain.ReleasePolicy{Code: "RP", RequireAcceptance: map[string]bool{leUSMF: true}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := s.CreateReleaseSession(leContext(1, "rita", leENG), releaseInput("REL-X", policy.ID, leUSMF), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	receiver := leContext(1, "bob", leUSMF)
	if _, err := s.DecideReleaseLine(receiver, rec.Lines[0].ID, "bob", "no", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecideReleaseLine(receiver, rec.Lines[0].ID, "bob", "", true); !domain.HasCode(err, domain.CodeReleaseState) {
		t.Fatalf("accept rejected line = %v; want %s", err, domain.CodeReleaseState)
	}
}

func TestPostgres_ReleaseSession_IdempotentReplay(t *testing.T) {
	s, _, setup := newStore(t, 1)
	policy, err := s.CreateReleasePolicy(setup, domain.ReleasePolicy{Code: "RP2", RequireAcceptance: map[string]bool{leDEMF: true}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := leContext(1, "rita", leENG)
	in := releaseInput("REL-Y", policy.ID, leDEMF)
	first, err := s.CreateReleaseSession(ctx, in, store.Idem{Key: "k", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.CreateReleaseSession(ctx, in, store.Idem{Key: "k", Hash: "h"})
	if err != nil || replay.Session.ID != first.Session.ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
}

func TestPostgres_AppRole_ReleaseReadinessStructureAndAudit(t *testing.T) {
	s, admin, appPool, setup := newAppStore(t, 1)
	rdy, err := s.CreateReadinessPolicy(setup, domain.ReadinessPolicy{Code: "RDY-APP", Checks: []domain.ReadinessCheck{
		{Key: "active_bom", Kind: domain.CheckAuto, Mandatory: true},
		{Key: "quality_signoff", Kind: domain.CheckManual, Mandatory: true},
	}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	fx := seedRelease(t, s, setup, "ENG-S", fullPolicy("RP-APP", map[string]bool{leUSMF: true, leDEMF: false}))

	in := releaseInput("", fx.policyID, leDEMF, leUSMF)
	in.ReadinessPolicyID = rdy.ID
	rec, err := s.CreateReleaseSession(leContext(1, "rita", leENG+","+leDEMF), in, store.Idem{})
	if err != nil {
		t.Fatalf("release session as the app role: %v", err)
	}
	if !strings.HasPrefix(rec.Session.Number, "REL-") {
		t.Fatalf("number = %q", rec.Session.Number)
	}
	var usmfLine string
	for _, l := range rec.Lines {
		if l.LegalEntityID == leUSMF {
			usmfLine = l.ID
		}
	}
	if _, err := s.DecideReleaseLine(leContext(1, "bob", leUSMF), usmfLine, "bob", "", true); err != nil {
		t.Fatalf("accept as the app role: %v", err)
	}
	demf, usmf := leContext(1, "carol", leDEMF), leContext(1, "bob", leUSMF)
	page, err := s.ListReleasedProducts(demf, leDEMF, "", store.MaxPageSize)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("released = %+v, %v", page, err)
	}
	if hidden, err := s.ListReleasedProducts(usmf, leDEMF, "", store.MaxPageSize); err != nil || len(hidden.Items) != 0 {
		t.Fatalf("another entity's released products are visible: %+v, %v", hidden, err)
	}
	if _, err := s.GetReleasedProduct(usmf, page.Items[0].ID); problemCode(err) != "ENG-RELEASED-NOTFOUND" {
		t.Fatalf("another entity's released product = %v; want not found under the legal-entity policy", err)
	}
	if lines, err := s.ListReleaseLines(demf, store.ReleaseLineFilter{}, "", store.MaxPageSize); err != nil || len(lines.Items) != 1 || lines.Items[0].LegalEntityID != leDEMF {
		t.Fatalf("receiver sees release lines %+v, %v; want only its own", lines, err)
	}
	if lines, err := s.ListReleaseLines(leContext(1, "rita", leENG), store.ReleaseLineFilter{}, "", store.MaxPageSize); err != nil || len(lines.Items) != 2 {
		t.Fatalf("sending engineering entity sees release lines %+v, %v; want both it sent", lines, err)
	}
	if lines, err := s.ListReleaseLines(setup, store.ReleaseLineFilter{}, "", store.MaxPageSize); err != nil || len(lines.Items) != 0 {
		t.Fatalf("a caller without a legal-entity scope sees release lines %+v, %v", lines, err)
	}
	if got, err := s.SignReadinessCheck(demf, page.Items[0].ID, "quality_signoff", "carol"); err != nil || got.State != domain.ReleasedActive {
		t.Fatalf("readiness sign as the app role = %+v, %v", got, err)
	}
	if orgs, err := s.ListOrganizations(usmf, "", store.MaxPageSize); err != nil || len(orgs.Items) != 0 {
		t.Fatalf("organizations visible outside scope: %+v, %v", orgs, err)
	}
	if _, err := s.CreateOrganization(leContext(1, "rita", leENG), domain.EngineeringOrganization{LegalEntityID: leENG, Code: "ENG", Name: "Eng"}, store.Idem{}); err != nil {
		t.Fatalf("organization as the app role: %v", err)
	}
	if orgs, err := s.ListOrganizations(usmf, "", store.MaxPageSize); err != nil || len(orgs.Items) != 0 {
		t.Fatalf("another entity's organization is visible: %+v, %v", orgs, err)
	}

	typ := newDrawingType(t, s, setup)
	parent, err := s.CreateDocument(setup, domain.Document{TypeCode: typ.Code, Number: "STR-P"}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateDocument(setup, domain.Document{TypeCode: typ.Code, Number: "STR-C"}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateDocument(setup, domain.Document{TypeCode: typ.Code, Number: "STR-O"}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDocumentStructure(setup, parent.ID, []string{child.ID}); err != nil {
		t.Fatalf("structure as the app role: %v", err)
	}
	if err := s.SetDocumentStructure(setup, parent.ID, []string{other.ID}); err != nil {
		t.Fatalf("replacing a structure as the app role (DELETE grant): %v", err)
	}

	doc := inReview(t, s, setup, typ, "SIG-1", "01")
	sig := sign(t, typ, doc)
	if _, err := s.TransitionDocument(leContext(1, "alice", leENG+","+leUSMF), doc.ID, domain.DocReleased, "carol", &sig); err != nil {
		t.Fatalf("sign as the app role: %v", err)
	}
	if n := rowCount(t, admin, `select count(*) from audit_outbox where id = $1::uuid and delivered_at is null`, sig.ID); n != 1 {
		t.Fatalf("the audit record must be queued in the signing transaction, found %d", n)
	}
	jobs, err := s.ClaimAudit(setup, 10, time.Minute)
	if err != nil || len(jobs) != 1 || jobs[0].Signature.RecordID != doc.ID || jobs[0].UserID != "alice" || jobs[0].TenantID != testkit.TenantID(1) || jobs[0].LegalEntityID != leENG {
		t.Fatalf("claim = %+v, %v", jobs, err)
	}
	if again, err := s.ClaimAudit(setup, 10, time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("a leased record must not be claimed twice: %+v, %v", again, err)
	}
	if ok, err := s.CanScanAudit(setup); err != nil || !ok {
		t.Fatalf("can scan = %v, %v; the claim function's owner must bypass row-level security", ok, err)
	}
	if n := rowCount(t, admin, `select count(*) from audit_outbox`); n != 1 {
		t.Fatalf("audit_outbox rows = %d", n)
	}
	var visible int
	if err := dbx.WithTenantTx(principalContext(2), appPool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select count(*) from audit_outbox`).Scan(&visible)
	}); err != nil || visible != 0 {
		t.Fatalf("another tenant reads %d audit records, %v; want 0 under the tenant policy", visible, err)
	}
	if err := s.CompleteAudit(principalContext(2), jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	if n := rowCount(t, admin, `select count(*) from audit_outbox where delivered_at is null`); n != 1 {
		t.Fatal("another tenant completed this tenant's audit record")
	}
	if err := s.FailAudit(setup, jobs[0].ID, 1, context.DeadlineExceeded, 0); err != nil {
		t.Fatal(err)
	}
	retry, err := s.ClaimAudit(setup, 10, time.Minute)
	if err != nil || len(retry) != 1 || retry[0].Attempts != 1 {
		t.Fatalf("a failed record comes back with its attempts: %+v, %v", retry, err)
	}
	if err := s.CompleteAudit(setup, retry[0].ID); err != nil {
		t.Fatal(err)
	}
	if done, err := s.ClaimAudit(setup, 10, time.Minute); err != nil || len(done) != 0 {
		t.Fatalf("a delivered record is done: %+v, %v", done, err)
	}
}

func TestPostgres_RefusedSignatureQueuesNoAuditRecord(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	typ := newDrawingType(t, s, ctx)
	doc := inReview(t, s, ctx, typ, "SIG-2", "01")
	sig := sign(t, typ, doc)
	sig.RecordID = ids.New()
	if _, err := s.TransitionDocument(ctx, doc.ID, domain.DocReleased, "carol", &sig); err == nil {
		t.Fatal("a signature for another record must be refused")
	}
	if n := rowCount(t, pool, `select count(*) from audit_outbox`) + rowCount(t, pool, `select count(*) from document_signature`); n != 0 {
		t.Fatalf("%d signature or audit rows persisted by a refused sign", n)
	}
}

func TestPostgres_OrgsDocsEbom_RLSHidesOtherTenant(t *testing.T) {
	s, _, owner := newStore(t, 1)
	intruder := principalContext(2)
	org, err := s.CreateOrganization(leContext(1, "alice", leENG), domain.EngineeringOrganization{LegalEntityID: leENG, Code: "ENG", Name: "Eng"}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.CreateDocument(owner, domain.Document{TypeCode: "DRW", Number: "N-1", LastEditor: "rita"}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetOrganization(intruder, org.ID); problemCode(err) != "ENG-ORG-NOTFOUND" {
		t.Fatalf("org = %v; want not found", err)
	}
	if _, err := s.GetDocument(intruder, doc.ID); problemCode(err) != "ENG-DOC-NOTFOUND" {
		t.Fatalf("doc = %v; want not found", err)
	}
	if page, err := s.ListOrganizations(intruder, "", store.MaxPageSize); err != nil || len(page.Items) != 0 {
		t.Fatalf("org list = %+v, %v", page, err)
	}
	if page, err := s.ListDocuments(intruder, "", "", store.MaxPageSize); err != nil || len(page.Items) != 0 {
		t.Fatalf("doc list = %+v, %v", page, err)
	}
}

func TestPostgres_Document_RLSForcedOnAppRole(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"engineering_organization", "document", "document_signature", "release_session", "ebom_import", "audit_outbox"} {
		var enabled, forced bool
		if err := admin.QueryRow(ctx, `select relrowsecurity, relforcerowsecurity from pg_class where oid = $1::regclass`, table).Scan(&enabled, &forced); err != nil || !enabled || !forced {
			t.Fatalf("%s: row security enabled=%v forced=%v, %v", table, enabled, forced, err)
		}
	}
	// Every tenant table forces RLS, and every legal_entity_id table has a restrictive scope policy
	// (the same rule tools/rlscheck enforces in CI).
	testkit.AssertTenantRLS(t, admin)
	s := store.New(app)
	owner, other := principalContext(1), principalContext(2)
	d, err := s.CreateDocument(owner, domain.Document{TypeCode: "DRW", Number: "X"}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDocument(other, d.ID); problemCode(err) != "ENG-DOC-NOTFOUND" {
		t.Fatalf("other tenant = %v", err)
	}
}

func TestPostgres_SignatureMustMatchTheLockedRecord(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	typ := newDrawingType(t, s, ctx)
	doc := inReview(t, s, ctx, typ, "SIG-H", "01")
	sig := sign(t, typ, doc)
	sig.OriginalsHash = strings.Repeat("0", 64)
	if _, err := s.TransitionDocument(ctx, doc.ID, domain.DocReleased, "carol", &sig); !domain.HasCode(err, domain.CodeDocState) {
		t.Fatalf("signature over a hash that is not the stored record's = %v; want %s", err, domain.CodeDocState)
	}
	if n := rowCount(t, pool, `select count(*) from document_signature`) + rowCount(t, pool, `select count(*) from audit_outbox`); n != 0 {
		t.Fatalf("%d signature or audit rows persisted for a stale hash", n)
	}
}

func TestPostgres_ChangeControlledReleaseIsObsoletedOnlyByECO(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	typ := newDrawingType(t, s, ctx)
	released := releasedDocument(t, s, ctx, typ, "DRW-O", "01")
	if _, err := s.TransitionDocument(ctx, released.ID, domain.DocObsolete, "rita", nil); !domain.HasCode(err, domain.CodeDocEcoRequired) {
		t.Fatalf("obsoleting a released change-controlled document by hand = %v; want %s", err, domain.CodeDocEcoRequired)
	}
	if got, err := s.GetDocument(ctx, released.ID); err != nil || got.Status != domain.DocReleased {
		t.Fatalf("document = %+v, %v; want still released", got, err)
	}
}

func TestPostgres_EcoCannotRebindADocumentOfAnotherEco(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	typ := newDrawingType(t, s, ctx)
	old := releasedDocument(t, s, ctx, typ, "DRW-R", "01")
	next := inReview(t, s, ctx, typ, "DRW-R", "02")
	first := submitDocumentEco(t, s, ctx, pool, "ECO-R1", "submitted", domain.DocumentReplacement{OldDocumentID: old.ID, NewDocumentID: next.ID})
	second := domain.Eco{ID: ids.New(), Number: "ECO-R2", State: domain.EcoSubmitted, CreatedBy: "mallory", Documents: []domain.DocumentReplacement{{OldDocumentID: old.ID, NewDocumentID: next.ID}}}
	if _, err := s.CreateEco(ctx, second, store.Idem{}); !domain.HasCode(err, domain.CodeDocState) {
		t.Fatalf("second ECO claiming the same replacing document = %v; want %s", err, domain.CodeDocState)
	}
	if got, err := s.GetDocument(ctx, next.ID); err != nil || got.EcoID != first.ID {
		t.Fatalf("document = %+v, %v; want still bound to %s", got, err, first.ID)
	}
	third := domain.Eco{ID: ids.New(), Number: "ECO-R3", State: domain.EcoSubmitted, CreatedBy: "mallory", Documents: []domain.DocumentReplacement{{OldDocumentID: next.ID, NewDocumentID: old.ID}}}
	if _, err := s.CreateEco(ctx, third, store.Idem{}); !domain.HasCode(err, domain.CodeDocState) {
		t.Fatalf("ECO naming a released document as the replacement = %v; want %s", err, domain.CodeDocState)
	}
}
