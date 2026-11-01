package store_test

import (
	"encoding/json"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	pmfevents "github.com/BenjaminSRussell/passage/services/processmfg/internal/events"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

func intp(n int) *int       { return &n }
func boolp(b bool) *bool    { return &b }
func strp(s string) *string { return &s }

func newReq(item string, mod func(*store.CustomerRequirement)) store.CustomerRequirement {
	r := store.CustomerRequirement{LegalEntityID: entityA, ItemID: item, SellableDays: intp(30)}
	if mod != nil {
		mod(&r)
	}
	return r
}

func TestCustomerRequirements_CreateReplayEventAndRetire(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	item, cust := ids.New(), ids.New()
	lo, hi := dec("40"), dec("60")
	in := newReq(item, func(r *store.CustomerRequirement) {
		r.CustomerID, r.SellableDays = &cust, intp(60)
		r.Attribute, r.Min, r.Max = "potency", &lo, &hi
		r.ValidFrom = strp("2027-01-01")
	})
	got, created, err := s.CreateCustomerRequirement(ctx, in, store.Idem{Key: "cr-1", Fingerprint: "1"})
	if err != nil || !created || got.Level != domain.ScopeCustomer || got.Version != 1 || got.RetiredAt != nil || got.ID == "" ||
		*got.SellableDays != 60 || got.Attribute != "potency" || !got.Min.Equal(lo) || *got.ValidFrom != "2027-01-01" || got.ValidTo != nil {
		t.Fatalf("create created=%v %+v err=%v", created, got, err)
	}
	evts := outboxEvents(t, admin, pmfevents.BatchRequirementDefined)
	var ev pmfevents.BatchRequirementData
	if len(evts) != 1 || json.Unmarshal(evts[0].Data, &ev) != nil || ev.RequirementID != got.ID || ev.Level != 3 || ev.CustomerID != cust || ev.Min != "40" || ev.ValidFrom != "2027-01-01" {
		t.Fatalf("events %d %+v", len(evts), ev)
	}
	again, created, err := s.CreateCustomerRequirement(ctx, in, store.Idem{Key: "cr-1", Fingerprint: "1"})
	if err != nil || created || again.ID != got.ID || len(outboxEvents(t, admin, pmfevents.BatchRequirementDefined)) != 1 {
		t.Fatalf("replay created=%v err=%v", created, err)
	}
	if _, _, err := s.CreateCustomerRequirement(ctx, in, store.Idem{Key: "cr-1", Fingerprint: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key other body: %v", err)
	}

	// The domain refuses a rule that sets nothing or an inverted range, and
	// nothing is written.
	for name, bad := range map[string]store.CustomerRequirement{
		"nothing": newReq(item, func(r *store.CustomerRequirement) { r.SellableDays = nil }),
		"days":    newReq(item, func(r *store.CustomerRequirement) { r.SellableDays = intp(4000) }),
		"range":   newReq(item, func(r *store.CustomerRequirement) { r.Attribute, r.Min, r.Max = "potency", &hi, &lo }),
	} {
		if _, _, err := s.CreateCustomerRequirement(ctx, bad, store.Idem{Key: "bad-" + name, Fingerprint: name}); problemCode(err) != domain.CodeInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}

	r2, err := s.GetCustomerRequirement(ctx, got.ID)
	if err != nil || r2.ID != got.ID || r2.Level != domain.ScopeCustomer {
		t.Fatalf("get %+v err=%v", r2, err)
	}
	if _, _, err := s.RetireCustomerRequirement(ctx, got.ID, 5, store.Idem{Key: "rt-stale", Fingerprint: "s"}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale retire: %v", err)
	}
	ret, changed, err := s.RetireCustomerRequirement(ctx, got.ID, 1, store.Idem{Key: "rt-1", Fingerprint: "r"})
	if err != nil || !changed || ret.Version != 2 || ret.RetiredAt == nil || ret.RetiredBy == nil || *ret.RetiredBy != "planner-1" {
		t.Fatalf("retire %+v err=%v", ret, err)
	}
	if again, changed, err := s.RetireCustomerRequirement(ctx, got.ID, 1, store.Idem{Key: "rt-1", Fingerprint: "r"}); err != nil || changed || again.Version != 2 ||
		len(outboxEvents(t, admin, pmfevents.BatchRequirementRetired)) != 1 {
		t.Fatalf("retire replay changed=%v err=%v", changed, err)
	}
	if _, _, err := s.RetireCustomerRequirement(ctx, got.ID, 2, store.Idem{Key: "rt-2", Fingerprint: "r2"}); problemCode(err) != store.CodeRequirementRetired {
		t.Fatalf("retire twice: %v", err)
	}
	if _, err := s.GetCustomerRequirement(ctx, ids.New()); problemCode(err) != store.CodeRequirementNotFound {
		t.Fatalf("unknown: %v", err)
	}
}

func TestCustomerRequirements_ListFiltersAndApplicable(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	item, other, cust, otherCust, group := ids.New(), ids.New(), ids.New(), ids.New(), ids.New()
	mk := func(key string, r store.CustomerRequirement) store.CustomerRequirement {
		out, _, err := s.CreateCustomerRequirement(ctx, r, store.Idem{Key: key, Fingerprint: key})
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		return out
	}
	base := mk("l-base", newReq(item, func(r *store.CustomerRequirement) { r.SellableDays = intp(10) }))
	grp := mk("l-group", newReq(item, func(r *store.CustomerRequirement) { r.CustomerGroupID, r.SellableDays = &group, intp(30) }))
	mine := mk("l-cust", newReq(item, func(r *store.CustomerRequirement) { r.CustomerID, r.SellableDays = &cust, intp(60) }))
	mk("l-other-cust", newReq(item, func(r *store.CustomerRequirement) { r.CustomerID, r.SellableDays = &otherCust, intp(90) }))
	later := mk("l-later", newReq(item, func(r *store.CustomerRequirement) {
		r.SameBatch, r.SellableDays, r.ValidFrom = boolp(true), nil, strp("2027-06-01")
	}))
	old := mk("l-old", newReq(item, func(r *store.CustomerRequirement) { r.SellableDays, r.ValidTo = intp(5), strp("2026-12-31") }))
	mk("l-other-item", newReq(other, nil))

	if all, err := s.ListCustomerRequirements(ctx, store.RequirementFilter{ItemID: item, Limit: 50}); err != nil || len(all) != 6 {
		t.Fatalf("list item: %d err=%v", len(all), err)
	}
	if byCust, err := s.ListCustomerRequirements(ctx, store.RequirementFilter{CustomerID: cust, Limit: 50}); err != nil || len(byCust) != 1 || byCust[0].ID != mine.ID {
		t.Fatalf("list customer: %+v err=%v", byCust, err)
	}
	if byGroup, err := s.ListCustomerRequirements(ctx, store.RequirementFilter{CustomerGroupID: group, Limit: 50}); err != nil || len(byGroup) != 1 || byGroup[0].ID != grp.ID {
		t.Fatalf("list group: %+v err=%v", byGroup, err)
	}
	if lim, err := s.ListCustomerRequirements(ctx, store.RequirementFilter{Limit: 2}); err != nil || len(lim) != 2 {
		t.Fatalf("limit: %d err=%v", len(lim), err)
	}
	active, err := s.ListCustomerRequirements(ctx, store.RequirementFilter{ItemID: item, ActiveOn: "2027-03-01", Limit: 50})
	if err != nil || len(active) != 4 {
		t.Fatalf("active on 2027-03-01: %d err=%v", len(active), err)
	}

	// The customer sees their own rule, their group's and the item default, in
	// precedence order; the other customer's rule, the later one and the
	// expired one stay out.
	ids2 := func(rs []store.CustomerRequirement) []string {
		out := make([]string, len(rs))
		for i, r := range rs {
			out[i] = r.ID
		}
		return out
	}
	same := func(got []store.CustomerRequirement, want ...string) bool {
		g := ids2(got)
		if len(g) != len(want) {
			return false
		}
		for i := range want {
			if g[i] != want[i] {
				return false
			}
		}
		return true
	}
	who := store.ReqScope{CustomerID: cust, CustomerGroupID: group}
	got, err := s.ApplicableRequirements(ctx, entityA, item, who, "2027-03-01")
	if err != nil || !same(got, mine.ID, grp.ID, base.ID) {
		t.Fatalf("applicable %v want %v err=%v", ids2(got), []string{mine.ID, grp.ID, base.ID}, err)
	}
	res, err := domain.Resolve([]domain.Requirement{got[0].DomainReq(), got[1].DomainReq(), got[2].DomainReq()})
	if err != nil || res.SellableDays != 60 {
		t.Fatalf("resolved %+v err=%v", res, err)
	}
	if got, _ := s.ApplicableRequirements(ctx, entityA, item, store.ReqScope{}, "2027-03-01"); !same(got, base.ID) {
		t.Fatalf("no customer: %v", ids2(got))
	}
	// Within one scope the rule that started last comes first, so the
	// same-batch rule from 2027-06-01 is ahead of the open-ended default.
	if got, _ := s.ApplicableRequirements(ctx, entityA, item, store.ReqScope{}, "2027-07-01"); !same(got, later.ID, base.ID) {
		t.Fatalf("after the later rule starts: %v", ids2(got))
	}
	// Two overlapping item defaults with no start: the one defined last wins
	// (5 days, not 10), and valid_to is inclusive.
	got, _ = s.ApplicableRequirements(ctx, entityA, item, store.ReqScope{}, "2026-12-31")
	if !same(got, old.ID, base.ID) {
		t.Fatalf("valid_to day is inclusive, newest first: %v", ids2(got))
	}
	if res, err := domain.Resolve([]domain.Requirement{got[0].DomainReq(), got[1].DomainReq()}); err != nil || res.SellableDays != 5 {
		t.Fatalf("overlapping defaults resolved %+v err=%v", res, err)
	}
	// A rule of another legal entity for the same item and customer is not
	// applied to this legal entity's line, even for a caller who sees both.
	otherLE := mk("l-other-le", newReq(item, func(r *store.CustomerRequirement) {
		r.LegalEntityID, r.CustomerID, r.SellableDays = entityB, &cust, intp(365)
	}))
	if got, _ := s.ApplicableRequirements(ctx, entityA, item, who, "2027-03-01"); !same(got, mine.ID, grp.ID, base.ID) {
		t.Fatalf("other legal entity leaked: %v", ids2(got))
	}
	if got, _ := s.ApplicableRequirements(ctx, entityB, item, who, "2027-03-01"); !same(got, otherLE.ID) {
		t.Fatalf("entity B rules: %v", ids2(got))
	}
	if got, err := s.ListCustomerRequirements(ctx, store.RequirementFilter{LegalEntityID: entityB, Limit: 50}); err != nil || !same(got, otherLE.ID) {
		t.Fatalf("list by legal entity: %v err=%v", ids2(got), err)
	}

	// A retired rule stops applying.
	if _, _, err := s.RetireCustomerRequirement(ctx, mine.ID, mine.Version, store.Idem{Key: "l-ret", Fingerprint: "r"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ApplicableRequirements(ctx, entityA, item, who, "2027-03-01"); !same(got, grp.ID, base.ID) {
		t.Fatalf("after retire: %v", ids2(got))
	}

	// Another tenant sees none of it; a caller scoped to entity B only its rule.
	if got, err := s.ListCustomerRequirements(ctxFor(2, ""), store.RequirementFilter{ItemID: item, Limit: 50}); err != nil || len(got) != 0 {
		t.Fatalf("tenant 2: %v err=%v", got, err)
	}
	if got, err := s.ListCustomerRequirements(ctxFor(1, entityB), store.RequirementFilter{ItemID: item, Limit: 50}); err != nil || !same(got, otherLE.ID) {
		t.Fatalf("entity B sees only its own rule: %v err=%v", ids2(got), err)
	}
	if _, err := s.GetCustomerRequirement(ctxFor(1, entityB), base.ID); problemCode(err) != store.CodeRequirementNotFound {
		t.Fatalf("get from entity B: %v", err)
	}
	if _, _, err := s.RetireCustomerRequirement(ctxFor(1, entityB), base.ID, 1, store.Idem{Key: "b-ret", Fingerprint: "b"}); problemCode(err) != store.CodeRequirementNotFound {
		t.Fatalf("retire from entity B: %v", err)
	}
}
