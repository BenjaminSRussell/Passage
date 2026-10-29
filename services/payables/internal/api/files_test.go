package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/payables"
	"github.com/BenjaminSRussell/passage/services/payables/internal/api"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

const (
	filePreviewPath = "/v1/payables/payment-files:preview"
	statusParsePath = "/v1/payables/payments/status:parse"
	previewDuty     = "payment-file.preview"
	parseDuty       = "payment-status.parse"
	fixtureDir      = "../../testdata/"
)

var creationTime = regexp.MustCompile(`<CreDtTm>[^<]*</CreDtTm>`)

type fakeValidator struct {
	found []domain.XSDError
	err   error
}

func (f fakeValidator) Validate(context.Context, []byte) ([]domain.XSDError, error) {
	return f.found, f.err
}

func routerWith(v api.FileValidator) (*chi.Mux, *api.API) {
	a := api.New(fakeDB{}, v, fixedNow)
	r := httpx.NewRouter()
	a.Register(r)
	return r, a
}

func readTestdata(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(fixtureDir + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

type previewBody struct {
	Content string `json:"content"`
	Summary struct {
		PaymentCount int    `json:"paymentCount"`
		ControlSum   string `json:"controlSum"`
		SHA256       string `json:"sha256"`
	} `json:"summary"`
	Validation struct {
		Valid  bool `json:"valid"`
		Errors []struct {
			Code, Path, Reason string
		} `json:"errors"`
	} `json:"validation"`
}

func TestPreviewPaymentFile_MatchesGolden(t *testing.T) {
	validator, err := domain.NewXSDValidator(payables.Pain001XSD)
	if err != nil {
		t.Skipf("xmllint unavailable: %v", err)
	}
	summaries := map[string]struct {
		count int
		sum   string
	}{
		"sepa-50": {50, "71567"}, "sepa-structured": {5, "1361.1"}, "us": {4, "6140.7"},
		"uk": {3, "3148.5"}, "ch": {3, "7733.31"}, "wire": {1, "1250000"},
	}
	for name, want := range summaries {
		t.Run(name, func(t *testing.T) {
			r, a := routerWith(validator)
			rec := do(r, http.MethodPost, filePreviewPath, readTestdata(t, "pain001/"+name+".json"), true, previewDuty)
			if rec.Code != http.StatusOK {
				t.Fatalf("got %d %s", rec.Code, rec.Body.String())
			}
			var got previewBody
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			golden := readTestdata(t, "pain001/"+name+".golden.xml")
			if creationTime.ReplaceAllString(got.Content, "") != creationTime.ReplaceAllString(golden, "") {
				t.Fatalf("content differs from %s golden", name)
			}
			if !got.Validation.Valid || len(got.Validation.Errors) != 0 {
				t.Fatalf("validation = %+v", got.Validation)
			}
			sum := sha256.Sum256([]byte(got.Content))
			if got.Summary.PaymentCount != want.count || got.Summary.ControlSum != want.sum || got.Summary.SHA256 != hex.EncodeToString(sum[:]) {
				t.Fatalf("summary = %+v", got.Summary)
			}
			if !strings.Contains(scrape(a), "payables_payment_file_previews_total 1") {
				t.Fatalf("preview counter not incremented:\n%s", scrape(a))
			}
		})
	}
}

func TestPreviewPaymentFile_ReportsXSDViolations(t *testing.T) {
	r, _ := routerWith(fakeValidator{found: []domain.XSDError{{Path: "InstdAmt", Reason: "bad amount"}}})
	rec := do(r, http.MethodPost, filePreviewPath, readTestdata(t, "pain001/us.json"), true, previewDuty)
	var got previewBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || got.Validation.Valid || len(got.Validation.Errors) != 1 ||
		got.Validation.Errors[0].Code != "AP-FIL-002" || got.Validation.Errors[0].Path != "InstdAmt" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestPreviewPaymentFile_ClientCancellationIsNotCounted(t *testing.T) {
	r, a := routerWith(fakeValidator{err: context.Canceled})
	req := httptest.NewRequest(http.MethodPost, filePreviewPath, strings.NewReader(readTestdata(t, "pain001/us.json")))
	req.Header.Set("X-Passage-Tenant", "00000000-0000-4000-8000-000000000001")
	req.Header.Set("X-Passage-Duties", previewDuty)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	metrics := scrape(a)
	if strings.Contains(metrics, "payables_payment_file_server_faults_total 1") || strings.Contains(metrics, "payables_payment_file_client_errors_total 1") {
		t.Fatalf("client cancellation changed error counters:\n%s", metrics)
	}
	if rec.Code >= 500 || rec.Body.Len() != 0 {
		t.Fatalf("client cancellation wrote %d %q", rec.Code, rec.Body.String())
	}
}

func TestPreviewPaymentFile_CanceledErrorOnLiveRequestIsAServerFault(t *testing.T) {
	r, a := routerWith(fakeValidator{err: context.Canceled})
	rec := do(r, http.MethodPost, filePreviewPath, readTestdata(t, "pain001/us.json"), true, previewDuty)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(scrape(a), "payables_payment_file_server_faults_total 1") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestPreviewPaymentFile_TimeoutRetryAfterOnlyWhenWaitingForSlot(t *testing.T) {
	busy := fmt.Errorf("wait for xmllint slot: %w: %w", domain.ErrValidatorBusy, context.DeadlineExceeded)
	for name, tc := range map[string]struct {
		err        error
		retryAfter string
	}{
		"waiting for slot": {busy, "1"},
		"full xmllint run": {fmt.Errorf("run xmllint: %w", context.DeadlineExceeded), ""},
	} {
		t.Run(name, func(t *testing.T) {
			r, _ := routerWith(fakeValidator{err: tc.err})
			rec := do(r, http.MethodPost, filePreviewPath, readTestdata(t, "pain001/us.json"), true, previewDuty)
			if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != tc.retryAfter {
				t.Fatalf("got %d Retry-After=%q, want 503 %q", rec.Code, rec.Header().Get("Retry-After"), tc.retryAfter)
			}
		})
	}
}

func TestPreviewPaymentFile_TimeoutIsLoggedWithCorrelationID(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	r, _ := routerWith(fakeValidator{err: fmt.Errorf("run xmllint: %w", context.DeadlineExceeded)})
	req := httptest.NewRequest(http.MethodPost, filePreviewPath, strings.NewReader(readTestdata(t, "pain001/us.json")))
	req.Header.Set("X-Passage-Tenant", "00000000-0000-4000-8000-000000000001")
	req.Header.Set("X-Passage-Duties", previewDuty)
	req.Header.Set("X-Request-Id", "req-229")
	r.ServeHTTP(httptest.NewRecorder(), req)
	for _, raw := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var line struct {
			Level         string
			CorrelationID string `json:"correlationId"`
		}
		if err := json.Unmarshal(raw, &line); err != nil {
			t.Fatalf("log line %q: %v", raw, err)
		}
		if line.Level == "WARN" && line.CorrelationID == "req-229" {
			return
		}
	}
	t.Fatalf("no WARN with correlationId req-229 in %s", logs.String())
}

func TestPreviewPaymentFile_Rejections(t *testing.T) {
	us := readTestdata(t, "pain001/us.json")
	manyPayments := `{"fileNumber":"F1","payments":[` + strings.Repeat(`{},`, 2000) + `{}]}`
	tests := []struct {
		name       string
		body       string
		tenant     bool
		duties     string
		validator  fakeValidator
		wantStatus int
		wantBody   string
	}{
		{"no tenant", us, false, "", fakeValidator{}, http.StatusUnauthorized, "AUTH-0001"},
		{"missing duty", us, true, "", fakeValidator{}, http.StatusForbidden, "AUTH-0003"},
		{"wrong duty", us, true, parseDuty, fakeValidator{}, http.StatusForbidden, "AUTH-0003"},
		{"malformed JSON", `{`, true, previewDuty, fakeValidator{}, http.StatusBadRequest, "COM-0001"},
		{"unknown field", `{"x":1}`, true, previewDuty, fakeValidator{}, http.StatusBadRequest, "COM-0001"},
		{"bad created", `{"fileNumber":"F1","created":"yesterday","payments":[{}]}`, true, previewDuty, fakeValidator{}, http.StatusBadRequest, "PAY-0001"},
		{"no payments", `{"fileNumber":"F1","created":"2026-01-01T00:00:00Z","profile":{"variant":"US"},"payments":[]}`, true, previewDuty, fakeValidator{}, http.StatusBadRequest, "PAY-0001"},
		{"too many payments", manyPayments, true, previewDuty, fakeValidator{}, http.StatusBadRequest, "PAY-0001"},
		{"body over limit", `{"fileNumber":"` + strings.Repeat("A", 5<<20) + `"}`, true, previewDuty, fakeValidator{}, http.StatusRequestEntityTooLarge, "COM-0413"},
		{"validator deadline", us, true, previewDuty, fakeValidator{err: context.DeadlineExceeded}, http.StatusServiceUnavailable, "COM-0503"},
		{"validator failure", us, true, previewDuty, fakeValidator{err: errors.New("xmllint crashed")}, http.StatusInternalServerError, "COM-0500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, a := routerWith(tc.validator)
			rec := do(r, http.MethodPost, filePreviewPath, tc.body, tc.tenant, tc.duties)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %.200s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			metrics := scrape(a)
			if strings.Contains(metrics, "payables_payment_file_previews_total 1") {
				t.Fatal("rejected request counted as a successful preview")
			}
			authRejected := tc.wantStatus == http.StatusUnauthorized || tc.wantStatus == http.StatusForbidden
			wantFaults := tc.wantStatus >= 500
			wantClientErrors := !authRejected && !wantFaults
			if wantFaults != strings.Contains(metrics, "payables_payment_file_server_faults_total 1") ||
				wantClientErrors != strings.Contains(metrics, "payables_payment_file_client_errors_total 1") ||
				strings.Contains(metrics, "payables_settlement_preview_client_errors_total 1") {
				t.Fatalf("error counters wrong for %d:\n%s", tc.wantStatus, metrics)
			}
		})
	}
}

func TestPreviewPaymentFile_BankRuleViolationIsUnprocessable(t *testing.T) {
	body := strings.Replace(readTestdata(t, "pain001/sepa-50.json"), `"debtorBic": "`, `"debtorBic": "1`, 1)
	r, _ := routerWith(fakeValidator{})
	rec := do(r, http.MethodPost, filePreviewPath, body, true, previewDuty)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"serviceCode":"AP-RUN-002"`) ||
		!strings.Contains(rec.Body.String(), `"code":"validation_failed"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestPreviewPaymentFile_DefaultsCreatedToServerClock(t *testing.T) {
	var dto map[string]any
	if err := json.Unmarshal([]byte(readTestdata(t, "pain001/us.json")), &dto); err != nil {
		t.Fatal(err)
	}
	delete(dto, "created")
	body, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := routerWith(fakeValidator{})
	rec := do(r, http.MethodPost, filePreviewPath, string(body), true, previewDuty)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "2026-01-10T00:00:00Z") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func statusBody(t *testing.T, file string, messages, payments any) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"content": readTestdata(t, "pain002/"+file+".xml"), "messages": messages, "payments": payments})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestParsePaymentStatus(t *testing.T) {
	r, a := routerWith(fakeValidator{})
	body := statusBody(t, "rjct-ac01",
		map[string][]string{"SEPA50-F1": {"SEPA-0006"}},
		map[string]map[string]string{"SEPA-0006": {"status": "SENT", "method": "TRANSFER"}})
	rec := do(r, http.MethodPost, statusParsePath, body, true, parseDuty)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		OriginalMessageID string `json:"originalMessageId"`
		Outcomes          []struct {
			EndToEndID, BankStatus, ReasonCode, From, To, Hold string
			Warning                                            struct{ Code string }
		} `json:"outcomes"`
		Unknown []any `json:"unknown"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OriginalMessageID != "SEPA50-F1" || len(got.Outcomes) != 1 || len(got.Unknown) != 0 {
		t.Fatalf("got %s", rec.Body.String())
	}
	o := got.Outcomes[0]
	if o.EndToEndID != "SEPA-0006" || o.BankStatus != "RJCT" || o.ReasonCode != "AC01" || o.From != "SENT" ||
		o.To != "REJECTED" || o.Hold != "BANK_DETAILS" || o.Warning.Code != "AP-STS-001" {
		t.Fatalf("outcome = %+v", o)
	}
	if !strings.Contains(scrape(a), "payables_payment_status_parses_total 1") {
		t.Fatalf("parse counter not incremented:\n%s", scrape(a))
	}
}

func TestParsePaymentStatus_UnknownIDsAreReportedNotFatal(t *testing.T) {
	r, _ := routerWith(fakeValidator{})
	rec := do(r, http.MethodPost, statusParsePath, statusBody(t, "mixed-unknown", map[string][]string{}, map[string]any{}), true, parseDuty)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"AP-STS-002"`) || !strings.Contains(rec.Body.String(), `"outcomes":[]`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestParsePaymentStatus_Rejections(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		tenant     bool
		duties     string
		wantStatus int
		wantBody   string
	}{
		{"no tenant", `{}`, false, "", http.StatusUnauthorized, "AUTH-0001"},
		{"missing duty", `{}`, true, "", http.StatusForbidden, "AUTH-0003"},
		{"preview duty is not enough", `{}`, true, previewDuty, http.StatusForbidden, "AUTH-0003"},
		{"malformed JSON", `{`, true, parseDuty, http.StatusBadRequest, "COM-0001"},
		{"unknown field", `{"x":1}`, true, parseDuty, http.StatusBadRequest, "COM-0001"},
		{"not a status report", `{"content":"<a/>"}`, true, parseDuty, http.StatusBadRequest, "PAY-0001"},
		{"empty content", `{"content":""}`, true, parseDuty, http.StatusBadRequest, "PAY-0001"},
		{"content over limit", `{"content":"` + strings.Repeat("A", 11<<20) + `"}`, true, parseDuty, http.StatusRequestEntityTooLarge, "COM-0413"},
		{"body over limit", `{"content":"` + strings.Repeat("A", 17<<20) + `"}`, true, parseDuty, http.StatusRequestEntityTooLarge, "COM-0413"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, a := routerWith(fakeValidator{})
			rec := do(r, http.MethodPost, statusParsePath, tc.body, tc.tenant, tc.duties)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %.200s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if strings.Contains(scrape(a), "payables_payment_status_parses_total 1") {
				t.Fatal("rejected request counted as a successful parse")
			}
		})
	}
}
