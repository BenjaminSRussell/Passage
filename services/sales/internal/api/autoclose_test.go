package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

type autoCloseStore struct {
	store.Store
	got   store.AutoCloseFilter
	actor string
	calls int
}

func (s *autoCloseStore) AutoCloseOrders(_ context.Context, f store.AutoCloseFilter, actor string, _ time.Time) (store.AutoCloseResult, error) {
	s.got, s.actor = f, actor
	s.calls++
	return store.AutoCloseResult{Closed: 1, Items: []store.AutoCloseItem{{OrderID: "o1", Result: "CLOSED"}}}, nil
}

func TestAutoCloseOrders(t *testing.T) {
	s := &autoCloseStore{}
	rec := serveBody(t, s, http.MethodPost, orders+":auto-close", "", principal(dutyWrite), nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"closed":1`) {
		t.Fatalf("run: %d %s", rec.Code, rec.Body)
	}
	if s.got.LegalEntityID != testLE || s.got.GraceDays != defaultAutoCloseDays || s.got.Limit != defaultAutoCloseBatch || s.got.DryRun || s.actor != "u1" {
		t.Fatalf("defaults: %+v %s", s.got, s.actor)
	}
	rec = serveBody(t, s, http.MethodPost, orders+":auto-close?graceDays=0&limit=5&dryRun=true", "", principal(dutyWrite), nil)
	if rec.Code != http.StatusOK || s.got.GraceDays != 0 || s.got.Limit != 5 || !s.got.DryRun {
		t.Fatalf("parameters: %d %+v", rec.Code, s.got)
	}
	calls := s.calls
	for _, q := range []string{"graceDays=-1", "graceDays=366", "graceDays=x", "limit=0", "limit=501", "dryRun=maybe"} {
		if rec := serveBody(t, s, http.MethodPost, orders+":auto-close?"+q, "", principal(dutyWrite), nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", q, rec.Code)
		}
	}
	if s.calls != calls {
		t.Fatal("a bad request must not reach the store")
	}
	if rec := serveBody(t, s, http.MethodPost, orders+":auto-close", "", principal(dutyRead), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("duty: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, orders+":auto-close?legalEntityId=11111111-1111-4111-8111-111111111111", "", principal(dutyWrite), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("legal entity outside scope: %d", rec.Code)
	}
}
