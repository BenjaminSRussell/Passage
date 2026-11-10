package store

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
)

const (
	otherLegalEntity = "00000000-0000-0000-0000-0000000000a2"
	billCustomer     = "00000000-0000-0000-0000-0000000000c1"
	invoiceDocID     = "00000000-0000-0000-0000-00000000b001"
	creditDocID      = "00000000-0000-0000-0000-00000000b002"
)

func scopedCtx(n int, legalEntityID string) context.Context {
	p, err := tenant.ServicePrincipal("receivables-billing", testkit.TenantID(n), legalEntityID)
	if err != nil {
		panic(err)
	}
	return tenant.With(context.Background(), p)
}

func invoiceDoc(t *testing.T, id, number string, installments ...domain.BillingInstallment) BillingDocument {
	t.Helper()
	items, err := domain.PlanInvoiceItems(number, "USD", amt("1000.00"), "2027-04-30", installments)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return BillingDocument{SourceType: "INVOICE", ID: id, Number: number, LegalEntityID: legalEntity, CustomerID: billCustomer, Currency: "USD",
		DocumentDate: "2027-03-31", Items: items}
}

func creditDoc(t *testing.T, id, original, number string) BillingDocument {
	t.Helper()
	item, err := domain.PlanCreditItem(number, "USD", amt("150.00"), "2027-04-10")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return BillingDocument{SourceType: "CREDIT_NOTE", ID: id, Number: number, OriginalID: original, LegalEntityID: legalEntity, CustomerID: billCustomer,
		Currency: "USD", DocumentDate: "2027-04-10", Items: []domain.PlannedItem{item}}
}

type itemRow struct {
	number, due, amount, open, status, sourceType string
	installment                                   int
	original                                      *string
}

func itemsOf(t *testing.T, s *Postgres, ctx context.Context) []itemRow {
	t.Helper()
	var out []itemRow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select document_number, due_date::text, amount::text, open_amount::text, status, source_type, coalesce(installment_no, 0), original_doc_id::text
			from open_item order by document_number`)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (itemRow, error) {
			var i itemRow
			return i, r.Scan(&i.number, &i.due, &i.amount, &i.open, &i.status, &i.sourceType, &i.installment, &i.original)
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestApplyBillingDocument_InvoiceOpensOneItemPerInstallmentAndReplayChangesNothing(t *testing.T) {
	s := setup(t)
	ctx := scopedCtx(40, legalEntity)
	doc := invoiceDoc(t, invoiceDocID, "INV-2027-0000001",
		domain.BillingInstallment{No: 1, Amount: amt("333.34"), DueDate: "2027-04-30"},
		domain.BillingInstallment{No: 2, Amount: amt("333.33"), DueDate: "2027-05-30"},
		domain.BillingInstallment{No: 3, Amount: amt("333.33"), DueDate: "2027-06-30"})
	created, err := s.ApplyBillingDocument(ctx, doc)
	if err != nil || created != 3 {
		t.Fatalf("apply: created %d err %v", created, err)
	}
	got := itemsOf(t, s, ctx)
	want := []itemRow{
		{"INV-2027-0000001/1", "2027-04-30", "333.340000", "333.340000", "OPEN", "INVOICE", 1, nil},
		{"INV-2027-0000001/2", "2027-05-30", "333.330000", "333.330000", "OPEN", "INVOICE", 2, nil},
		{"INV-2027-0000001/3", "2027-06-30", "333.330000", "333.330000", "OPEN", "INVOICE", 3, nil},
	}
	if len(got) != 3 {
		t.Fatalf("items %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("item %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	for range 2 {
		again, err := s.ApplyBillingDocument(ctx, doc)
		if err != nil || again != 0 {
			t.Fatalf("replay: created %d err %v", again, err)
		}
	}
	if after := itemsOf(t, s, ctx); len(after) != 3 {
		t.Fatalf("replay changed the items: %+v", after)
	}
}

func TestApplyBillingDocument_InvoiceWithoutPlanIsOneItemDueOnTheDueDate(t *testing.T) {
	s := setup(t)
	ctx := scopedCtx(41, legalEntity)
	if created, err := s.ApplyBillingDocument(ctx, invoiceDoc(t, invoiceDocID, "INV-2027-0000002")); err != nil || created != 1 {
		t.Fatalf("apply: created %d err %v", created, err)
	}
	got := itemsOf(t, s, ctx)
	if len(got) != 1 || got[0].number != "INV-2027-0000002" || got[0].due != "2027-04-30" || got[0].amount != "1000.000000" {
		t.Fatalf("items %+v", got)
	}
}

func TestApplyBillingDocument_CreditNoteOpensALinkedCreditItemOnce(t *testing.T) {
	s := setup(t)
	ctx := scopedCtx(42, legalEntity)
	if _, err := s.ApplyBillingDocument(ctx, invoiceDoc(t, invoiceDocID, "INV-2027-0000003")); err != nil {
		t.Fatal(err)
	}
	cn := creditDoc(t, creditDocID, invoiceDocID, "CN-2027-0000001")
	for i, want := range []int{1, 0} {
		created, err := s.ApplyBillingDocument(ctx, cn)
		if err != nil || created != want {
			t.Fatalf("apply %d: created %d err %v", i, created, err)
		}
	}
	got := itemsOf(t, s, ctx)
	if len(got) != 2 {
		t.Fatalf("items %+v", got)
	}
	credit := got[0]
	if credit.number != "CN-2027-0000001" || credit.sourceType != "CREDIT_NOTE" || credit.amount != "-150.000000" ||
		credit.open != "-150.000000" || credit.due != "2027-04-10" || credit.original == nil || *credit.original != invoiceDocID {
		t.Fatalf("credit item %+v", credit)
	}
	var side string
	item, err := s.ListOpenItems(ctx, OpenItemFilter{OpenOnly: true}, 10)
	if err != nil || len(item) != 2 {
		t.Fatalf("list %v %v", item, err)
	}
	for _, it := range item {
		if it.SourceType == "CREDIT_NOTE" {
			side = it.Side
		}
	}
	if side != "CREDIT" {
		t.Fatalf("side %q", side)
	}
}

func TestApplyBillingDocument_CreditNoteWaitsForItsInvoiceInTheSameLegalEntity(t *testing.T) {
	s := setup(t)
	ctx := scopedCtx(43, legalEntity)
	cn := creditDoc(t, creditDocID, invoiceDocID, "CN-2027-0000002")
	if _, err := s.ApplyBillingDocument(ctx, cn); !errors.Is(err, ErrOriginalInvoiceNotFound) {
		t.Fatalf("credit note before its invoice: %v", err)
	}
	other := invoiceDoc(t, invoiceDocID, "INV-2027-0000020")
	other.LegalEntityID = otherLegalEntity
	if _, err := s.ApplyBillingDocument(scopedCtx(43, otherLegalEntity), other); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyBillingDocument(ctx, cn); !errors.Is(err, ErrOriginalInvoiceNotFound) {
		t.Fatalf("credit note whose invoice is in another legal entity: %v", err)
	}
	if got := itemsOf(t, s, ctx); len(got) != 0 {
		t.Fatalf("a credit note without its invoice opened %+v", got)
	}
	third := invoiceDoc(t, "00000000-0000-0000-0000-00000000b0f1", "INV-2027-0000021")
	if _, err := s.ApplyBillingDocument(ctx, third); err != nil {
		t.Fatal(err)
	}
	cn.OriginalID = "00000000-0000-0000-0000-00000000b0f1"
	if created, err := s.ApplyBillingDocument(ctx, cn); err != nil || created != 1 {
		t.Fatalf("retry once the invoice is here: created %d err %v", created, err)
	}
}

func TestApplyBillingDocument_CreditNoteForAnotherCustomerOrCurrencyIsRefusedAndOpensNothing(t *testing.T) {
	s := setup(t)
	ctx := scopedCtx(44, legalEntity)
	if _, err := s.ApplyBillingDocument(ctx, invoiceDoc(t, invoiceDocID, "INV-2027-0000004")); err != nil {
		t.Fatal(err)
	}
	other := creditDoc(t, creditDocID, invoiceDocID, "CN-2027-0000003")
	other.CustomerID = "00000000-0000-0000-0000-0000000000c2"
	if _, err := s.ApplyBillingDocument(ctx, other); problemCode(err) != CodeBillingMismatch {
		t.Fatalf("other customer: %v", err)
	}
	eur := creditDoc(t, creditDocID, invoiceDocID, "CN-2027-0000003")
	eur.Currency = "EUR"
	if _, err := s.ApplyBillingDocument(ctx, eur); problemCode(err) != CodeBillingMismatch {
		t.Fatalf("other currency: %v", err)
	}
	if got := itemsOf(t, s, ctx); len(got) != 1 {
		t.Fatalf("items %+v", got)
	}
}

func TestApplyBillingDocument_NumberTakenByAHandEnteredItemParksAndNothingIsDoubled(t *testing.T) {
	s := setup(t)
	ctx := scopedCtx(45, legalEntity)
	manual, err := s.CreateOpenItem(tenantCtx(45), OpenItem{LegalEntityID: legalEntity, CustomerID: billCustomer, SourceType: "INVOICE",
		DocumentNumber: "INV-2027-0000005", DocumentDate: "2027-03-31", DueDate: "2027-04-30", Currency: "USD",
		Amount: amt("1000.00"), OpenAmount: amt("1000.00")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyBillingDocument(ctx, invoiceDoc(t, invoiceDocID, "INV-2027-0000005")); problemCode(err) != CodeBillingNumberTaken {
		t.Fatalf("apply: %v", err)
	}
	got := itemsOf(t, s, ctx)
	if len(got) != 1 || got[0].number != manual.DocumentNumber {
		t.Fatalf("items %+v", got)
	}
}

func TestApplyBillingDocument_ManualItemWithABillingNumberIsRefusedAfterTheEvent(t *testing.T) {
	s := setup(t)
	if _, err := s.ApplyBillingDocument(scopedCtx(46, legalEntity), invoiceDoc(t, invoiceDocID, "INV-2027-0000006")); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateOpenItem(tenantCtx(46), OpenItem{LegalEntityID: legalEntity, CustomerID: billCustomer, SourceType: "INVOICE",
		DocumentNumber: "INV-2027-0000006", DocumentDate: "2027-03-31", DueDate: "2027-04-30", Currency: "USD",
		Amount: amt("1000.00"), OpenAmount: amt("1000.00")})
	if problemCode(err) != CodePaymentDuplicate {
		t.Fatalf("manual create after the event: %v", err)
	}
}

func TestApplyBillingDocument_PartialFailureRollsBackAndRetryOpensTheWholeSet(t *testing.T) {
	s := setup(t)
	ctx := scopedCtx(47, legalEntity)
	execAdminTenant(t, s, 47, legalEntity, "INV-2027-0000007/3")
	doc := invoiceDoc(t, invoiceDocID, "INV-2027-0000007",
		domain.BillingInstallment{No: 1, Amount: amt("500.00"), DueDate: "2027-04-30"},
		domain.BillingInstallment{No: 2, Amount: amt("250.00"), DueDate: "2027-05-30"},
		domain.BillingInstallment{No: 3, Amount: amt("250.00"), DueDate: "2027-06-30"})
	if _, err := s.ApplyBillingDocument(ctx, doc); problemCode(err) != CodeBillingNumberTaken {
		t.Fatalf("apply: %v", err)
	}
	if got := itemsOf(t, s, ctx); len(got) != 1 {
		t.Fatalf("a refused document left items behind: %+v", got)
	}
	execAdmin(t, s, `delete from open_item where document_number = 'INV-2027-0000007/3'`)
	if created, err := s.ApplyBillingDocument(ctx, doc); err != nil || created != 3 {
		t.Fatalf("retry: created %d err %v", created, err)
	}
}

func TestApplyBillingDocument_AnImportedItemWithTheInvoiceNumberBlocksItsInstallments(t *testing.T) {
	s := setup(t)
	ctx := scopedCtx(50, legalEntity)
	if _, err := s.CreateOpenItem(tenantCtx(50), OpenItem{LegalEntityID: legalEntity, CustomerID: billCustomer, SourceType: "INVOICE",
		DocumentNumber: "INV-2027-0000030", DocumentDate: "2027-03-31", DueDate: "2027-04-30", Currency: "USD",
		Amount: amt("1000.00"), OpenAmount: amt("1000.00")}); err != nil {
		t.Fatal(err)
	}
	doc := invoiceDoc(t, invoiceDocID, "INV-2027-0000030",
		domain.BillingInstallment{No: 1, Amount: amt("600.00"), DueDate: "2027-04-30"},
		domain.BillingInstallment{No: 2, Amount: amt("400.00"), DueDate: "2027-05-30"})
	if _, err := s.ApplyBillingDocument(ctx, doc); problemCode(err) != CodeBillingNumberTaken {
		t.Fatalf("installments beside an imported whole invoice: %v", err)
	}
	if got := itemsOf(t, s, ctx); len(got) != 1 {
		t.Fatalf("the receivable was doubled: %+v", got)
	}
}

func TestApplyBillingDocument_NumberCheckTreatsLikeWildcardsAsText(t *testing.T) {
	s := setup(t)
	ctx := scopedCtx(51, legalEntity)
	execAdminTenant(t, s, 51, legalEntity, "INVX0001/1")
	execAdminTenant(t, s, 51, legalEntity, "INV%0002/1")
	if created, err := s.ApplyBillingDocument(ctx, invoiceDoc(t, invoiceDocID, "INV_0001")); err != nil || created != 1 {
		t.Fatalf("a number with _ matched another number: created %d err %v", created, err)
	}
	if created, err := s.ApplyBillingDocument(ctx, invoiceDoc(t, "00000000-0000-0000-0000-00000000b0f2", "INV%")); err != nil || created != 1 {
		t.Fatalf("a number with %% matched other numbers: created %d err %v", created, err)
	}
}

func TestApplyBillingDocument_SameDocumentIdWithOtherContentIsAMismatchNotARepeat(t *testing.T) {
	s := setup(t)
	a := scopedCtx(52, legalEntity)
	doc := invoiceDoc(t, invoiceDocID, "INV-2027-0000040")
	if _, err := s.ApplyBillingDocument(a, doc); err != nil {
		t.Fatal(err)
	}
	cheaper := doc
	cheaper.Items = []domain.PlannedItem{{DocumentNumber: "INV-2027-0000040", Amount: amt("999.99"), DueDate: "2027-04-30", InstallmentNo: 1}}
	eur := doc
	eur.Currency = "EUR"
	otherCustomer := doc
	otherCustomer.CustomerID = "00000000-0000-0000-0000-0000000000c2"
	for name, changed := range map[string]BillingDocument{"amount": cheaper, "currency": eur, "customer": otherCustomer} {
		if _, err := s.ApplyBillingDocument(a, changed); problemCode(err) != CodeBillingMismatch {
			t.Fatalf("%s: %v", name, err)
		}
	}
	elsewhere := doc
	elsewhere.LegalEntityID = otherLegalEntity
	if _, err := s.ApplyBillingDocument(scopedCtx(52, otherLegalEntity), elsewhere); problemCode(err) != CodeBillingMismatch {
		t.Fatalf("same id in another legal entity: %v", err)
	}
	if got := itemsOf(t, s, a); len(got) != 1 || got[0].amount != "1000.000000" {
		t.Fatalf("items %+v", got)
	}
	if created, err := s.ApplyBillingDocument(a, doc); err != nil || created != 0 {
		t.Fatalf("the true repeat: created %d err %v", created, err)
	}
}

func TestPostRMACredit_FindsAnInstallmentInvoiceByBillingIdAndBoundsItBySumOfInstallments(t *testing.T) {
	s, _, _ := rmaSetup(t)
	doc := invoiceDoc(t, invoiceDocID, "INV-2027-0000050",
		domain.BillingInstallment{No: 1, Amount: amt("600.00"), DueDate: "2027-04-30"},
		domain.BillingInstallment{No: 2, Amount: amt("400.00"), DueDate: "2027-05-30"})
	if _, err := s.ApplyBillingDocument(scopedCtx(53, legalEntity), doc); err != nil {
		t.Fatal(err)
	}
	ctx := tenantCtx(53)
	in := rmaIn("INV-2027-0000050")
	in.InvoiceID = invoiceDocID
	in.Gross, in.Restocking, in.Net = amt("900.00"), amt("0"), amt("900.00")
	note, err := s.PostRMACredit(ctx, in)
	if err != nil || note.CustomerID != billCustomer || !note.Net.Equal(amt("900.00")) {
		t.Fatalf("900.00 against a 1000.00 invoice of two installments: %+v %v", note, err)
	}
	second := rmaIn("INV-2027-0000050")
	second.RMANumber, second.InvoiceID = "RMA-0000051", invoiceDocID
	second.Gross, second.Restocking, second.Net = amt("200.00"), amt("0"), amt("200.00")
	if _, err := s.PostRMACredit(ctx, second); problemCode(err) != CodeRMAInvoice {
		t.Fatalf("200.00 more than the 100.00 left across the installments: %v", err)
	}
}

func execAdminTenant(t *testing.T, s *Postgres, n int, entity, number string) {
	t.Helper()
	execAdmin(t, s, `insert into open_item (tenant_id, id, legal_entity_id, customer_id, source_type, document_number, document_date, due_date, currency, amount, open_amount, status)
		values ($1, gen_random_uuid(), $2, $3, 'INVOICE', $4, '2027-03-31', '2027-04-30', 'USD', 10, 10, 'OPEN')`,
		testkit.TenantID(n), entity, billCustomer, number)
}

func TestApplyBillingDocument_ConcurrentDeliveriesOpenTheItemsOnce(t *testing.T) {
	s := setup(t)
	ctx := scopedCtx(48, legalEntity)
	doc := invoiceDoc(t, invoiceDocID, "INV-2027-0000008",
		domain.BillingInstallment{No: 1, Amount: amt("600.00"), DueDate: "2027-04-30"},
		domain.BillingInstallment{No: 2, Amount: amt("400.00"), DueDate: "2027-05-30"})
	var wg sync.WaitGroup
	totals := make(chan int, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			created, err := s.ApplyBillingDocument(ctx, doc)
			if err != nil {
				t.Errorf("apply: %v", err)
			}
			totals <- created
		}()
	}
	wg.Wait()
	close(totals)
	sum := 0
	for c := range totals {
		sum += c
	}
	if sum != 2 {
		t.Fatalf("items created across deliveries = %d, want 2", sum)
	}
	if got := itemsOf(t, s, ctx); len(got) != 2 {
		t.Fatalf("items %+v", got)
	}
}

func TestApplyBillingDocument_AnotherLegalEntityNeitherSeesNorSharesTheItems(t *testing.T) {
	s := setup(t)
	a, b := scopedCtx(49, legalEntity), scopedCtx(49, otherLegalEntity)
	if _, err := s.ApplyBillingDocument(a, invoiceDoc(t, invoiceDocID, "INV-2027-0000009")); err != nil {
		t.Fatal(err)
	}
	if got := itemsOf(t, s, b); len(got) != 0 {
		t.Fatalf("legal entity B sees A's items: %+v", got)
	}
	bDoc := invoiceDoc(t, "00000000-0000-0000-0000-00000000b003", "INV-2027-0000009")
	bDoc.LegalEntityID = otherLegalEntity
	if created, err := s.ApplyBillingDocument(b, bDoc); err != nil || created != 1 {
		t.Fatalf("same number in another entity: created %d err %v", created, err)
	}
	wrong := invoiceDoc(t, "00000000-0000-0000-0000-00000000b004", "INV-2027-0000010")
	if _, err := s.ApplyBillingDocument(b, wrong); err == nil {
		t.Fatal("a principal scoped to entity B opened an item of entity A")
	}
	if got := itemsOf(t, s, a); len(got) != 1 {
		t.Fatalf("entity A items %+v", got)
	}
}
