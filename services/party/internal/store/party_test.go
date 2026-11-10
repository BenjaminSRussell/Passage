package store_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

type numberSource struct{ calls int }

func (n *numberSource) next(context.Context) (string, error) {
	n.calls++
	return fmt.Sprintf("PTY-%06d", n.calls), nil
}

func str(s string) *string { return &s }

func org(name string) store.NewParty {
	return store.NewParty{PartyType: domain.TypeOrganization, Fields: domain.PartyFields{LegalName: str(name)}}
}

func person(given, family, dob string) store.NewParty {
	return store.NewParty{PartyType: domain.TypePerson, Fields: domain.PartyFields{GivenName: str(given), FamilyName: str(family), DOB: str(dob), Gender: str("f")}}
}

type outboxRow struct{ typ, payload string }

func outboxRows(t *testing.T, ctx context.Context, app *pgxpool.Pool) []outboxRow {
	t.Helper()
	var rows []outboxRow
	err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, `select type, payload::text from outbox order by id`)
		if err != nil {
			return err
		}
		defer r.Close()
		for r.Next() {
			var row outboxRow
			if err := r.Scan(&row.typ, &row.payload); err != nil {
				return err
			}
			rows = append(rows, row)
		}
		return r.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func countParties(t *testing.T, ctx context.Context, app *pgxpool.Pool) int {
	t.Helper()
	var n int
	err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*) from party`).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func wantProblem(t *testing.T, err error, status int, code string) {
	t.Helper()
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Status != status || p.Code != code {
		t.Fatalf("got %v, want %d %s", err, status, code)
	}
}

func TestCreateParty_WritesPartyAndOneCreatedEvent(t *testing.T) {
	app := migratedApp(t)
	ctx, numbers := principalContext(1, ""), &numberSource{}
	s := store.New(app)
	created, isNew, err := s.CreateParty(ctx, org("Acme GmbH"), numbers.next)
	if err != nil || !isNew {
		t.Fatalf("got %v, new=%v", err, isNew)
	}
	if created.PartyNumber != "PTY-000001" || created.Status != domain.StatusActive || created.Version != 1 || created.Name != "Acme GmbH" || created.SearchName != "acme gmbh" {
		t.Fatalf("got %+v", created)
	}
	rows := outboxRows(t, ctx, app)
	if len(rows) != 1 || rows[0].typ != "dev.passage.party.party.created.v1" || !strings.Contains(rows[0].payload, created.ID) {
		t.Fatalf("got %+v", rows)
	}
}

func TestCreateParty_ReplayReturnsOriginalWithoutSecondNumber(t *testing.T) {
	app := migratedApp(t)
	ctx, numbers := principalContext(1, ""), &numberSource{}
	s := store.New(app)
	in := org("Acme")
	in.Key, in.Hash = "key-1", "hash-a"
	first, _, err := s.CreateParty(ctx, in, numbers.next)
	if err != nil {
		t.Fatal(err)
	}
	replay, isNew, err := s.CreateParty(ctx, in, numbers.next)
	if err != nil || isNew || replay.ID != first.ID || replay.PartyNumber != first.PartyNumber {
		t.Fatalf("got %+v new=%v err=%v", replay, isNew, err)
	}
	if numbers.calls != 1 || countParties(t, ctx, app) != 1 || len(outboxRows(t, ctx, app)) != 1 {
		t.Fatalf("calls=%d parties=%d", numbers.calls, countParties(t, ctx, app))
	}
	in.Hash = "hash-b"
	_, _, err = s.CreateParty(ctx, in, numbers.next)
	wantProblem(t, err, http.StatusConflict, store.CodeIdempotencyReuse)
	if numbers.calls != 1 {
		t.Fatalf("numbering called %d times", numbers.calls)
	}
}

func TestCreateParty_KeyIsScopedToTenant(t *testing.T) {
	app := migratedApp(t)
	s, numbers := store.New(app), &numberSource{}
	in := org("Acme")
	in.Key, in.Hash = "shared", "h"
	a, _, errA := s.CreateParty(principalContext(1, ""), in, numbers.next)
	b, isNew, errB := s.CreateParty(principalContext(2, ""), in, numbers.next)
	if errA != nil || errB != nil || !isNew || a.ID == b.ID {
		t.Fatalf("a=%+v b=%+v %v %v", a, b, errA, errB)
	}
}

func TestCreateParty_FailureLeavesNoRow(t *testing.T) {
	app := migratedApp(t)
	ctx := principalContext(1, "")
	boom := errors.New("outbox down")
	failing := store.WithFailpoint(store.New(app), func() error { return boom })
	if _, _, err := failing.CreateParty(ctx, org("Acme"), (&numberSource{}).next); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	unavailable := func(context.Context) (string, error) {
		return "", httpx.NewProblem(http.StatusServiceUnavailable, "PTY-NUMBERING-UNAVAILABLE", "down")
	}
	_, _, err := store.New(app).CreateParty(ctx, org("Acme"), unavailable)
	wantProblem(t, err, http.StatusServiceUnavailable, "PTY-NUMBERING-UNAVAILABLE")
	if countParties(t, ctx, app) != 0 || len(outboxRows(t, ctx, app)) != 0 {
		t.Fatal("a failed create left rows behind")
	}
}

func TestCreateParty_ReconcilesPartiesWithCreatedEvents(t *testing.T) {
	app := migratedApp(t)
	ctx, numbers, s := principalContext(1, ""), &numberSource{}, store.New(app)
	const n = 12
	for i := 0; i < n; i++ {
		if _, _, err := s.CreateParty(ctx, org(fmt.Sprintf("Org %d", i)), numbers.next); err != nil {
			t.Fatal(err)
		}
	}
	rows := outboxRows(t, ctx, app)
	if countParties(t, ctx, app) != n || len(rows) != n {
		t.Fatalf("parties=%d outbox=%d", countParties(t, ctx, app), len(rows))
	}
	for _, r := range rows {
		if r.typ != "dev.passage.party.party.created.v1" {
			t.Fatalf("unexpected %s", r.typ)
		}
	}
}

func TestNoPayloadCarriesPersonalData(t *testing.T) {
	app := migratedApp(t)
	ctx, s := principalContext(1, ""), store.New(app)
	p, _, err := s.CreateParty(ctx, person("Ada", "Lovelace", "1990-02-03"), (&numberSource{}).next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateParty(ctx, p.ID, 1, store.PartyPatch{Fields: domain.PartyFields{DOB: str("1991-01-01"), Gender: str("x"), GivenName: str("Grace")}}); err != nil {
		t.Fatal(err)
	}
	rows := outboxRows(t, ctx, app)
	if len(rows) != 2 {
		t.Fatalf("got %d rows", len(rows))
	}
	for _, r := range rows {
		for _, banned := range []string{`"dob"`, `"gender"`, "1990-02-03", "1991-01-01"} {
			if strings.Contains(r.payload, banned) {
				t.Errorf("%s payload contains %s: %s", r.typ, banned, r.payload)
			}
		}
	}
}

func TestGetParty_FollowsMergedRedirectAndStoresDOB(t *testing.T) {
	app := migratedApp(t)
	ctx, s, numbers := principalContext(1, ""), store.New(app), &numberSource{}
	survivor, _, err := s.CreateParty(ctx, person("Ada", "Lovelace", "1990-02-03"), numbers.next)
	if err != nil {
		t.Fatal(err)
	}
	loser, _, err := s.CreateParty(ctx, person("Ada", "Lovelace", "1990-02-03"), numbers.next)
	if err != nil {
		t.Fatal(err)
	}
	err = dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update party set status = 'merged', merged_into_id = $2 where id = $1`, loser.ID, survivor.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got, redirected, err := s.GetParty(ctx, loser.ID)
	if err != nil || !redirected || got.ID != survivor.ID || *got.DOB != "1990-02-03" {
		t.Fatalf("got %+v redirected=%v err=%v", got, redirected, err)
	}
	direct, redirected, err := s.GetParty(ctx, survivor.ID)
	if err != nil || redirected || direct.ID != survivor.ID {
		t.Fatalf("got %+v redirected=%v err=%v", direct, redirected, err)
	}
}

func TestGetParty_MergedChainIsBounded(t *testing.T) {
	app := migratedApp(t)
	ctx, s, numbers := principalContext(1, ""), store.New(app), &numberSource{}
	chain := make([]string, 8)
	for i := range chain {
		p, _, err := s.CreateParty(ctx, org(fmt.Sprintf("Chain %d", i)), numbers.next)
		if err != nil {
			t.Fatal(err)
		}
		chain[i] = p.ID
	}
	err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		for i := 0; i < len(chain)-1; i++ {
			if _, err := tx.Exec(ctx, `update party set status = 'merged', merged_into_id = $2 where id = $1`, chain[i], chain[i+1]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, redirected, err := s.GetParty(ctx, chain[2]); err != nil || !redirected || got.ID != chain[7] {
		t.Fatalf("5 hops: got %v %v %v", got.ID, redirected, err)
	}
	if _, _, err := s.GetParty(ctx, chain[0]); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("7 hops: got %v", err)
	}
}

func TestGetParty_UnknownAndForeignTenantAreNotFound(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	p, _, err := s.CreateParty(principalContext(1, ""), org("Acme"), (&numberSource{}).next)
	if err != nil {
		t.Fatal(err)
	}
	for name, ctxAndID := range map[string]struct {
		ctx context.Context
		id  string
	}{
		"other tenant": {principalContext(2, ""), p.ID},
		"unknown id":   {principalContext(1, ""), "00000000-0000-0000-0000-00000000dead"},
	} {
		if _, _, err := s.GetParty(ctxAndID.ctx, ctxAndID.id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if _, _, err := s.GetParty(principalContext(1, ""), "not-a-uuid"); !errors.Is(err, store.ErrInvalidID) {
		t.Fatalf("got %v", err)
	}
	if _, err := s.UpdateParty(principalContext(2, ""), p.ID, 1, store.PartyPatch{Status: str("inactive")}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update across tenants: got %v", err)
	}
}

func TestUpdateParty_VersionAndStatusMachine(t *testing.T) {
	app := migratedApp(t)
	ctx, s := principalContext(1, ""), store.New(app)
	p, _, err := s.CreateParty(ctx, org("Acme"), (&numberSource{}).next)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.UpdateParty(ctx, p.ID, 7, store.PartyPatch{Status: str("inactive")})
	wantProblem(t, err, http.StatusConflict, store.CodeVersionConflict)

	inactive, err := s.UpdateParty(ctx, p.ID, 1, store.PartyPatch{Status: str("inactive")})
	if err != nil || inactive.Version != 2 || inactive.Status != "inactive" {
		t.Fatalf("got %+v %v", inactive, err)
	}
	active, err := s.UpdateParty(ctx, p.ID, 2, store.PartyPatch{Status: str("active")})
	if err != nil || active.Version != 3 || active.Status != "active" {
		t.Fatalf("got %+v %v", active, err)
	}
	_, err = s.UpdateParty(ctx, p.ID, 3, store.PartyPatch{Status: str("merged")})
	wantProblem(t, err, http.StatusUnprocessableEntity, store.CodeStatusTransition)
	_, err = s.UpdateParty(ctx, p.ID, 3, store.PartyPatch{Status: str("draft")})
	wantProblem(t, err, http.StatusUnprocessableEntity, store.CodeStatusTransition)
	_, err = s.UpdateParty(ctx, p.ID, 3, store.PartyPatch{PartyType: str("person")})
	wantProblem(t, err, http.StatusUnprocessableEntity, store.CodeInvalid)
	_, err = s.UpdateParty(ctx, p.ID, 3, store.PartyPatch{Fields: domain.PartyFields{GivenName: str("Ada")}})
	wantProblem(t, err, http.StatusUnprocessableEntity, store.CodeInvalid)
	_, err = s.UpdateParty(ctx, p.ID, 3, store.PartyPatch{Fields: domain.PartyFields{LegalName: str("  ")}})
	wantProblem(t, err, http.StatusUnprocessableEntity, store.CodeInvalid)
}

func TestUpdateParty_MergedPartyCannotChange(t *testing.T) {
	app := migratedApp(t)
	ctx, s, numbers := principalContext(1, ""), store.New(app), &numberSource{}
	survivor, _, _ := s.CreateParty(ctx, org("Acme"), numbers.next)
	loser, _, _ := s.CreateParty(ctx, org("Acme Ltd"), numbers.next)
	err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update party set status = 'merged', merged_into_id = $2 where id = $1`, loser.ID, survivor.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.UpdateParty(ctx, loser.ID, 1, store.PartyPatch{Fields: domain.PartyFields{TradeName: str("x")}})
	wantProblem(t, err, http.StatusUnprocessableEntity, store.CodeStatusTransition)
}

func TestUpdateParty_NameChangeRecomputesAndEmitsSortedFields(t *testing.T) {
	app := migratedApp(t)
	ctx, s := principalContext(1, ""), store.New(app)
	p, _, err := s.CreateParty(ctx, person("Ada", "Lovelace", "1990-02-03"), (&numberSource{}).next)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.UpdateParty(ctx, p.ID, 1, store.PartyPatch{Fields: domain.PartyFields{GivenName: str("Grace"), FamilyName: str("Hopper"), TimeZone: str("UTC")}})
	if err != nil || updated.Name != "Grace Hopper" || updated.SearchName != "grace hopper" || updated.Version != 2 {
		t.Fatalf("got %+v %v", updated, err)
	}
	rows := outboxRows(t, ctx, app)
	last := rows[len(rows)-1]
	if last.typ != "dev.passage.party.party.updated.v1" || !strings.Contains(last.payload, `"changed_fields": ["familyName", "givenName", "timeZone"]`) || !strings.Contains(last.payload, `"version": 2`) {
		t.Fatalf("got %+v", last)
	}
	for _, want := range []string{`"party_number": "` + updated.PartyNumber + `"`, `"name": "Grace Hopper"`, `"party_type": "` + updated.PartyType + `"`, `"status": "`} {
		if !strings.Contains(last.payload, want) {
			t.Fatalf("updated event lacks %s for the search index: %s", want, last.payload)
		}
	}
}

func TestUpdateParty_NoOpKeepsVersionAndWritesNoEvent(t *testing.T) {
	app := migratedApp(t)
	ctx, s := principalContext(1, ""), store.New(app)
	p, _, err := s.CreateParty(ctx, org("Acme"), (&numberSource{}).next)
	if err != nil {
		t.Fatal(err)
	}
	same, err := s.UpdateParty(ctx, p.ID, 1, store.PartyPatch{Status: str("active"), Fields: domain.PartyFields{LegalName: str("Acme")}})
	if err != nil || same.Version != 1 || !same.UpdatedAt.Equal(p.UpdatedAt) {
		t.Fatalf("got %+v %v", same, err)
	}
	if len(outboxRows(t, ctx, app)) != 1 {
		t.Fatal("a no-op patch wrote an event")
	}
}

func TestUpdateParty_FailureRollsBackChange(t *testing.T) {
	app := migratedApp(t)
	ctx, s := principalContext(1, ""), store.New(app)
	p, _, err := s.CreateParty(ctx, org("Acme"), (&numberSource{}).next)
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("outbox down")
	if _, err := store.WithFailpoint(s, func() error { return boom }).UpdateParty(ctx, p.ID, 1, store.PartyPatch{Status: str("inactive")}); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	got, _, err := s.GetParty(ctx, p.ID)
	if err != nil || got.Version != 1 || got.Status != "active" {
		t.Fatalf("got %+v %v", got, err)
	}
}
