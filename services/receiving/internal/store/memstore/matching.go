package memstore

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/depot/services/receiving/internal/store"
)

func (s *Store) SetMatchingPolicy(ctx context.Context, mp domain.MatchingPolicy, _ string) error {
	return s.with(ctx, func(t *tenantData) error {
		t.matchPolicies[mp.ID] = mp
		return nil
	})
}

func (s *Store) ListMatchingPolicies(ctx context.Context, legalEntityID string) (out []domain.MatchingPolicy, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		for _, mp := range t.matchPolicies {
			if mp.LegalEntityID == legalEntityID {
				out = append(out, mp)
			}
		}
		return nil
	})
	return out, err
}

func (s *Store) ListExceptions(ctx context.Context, legalEntityID, status string) (out []store.InvoiceException, err error) {
	principal, _ := tenant.FromContext(ctx)
	err = s.with(ctx, func(t *tenantData) error {
		for _, e := range t.exceptions {
			if e.Status == status && (legalEntityID == "" || e.LegalEntityID == legalEntityID) && tenant.InLegalEntityScope(principal, e.LegalEntityID) {
				out = append(out, e)
			}
		}
		slices.SortFunc(out, func(a, b store.InvoiceException) int {
			return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.InvoiceID, b.InvoiceID))
		})
		return nil
	})
	return out, err
}

func checkAssignee(t *tenantData, invoiceID, by string) error {
	e, ok := t.exceptions[invoiceID]
	switch {
	case !ok:
		return store.ErrInvoiceNotFound
	case e.Status != domain.ExceptionOpen:
		return &domain.Error{Code: domain.CodeExceptionNotOpen, Detail: "the exception of invoice " + invoiceID + " is " + e.Status + ", not OPEN"}
	case !store.SameActor(e.Assignee, by):
		return &domain.Error{Code: domain.CodeExceptionNotAssignee, Detail: "only the owner of the matching policy can decide this exception"}
	}
	return nil
}

func closeException(t *tenantData, invoiceID, status, by, reason string) {
	e, ok := t.exceptions[invoiceID]
	if !ok || e.Status != domain.ExceptionOpen {
		return
	}
	now := time.Now().UTC()
	e.Status, e.DecidedBy, e.DecidedAt, e.DecisionReason = status, by, &now, reason
	t.exceptions[invoiceID] = e
}
