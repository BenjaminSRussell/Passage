package store

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
)

// Outcomes of one remittance advice line.
const (
	AdviceAttached  = "ATTACHED"
	AdviceUnmatched = "UNMATCHED"
	AdviceAmbiguous = "AMBIGUOUS"
	AdviceApplied   = "ALREADY_APPLIED"
	AdviceInvalid   = "INVALID"
	// AdviceAdvised is a line with no payment to attach to, for which a payment in status ADVISED was recorded (CreateMissing).
	AdviceAdvised = "ADVISED"
)

// AdviceOptions says what to do with an advice line that names no payment (30-4). With CreateMissing a payment in status
// ADVISED is recorded for it: the payer's advice came before the bank credit. It books no cash, cannot be matched or applied
// and is on no statement until the bank credit arrives (confirmAdvised). Currency is required then; ValueDate defaults to
// today and PaymentMode to WIRE.
type AdviceOptions struct {
	CreateMissing bool
	Currency      string
	ValueDate     string
	PaymentMode   string
}

// AdviceLineResult is what became of one advice line: the payment it was attached to, or why it was not.
type AdviceLineResult struct {
	PaymentRef string `json:"paymentRef"`
	Outcome    string `json:"outcome"`
	PaymentID  string `json:"paymentId,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// AdviceResult counts the outcomes of one advice file, with the line detail.
type AdviceResult struct {
	Lines    []AdviceLineResult `json:"lines"`
	Attached int                `json:"attached"`
	// Created counts the payments recorded as ADVISED for lines with no bank credit (CreateMissing).
	Created int `json:"created"`
	Skipped int `json:"skipped"`
	// PaymentIDs are the payments that got an advice, in file order, for the auto-application that follows.
	PaymentIDs []string `json:"-"`
}

// ApplyRemittanceAdvice attaches the references of a remittance advice to the payments they describe (FR-30.4, 30-4). A
// payment is found by its source reference within the legal entity (a bank statement payment's reference is
// "<statement>:<entry>", so the entry reference alone also finds it). The advice replaces the payment's remittance, so a
// repeat of the file changes nothing; a payment already applied is left alone, an advice whose references total more than the
// payment is refused for that line, and a reference shared by several payments is ambiguous. The matcher then reads the
// references as R1 (document number in the remittance); applying is a separate step (AutoApplyPayments or the clerk).
func (s *Postgres) ApplyRemittanceAdvice(ctx context.Context, legalEntityID string, lines []domain.AdviceLine) (AdviceResult, error) {
	return s.ApplyRemittanceAdviceWith(ctx, legalEntityID, lines, AdviceOptions{})
}

// ApplyRemittanceAdviceWith is ApplyRemittanceAdvice with options for the lines that name no payment.
func (s *Postgres) ApplyRemittanceAdviceWith(ctx context.Context, legalEntityID string, lines []domain.AdviceLine, opts AdviceOptions) (AdviceResult, error) {
	p, _ := tenant.FromContext(ctx)
	out := AdviceResult{Lines: make([]AdviceLineResult, 0, len(lines))}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		for _, l := range lines {
			res := AdviceLineResult{PaymentRef: l.PaymentRef}
			rows, err := tx.Query(ctx, `select id::text, status, amount::text from payment
				where legal_entity_id = $1 and (source_ref = $2 or source_ref like '%:' || $3) and status not in ('REVERSED', 'CONFIRMED') for update`,
				legalEntityID, l.PaymentRef, likeEscaper.Replace(l.PaymentRef))
			if err != nil {
				return err
			}
			type found struct{ id, status, amount string }
			var hits []found
			for rows.Next() {
				var f found
				if err := rows.Scan(&f.id, &f.status, &f.amount); err != nil {
					rows.Close()
					return err
				}
				hits = append(hits, f)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			total := decimal.Zero
			for _, r := range l.References {
				total = total.Add(r.Amount)
			}
			switch {
			case len(hits) == 0 && opts.CreateMissing && total.IsPositive():
				id, created, err := insertAdvisedPayment(ctx, tx, p.TenantID, legalEntityID, l, total, opts)
				if err != nil {
					return err
				}
				if !created {
					res.Outcome, res.Detail = AdviceInvalid, "another legal entity already holds a payment with this reference"
					break
				}
				res.Outcome, res.PaymentID = AdviceAdvised, id
				res.Detail = "no bank credit carries this reference yet; the payment is recorded as advised and books no cash until it does"
				out.Created++
			case len(hits) == 0:
				res.Outcome, res.Detail = AdviceUnmatched, "no payment carries this reference"
			case len(hits) > 1:
				res.Outcome, res.Detail = AdviceAmbiguous, "several payments carry this reference"
			case hits[0].status == "APPLIED":
				res.Outcome, res.PaymentID, res.Detail = AdviceApplied, hits[0].id, "the payment is already applied"
			case total.GreaterThan(decimal.RequireFromString(hits[0].amount)):
				res.Outcome, res.PaymentID = AdviceInvalid, hits[0].id
				res.Detail = "the references total " + total.String() + ", more than the payment of " + hits[0].amount
			default:
				raw, err := json.Marshal(l.References)
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `update payment set remittance = $3, version = version + 1 where tenant_id = $1 and id = $2`,
					p.TenantID, hits[0].id, raw); err != nil {
					return err
				}
				res.Outcome, res.PaymentID = AdviceAttached, hits[0].id
				out.Attached++
				out.PaymentIDs = append(out.PaymentIDs, hits[0].id)
			}
			if res.Outcome != AdviceAttached {
				out.Skipped++
			}
			out.Lines = append(out.Lines, res)
		}
		return nil
	})
	return out, mapWriteErr(err)
}

// AutoApplyResult counts what the auto-application of a set of payments did.
type AutoApplyResult struct {
	Applied   int `json:"applied"`
	Suggested int `json:"suggested"`
	OnAccount int `json:"onAccount"`
	// Failed counts the payments the matcher recommended applying whose application was refused (an item locked or settled in
	// the meantime); they stay as they were for the clerk.
	Failed            int      `json:"failed"`
	AppliedPaymentIDs []string `json:"appliedPaymentIds"`
}

// AutoApplyPayments matches each payment in order and applies the AUTO_APPLY decision at once (BR-30.1), so a later payment
// sees the items an earlier one settled and the payer bindings it taught. A SUGGESTED or ON_ACCOUNT payment is left for the
// clerk: SUGGESTED is never applied without a person. Each application is its own transaction under the key
// auto-apply:<payment>, so a repeat applies nothing twice. Only RECEIVED and SUGGESTED payments are considered.
func (s *Postgres) AutoApplyPayments(ctx context.Context, paymentIDs []string) (AutoApplyResult, error) {
	res := AutoApplyResult{AppliedPaymentIDs: []string{}}
	for _, id := range paymentIDs {
		pay, err := s.GetPayment(ctx, id)
		if err != nil {
			return res, err
		}
		if pay.Status != "RECEIVED" && pay.Status != "SUGGESTED" {
			continue
		}
		match, err := s.MatchPayment(ctx, id)
		if err != nil {
			return res, err
		}
		switch match.Decision {
		case domain.DecisionSuggested:
			res.Suggested++
			continue
		case domain.DecisionOnAccount:
			res.OnAccount++
			continue
		}
		if len(match.Top) == 0 {
			res.Failed++
			continue
		}
		plan, err := s.autoApplyPlan(ctx, pay, match.Top[0])
		if err == nil {
			_, err = s.ApplyPayment(ctx, id, pay.Version, plan, "auto-apply:"+id)
		}
		if err != nil {
			slog.WarnContext(ctx, "auto-application refused; the payment stays for the clerk", "payment", id, "err", err, "correlationId", middleware.GetReqID(ctx))
			res.Failed++
			continue
		}
		res.Applied++
		res.AppliedPaymentIDs = append(res.AppliedPaymentIDs, id)
	}
	return res, nil
}

// autoApplyPlan settles the matcher's winning set: each item for its open amount (less its cash discount when R3 scored a
// single item), capped by what is left of the payment, the remainder on account.
func (s *Postgres) autoApplyPlan(ctx context.Context, pay Payment, c domain.Candidate) (ApplyRequest, error) {
	left := pay.UnappliedAmount
	var req ApplyRequest
	for _, id := range c.ItemIDs {
		it, err := s.GetOpenItem(ctx, id)
		if err != nil {
			return ApplyRequest{}, err
		}
		a := domain.Allocation{OpenItemID: id, Amount: it.OpenAmount}
		if len(c.ItemIDs) == 1 && slices.Contains(c.Rules, "R3") {
			a.CashDiscount = it.CashDiscountAmount
			a.Amount = it.OpenAmount.Sub(it.CashDiscountAmount)
		}
		if a.Amount.GreaterThan(left) {
			a.Amount = left
		}
		left = left.Sub(a.Amount)
		req.Allocations = append(req.Allocations, a)
	}
	req.Residual = left
	return req, nil
}

// insertAdvisedPayment records the ADVISED payment of an advice line: the references the payer listed, their total as the
// amount, source REMITTANCE and the advice's payment reference as the source reference. It posts no journal and emits no
// event, because no money has reached the bank. created is false when another legal entity holds the reference.
func insertAdvisedPayment(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, l domain.AdviceLine, total decimal.Decimal, opts AdviceOptions) (string, bool, error) {
	mode := opts.PaymentMode
	if mode == "" {
		mode = "WIRE"
	}
	day := opts.ValueDate
	if day == "" {
		day = today()
	}
	raw, err := json.Marshal(l.References)
	if err != nil {
		return "", false, err
	}
	id := ids.New()
	tag, err := tx.Exec(ctx, `insert into payment (tenant_id, id, legal_entity_id, customer_id, payment_mode, source, source_ref,
		amount, unapplied_amount, currency, received_date, value_date, payer_name, remittance, status, idempotency_key)
		values ($1, $2, $3, null, $4, 'REMITTANCE', $5, $6, $6, $7, $8::date, $8::date, null, $9, 'ADVISED', $10)
		on conflict (tenant_id, source, source_ref) do nothing`,
		tenantID, id, legalEntityID, mode, l.PaymentRef, total.String(), opts.Currency, day, raw, "advice:"+legalEntityID+":"+l.PaymentRef)
	if err != nil {
		return "", false, err
	}
	return id, tag.RowsAffected() == 1, nil
}

// confirmAdvised is the bank credit arriving for an advised payment (30-4): inside the transaction that recorded bank, when
// exactly one ADVISED payment of the same legal entity, currency and amount carries its reference (equal to the bank payment's
// source reference or to its last colon-separated part, as a statement entry reference is), that payment becomes CONFIRMED,
// pointing at bank, and bank takes the advice's references when it has none of its own. It returns bank as stored. A different
// amount or several candidates change nothing: the advice stays for a person to settle.
func confirmAdvised(ctx context.Context, tx pgx.Tx, bank Payment) (Payment, error) {
	if bank.SourceRef == nil || bank.Source == "REMITTANCE" {
		return bank, nil
	}
	rows, err := tx.Query(ctx, `select id::text, remittance from payment
		where legal_entity_id = $1 and source = 'REMITTANCE' and status = 'ADVISED' and currency = $2 and amount = $3::numeric
		  and ($4 = source_ref or right($4, length(source_ref) + 1) = ':' || source_ref) for update`,
		bank.LegalEntityID, bank.Currency, bank.Amount.String(), *bank.SourceRef)
	if err != nil {
		return bank, err
	}
	type advised struct {
		id  string
		raw []byte
	}
	var found []advised
	for rows.Next() {
		var a advised
		if err := rows.Scan(&a.id, &a.raw); err != nil {
			rows.Close()
			return bank, err
		}
		found = append(found, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return bank, err
	}
	if len(found) != 1 {
		return bank, nil
	}
	if _, err := tx.Exec(ctx, `update payment set status = 'CONFIRMED', confirmed_by_payment_id = $2::uuid, version = version + 1 where id = $1::uuid`, found[0].id, bank.ID); err != nil {
		return bank, err
	}
	if len(bank.Remittance) > 0 {
		return bank, nil
	}
	return scanPayment(tx.QueryRow(ctx, `update payment set remittance = $2, version = version + 1 where id = $1::uuid returning `+paymentColumns, bank.ID, found[0].raw))
}
