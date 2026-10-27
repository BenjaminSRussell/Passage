package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

const lockboxSampleFile = `01,BANKX,LOCKBOX,260101,1200,1,,/
02,LOCKBOX,BANKX,1,260101,1200,USD,2/
03,123456789,USD/
16,116,125000,0,REF1,INV-100,CHECK 4411/
16,999,500,0,REF2,,OTHER/
49,125500,4/
98,125500,1,6/
99,125500,1,8/
`

func lockboxBody(t *testing.T, content string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"legalEntityId": testLE, "receivedDate": "2027-03-05", "content": content})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestImportLockbox_CreatesOnePaymentPerCheck(t *testing.T) {
	f := &fakeStore{}
	h := router(f)
	rec := do(h, http.MethodPost, "/api/v1/receivables/lockbox/imports", lockboxBody(t, lockboxSampleFile), "ARImport", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var out struct {
		PaymentCount int `json:"paymentCount"`
		Payments     []struct {
			Source string `json:"source"`
		} `json:"payments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.PaymentCount != 1 || len(out.Payments) != 1 || out.Payments[0].Source != "LOCKBOX" {
		t.Fatalf("payments = %+v", out)
	}
	if f.lockbox.AccountCount != 1 || len(f.lockbox.Checks) != 1 || len(f.lockbox.FileSHA256) != 64 {
		t.Errorf("forwarded import = %+v", f.lockbox)
	}
}

func TestImportLockbox_RefusesDuplicateFile(t *testing.T) {
	h := router(&fakeStore{})
	body := lockboxBody(t, lockboxSampleFile)
	if rec := do(h, http.MethodPost, "/api/v1/receivables/lockbox/imports", body, "ARImport", nil); rec.Code != http.StatusCreated {
		t.Fatalf("first import status = %d", rec.Code)
	}
	rec := do(h, http.MethodPost, "/api/v1/receivables/lockbox/imports", body, "ARImport", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.CodeLockboxDuplicate) {
		t.Fatalf("duplicate import: status %d body %s", rec.Code, rec.Body)
	}
}

func TestImportLockbox_MalformedFileReportsLine(t *testing.T) {
	h := router(&fakeStore{})
	bad := strings.Replace(lockboxSampleFile, "125000", "1250.00", 1)
	rec := do(h, http.MethodPost, "/api/v1/receivables/lockbox/imports", lockboxBody(t, bad), "ARImport", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "AR-0004") || !strings.Contains(body, "Line 4") {
		t.Errorf("problem does not name the code and line: %s", body)
	}
}

func TestImportLockbox_Validation(t *testing.T) {
	h := router(&fakeStore{})
	tests := []struct {
		name   string
		body   string
		duties string
		status int
	}{
		{"without the import duty", lockboxBody(t, lockboxSampleFile), "ARView", http.StatusForbidden},
		{"empty content", lockboxBody(t, "  "), "ARImport", http.StatusBadRequest},
		{"bad legal entity", `{"legalEntityId":"nope","receivedDate":"2027-03-05","content":"x"}`, "ARImport", http.StatusBadRequest},
		{"bad type code", `{"legalEntityId":"` + testLE + `","receivedDate":"2027-03-05","typeCodes":["1950"],"content":"x"}`, "ARImport", http.StatusBadRequest},
		{"debit type code", `{"legalEntityId":"` + testLE + `","receivedDate":"2027-03-05","typeCodes":["475"],"content":"x"}`, "ARImport", http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(h, http.MethodPost, "/api/v1/receivables/lockbox/imports", tc.body, tc.duties, nil)
			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d; body %s", rec.Code, tc.status, rec.Body)
			}
		})
	}
}

func TestWorklist_ReturnsRankedRows(t *testing.T) {
	h := router(&fakeStore{})
	rec := do(h, http.MethodGet, "/api/v1/receivables/worklist?legalEntityId="+testLE+"&asOf=2027-03-31", "", "ARView", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var out struct {
		AsOf  string `json:"asOf"`
		Items []struct {
			RiskScore int `json:"riskScore"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.AsOf != "2027-03-31" || len(out.Items) != 1 || out.Items[0].RiskScore != 45 {
		t.Errorf("worklist = %+v", out)
	}
}

func TestWorklist_RequiresLegalEntity(t *testing.T) {
	h := router(&fakeStore{})
	rec := do(h, http.MethodGet, "/api/v1/receivables/worklist", "", "ARView", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
