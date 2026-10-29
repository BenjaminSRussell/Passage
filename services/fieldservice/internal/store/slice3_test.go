package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	svcevents "github.com/BenjaminSRussell/passage/services/fieldservice/internal/events"
	"github.com/BenjaminSRussell/passage/services/fieldservice/migrations"
)

func newSliceStore(t *testing.T, tenantN int) (*Postgres, context.Context) {
	t.Helper()
	admin, pool := testkit.DBWithApp(t)
	ctx := principalCtx(testkit.TenantID(tenantN))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	testkit.AssertTenantRLS(t, admin)
	return New(pool), ctx
}

// outboxTypes lists the event types the outbox holds for one aggregate.
func outboxTypes(t *testing.T, s *Postgres, ctx context.Context, aggregateID string) []string {
	t.Helper()
	var out []string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select type from outbox where aggregate_id = $1`, aggregateID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// AC-45.8: a confirmed shipment of 10 serialized units creates 10 ACTIVE service
// objects with warranty starting on the ship date; a re-delivery creates none.
// An INSTALL warranty waits for the installation, which is recorded once.
func TestStore_ShipmentCreatesObjectsAndInstallStartsWarranty(t *testing.T) {
	s, ctx := newSliceStore(t, 31)
	customer, le, item, pump := ids.New(), ids.New(), ids.New(), ids.New()
	serials := make([]string, 10)
	for i := range serials {
		serials[i] = fmt.Sprintf("SN-%03d", i)
	}
	receipt := ShipmentReceipt{ShipmentID: ids.New(), ShipDate: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC),
		CustomerPartyID: customer, LegalEntityID: le, Lines: []ShipmentLine{
			{ItemID: item, Serials: serials, Warranty: domain.WarrantyTerm{StartTrigger: domain.StartShip, Months: 24, PartsCovered: true}},
			{ItemID: pump, Serials: []string{"P-1"}, Warranty: domain.WarrantyTerm{StartTrigger: domain.StartInstall, Months: 12, LaborCovered: true}},
		}}
	first, err := s.ReceiveShipment(ctx, receipt)
	if err != nil || first.Created != 11 || first.Existing != 0 {
		t.Fatalf("receive = %+v, %v; want 11 created", first, err)
	}
	again, err := s.ReceiveShipment(ctx, receipt)
	if err != nil || again.Created != 0 || again.Existing != 11 {
		t.Fatalf("re-delivery = %+v, %v; want 11 existing", again, err)
	}
	page, err := s.ListServiceObjects(ctx, ObjectFilter{Customer: customer, Item: item, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 10 || page.NextCursor != "" {
		t.Fatalf("got %d objects (cursor %q), want 10", len(page.Items), page.NextCursor)
	}
	for _, o := range page.Items {
		if o.Status != domain.ObjectActive || o.WarrantyStartsOn == nil || *o.WarrantyStartsOn != "2026-10-23" ||
			o.WarrantyEndsOn == nil || *o.WarrantyEndsOn != "2028-10-23" || o.ShippedOn == nil || *o.ShippedOn != "2026-10-23" {
			t.Fatalf("object %s = %+v, want ACTIVE with warranty 2026-10-23 to 2028-10-23", o.Serial, o)
		}
	}
	// Keyset pages cover every object exactly once.
	seen := map[string]bool{}
	cursor := ""
	for {
		p, err := s.ListServiceObjects(ctx, ObjectFilter{Customer: customer, Before: cursor, Limit: 4})
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range p.Items {
			if seen[o.ID] {
				t.Fatalf("object %s on two pages", o.Serial)
			}
			seen[o.ID] = true
		}
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	if len(seen) != 11 {
		t.Fatalf("paged through %d objects, want 11", len(seen))
	}

	bySerial, err := s.ListServiceObjects(ctx, ObjectFilter{Serial: "P-1", Limit: 5})
	if err != nil || len(bySerial.Items) != 1 {
		t.Fatalf("serial search = %d, %v", len(bySerial.Items), err)
	}
	p1 := bySerial.Items[0]
	if p1.WarrantyStartsOn != nil {
		t.Fatalf("INSTALL warranty started before installation: %v", *p1.WarrantyStartsOn)
	}
	if _, err := s.InstallServiceObject(ctx, p1.ID, p1.Version+1, "2026-11-02", nil); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale install = %v, want %s", err, CodeVersionConflict)
	}
	installed, err := s.InstallServiceObject(ctx, p1.ID, p1.Version, "2026-11-02", nil)
	if err != nil {
		t.Fatal(err)
	}
	if installed.InstalledOn == nil || *installed.InstalledOn != "2026-11-02" || installed.WarrantyStartsOn == nil ||
		*installed.WarrantyStartsOn != "2026-11-02" || installed.Version != p1.Version+1 {
		t.Fatalf("installed = %+v, want warranty from 2026-11-02", installed)
	}
	if _, err := s.InstallServiceObject(ctx, p1.ID, installed.Version, "2026-11-03", nil); problemCode(err) != CodeObjectInstalled {
		t.Fatalf("second install = %v, want %s", err, CodeObjectInstalled)
	}
}

// FR-45.1: a hand-registered object is unique per item and serial, and its
// parent must belong to the same customer.
func TestStore_CreateServiceObjectRules(t *testing.T) {
	s, ctx := newSliceStore(t, 32)
	customer, le, item := ids.New(), ids.New(), ids.New()
	term := domain.WarrantyTerm{StartTrigger: domain.StartRegistration, Months: 6, LaborCovered: true}
	parent, replayed, err := s.CreateServiceObject(ctx, ServiceObject{ItemID: item, Serial: "M-1", CustomerPartyID: customer,
		LegalEntityID: le, Warranty: term}, "obj-1", "hash-1")
	if err != nil || replayed {
		t.Fatalf("create: %v replayed=%v", err, replayed)
	}
	if parent.WarrantyStartsOn == nil || *parent.WarrantyStartsOn != domain.Day(time.Now()).Format(dateLayout) {
		t.Fatalf("REGISTRATION warranty starts %v, want today", parent.WarrantyStartsOn)
	}
	again, replayed, err := s.CreateServiceObject(ctx, ServiceObject{ItemID: item, Serial: "M-1", CustomerPartyID: customer,
		LegalEntityID: le, Warranty: term}, "obj-1", "hash-1")
	if err != nil || !replayed || again.ID != parent.ID {
		t.Fatalf("replay = %s, %v, replayed=%v", again.ID, err, replayed)
	}
	if _, _, err := s.CreateServiceObject(ctx, ServiceObject{ItemID: item, Serial: "M-1", CustomerPartyID: customer,
		LegalEntityID: le, Warranty: term}, "obj-2", "hash-2"); problemCode(err) != CodeObjectDuplicate {
		t.Fatalf("duplicate serial = %v, want %s", err, CodeObjectDuplicate)
	}
	if _, _, err := s.CreateServiceObject(ctx, ServiceObject{ItemID: item, Serial: "M-2", CustomerPartyID: ids.New(),
		LegalEntityID: le, ParentID: &parent.ID, Warranty: term}, "obj-3", "hash-3"); problemCode(err) != CodeObjectParent {
		t.Fatalf("foreign parent = %v, want %s", err, CodeObjectParent)
	}
	child, _, err := s.CreateServiceObject(ctx, ServiceObject{ItemID: item, Serial: "M-2", CustomerPartyID: customer,
		LegalEntityID: le, ParentID: &parent.ID, Warranty: term}, "obj-4", "hash-4")
	if err != nil {
		t.Fatal(err)
	}
	kids, err := s.ListServiceObjects(ctx, ObjectFilter{Parent: parent.ID, Limit: 10})
	if err != nil || len(kids.Items) != 1 || kids.Items[0].ID != child.ID {
		t.Fatalf("children = %+v, %v", kids.Items, err)
	}
}

// AC-45.6 end to end: the part is covered by the service object's warranty
// (zero-priced, warranty cost recorded), 1 of 3 labor hours by the agreement
// and 2 are billable. A case cannot name another customer's object.
func TestStore_WarrantyFromServiceObjectCoversPart(t *testing.T) {
	f := newFixture(t)
	obj, _, err := f.s.CreateServiceObject(f.ctx, ServiceObject{ItemID: ids.New(), Serial: "PUMP-7", CustomerPartyID: f.customer,
		LegalEntityID: f.le, ShippedOn: ptr("2026-01-15"),
		Warranty: domain.WarrantyTerm{StartTrigger: domain.StartShip, Months: 120, PartsCovered: true}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.OpenCase(f.ctx, OpenCaseInput{CustomerPartyID: ids.New(), LegalEntityID: f.le, ServiceObjectID: &obj.ID,
		Channel: "PHONE", Priority: 2, OpenedAt: time.Now(), FallbackSLAProfileID: &f.profile.ID}, "", ""); problemCode(err) != CodeObjectUnknown {
		t.Fatalf("case on another customer's object = %v, want %s", err, CodeObjectUnknown)
	}
	c, _, err := f.s.OpenCase(f.ctx, OpenCaseInput{CustomerPartyID: f.customer, LegalEntityID: f.le, ServiceObjectID: &obj.ID,
		Channel: "PHONE", Priority: 2, OpenedAt: time.Now()}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	lines := []domain.WorkLine{
		{ID: ids.New(), Kind: domain.KindPart, Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(250), UnitCost: decimal.NewFromInt(180)},
		{ID: ids.New(), Kind: domain.KindLabor, Qty: decimal.NewFromInt(3), UnitPrice: decimal.NewFromInt(100)},
	}
	wo := f.completedOrder(t, WorkOrder{CaseID: &c.ID, CustomerPartyID: f.customer, LegalEntityID: f.le, TerritoryID: f.terr,
		EstDurationMinutes: 180, Currency: "USD", Lines: lines})
	posted, _, err := f.s.PostWorkOrder(f.ctx, wo.ID, wo.Version, "post-warranty")
	if err != nil {
		t.Fatal(err)
	}
	p := posted.Posting
	if !p.WarrantyCost.Equal(decimal.NewFromInt(180)) || !p.BillableTotal.Equal(decimal.NewFromInt(200)) || !p.EntitlementConsumed.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("posting = warranty %s billable %s consumed %s; want 180, 200, 1", p.WarrantyCost, p.BillableTotal, p.EntitlementConsumed)
	}
	for _, l := range p.Lines {
		if l.Kind == domain.KindPart && (l.Coverage != domain.CoverWarranty || !l.Amount.IsZero()) {
			t.Fatalf("part line = %+v, want zero-priced WARRANTY", l)
		}
	}
}

// FR-45.13, BR-45.11, AC-45.12: the author cannot publish (KB-PUB-001);
// publishing version 2 retires version 1 and writes the publish event; only
// one version is open at a time; the applicable article ranks first; one vote
// per user, replaced when the user votes again.
func TestStore_KnowledgeLifecycleAndSuggestions(t *testing.T) {
	s, ctx := newSliceStore(t, 33)
	author, reviewer, item := ids.New(), ids.New(), ids.New()
	create := func(title, symptom, key string, app domain.Applicability) Article {
		t.Helper()
		a, _, err := s.CreateArticle(ctx, ArticleContent{Title: title, Language: "en", Symptom: symptom, Solution: "fix it", Applicability: app},
			author, key, "hash-"+key)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	publish := func(a Article) PublishResult {
		t.Helper()
		sub, err := s.SubmitArticle(ctx, a.ID, a.Revision)
		if err != nil {
			t.Fatal(err)
		}
		res, err := s.PublishArticle(ctx, sub.ID, sub.Revision, reviewer)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	pump := create("Pump noise", "pump noise at start", "kb-1", domain.Applicability{ItemIDs: []string{item}})
	if pump.Number == "" || pump.Version != 1 || pump.Status != domain.ArticleDraft || pump.Applicability.IncidentTypeIDs == nil {
		t.Fatalf("created = %+v", pump)
	}
	edited, err := s.UpdateArticle(ctx, pump.ID, pump.Revision, ArticleContent{Title: "Pump noise at start-up", Symptom: "pump noise at start",
		Cause: "worn bearing", Solution: "replace the bearing", Applicability: domain.Applicability{ItemIDs: []string{item}}})
	if err != nil || edited.Title != "Pump noise at start-up" || edited.Revision != pump.Revision+1 {
		t.Fatalf("edit = %+v, %v", edited, err)
	}
	if _, err := s.UpdateArticle(ctx, pump.ID, pump.Revision, edited.ArticleContent); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale edit = %v, want %s", err, CodeVersionConflict)
	}
	sub, err := s.SubmitArticle(ctx, edited.ID, edited.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishArticle(ctx, sub.ID, sub.Revision, author); problemCode(err) != domain.CodeReviewerSoD {
		t.Fatalf("author publishing = %v, want %s", err, domain.CodeReviewerSoD)
	}
	back, err := s.RejectArticle(ctx, sub.ID, sub.Revision)
	if err != nil || back.Status != domain.ArticleDraft {
		t.Fatalf("reject = %+v, %v", back, err)
	}
	v1 := publish(back)
	if v1.RetiredVersion != nil || v1.Article.PublishedAt == nil || v1.Article.ReviewerID == nil || *v1.Article.ReviewerID != reviewer {
		t.Fatalf("v1 = %+v", v1)
	}
	if got := outboxTypes(t, s, ctx, v1.Article.ID); len(got) != 1 || got[0] != svcevents.ArticlePublished {
		t.Fatalf("outbox = %v, want one %s", got, svcevents.ArticlePublished)
	}

	general := create("General pump noise", "pump noise", "kb-2", domain.Applicability{})
	publish(general)
	create("Unrelated", "door squeak", "kb-3", domain.Applicability{ItemIDs: []string{item}})

	v2, err := s.NewArticleVersion(ctx, v1.Article.ID, v1.Article.Revision, author)
	if err != nil {
		t.Fatal(err)
	}
	if v2.Number != v1.Article.Number || v2.Version != 2 || v2.Status != domain.ArticleDraft || v2.Title != v1.Article.Title {
		t.Fatalf("v2 = %+v", v2)
	}
	if _, err := s.NewArticleVersion(ctx, v1.Article.ID, v1.Article.Revision, author); problemCode(err) != CodeArticleOpen {
		t.Fatalf("second open version = %v, want %s", err, CodeArticleOpen)
	}
	pub2 := publish(v2)
	if pub2.RetiredVersion == nil || *pub2.RetiredVersion != 1 {
		t.Fatalf("publishing v2 retired %v, want version 1", pub2.RetiredVersion)
	}
	if old, err := s.GetArticle(ctx, v1.Article.ID); err != nil || old.Status != domain.ArticleRetired {
		t.Fatalf("v1 = %s, %v; want RETIRED", old.Status, err)
	}

	got, err := s.SuggestArticles(ctx, SuggestQuery{Text: "pump noise", ItemID: item, Language: "en", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Article.ID != pub2.Article.ID || got[0].Score <= got[1].Score {
		t.Fatalf("suggestions = %+v, want the applicable v2 first of the two PUBLISHED matches", got)
	}
	// AC-45.12 with a real case description: it holds words no article has, and
	// any shared word is enough to be a candidate.
	desc, err := s.SuggestArticles(ctx, SuggestQuery{Text: "Customer reports the pump makes a grinding noise every morning", ItemID: item, Language: "en", Limit: 5})
	if err != nil || len(desc) != 2 || desc[0].Article.ID != pub2.Article.ID {
		t.Fatalf("description suggestions = %+v, %v; want the applicable v2 first of two", desc, err)
	}
	// An item match alone makes a candidate, scored by the item weight only.
	byItem, err := s.SuggestArticles(ctx, SuggestQuery{Text: "display flickers", ItemID: item, Language: "en", Limit: 5})
	if err != nil || len(byItem) != 1 || byItem[0].Article.ID != pub2.Article.ID || byItem[0].Score != domain.WeightItem {
		t.Fatalf("item-only suggestions = %+v, %v; want v2 at %v", byItem, err, domain.WeightItem)
	}
	if none, err := s.SuggestArticles(ctx, SuggestQuery{Text: "display flickers", Language: "en", Limit: 5}); err != nil || len(none) != 0 {
		t.Fatalf("unmatched suggestions = %d, %v; want none", len(none), err)
	}
	if fr, err := s.SuggestArticles(ctx, SuggestQuery{Text: "pump noise", Language: "fr", Limit: 5}); err != nil || len(fr) != 0 {
		t.Fatalf("French suggestions = %d, %v; want none", len(fr), err)
	}

	if _, err := s.VoteArticle(ctx, v1.Article.ID, ids.New(), true); problemCode(err) != domain.CodeArticleNotInState {
		t.Fatalf("vote on retired = %v, want %s", err, domain.CodeArticleNotInState)
	}
	voter := ids.New()
	for _, helpful := range []bool{true, true, false} {
		if _, err := s.VoteArticle(ctx, pub2.Article.ID, voter, helpful); err != nil {
			t.Fatal(err)
		}
	}
	var art Article
	for i := 0; i < 9; i++ {
		if art, err = s.VoteArticle(ctx, pub2.Article.ID, ids.New(), true); err != nil {
			t.Fatal(err)
		}
	}
	if art.HelpfulYes != 9 || art.HelpfulNo != 1 || art.HelpfulPercent == nil || art.HelpfulPercent.String() != "90" {
		t.Fatalf("votes = %d yes %d no %v%%, want 9, 1, 90", art.HelpfulYes, art.HelpfulNo, art.HelpfulPercent)
	}

	retired, err := s.RetireArticle(ctx, pub2.Article.ID, art.Revision)
	if err != nil || retired.Status != domain.ArticleRetired {
		t.Fatalf("retire = %+v, %v", retired, err)
	}
	list, err := s.ListArticles(ctx, ArticleFilter{Number: v1.Article.Number, Limit: 10})
	if err != nil || len(list.Items) != 2 {
		t.Fatalf("versions of %s = %d, %v; want 2", v1.Article.Number, len(list.Items), err)
	}
}

// FR-45.14, AC-45.15: "My activities" lists an owner's activities earliest due
// first across records; the activities of one record are found by association;
// completing stamps DONE; a stale version conflicts; canceled and done are final.
func TestStore_ActivitiesListCompleteAndReschedule(t *testing.T) {
	s, ctx := newSliceStore(t, 34)
	owner, caseID, project := ids.New(), ids.New(), ids.New()
	due := func(h int) *time.Time { t := time.Date(2026, 10, 27, h, 0, 0, 0, time.UTC); return &t }
	mk := func(subject string, at *time.Time, assoc Association) Activity {
		t.Helper()
		a, _, err := s.CreateActivity(ctx, Activity{Kind: domain.ActivityTask, Subject: subject, OwnerWorkerID: owner, DueAt: at,
			Associations: []Association{assoc}}, "", "")
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	onProject := mk("Review project plan", due(15), Association{EntityType: "project", EntityID: project})
	onCase := mk("Call customer", due(9), Association{EntityType: "case", EntityID: caseID})
	undated := mk("Write notes", nil, Association{EntityType: "case", EntityID: caseID})
	if _, _, err := s.CreateActivity(ctx, Activity{Kind: domain.ActivityTask, Subject: "Someone else's", OwnerWorkerID: ids.New()}, "", ""); err != nil {
		t.Fatal(err)
	}

	mine, err := s.ListActivities(ctx, ActivityFilter{OwnerWorkerID: owner, Status: domain.ActivityOpen, Limit: 2})
	if err != nil || len(mine.Items) != 2 || mine.Items[0].ID != onCase.ID || mine.Items[1].ID != onProject.ID || mine.NextCursor == "" {
		t.Fatalf("first page = %+v, %v; want the case task then the project task", mine.Items, err)
	}
	rest, err := s.ListActivities(ctx, ActivityFilter{OwnerWorkerID: owner, Status: domain.ActivityOpen, Before: mine.NextCursor, Limit: 2})
	if err != nil || len(rest.Items) != 1 || rest.Items[0].ID != undated.ID || rest.NextCursor != "" {
		t.Fatalf("second page = %+v, %v; want the undated task last", rest.Items, err)
	}
	onRecord, err := s.ListActivities(ctx, ActivityFilter{EntityType: "case", EntityID: caseID, Limit: 10})
	if err != nil || len(onRecord.Items) != 2 {
		t.Fatalf("activities on the case = %d, %v; want 2", len(onRecord.Items), err)
	}

	done, err := s.CompleteActivity(ctx, onCase.ID, onCase.Version)
	if err != nil || done.Status != domain.ActivityDone || done.CompletedAt == nil || done.Version != onCase.Version+1 {
		t.Fatalf("complete = %+v, %v", done, err)
	}
	if _, err := s.CompleteActivity(ctx, onCase.ID, onCase.Version); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale complete = %v, want %s", err, CodeVersionConflict)
	}
	if again, err := s.CompleteActivity(ctx, onCase.ID, done.Version); err != nil || again.Version != done.Version {
		t.Fatalf("completing DONE again = %v, version %d; want a no-op", err, again.Version)
	}
	if _, err := s.CancelActivity(ctx, onCase.ID, done.Version); problemCode(err) != domain.CodeActivityNotOpen {
		t.Fatalf("cancel done = %v, want %s", err, domain.CodeActivityNotOpen)
	}
	if _, err := s.RescheduleActivity(ctx, onCase.ID, done.Version, Schedule{DueAt: due(10)}); problemCode(err) != domain.CodeActivityNotOpen {
		t.Fatalf("reschedule done = %v, want %s", err, domain.CodeActivityNotOpen)
	}
	moved, err := s.RescheduleActivity(ctx, onProject.ID, onProject.Version, Schedule{DueAt: due(8), StartAt: due(7), EndAt: due(8)})
	if err != nil || !moved.DueAt.Equal(*due(8)) || moved.StartAt == nil || moved.EndAt == nil {
		t.Fatalf("reschedule = %+v, %v", moved, err)
	}
	canceled, err := s.CancelActivity(ctx, undated.ID, undated.Version)
	if err != nil || canceled.Status != domain.ActivityCanceled || canceled.CompletedAt != nil {
		t.Fatalf("cancel = %+v, %v", canceled, err)
	}

	// Tenant isolation: another tenant sees none of these activities.
	other := principalCtx(testkit.TenantID(35))
	if _, err := s.GetActivity(other, onProject.ID); problemCode(err) != CodeActivityNotFound {
		t.Fatalf("other tenant read = %v, want %s", err, CodeActivityNotFound)
	}
	if page, err := s.ListActivities(other, ActivityFilter{OwnerWorkerID: owner, Limit: 10}); err != nil || len(page.Items) != 0 {
		t.Fatalf("other tenant list = %d, %v; want none", len(page.Items), err)
	}
}

func ptr(s string) *string { return &s }

// Slice 3 under RLS as passage_app: another tenant sees none of the service
// objects, activities or articles, and a caller scoped to another legal entity
// sees none of the entity's service objects.
func TestStore_Slice3TenantAndLegalEntityIsolation(t *testing.T) {
	s, t1 := newSliceStore(t, 35)
	t2 := principalCtx(testkit.TenantID(36))
	customer, le, owner := ids.New(), ids.New(), ids.New()
	obj, _, err := s.CreateServiceObject(t1, ServiceObject{ItemID: ids.New(), Serial: "ISO-1", CustomerPartyID: customer, LegalEntityID: le,
		Warranty: domain.WarrantyTerm{StartTrigger: domain.StartRegistration, Months: 12}}, "iso-object", "hash-iso-object")
	if err != nil {
		t.Fatal(err)
	}
	act, _, err := s.CreateActivity(t1, Activity{Kind: domain.ActivityTask, Subject: "Isolated", OwnerWorkerID: owner,
		Associations: []Association{{EntityType: "service_object", EntityID: obj.ID}}}, "iso-activity", "hash-iso-activity")
	if err != nil {
		t.Fatal(err)
	}
	art, _, err := s.CreateArticle(t1, ArticleContent{Title: "Isolated", Language: "en", Solution: "none"}, owner, "iso-article", "hash-iso-article")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.GetServiceObject(t2, obj.ID); problemCode(err) != CodeObjectNotFound {
		t.Fatalf("tenant 2 read tenant 1's service object: %v", err)
	}
	if _, err := s.InstallServiceObject(t2, obj.ID, obj.Version, "2026-10-26", nil); problemCode(err) != CodeObjectNotFound {
		t.Fatalf("tenant 2 installed tenant 1's service object: %v", err)
	}
	if _, err := s.GetActivity(t2, act.ID); problemCode(err) != CodeActivityNotFound {
		t.Fatalf("tenant 2 read tenant 1's activity: %v", err)
	}
	if page, err := s.ListActivities(t2, ActivityFilter{OwnerWorkerID: owner, Limit: 10}); err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant 2 lists %d activities, %v", len(page.Items), err)
	}
	if _, err := s.GetArticle(t2, art.ID); problemCode(err) != CodeArticleNotFound {
		t.Fatalf("tenant 2 read tenant 1's article: %v", err)
	}
	if _, err := s.VoteArticle(t2, art.ID, ids.New(), true); problemCode(err) != CodeArticleNotFound {
		t.Fatalf("tenant 2 voted on tenant 1's article: %v", err)
	}
	if page, err := s.ListArticles(t2, ArticleFilter{Limit: 10}); err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant 2 lists %d articles, %v", len(page.Items), err)
	}

	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(35), LegalEntityID: ids.New(), Duties: map[string]struct{}{}})
	if page, err := s.ListServiceObjects(scoped, ObjectFilter{Limit: 10}); err != nil || len(page.Items) != 0 {
		t.Fatalf("a caller scoped to another legal entity lists %d service objects, %v", len(page.Items), err)
	}
	if page, err := s.ListServiceObjects(t1, ObjectFilter{Limit: 10}); err != nil || len(page.Items) != 1 {
		t.Fatalf("tenant 1 lists %d service objects, %v", len(page.Items), err)
	}
}
