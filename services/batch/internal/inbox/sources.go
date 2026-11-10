package inbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	maxResponseBytes = 4 << 20

	SourcePurchasing = "purchasing"
	SourceParty      = "party"
	SourceLedger     = "ledger"
	SourcePayables   = "payables"

	DutyRequisitionApprove = "procurement.requisition.approve"
	DutyPartyApprove       = "party.approve"
	DutyJournalApprove     = "ledger.journal.approve"
	DutyPaymentHold        = "payment-hold.manage"

	TokenDutyRequisitions = "procurement.inbox.read"
	TokenDutyPartyChanges = "party.inbox.read"
	TokenDutyJournals     = "ledger.inbox.read"
	TokenDutyHolds        = "payables.inbox.read"

	statusInApproval = "in_approval"
)

type decoder func(body []byte, userID string) (Fetched, error)

type HTTPSource struct {
	name       string
	duty       string
	audience   string
	tokenDuty  string
	baseURL    string
	path       string
	query      url.Values
	decode     decoder
	signer     *tenant.ServiceSigner
	httpClient *http.Client
}

func (s *HTTPSource) Name() string { return s.name }
func (s *HTTPSource) Duty() string { return s.duty }

func (s *HTTPSource) Fetch(ctx context.Context, p tenant.Principal, legalEntity string) (Fetched, error) {
	if s.baseURL == "" || s.signer == nil {
		return Fetched{}, ErrNotConfigured
	}
	query := url.Values{"limit": {strconv.Itoa(MaxPerSource)}}
	for k, v := range s.query {
		query[k] = v
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+s.path+"?"+query.Encode(), nil)
	if err != nil {
		return Fetched{}, err
	}
	if signed, err := s.signer.ApplyDuties(req, p, s.audience, legalEntity, []string{s.tokenDuty}); err != nil || !signed {
		return Fetched{}, fmt.Errorf("sign %s request: %v", s.name, err)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return Fetched{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return Fetched{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Fetched{}, fmt.Errorf("%s responded with status %d", s.name, resp.StatusCode)
	}
	return s.decode(body, p.UserID)
}

type Endpoints struct {
	PurchasingURL, PartyURL, LedgerURL, PayablesURL string
}

func NewSources(e Endpoints, signer *tenant.ServiceSigner, timeout time.Duration) []Source {
	if timeout <= 0 {
		timeout = DefaultSourceTimeout
	}
	client := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	build := func(name, duty, audience, tokenDuty, base, path string, query url.Values, decode decoder) Source {
		return &HTTPSource{name: name, duty: duty, audience: audience, tokenDuty: tokenDuty, baseURL: strings.TrimRight(base, "/"),
			path: path, query: query, decode: decode, signer: signer, httpClient: client}
	}
	return []Source{
		build(SourcePurchasing, DutyRequisitionApprove, "purchasing", TokenDutyRequisitions, e.PurchasingURL, "/v1/procurement/requisition-approvals", nil, decodeRequisitions),
		build(SourceParty, DutyPartyApprove, "party", TokenDutyPartyChanges, e.PartyURL, "/v1/party/pending-changes", nil, decodePartyChanges),
		build(SourceLedger, DutyJournalApprove, "ledger", TokenDutyJournals, e.LedgerURL, "/v1/ledger/journal-batches", url.Values{"status": {statusInApproval}}, decodeJournalBatches),
		build(SourcePayables, DutyPaymentHold, "payables", TokenDutyHolds, e.PayablesURL, "/v1/payables/open-items", url.Values{"held": {"true"}}, decodeHolds),
	}
}

func decodeRequisitions(body []byte, _ string) (Fetched, error) {
	var page struct {
		Items []struct {
			ID          string    `json:"id"`
			Number      string    `json:"number"`
			SubmittedAt time.Time `json:"submittedAt"`
		} `json:"items"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return Fetched{}, fmt.Errorf("decode purchasing response: %w", err)
	}
	out := Fetched{More: page.NextCursor != ""}
	for _, r := range page.Items {
		out.Items = append(out.Items, Item{
			ID: r.ID, Source: SourcePurchasing, Kind: "Requisition approval", Number: r.Number, CreatedAt: r.SubmittedAt,
			Reason: "Waiting for approval", Link: "/purchasing/requisitions/" + r.ID,
			Actions: []string{"approve", "reject", "open"},
		})
	}
	return out, nil
}

func decodePartyChanges(body []byte, userID string) (Fetched, error) {
	var page struct {
		Items []struct {
			ID             string    `json:"id"`
			Field          string    `json:"field"`
			RequestedValue string    `json:"requestedValue"`
			RequestedBy    string    `json:"requestedBy"`
			RequestedAt    time.Time `json:"requestedAt"`
		} `json:"items"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return Fetched{}, fmt.Errorf("decode party response: %w", err)
	}
	out := Fetched{More: page.NextCursor != ""}
	for _, c := range page.Items {
		if c.RequestedBy == userID {
			continue
		}
		out.Items = append(out.Items, Item{
			ID: c.ID, Source: SourceParty, Kind: "Customer change", Number: strings.ReplaceAll(c.Field, "_", " ") + " " + c.RequestedValue, CreatedAt: c.RequestedAt,
			Reason: "A change to a protected customer field needs a second person to approve it", Link: "/party/pending-changes/" + c.ID,
			Actions: []string{"approve", "reject", "open"},
		})
	}
	return out, nil
}

func decodeJournalBatches(body []byte, userID string) (Fetched, error) {
	var page struct {
		Items []struct {
			ID                  string    `json:"id"`
			JournalName         string    `json:"journalName"`
			Description         string    `json:"description"`
			SubmittedBy         string    `json:"submittedBy"`
			SelfApprovalAllowed bool      `json:"selfApprovalAllowed"`
			CreatedAt           time.Time `json:"createdAt"`
		} `json:"items"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return Fetched{}, fmt.Errorf("decode ledger response: %w", err)
	}
	out := Fetched{More: page.NextCursor != ""}
	for _, b := range page.Items {
		if b.SubmittedBy == userID && !b.SelfApprovalAllowed {
			continue
		}
		out.Items = append(out.Items, Item{
			ID: b.ID, Source: SourceLedger, Kind: "Journal batch", Number: b.JournalName + " " + b.Description, CreatedAt: b.CreatedAt,
			Reason: "Waiting for your approval", Link: "/ledger/journal-batches/" + b.ID,
			Actions: []string{"approve", "reject", "open"},
		})
	}
	return out, nil
}

func decodeHolds(body []byte, _ string) (Fetched, error) {
	var page struct {
		Items []struct {
			ID            string `json:"id"`
			InvoiceNumber string `json:"invoiceNumber"`
			InvoiceDate   string `json:"invoiceDate"`
			DueDate       string `json:"dueDate"`
		} `json:"items"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return Fetched{}, fmt.Errorf("decode payables response: %w", err)
	}
	out := Fetched{More: page.NextCursor != ""}
	for _, h := range page.Items {
		created, _ := time.Parse(time.DateOnly, h.InvoiceDate)
		reason := "On payment hold"
		if _, err := time.Parse(time.DateOnly, h.DueDate); err != nil {
			h.DueDate = ""
		} else {
			reason += "; due " + h.DueDate
		}
		out.Items = append(out.Items, Item{
			ID: h.ID, Source: SourcePayables, Kind: "Payment hold", Number: h.InvoiceNumber, CreatedAt: created, DueDate: h.DueDate,
			Reason: reason, Link: "/payables",
			Actions: []string{"open"},
		})
	}
	return out, nil
}
