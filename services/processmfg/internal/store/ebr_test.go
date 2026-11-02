package store_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	pmfevents "github.com/BenjaminSRussell/passage/services/processmfg/internal/events"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

// ebrBatch registers a batch of a fresh item on a batch order of the same item,
// with the attribute definitions of defineItem, held until released.
func ebrBatch(t *testing.T, s *store.Postgres, code string) (itemID string, b store.Batch) {
	t.Helper()
	ctx := ctxFor(1, "")
	f, _, err := s.CreateFormula(ctx, ac241(code), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	o, _, err := s.CreateBatchOrder(ctx, store.NewBatchOrder{LegalEntityID: entityA, FormulaID: f.ID, BatchSize: dec("1000")}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	defineItem(t, s, f.ItemID)
	b, _, err = s.RegisterBatch(ctx, store.NewBatch{LegalEntityID: entityA, ItemID: f.ItemID, BatchNumber: "L-" + code, BatchOrderID: &o.ID, Disposition: "quarantine"},
		store.Idem{Key: "reg-" + code, Fingerprint: code})
	if err != nil {
		t.Fatal(err)
	}
	return f.ItemID, b
}

func defineProfile(t *testing.T, s *store.Postgres, code, item string, required bool) store.RecordProfile {
	t.Helper()
	p, created, err := s.DefineRecordProfile(ctxFor(1, ""), store.RecordProfile{LegalEntityID: entityA, Code: code, ItemID: item, ApprovalRequiredForRelease: required},
		store.Idem{Key: "prof-" + code, Fingerprint: code})
	if err != nil || !created || p.Version != 1 {
		t.Fatalf("define profile: created=%v %+v err=%v", created, p, err)
	}
	return p
}

func recordValues(t *testing.T, s *store.Postgres, b store.Batch, key, potency string) store.Batch {
	t.Helper()
	cur, err := s.GetBatch(ctxFor(1, ""), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	res, _, err := s.RecordAttributes(ctxFor(1, ""), b.ID, map[string]domain.Value{"potency": num(potency)}, cur.Version, store.Idem{Key: key, Fingerprint: key})
	if err != nil {
		t.Fatal(err)
	}
	return res.Batch
}

func sig() string { return ids.New() }

// asUser is tenant 1 with the whole scope acting as user. planner-1 (ctxFor)
// creates the batch orders of these tests, so it can never approve their records.
func asUser(user string) context.Context {
	p, _ := tenant.FromContext(ctxFor(1, ""))
	p.UserID = user
	return tenant.With(context.Background(), p)
}

func TestRecordProfile_DefineRetireAndList(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	item := ids.New()
	p := defineProfile(t, s, "EBR-1", item, true)
	if evts := outboxEvents(t, admin, pmfevents.BatchRecordProfileDefined); len(evts) != 1 {
		t.Fatalf("defined events %d", len(evts))
	}
	again, created, err := s.DefineRecordProfile(ctx, store.RecordProfile{LegalEntityID: entityA, Code: "EBR-1", ItemID: item, ApprovalRequiredForRelease: true},
		store.Idem{Key: "prof-EBR-1", Fingerprint: "EBR-1"})
	if err != nil || created || again.ID != p.ID || len(outboxEvents(t, admin, pmfevents.BatchRecordProfileDefined)) != 1 {
		t.Fatalf("replay created=%v err=%v", created, err)
	}
	if _, _, err := s.DefineRecordProfile(ctx, store.RecordProfile{LegalEntityID: entityA, Code: "EBR-X", ItemID: item}, store.Idem{Key: "dup-item", Fingerprint: "1"}); problemCode(err) != store.CodeProfileDuplicate {
		t.Fatalf("second profile for the item: %v", err)
	}
	if _, _, err := s.DefineRecordProfile(ctx, store.RecordProfile{LegalEntityID: entityA, Code: "EBR-1", ItemID: ids.New()}, store.Idem{Key: "dup-code", Fingerprint: "2"}); problemCode(err) != store.CodeProfileDuplicate {
		t.Fatalf("code taken: %v", err)
	}
	if _, _, err := s.RetireRecordProfile(ctx, p.ID, 9, store.Idem{Key: "rt-stale", Fingerprint: "s"}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale retire: %v", err)
	}
	ret, changed, err := s.RetireRecordProfile(ctx, p.ID, 1, store.Idem{Key: "rt-1", Fingerprint: "r"})
	if err != nil || !changed || ret.Version != 2 || ret.RetiredAt == nil || ret.RetiredBy == nil || *ret.RetiredBy != "planner-1" {
		t.Fatalf("retire %+v err=%v", ret, err)
	}
	if _, changed, err := s.RetireRecordProfile(ctx, p.ID, 1, store.Idem{Key: "rt-1", Fingerprint: "r"}); err != nil || changed || len(outboxEvents(t, admin, pmfevents.BatchRecordProfileRetired)) != 1 {
		t.Fatalf("retire replay changed=%v err=%v", changed, err)
	}
	if _, _, err := s.RetireRecordProfile(ctx, p.ID, 2, store.Idem{Key: "rt-2", Fingerprint: "r2"}); problemCode(err) != store.CodeProfileRetired {
		t.Fatalf("retire twice: %v", err)
	}
	// A retired profile frees the item for a new one.
	defineProfile(t, s, "EBR-2", item, false)
	active, err := s.ListRecordProfiles(ctx, store.ProfileFilter{ItemID: item, ActiveOnly: true, Limit: 10})
	all, err2 := s.ListRecordProfiles(ctx, store.ProfileFilter{ItemID: item, Limit: 10})
	if err != nil || err2 != nil || len(active) != 1 || active[0].Code != "EBR-2" || len(all) != 2 {
		t.Fatalf("list active=%d all=%d %v %v", len(active), len(all), err, err2)
	}
	if _, err := s.GetRecordProfile(ctx, ids.New()); problemCode(err) != store.CodeProfileNotFound {
		t.Fatalf("missing profile: %v", err)
	}
	if _, err := s.GetRecordProfile(ctxFor(2, ""), p.ID); problemCode(err) != store.CodeProfileNotFound {
		t.Fatalf("other tenant sees the profile: %v", err)
	}
}

// AC-24.16 and BR-24.15: the record is compiled, every exception is decided
// with a signature, approval is refused until then, and the sealed document
// matches its hash.
func TestRecord_CompileReviewApproveSeal(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	item, b := ebrBatch(t, s, "EBR-A")

	if _, _, err := s.CompileRecord(ctx, b.ID, store.Idem{Key: "c-none", Fingerprint: "n"}); problemCode(err) != store.CodeRecordNoProfile {
		t.Fatalf("no profile: %v", err)
	}
	defineProfile(t, s, "EBR-A", item, true)
	loose, _, err := s.RegisterBatch(ctx, store.NewBatch{LegalEntityID: entityA, ItemID: item, BatchNumber: "LOOSE", Disposition: "quarantine"}, store.Idem{Key: "loose", Fingerprint: "l"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CompileRecord(ctx, loose.ID, store.Idem{Key: "c-loose", Fingerprint: "l"}); problemCode(err) != store.CodeRecordNoOrder {
		t.Fatalf("no order: %v", err)
	}
	if _, _, err := s.CompileRecord(ctx, ids.New(), store.Idem{Key: "c-miss", Fingerprint: "m"}); problemCode(err) != store.CodeBatchNotFound {
		t.Fatalf("missing batch: %v", err)
	}

	// A value outside its limits (40 to 60) becomes an exception at compile.
	b = recordValues(t, s, b, "rec-1", "70")
	rec, changed, err := s.CompileRecord(ctx, b.ID, store.Idem{Key: "c-1", Fingerprint: "1"})
	if err != nil || !changed || rec.Status != store.RecordInReview || rec.ExceptionCount != 1 || rec.OpenExceptions != 1 || rec.Version != 1 ||
		len(rec.Exceptions) != 1 || rec.Exceptions[0].SourceType != "attribute" || !strings.Contains(rec.Exceptions[0].Description, "potency") ||
		rec.BatchNumber != "L-EBR-A" || !strings.Contains(string(rec.Content), `"batchOrder"`) || !strings.Contains(string(rec.Content), `"batch"`) {
		t.Fatalf("compile %+v err=%v", rec, err)
	}
	if again, changed, err := s.CompileRecord(ctx, b.ID, store.Idem{Key: "c-1", Fingerprint: "1"}); err != nil || changed || again.ID != rec.ID ||
		len(outboxEvents(t, admin, pmfevents.BatchRecordCompiled)) != 1 || len(outboxEvents(t, admin, pmfevents.BatchRecordExceptionRaised)) != 1 {
		t.Fatalf("compile replay changed=%v err=%v", changed, err)
	}
	// Compiling again refreshes the record and does not repeat the exception.
	re, _, err := s.CompileRecord(ctx, b.ID, store.Idem{Key: "c-2", Fingerprint: "2"})
	if err != nil || re.ID != rec.ID || re.ExceptionCount != 1 || re.Version != 2 {
		t.Fatalf("recompile %+v err=%v", re, err)
	}

	// A value recorded after the compile blocks approval until compiled again.
	b = recordValues(t, s, b, "rec-2", "20")
	if _, _, err := s.ApproveRecord(ctx, rec.ID, 2, store.Idem{Key: "ap-stale", Fingerprint: "s"}); problemCode(err) != store.CodeRecordStale {
		t.Fatalf("approve out of date: %v", err)
	}
	rec, _, err = s.CompileRecord(ctx, b.ID, store.Idem{Key: "c-3", Fingerprint: "3"})
	if err != nil || rec.ExceptionCount != 2 || rec.Version != 3 {
		t.Fatalf("recompile after second value %+v err=%v", rec, err)
	}

	if _, _, err := s.ApproveRecord(ctx, rec.ID, rec.Version, store.Idem{Key: "ap-open", Fingerprint: "o"}); problemCode(err) != domain.CodeEBRExceptions {
		t.Fatalf("approve with open exceptions: %v", err)
	}

	// A reviewer adds an exception by hand; each one is decided with a signature.
	rec, changed, err = s.RaiseException(ctx, rec.ID, store.NewException{Description: "Torn seal on drum 4", Severity: "critical"}, rec.Version, store.Idem{Key: "x-1", Fingerprint: "x"})
	if err != nil || !changed || rec.ExceptionCount != 3 || rec.Version != 4 {
		t.Fatalf("raise %+v err=%v", rec, err)
	}
	if _, _, err := s.RaiseException(ctx, rec.ID, store.NewException{Description: "late", Severity: "minor"}, 1, store.Idem{Key: "x-stale", Fingerprint: "xs"}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale raise: %v", err)
	}
	if _, _, err := s.DispositionException(ctx, rec.ID, ids.New(), store.ExceptionDisposition{Disposition: "accepted", SignatureID: sig()}, rec.Version, store.Idem{Key: "d-miss", Fingerprint: "dm"}); problemCode(err) != store.CodeExceptionNotFound {
		t.Fatalf("missing exception: %v", err)
	}
	reviewer := asUser("reviewer-1")
	for i, e := range rec.Exceptions {
		d := []string{"accepted", "deviation-raised", "rejected"}[i]
		rec, changed, err = s.DispositionException(reviewer, rec.ID, e.ID, store.ExceptionDisposition{Disposition: d, Comment: "reviewed", SignatureID: sig()}, rec.Version,
			store.Idem{Key: "d-" + e.ID, Fingerprint: e.ID})
		if err != nil || !changed {
			t.Fatalf("disposition %d: %+v err=%v", i, rec, err)
		}
	}
	if rec.OpenExceptions != 0 || rec.Version != 7 {
		t.Fatalf("after dispositions %+v", rec)
	}
	for _, e := range rec.Exceptions {
		if e.Disposition == "" || e.SignatureID == nil || e.DispositionedBy == nil || *e.DispositionedBy != "reviewer-1" {
			t.Fatalf("exception %+v", e)
		}
	}
	// A signed decision is final: it is not overwritten, by the store or the database.
	if _, _, err := s.DispositionException(reviewer, rec.ID, rec.Exceptions[0].ID, store.ExceptionDisposition{Disposition: "rejected", SignatureID: sig()}, rec.Version,
		store.Idem{Key: "d-again", Fingerprint: "da"}); problemCode(err) != store.CodeExceptionDecided {
		t.Fatalf("decide again: %v", err)
	}
	if _, err := admin.Exec(context.Background(), `update ebr_exception set disposition = 'rejected' where id = $1`, rec.Exceptions[0].ID); err == nil {
		t.Fatal("database overwrote a decided exception")
	}

	// Segregation of duties (PRD 4.24.17): whoever reviewed the record, or
	// created its batch order (planner-1), does not approve it.
	if _, _, err := s.ApproveRecord(reviewer, rec.ID, rec.Version, store.Idem{Key: "ap-rev", Fingerprint: "ar"}); problemCode(err) != domain.CodeEBRSegregation {
		t.Fatalf("reviewer approves: %v", err)
	}
	if _, _, err := s.ApproveRecord(ctx, rec.ID, rec.Version, store.Idem{Key: "ap-own", Fingerprint: "ao"}); problemCode(err) != domain.CodeEBRSegregation {
		t.Fatalf("order creator (who also raised an exception) approves: %v", err)
	}
	qa := asUser("qa-1")

	// The batch is held: releasing it needs the approved record (PMF-0015).
	cur, _ := s.GetBatch(ctx, b.ID)
	if _, _, err := s.ChangeDisposition(ctx, b.ID, "available", "release", cur.Version, store.Idem{Key: "rel-1", Fingerprint: "r1"}); problemCode(err) != domain.CodeEBRNotApproved {
		t.Fatalf("release before approval: %v", err)
	}
	if _, _, err := s.SealRecord(qa, rec.ID, rec.Version, store.Idem{Key: "seal-early", Fingerprint: "se"}); problemCode(err) != store.CodeRecordState {
		t.Fatalf("seal before approval: %v", err)
	}
	if _, _, err := s.ApproveRecord(qa, rec.ID, rec.Version-1, store.Idem{Key: "ap-v", Fingerprint: "av"}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("approve stale version: %v", err)
	}
	app, changed, err := s.ApproveRecord(qa, rec.ID, rec.Version, store.Idem{Key: "ap-1", Fingerprint: "a"})
	if err != nil || !changed || app.Status != store.RecordApproved || app.ApprovedBy == nil || *app.ApprovedBy != "qa-1" || app.ApprovedAt == nil {
		t.Fatalf("approve %+v err=%v", app, err)
	}
	if _, changed, err := s.ApproveRecord(qa, rec.ID, rec.Version, store.Idem{Key: "ap-1", Fingerprint: "a"}); err != nil || changed || len(outboxEvents(t, admin, pmfevents.BatchRecordApproved)) != 1 {
		t.Fatalf("approve replay changed=%v err=%v", changed, err)
	}
	if _, _, err := s.RaiseException(ctx, rec.ID, store.NewException{Description: "after", Severity: "minor"}, app.Version, store.Idem{Key: "x-late", Fingerprint: "xl"}); problemCode(err) != store.CodeRecordState {
		t.Fatalf("raise after approval: %v", err)
	}
	if _, _, err := s.DispositionException(reviewer, rec.ID, app.Exceptions[0].ID, store.ExceptionDisposition{Disposition: "accepted", SignatureID: sig()}, app.Version, store.Idem{Key: "d-late", Fingerprint: "dl"}); problemCode(err) != store.CodeRecordState {
		t.Fatalf("disposition after approval: %v", err)
	}
	if _, _, err := s.CompileRecord(ctx, b.ID, store.Idem{Key: "c-late", Fingerprint: "cl"}); problemCode(err) != store.CodeRecordState {
		t.Fatalf("compile after approval: %v", err)
	}
	if _, _, err := s.ApproveRecord(qa, rec.ID, app.Version, store.Idem{Key: "ap-2", Fingerprint: "a2"}); problemCode(err) != store.CodeRecordState {
		t.Fatalf("approve twice: %v", err)
	}
	// The database keeps an approved record's content and lets it move only to sealed.
	if _, err := admin.Exec(context.Background(), `update electronic_batch_record set content = '{}' where id = $1`, rec.ID); err == nil {
		t.Fatal("approved content changed")
	}
	if _, err := admin.Exec(context.Background(), `update electronic_batch_record set status = 'in-review' where id = $1`, rec.ID); err == nil {
		t.Fatal("approved record moved back to review")
	}

	// With the record approved the batch is released.
	cur, _ = s.GetBatch(ctx, b.ID)
	if got, _, err := s.ChangeDisposition(ctx, b.ID, "available", "release", cur.Version, store.Idem{Key: "rel-2", Fingerprint: "r2"}); err != nil || got.Disposition != "available" {
		t.Fatalf("release after approval: %+v err=%v", got, err)
	}

	sealed, changed, err := s.SealRecord(qa, rec.ID, app.Version, store.Idem{Key: "seal-1", Fingerprint: "s1"})
	if err != nil || !changed || sealed.Status != store.RecordSealed || sealed.SealedDocument == nil || sealed.ContentHash == nil || sealed.SealedBy == nil || sealed.SealedAt == nil {
		t.Fatalf("seal %+v err=%v", sealed, err)
	}
	if *sealed.ContentHash != store.HashHex(*sealed.SealedDocument) {
		t.Fatalf("content hash %s is not the SHA-256 of the sealed document", *sealed.ContentHash)
	}
	var doc struct {
		RecordID           string `json:"recordId"`
		ApprovedBy         string `json:"approvedBy"`
		ApprovedAt         string `json:"approvedAt"`
		SealedAt           string `json:"sealedAt"`
		SignaturesVerified *bool  `json:"signaturesVerified"`
		Content            struct {
			Batch struct {
				Disposition string `json:"disposition"`
			} `json:"batch"`
		} `json:"content"`
		Exceptions []struct {
			Disposition string `json:"disposition"`
			SignatureID string `json:"signatureId"`
		} `json:"exceptions"`
	}
	// What is sealed is what was approved: the release that followed the approval
	// is not in it. Times are UTC, and the document says the signature ids were
	// not verified (there is no signature service yet).
	if err := json.Unmarshal([]byte(*sealed.SealedDocument), &doc); err != nil || doc.RecordID != rec.ID || doc.ApprovedBy != "qa-1" || len(doc.Exceptions) != 3 ||
		doc.Exceptions[0].Disposition == "" || doc.Exceptions[0].SignatureID == "" || doc.Content.Batch.Disposition != "quarantine" ||
		!strings.HasSuffix(doc.ApprovedAt, "Z") || !strings.HasSuffix(doc.SealedAt, "Z") || doc.SignaturesVerified == nil || *doc.SignaturesVerified {
		t.Fatalf("sealed document %v %+v", err, doc)
	}
	if string(app.Content) == "" || !jsonEqual(t, string(app.Content), *sealed.SealedDocument) {
		t.Fatal("sealed content differs from the approved content")
	}
	if _, changed, err := s.SealRecord(qa, rec.ID, app.Version, store.Idem{Key: "seal-1", Fingerprint: "s1"}); err != nil || changed || len(outboxEvents(t, admin, pmfevents.BatchRecordSealed)) != 1 {
		t.Fatalf("seal replay changed=%v err=%v", changed, err)
	}
	if _, _, err := s.SealRecord(qa, rec.ID, sealed.Version, store.Idem{Key: "seal-2", Fingerprint: "s2"}); problemCode(err) != store.CodeRecordState {
		t.Fatalf("seal twice: %v", err)
	}
	got, err := s.GetRecord(ctx, rec.ID)
	if err != nil || got.ContentHash == nil || *got.ContentHash != *sealed.ContentHash || got.Status != store.RecordSealed {
		t.Fatalf("get sealed %+v err=%v", got, err)
	}

	// The database itself refuses any change to a sealed record, its deletion and
	// any change to its exceptions, even for the table owner.
	for name, stmt := range map[string]string{
		"drop the document": `update electronic_batch_record set sealed_document = null where id = $1`,
		"rewrite the hash":  `update electronic_batch_record set content_hash = sha256('x') where id = $1`,
		"touch the version": `update electronic_batch_record set version = version + 1 where id = $1`,
		"delete the record": `delete from electronic_batch_record where id = $1`,
		"edit an exception": `update ebr_exception set description = 'nothing' where record_id = $1`,
		"delete exceptions": `delete from ebr_exception where record_id = $1`,
		"add an exception":  `insert into ebr_exception (tenant_id, id, record_id, legal_entity_id, source_type, source_id, description, created_by) select tenant_id, gen_random_uuid(), id, legal_entity_id, 'manual', 'late', 'late', 'x' from electronic_batch_record where id = $1`,
	} {
		if _, err := admin.Exec(context.Background(), stmt, rec.ID); err == nil {
			t.Fatalf("%s: the database accepted a change to a sealed record", name)
		}
	}
	if store.HashHex(*got.SealedDocument) != *got.ContentHash {
		t.Fatal("stored hash does not match the stored document")
	}

	var ex pmfevents.BatchRecordExceptionData
	if evts := outboxEvents(t, admin, pmfevents.BatchRecordExceptionDisposed); len(evts) != 3 || json.Unmarshal(evts[0].Data, &ex) != nil || ex.RecordID != rec.ID || ex.Disposition != "accepted" || ex.SignatureID == "" {
		t.Fatalf("disposition events %d %+v", len(evts), ex)
	}
	var sev pmfevents.BatchRecordData
	if evts := outboxEvents(t, admin, pmfevents.BatchRecordSealed); len(evts) != 1 || json.Unmarshal(evts[0].Data, &sev) != nil || sev.ContentHash != *sealed.ContentHash || sev.Status != "sealed" {
		t.Fatalf("sealed event %+v", sev)
	}

	list, err := s.ListRecords(ctx, store.RecordFilter{Status: "sealed", ItemID: item, Limit: 10})
	if err != nil || len(list) != 1 || list[0].Content != nil || list[0].ExceptionCount != 3 {
		t.Fatalf("list %+v err=%v", list, err)
	}
	if none, err := s.ListRecords(ctx, store.RecordFilter{Status: "in-review", Limit: 10}); err != nil || len(none) != 0 {
		t.Fatalf("in-review list %d %v", len(none), err)
	}
	if _, err := s.GetRecord(ctxFor(2, ""), rec.ID); problemCode(err) != store.CodeRecordNotFound {
		t.Fatalf("other tenant sees the record: %v", err)
	}
	if _, err := s.GetRecord(ctxFor(1, entityB), rec.ID); problemCode(err) != store.CodeRecordNotFound {
		t.Fatalf("other legal entity sees the record: %v", err)
	}
}

// A record with no exception is approved at once; a profile that does not ask
// for approval never holds a release.
func TestRecord_CleanBatchAndProfileWithoutGate(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	item, b := ebrBatch(t, s, "EBR-B")
	defineProfile(t, s, "EBR-B", item, true)
	b = recordValues(t, s, b, "ok-1", "50")
	rec, _, err := s.CompileRecord(ctx, b.ID, store.Idem{Key: "c-b", Fingerprint: "b"})
	if err != nil || rec.ExceptionCount != 0 {
		t.Fatalf("compile %+v err=%v", rec, err)
	}
	if _, _, err := s.RegisterBatch(ctx, store.NewBatch{LegalEntityID: entityA, ItemID: item, BatchNumber: "BORN-AVAILABLE", Disposition: "available"}, store.Idem{Key: "born", Fingerprint: "b"}); problemCode(err) != domain.CodeEBRNotApproved {
		t.Fatalf("register as available under a profile: %v", err)
	}
	app, _, err := s.ApproveRecord(asUser("qa-1"), rec.ID, rec.Version, store.Idem{Key: "ap-b", Fingerprint: "ab"})
	if err != nil || app.Status != store.RecordApproved {
		t.Fatalf("approve clean record %+v err=%v", app, err)
	}
	// The stored hash must be the SHA-256 of the stored document.
	if _, err := admin.Exec(context.Background(), `update electronic_batch_record set status = 'sealed', sealed_at = now(), sealed_document = '{}', content_hash = sha256('other') where id = $1`, rec.ID); err == nil {
		t.Fatal("sealed with a hash of another document")
	}

	item2, b2 := ebrBatch(t, s, "EBR-C")
	defineProfile(t, s, "EBR-C", item2, false)
	cur, _ := s.GetBatch(ctx, b2.ID)
	if got, _, err := s.ChangeDisposition(ctx, b2.ID, "available", "release", cur.Version, store.Idem{Key: "rel-c", Fingerprint: "c"}); err != nil || got.Disposition != "available" {
		t.Fatalf("release without gate: %+v err=%v", got, err)
	}
	// A retired profile no longer holds anything.
	other := ids.New()
	if _, _, err := s.RegisterBatch(ctx, store.NewBatch{LegalEntityID: entityA, ItemID: other, BatchNumber: "NO-PROFILE", Disposition: "available"}, store.Idem{Key: "np", Fingerprint: "np"}); err != nil {
		t.Fatalf("item without profile: %v", err)
	}
}

func jsonEqual(t *testing.T, content, sealedDoc string) bool {
	t.Helper()
	var c, d any
	var doc struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal([]byte(content), &c) != nil || json.Unmarshal([]byte(sealedDoc), &doc) != nil || json.Unmarshal(doc.Content, &d) != nil {
		t.Fatal("not JSON")
	}
	a, _ := json.Marshal(c)
	b, _ := json.Marshal(d)
	return string(a) == string(b)
}

// approvedBatch registers a quarantined batch of item on order, records clean
// values, compiles its record, has qa-1 approve it and releases it.
func approvedBatch(t *testing.T, s *store.Postgres, item, orderID, number string) store.Batch {
	t.Helper()
	ctx := ctxFor(1, "")
	b, _, err := s.RegisterBatch(ctx, store.NewBatch{LegalEntityID: entityA, ItemID: item, BatchNumber: number, BatchOrderID: &orderID, Disposition: "quarantine"},
		store.Idem{Key: "reg-" + number, Fingerprint: number})
	if err != nil {
		t.Fatal(err)
	}
	b = recordFor(t, s, b, "50", "A", "2027-06-01")
	rec, _, err := s.CompileRecord(ctx, b.ID, store.Idem{Key: "c-" + number, Fingerprint: number})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ApproveRecord(asUser("qa-1"), rec.ID, rec.Version, store.Idem{Key: "ap-" + number, Fingerprint: number}); err != nil {
		t.Fatal(err)
	}
	cur, _ := s.GetBatch(ctx, b.ID)
	out, _, err := s.ChangeDisposition(ctx, b.ID, "available", "release", cur.Version, store.Idem{Key: "rel-" + number, Fingerprint: number})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A merge cannot make a releasable batch out of one whose record was never
// approved, and a merged batch is released on its sources' records, through
// earlier merges too (PMF-0015).
func TestRecord_ReleaseGateFollowsMerges(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	f, _, err := s.CreateFormula(ctx, ac241("EBR-M"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	o, _, err := s.CreateBatchOrder(ctx, store.NewBatchOrder{LegalEntityID: entityA, FormulaID: f.ID, BatchSize: dec("1000")}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	item := f.ItemID
	defineItem(t, s, item)
	// Released before the item had a profile, so it has no record.
	old := recordFor(t, s, register(t, s, item, "OLD"), "50", "A", "2027-06-01")
	defineProfile(t, s, "EBR-M", item, true)
	a := approvedBatch(t, s, item, o.ID, "A")
	b := approvedBatch(t, s, item, o.ID, "B")

	if _, _, err := s.MergeBatches(ctx, store.MergeInput{TargetNumber: "A-OLD", Sources: []store.MergeSource{src(a, "10", "1"), src(old, "10", "1")}},
		store.Idem{Key: "m-old", Fingerprint: "o"}); problemCode(err) != domain.CodeEBRNotApproved || !strings.Contains(err.Error(), "OLD") {
		t.Fatalf("merge with an unapproved source: %v", err)
	}
	ab, _, err := s.MergeBatches(ctx, store.MergeInput{TargetNumber: "AB", Sources: []store.MergeSource{src(a, "10", "1"), src(b, "10", "1")}},
		store.Idem{Key: "m-ab", Fingerprint: "ab"})
	if err != nil || ab.Batch.Disposition != "available" {
		t.Fatalf("merge of approved batches %+v err=%v", ab, err)
	}
	// AB has no record of its own; it stands on A and B, also when merged again
	// or held and released.
	a, _ = s.GetBatch(ctx, a.ID)
	abc, _, err := s.MergeBatches(ctx, store.MergeInput{TargetNumber: "ABA", Sources: []store.MergeSource{src(ab.Batch, "5", "1"), src(a, "5", "1")}},
		store.Idem{Key: "m-aba", Fingerprint: "aba"})
	if err != nil {
		t.Fatalf("merge of a merged batch: %v", err)
	}
	held, _, err := s.ChangeDisposition(ctx, abc.Batch.ID, "quarantine", "hold", abc.Batch.Version, store.Idem{Key: "hold-aba", Fingerprint: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ChangeDisposition(ctx, held.ID, "available", "release", held.Version, store.Idem{Key: "rel-aba", Fingerprint: "r"}); err != nil {
		t.Fatalf("release of a merged batch from approved sources: %v", err)
	}
}
