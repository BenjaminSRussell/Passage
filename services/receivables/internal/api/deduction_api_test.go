package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

// Zero-value stubs so the shared fakeStore still satisfies store.Store for the other tests.
func (f *fakeStore) ImportBankStatement(context.Context, store.BankStatementImport) (store.BankStatementBatch, error) {
	return store.BankStatementBatch{}, nil
}
func (f *fakeStore) ListBankStatementImports(context.Context, int) ([]store.BankStatementBatch, error) {
	return []store.BankStatementBatch{}, nil
}
func (f *fakeStore) CreateDeduction(context.Context, store.Deduction, string) (store.Deduction, error) {
	return store.Deduction{}, nil
}
func (f *fakeStore) ListDeductions(context.Context, store.DeductionFilter, int) ([]store.Deduction, error) {
	return []store.Deduction{}, nil
}
func (f *fakeStore) GetDeduction(context.Context, string) (store.Deduction, error) {
	return store.Deduction{}, nil
}
func (f *fakeStore) MatchDeduction(context.Context, string, int, store.ClaimMatch) (store.Deduction, error) {
	return store.Deduction{}, nil
}
func (f *fakeStore) ApproveDeduction(context.Context, string, int, *string) (store.Deduction, error) {
	return store.Deduction{}, nil
}
func (f *fakeStore) DenyDeduction(context.Context, string, int, string, *string) (store.Deduction, error) {
	return store.Deduction{}, nil
}
func (f *fakeStore) SplitDeduction(context.Context, string, int, []decimal.Decimal) (store.Deduction, error) {
	return store.Deduction{}, nil
}
func (f *fakeStore) WriteOffDeduction(context.Context, string, int, bool, *string) (store.Deduction, error) {
	return store.Deduction{}, nil
}

// deductionFake records what the handlers forward to the store.
type deductionFake struct {
	fakeStore
	imported   store.BankStatementImport
	created    store.Deduction
	key        string
	filter     store.DeductionFilter
	action     string
	version    int
	claim      store.ClaimMatch
	reason     string
	note       *string
	parts      []decimal.Decimal
	mayApprove bool
}

const testDeduction = "00000000-0000-0000-0000-000000000020"

func (f *deductionFake) ImportBankStatement(_ context.Context, in store.BankStatementImport) (store.BankStatementBatch, error) {
	f.imported = in
	return store.BankStatementBatch{ID: ids.New(), FileSHA256: in.FileSHA256, PaymentCount: len(in.Credits), Payments: []store.Payment{}}, nil
}
func (f *deductionFake) CreateDeduction(_ context.Context, in store.Deduction, key string) (store.Deduction, error) {
	f.created, f.key = in, key
	in.ID, in.Status, in.Version = testDeduction, "OPEN", 1
	return in, nil
}
func (f *deductionFake) ListDeductions(_ context.Context, filter store.DeductionFilter, _ int) ([]store.Deduction, error) {
	f.filter = filter
	return []store.Deduction{}, nil
}
func (f *deductionFake) moved(action string, version int) (store.Deduction, error) {
	f.action, f.version = action, version
	return store.Deduction{ID: testDeduction, Version: version + 1}, nil
}
func (f *deductionFake) MatchDeduction(_ context.Context, _ string, version int, claim store.ClaimMatch) (store.Deduction, error) {
	f.claim = claim
	return f.moved("match", version)
}
func (f *deductionFake) ApproveDeduction(_ context.Context, _ string, version int, note *string) (store.Deduction, error) {
	f.note = note
	return f.moved("approve", version)
}
func (f *deductionFake) DenyDeduction(_ context.Context, _ string, version int, reason string, note *string) (store.Deduction, error) {
	f.reason, f.note = reason, note
	return f.moved("deny", version)
}
func (f *deductionFake) SplitDeduction(_ context.Context, _ string, version int, parts []decimal.Decimal) (store.Deduction, error) {
	f.parts = parts
	return f.moved("split", version)
}
func (f *deductionFake) WriteOffDeduction(_ context.Context, _ string, version int, mayApprove bool, note *string) (store.Deduction, error) {
	f.mayApprove, f.note = mayApprove, note
	return f.moved("write-off", version)
}

const validCAMT = `<?xml version="1.0"?>
<Document><BkToCstmrStmt><Stmt><Id>STMT-1</Id>
<Ntry><Amt Ccy="USD">100.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><ValDt><Dt>2027-03-20</Dt></ValDt><NtryRef>R1</NtryRef></Ntry>
<Ntry><Amt Ccy="USD">9.00</Amt><CdtDbtInd>DBIT</CdtDbtInd><ValDt><Dt>2027-03-20</Dt></ValDt><NtryRef>R2</NtryRef></Ntry>
</Stmt></BkToCstmrStmt></Document>`

func fieldErrors(t *testing.T, body []byte) map[string]string {
	t.Helper()
	var p struct {
		Code   string            `json:"code"`
		Errors map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	return p.Errors
}

func TestBankStatementImport_ValidatesParsesAndForwards(t *testing.T) {
	f := &deductionFake{}
	h := router(f)
	path := "/v1/receivables/bank-statements/imports"
	body := func(le, content string) string {
		return `{"legalEntityId":"` + le + `","content":` + jsonString(content) + `}`
	}
	if rec := do(h, http.MethodPost, path, body(testLE, validCAMT), "ARView", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("without ARImport: %d", rec.Code)
	}
	rec := do(h, http.MethodPost, path, body("nope", "   "), "ARImport", nil)
	if fields := fieldErrors(t, rec.Body.Bytes()); rec.Code != http.StatusBadRequest || fields["content"] == "" || fields["legalEntityId"] == "" {
		t.Fatalf("empty content and bad entity: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(h, http.MethodPost, path, body(testLE, strings.Repeat("x", maxStatementBytes+1)), "ARImport", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized file: %d", rec.Code)
	}
	bad := strings.Replace(validCAMT, ">100.00<", ">10.0.0<", 1)
	rec = do(h, http.MethodPost, path, body(testLE, bad), "ARImport", nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"AR-0004"`) || !strings.Contains(rec.Body.String(), "Line 3") {
		t.Fatalf("malformed entry: %d %s", rec.Code, rec.Body.String())
	}
	if f.imported.FileSHA256 != "" {
		t.Fatal("a malformed file reached the store")
	}
	rec = do(h, http.MethodPost, path, body(testLE, validCAMT), "ARImport", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid import: %d %s", rec.Code, rec.Body.String())
	}
	in := f.imported
	if len(in.Credits) != 1 || in.SkippedCount != 1 || in.StatementCount != 1 || len(in.FileSHA256) != 64 || in.LegalEntityID != testLE {
		t.Fatalf("forwarded %+v, want one credit, the debit skipped and the file hash", in)
	}
	if rec := do(h, http.MethodGet, path, "", "ARView", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCreateDeduction_Validation(t *testing.T) {
	key := map[string]string{"Idempotency-Key": "ded-1"}
	body := func(item, typ, amount string) string {
		return `{"legalEntityId":"` + testLE + `","openItemId":"` + item + `","paymentId":"` + testPay + `","type":"` + typ +
			`","reasonCode":" promo spring ","amount":"` + amount + `"}`
	}
	tests := []struct {
		name    string
		body    string
		headers map[string]string
		duties  string
		status  int
		field   string
	}{
		{"missing key", body(testItem, "PROMO", "300.00"), nil, "DeductionMaintain", http.StatusBadRequest, "Idempotency-Key"},
		{"without duty", body(testItem, "PROMO", "300.00"), key, "ARView", http.StatusForbidden, ""},
		{"no open item", body("x", "PROMO", "300.00"), key, "DeductionMaintain", http.StatusBadRequest, "openItemId"},
		{"unknown type", body(testItem, "OTHER", "300.00"), key, "DeductionMaintain", http.StatusBadRequest, "type"},
		{"zero amount", body(testItem, "PROMO", "0"), key, "DeductionMaintain", http.StatusBadRequest, "amount"},
		{"unknown field", strings.Replace(body(testItem, "PROMO", "300.00"), `"type"`, `"customerId":"x","type"`, 1), key, "DeductionMaintain", http.StatusBadRequest, ""},
		{"valid", body(testItem, "PROMO", "300.00"), key, "DeductionMaintain", http.StatusCreated, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &deductionFake{}
			rec := do(router(f), http.MethodPost, "/v1/receivables/deductions", tc.body, tc.duties, tc.headers)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.field != "" && fieldErrors(t, rec.Body.Bytes())[tc.field] == "" {
				t.Fatalf("want field error %q in %s", tc.field, rec.Body.String())
			}
			if tc.status == http.StatusCreated {
				if f.key != "ded-1" || f.created.OpenItemID != testItem || f.created.ReasonCode != "promo spring" ||
					!f.created.Amount.Equal(decimal.RequireFromString("300")) || f.created.PaymentID == nil || rec.Header().Get("ETag") != `"1"` {
					t.Fatalf("forwarded %+v key %q etag %q", f.created, f.key, rec.Header().Get("ETag"))
				}
			} else if f.key != "" {
				t.Fatal("a refused request reached the store")
			}
		})
	}
}

func TestListDeductions_Filters(t *testing.T) {
	f := &deductionFake{}
	h := router(f)
	base := "/v1/receivables/deductions"
	if rec := do(h, http.MethodGet, base, "", "", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("without ARView: %d", rec.Code)
	}
	for _, q := range []string{"?status=CLOSED", "?type=promo", "?customer=x", "?limit=0"} {
		if rec := do(h, http.MethodGet, base+q, "", "ARView", nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", q, rec.Code)
		}
	}
	rec := do(h, http.MethodGet, base+"?status=MATCHED&type=PRICE&customer="+testItem, "", "ARView", nil)
	if rec.Code != http.StatusOK || f.filter != (store.DeductionFilter{Status: "MATCHED", Type: "PRICE", CustomerID: testItem}) {
		t.Fatalf("list: %d filter %+v", rec.Code, f.filter)
	}
	if rec := do(h, http.MethodGet, base+"/not-a-uuid", "", "ARView", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
}

func TestDeductionActions_ValidateAndForward(t *testing.T) {
	base := "/v1/receivables/deductions/" + testDeduction
	v3 := map[string]string{"If-Match": `"3"`}
	tests := []struct {
		name   string
		path   string
		body   string
		duties string
		hdr    map[string]string
		status int
		field  string
		check  func(t *testing.T, f *deductionFake)
	}{
		{"needs the duty", ":approve", "", "ARView", v3, http.StatusForbidden, "", nil},
		{"needs If-Match", ":approve", "", "DeductionMaintain", nil, http.StatusPreconditionRequired, "", nil},
		{"unknown action", ":refund", "", "DeductionMaintain", v3, http.StatusNotFound, "", nil},
		{"match needs a claim", ":match", `{"claimType":"INVOICE","claimRef":" ","claimRemaining":"-1"}`, "DeductionMaintain", v3, http.StatusBadRequest, "claimRef", nil},
		{"match needs the remaining amount", ":match", `{"claimType":"PROMOTION_FUND","claimRef":"FUND-7"}`, "DeductionMaintain", v3, http.StatusBadRequest, "claimRemaining", nil},
		{"match", ":match", `{"claimType":"PROMOTION_FUND","claimRef":" FUND-7 ","claimRemaining":"300.00"}`, "DeductionMaintain", v3, http.StatusOK, "",
			func(t *testing.T, f *deductionFake) {
				if f.action != "match" || f.version != 3 || f.claim.Ref != "FUND-7" || f.claim.Type != "PROMOTION_FUND" || !f.claim.Remaining.Equal(decimal.RequireFromString("300")) {
					t.Errorf("forwarded %+v v%d", f.claim, f.version)
				}
			}},
		{"approve with a note", ":approve", `{"note":"  fund confirmed  "}`, "DeductionMaintain", v3, http.StatusOK, "",
			func(t *testing.T, f *deductionFake) {
				if f.action != "approve" || f.note == nil || *f.note != "fund confirmed" {
					t.Errorf("forwarded action %q note %v", f.action, f.note)
				}
			}},
		{"deny needs a configured reason", ":deny", `{"reason":"late"}`, "DeductionMaintain", v3, http.StatusBadRequest, "reason", nil},
		{"deny", ":deny", `{"reason":"NOT_AUTHORIZED","note":" "}`, "DeductionMaintain", v3, http.StatusOK, "",
			func(t *testing.T, f *deductionFake) {
				if f.action != "deny" || f.reason != "NOT_AUTHORIZED" || f.note != nil {
					t.Errorf("forwarded %q %q note %v; a blank note is no note", f.action, f.reason, f.note)
				}
			}},
		{"split needs two parts", ":split", `{"parts":["300.00"]}`, "DeductionMaintain", v3, http.StatusBadRequest, "parts", nil},
		{"split refuses a negative part", ":split", `{"parts":["400.00","-100.00"]}`, "DeductionMaintain", v3, http.StatusBadRequest, "parts[1]", nil},
		{"split", ":split", `{"parts":["200.00","100.00"]}`, "DeductionMaintain", v3, http.StatusOK, "",
			func(t *testing.T, f *deductionFake) {
				if f.action != "split" || len(f.parts) != 2 {
					t.Errorf("forwarded %q %v", f.action, f.parts)
				}
			}},
		{"write-off as a clerk", ":write-off", "", "DeductionMaintain", v3, http.StatusOK, "",
			func(t *testing.T, f *deductionFake) {
				if f.action != "write-off" || f.mayApprove {
					t.Errorf("a clerk without ARWriteOffApprove was forwarded as an approver")
				}
			}},
		{"write-off as an approver", ":write-off", "", "DeductionMaintain,ARWriteOffApprove", v3, http.StatusOK, "",
			func(t *testing.T, f *deductionFake) {
				if !f.mayApprove {
					t.Errorf("ARWriteOffApprove not forwarded")
				}
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &deductionFake{}
			rec := do(router(f), http.MethodPost, base+tc.path, tc.body, tc.duties, tc.hdr)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.field != "" && fieldErrors(t, rec.Body.Bytes())[tc.field] == "" {
				t.Fatalf("want field error %q in %s", tc.field, rec.Body.String())
			}
			if tc.status != http.StatusOK && f.action != "" {
				t.Fatalf("a refused request reached the store (%s)", f.action)
			}
			if tc.status == http.StatusOK && rec.Header().Get("ETag") != `"4"` {
				t.Fatalf("ETag = %q, want the new version", rec.Header().Get("ETag"))
			}
			if tc.check != nil {
				tc.check(t, f)
			}
		})
	}
}

// jsonString quotes s as a JSON string literal.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
