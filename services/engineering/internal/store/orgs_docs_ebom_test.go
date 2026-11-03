package store_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
	"github.com/BenjaminSRussell/passage/services/engineering/migrations"
)

func TestPostgres_AC911_ReleaseAcceptanceSplit(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	if _, err := s.CreateOrganization(ctx, domain.EngineeringOrganization{LegalEntityID: "ENG1", Code: "ENG", Name: "Engineering"}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	policy := domain.ReleasePolicy{
		Code: "RP-1", CopyBOM: true, ActivateBOM: true, CopyRoute: true, ActivateRoute: true,
		RequireAcceptance: map[string]bool{"USMF": true, "DEMF": false},
	}
	created, err := s.CreateReleasePolicy(ctx, policy, store.Idem{Key: "rp", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutSiteMapping(ctx, domain.SiteMapping{EngineeringLEID: "ENG1", OperationalLEID: "DEMF", OperationalSite: "DEMF-S"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSiteMapping(ctx, domain.SiteMapping{EngineeringLEID: "ENG1", OperationalLEID: "USMF", OperationalSite: "USMF-S"}); err != nil {
		t.Fatal(err)
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

	rec, err := s.CreateReleaseSession(ctx, store.ReleaseSessionInput{
		Number: "REL-2026-00001", CreatedBy: "rita", PolicyID: created.ID, EngineeringLEID: "ENG1",
		ProductID: "P", VersionCode: "V-02", LegalEntities: []string{"DEMF", "USMF"},
	}, store.Idem{Key: "rel", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	byLE := map[string]domain.ReleaseLine{}
	for _, l := range rec.Lines {
		byLE[l.LegalEntityID] = l
	}
	if byLE["DEMF"].Status != domain.LineAccepted || byLE["DEMF"].CopiedBOMVersionID == "" || byLE["DEMF"].CopiedRouteVersionID == "" {
		t.Fatalf("DEMF = %+v; want accepted with copied BOM and route", byLE["DEMF"])
	}
	copied, err := s.GetBomVersion(ctx, byLE["DEMF"].CopiedBOMVersionID)
	if err != nil || copied.Status != domain.StatusActive || copied.SiteID != "DEMF-S" {
		t.Fatalf("DEMF BOM = %+v, %v; want active on DEMF-S", copied, err)
	}
	if byLE["USMF"].Status != domain.LineSent {
		t.Fatalf("USMF = %s; want sent (open product releases)", byLE["USMF"].Status)
	}

	open, err := s.ListReleaseLines(ctx, store.ReleaseLineFilter{Status: domain.LineSent}, "", store.MaxPageSize)
	if err != nil || len(open.Items) != 1 || open.Items[0].LegalEntityID != "USMF" {
		t.Fatalf("open releases = %+v, %v", open, err)
	}
	accepted, err := s.DecideReleaseLine(ctx, byLE["USMF"].ID, "bob", "", true)
	if err != nil || accepted.Status != domain.LineAccepted || accepted.CopiedBOMVersionID == "" {
		t.Fatalf("USMF accept = %+v, %v", accepted, err)
	}
	products, err := s.ListReleasedProducts(ctx, "USMF", "", store.MaxPageSize)
	if err != nil || len(products.Items) != 1 || products.Items[0].ProductID != "P" {
		t.Fatalf("USMF released products = %+v, %v", products, err)
	}
}

func TestPostgres_AC912_ReadinessBlocksThenActivates(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	policy := domain.ReadinessPolicy{Code: "RDY", TargetState: domain.ReleasedActive, Checks: []domain.ReadinessCheck{
		{Key: "active_bom", Kind: domain.CheckAuto, Mandatory: true},
		{Key: "tax_group", Kind: domain.CheckAuto, Mandatory: true},
		{Key: "warehouse_setup", Kind: domain.CheckAuto, Mandatory: true},
		{Key: "quality_signoff", Kind: domain.CheckManual, Mandatory: true},
	}}
	rdy, err := s.CreateReadinessPolicy(ctx, policy, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	cat, err := s.CreateCategory(ctx, domain.EngineeringCategory{Code: "FIN", Name: "Finished", TrackVersions: true, ReadinessPolicyID: rdy.ID}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateEngineeringVersion(ctx, domain.EngineeringVersion{ProductID: "P", VersionCode: "V-01", EngineeringLEID: "ENG1", CategoryID: cat.ID}, store.Idem{}); err != nil {
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
	session, err := s.CreateReleaseSession(ctx, store.ReleaseSessionInput{
		Number: "REL-1", CreatedBy: "rita", PolicyID: rel.ID, ProductID: "P", VersionCode: "V-01", LegalEntities: []string{"USMF"}, ReadinessPolicyID: rdy.ID,
	}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ListReleasedProducts(ctx, "USMF", "", store.MaxPageSize)
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
	if _, err := s.SignReadinessCheck(ctx, got.ID, "quality_signoff", "rita"); !domain.HasCode(err, domain.CodeReadinessSelfSign) {
		t.Fatalf("creator sign = %v; want %s", err, domain.CodeReadinessSelfSign)
	}
	signed, err := s.SignReadinessCheck(ctx, got.ID, "quality_signoff", "carol")
	if err != nil || signed.State != domain.ReleasedActive {
		t.Fatalf("after sign = %+v, %v; want Active (session %s)", signed, err, session.Session.ID)
	}
	if code := domain.ProcessPolicy(signed.State, "sales_order"); code != "" {
		t.Fatalf("active product still blocked: %s", code)
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

func TestPostgres_AC916_DocumentSignatureAndECO(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	typ, err := s.CreateDocumentType(ctx, domain.DocumentType{Code: "DRW", Name: "Drawing", ChangeControlled: true, SignatureOnRelease: true, IndependentReview: true, SignatureMeaning: "Approved for release"}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	v01 := domain.Document{TypeCode: typ.Code, Number: "DRW-1", Version: "01", LastEditor: "rita", Originals: []domain.DocumentOriginal{{StorageKey: "a", SHA256: "aaa"}}}
	first, err := s.CreateDocument(ctx, v01, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionDocument(ctx, first.ID, domain.DocInReview, "rita", nil); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	sig01, err := domain.SignDocument(typ, first, domain.DocReleased, domain.Reauth{UserID: "carol", Assertion: "ok", Method: domain.AuthPassword}, "carol", "Carol Kane", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionDocument(ctx, first.ID, domain.DocReleased, "carol", &sig01); err != nil {
		t.Fatal(err)
	}

	v02 := domain.Document{TypeCode: typ.Code, Number: "DRW-1", Version: "02", LastEditor: "rita", Originals: []domain.DocumentOriginal{{StorageKey: "b", SHA256: "bbb"}}}
	next, err := s.CreateDocument(ctx, v02, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionDocument(ctx, next.ID, domain.DocInReview, "rita", nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetDocument(ctx, next.ID)
	if err != nil {
		t.Fatal(err)
	}
	sig02, err := domain.SignDocument(typ, loaded, domain.DocReleased, domain.Reauth{UserID: "carol", Assertion: "ok"}, "carol", "Carol Kane", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionDocument(ctx, next.ID, domain.DocReleased, "carol", &sig02); !domain.HasCode(err, domain.CodeDocEcoRequired) {
		t.Fatalf("release without ECO = %v; want %s", err, domain.CodeDocEcoRequired)
	}

	eco := domain.Eco{ID: ids.New(), Number: "ECO-DOC", State: domain.EcoSubmitted, CreatedBy: "rita", Documents: []domain.DocumentReplacement{{OldDocumentID: first.ID, NewDocumentID: next.ID}}}
	if _, err := s.CreateEco(ctx, eco, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	withEco, err := s.GetDocument(ctx, next.ID)
	if err != nil || withEco.EcoID != eco.ID {
		t.Fatalf("new DIR should name the ECO, got %+v, %v", withEco, err)
	}
	sig02, err = domain.SignDocument(typ, withEco, domain.DocReleased, domain.Reauth{UserID: "carol", Assertion: "ok"}, "carol", "Carol Kane", now)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := s.TransitionDocument(ctx, next.ID, domain.DocReleased, "carol", &sig02)
	if err != nil || signed.Status != domain.DocReleased || signed.SignedAt.IsZero() {
		t.Fatalf("signed release = %+v, %v", signed, err)
	}
	sigs, err := s.ListSignatures(ctx, next.ID)
	if err != nil || len(sigs) != 1 || sigs[0].Meaning != "Approved for release" || sigs[0].SignerID != "carol" || sigs[0].RecordID != next.ID || sigs[0].OriginalsHash == "" {
		t.Fatalf("signatures = %+v, %v", sigs, err)
	}

	plan, err := func() (domain.ReleasePlan, error) {
		loaded := eco
		loaded.State = domain.EcoApproved
		return loaded.Release(nil, releaseDay)
	}()
	if err != nil {
		t.Fatal(err)
	}
	released := eco
	released.State = domain.EcoReleased
	released.DecidedBy = "carol"
	if err := s.ReleaseEco(ctx, released, domain.EcoSubmitted, plan); err != nil {
		t.Fatal(err)
	}
	old, err := s.GetDocument(ctx, first.ID)
	if err != nil || old.Status != domain.DocObsolete {
		t.Fatalf("previous = %+v, %v; want obsolete", old, err)
	}

	if _, err := pool.Exec(ctx, `update document_signature set meaning = 'tamper' where id = $1::uuid`, sigs[0].ID); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("update error = %v; want append-only", err)
	}
	if _, err := pool.Exec(ctx, `delete from document_signature where id = $1::uuid`, sigs[0].ID); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("delete error = %v; want append-only", err)
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
	rec, err := s.ImportEbom(ctx, store.EbomImportInput{Source: "cad", Payload: payload, Actor: "rita"}, store.Idem{Key: "ebom", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.BOMVersionID == "" || rec.EcrID == "" || len(rec.Exceptions) != 5 || rec.Comparison.Added != 115 {
		t.Fatalf("import = %+v; want draft BOM, ECR, 5 exceptions, 115 added components", rec)
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

func TestPostgres_OrgsDocsEbom_RLSHidesOtherTenant(t *testing.T) {
	s, _, owner := newStore(t, 1)
	intruder := principalContext(2)
	org, err := s.CreateOrganization(owner, domain.EngineeringOrganization{LegalEntityID: "ENG1", Code: "ENG", Name: "Eng"}, store.Idem{})
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
	used, err := s.DocumentWhereUsed(ctx, b.ID)
	if err != nil || len(used) != 1 || used[0].ParentID != a.ID {
		t.Fatalf("where-used = %+v, %v", used, err)
	}
}

func TestPostgres_OwnedFieldAndReleaseLineState(t *testing.T) {
	if err := domain.CheckOwnedField("BOM", "ENG1", "USMF"); !domain.HasCode(err, domain.CodeOwnedField) {
		t.Fatalf("got %v", err)
	}
	s, _, ctx := newStore(t, 1)
	policy, err := s.CreateReleasePolicy(ctx, domain.ReleasePolicy{Code: "RP", RequireAcceptance: map[string]bool{"USMF": true}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := s.CreateReleaseSession(ctx, store.ReleaseSessionInput{Number: "REL-X", CreatedBy: "rita", PolicyID: policy.ID, ProductID: "P", VersionCode: "V-01", LegalEntities: []string{"USMF"}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecideReleaseLine(ctx, rec.Lines[0].ID, "bob", "no", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecideReleaseLine(ctx, rec.Lines[0].ID, "bob", "", true); !domain.HasCode(err, domain.CodeReleaseState) {
		t.Fatalf("accept rejected line = %v; want %s", err, domain.CodeReleaseState)
	}
}

func TestPostgres_ReleaseSession_IdempotentReplay(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	policy, err := s.CreateReleasePolicy(ctx, domain.ReleasePolicy{Code: "RP2", RequireAcceptance: map[string]bool{}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	in := store.ReleaseSessionInput{Number: "REL-Y", CreatedBy: "rita", PolicyID: policy.ID, ProductID: "P", VersionCode: "V-01", LegalEntities: []string{"DEMF"}}
	first, err := s.CreateReleaseSession(ctx, in, store.Idem{Key: "k", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.CreateReleaseSession(ctx, in, store.Idem{Key: "k", Hash: "h"})
	if err != nil || replay.Session.ID != first.Session.ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
}

func TestPostgres_Document_RLSForcedOnAppRole(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"engineering_organization", "document", "document_signature", "release_session", "ebom_import"} {
		var enabled, forced bool
		if err := admin.QueryRow(ctx, `select relrowsecurity, relforcerowsecurity from pg_class where oid = $1::regclass`, table).Scan(&enabled, &forced); err != nil || !enabled || !forced {
			t.Fatalf("%s: row security enabled=%v forced=%v, %v", table, enabled, forced, err)
		}
	}
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
