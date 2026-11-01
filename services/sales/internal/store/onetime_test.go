package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

func walkIn() *domain.OneTimeParty {
	return &domain.OneTimeParty{Name: "Walk-in Buyer GmbH", Line1: "Hauptstr. 1", City: "Berlin", PostalCode: "10115", Country: "DE", TaxID: "DE123456789"}
}

func oneTimeOrder(le string, amount int64, key string) NewOrder {
	in := oneLineOrder(le, ids.New(), ids.New(), time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC))
	in.Lines[0].Quantity, in.Lines[0].UnitPrice, in.Lines[0].LineNet = d(1), d(amount), d(amount)
	in.OneTime = walkIn()
	in.IdempotencyKey = &key
	return in
}

// testKeys returns a keyring whose current key is current, holding a distinct
// 32-byte key for each id in keyIDs (which must include current).
func testKeys(t *testing.T, current string, keyIDs ...string) *fieldcrypt.Keys {
	t.Helper()
	m := map[string][]byte{}
	for i, id := range keyIDs {
		m[id] = bytes.Repeat([]byte{byte(i + 1)}, 32)
	}
	k, err := fieldcrypt.NewKeys(current, m)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func sealedStore(t *testing.T) (*Postgres, *pgxpool.Pool, context.Context) {
	s, pool, ctx := migrated(t)
	return s.WithFieldCrypt(testKeys(t, "k1", "k1")), pool, ctx
}

// TestPostgres_OneTimeOrderCarriesPartyAndLimit covers AC-27.14 on the order
// side: the buyer's name, address and tax id are kept on the header and read
// back, order.confirmed marks the order as one-time; an order over the default
// limit (5,000 USD) is refused with SLS-0016 and leaves no order behind.
func TestPostgres_OneTimeOrderCarriesPartyAndLimit(t *testing.T) {
	s, pool, ctx := sealedStore(t)
	le := ids.New()
	order, _, err := s.CreateOrder(ctx, oneTimeOrder(le, 4999, "k-one-time"))
	if err != nil || order.OneTime == nil || *order.OneTime != *walkIn() {
		t.Fatalf("create: %+v %v", order.OneTime, err)
	}
	got, err := s.GetOrder(ctx, order.ID)
	if err != nil || got.OneTime == nil || *got.OneTime != *walkIn() {
		t.Fatalf("read back: %+v %v", got.OneTime, err)
	}
	res, err := s.ConfirmOrder(ctx, order.ID, order.Version, "sales-rep", time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC))
	if err != nil || !res.Confirmed || res.Order.OneTime == nil || res.Order.OneTime.Name != "Walk-in Buyer GmbH" {
		t.Fatalf("confirm: %+v %v", res.Order.OneTime, err)
	}
	var data salesevents.OrderConfirmedData
	if err := json.Unmarshal(outboxData(t, pool, order.ID, salesevents.OrderConfirmed), &data); err != nil || data.OneTime == nil || data.OneTime.Country != "DE" {
		t.Fatalf("order.confirmed one_time: %+v %v", data.OneTime, err)
	}

	before := countWhere(t, pool, `select count(*) from sales_order where legal_entity_id = $1`, le)
	_, _, err = s.CreateOrder(ctx, oneTimeOrder(le, 5001, "k-over"))
	if problemCode(t, err) != CodeOneTimeLimit {
		t.Fatalf("over the limit: %v", err)
	}
	if n := countWhere(t, pool, `select count(*) from sales_order where legal_entity_id = $1`, le); n != before {
		t.Fatalf("a refused order left %d rows", n-before)
	}
	plain := oneTimeOrder(le, 9000, "k-plain")
	plain.OneTime = nil
	if o, _, err := s.CreateOrder(ctx, plain); err != nil || o.OneTime != nil {
		t.Fatalf("an ordinary order is not held to the one-time limit: %+v %v", o.OneTime, err)
	}
}

// TestPostgres_OneTimePartyIsSealed checks that the buyer's personal data is
// in clear in none of the places it could land: the order row, the stored
// idempotency hash and the outbox.
func TestPostgres_OneTimePartyIsSealed(t *testing.T) {
	s, pool, ctx := sealedStore(t)
	in := oneTimeOrder(ids.New(), 100, "k-sealed")
	plainHash := in.RequestHash
	order, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmOrder(ctx, order.ID, order.Version, "sales-rep", time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	var row, hash, country string
	var sealed []byte
	if err := pool.QueryRow(context.Background(), `select row_to_json(o)::text, request_hash, one_time_country, one_time_party_sealed from sales_order o where id = $1`, order.ID).
		Scan(&row, &hash, &country, &sealed); err != nil {
		t.Fatal(err)
	}
	var events string
	if err := pool.QueryRow(context.Background(), `select coalesce(string_agg(payload::text, ' '), '') from outbox where aggregate_id = $1`, order.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"Walk-in", "Hauptstr", "10115", "DE123456789"} {
		if strings.Contains(row, secret) || bytes.Contains(sealed, []byte(secret)) || strings.Contains(events, secret) {
			t.Fatalf("%q is stored in clear: row %s events %s", secret, row, events)
		}
	}
	if len(sealed) == 0 || country != "DE" || !strings.HasPrefix(hash, "hmac:k1:") || strings.Contains(hash, plainHash) {
		t.Fatalf("sealed %d bytes, country %q, request hash %q must be a keyed MAC", len(sealed), country, hash)
	}
}

// TestPostgres_OneTimeReplayAndKeys covers the Idempotency-Key replay of a
// one-time order (also after a key rotation), a reuse with another body, and
// the service without field encryption keys: one-time orders are refused and
// cannot be read, ordinary orders are unaffected.
func TestPostgres_OneTimeReplayAndKeys(t *testing.T) {
	s, _, ctx := sealedStore(t)
	le := ids.New()
	in := oneTimeOrder(le, 100, "k-replay")
	order, created, err := s.CreateOrder(ctx, in)
	if err != nil || !created {
		t.Fatal(err)
	}
	rotated := New(s.pool).WithFieldCrypt(testKeys(t, "k2", "k1", "k2"))
	replay, created, err := rotated.CreateOrder(ctx, in)
	if err != nil || created || replay.ID != order.ID || replay.OneTime == nil || replay.OneTime.Name != walkIn().Name {
		t.Fatalf("replay after rotation: created=%v %+v %v", created, replay.OneTime, err)
	}
	other := in
	other.RequestHash = ids.New()
	if _, _, err := rotated.CreateOrder(ctx, other); problemCode(t, err) != CodeIdempotencyReuse {
		t.Fatalf("the key with another body: %v", err)
	}

	keyless := New(s.pool)
	_, _, createErr := keyless.CreateOrder(ctx, oneTimeOrder(le, 100, "k-keyless"))
	_, readErr := keyless.GetOrder(ctx, order.ID)
	for name, err := range map[string]error{"create": createErr, "read": readErr} {
		var p *httpx.Problem
		if !errors.As(err, &p) || p.Status != http.StatusServiceUnavailable || p.Code != CodeFieldCryptMissing {
			t.Fatalf("%s without keys: %v", name, err)
		}
	}
	plain := oneTimeOrder(le, 100, "k-keyless-plain")
	plain.OneTime = nil
	if _, _, err := keyless.CreateOrder(ctx, plain); err != nil {
		t.Fatalf("an ordinary order needs no keys: %v", err)
	}
}

// TestPostgres_OneTimeLimitFollowsParametersAndChanges checks that a saved
// limit replaces the default (and is stated in one currency), that a change
// that raises the total over the limit is refused, that confirm re-checks,
// and that a CREDIT release does not confirm an order the lowered limit no
// longer allows (the release stands, SLS-0016 is a warning).
func TestPostgres_OneTimeLimitFollowsParametersAndChanges(t *testing.T) {
	s, pool, ctx := sealedStore(t)
	le := ids.New()
	limit, cur := d(1000), "USD"
	params := domain.DefaultParameters()
	params.OneTimeOrderLimit, params.OneTimeOrderLimitCurrency = &limit, &cur
	saved, err := s.PutParameters(ctx, SalesParameters{LegalEntityID: le, Parameters: params}, 0, "admin")
	if err != nil || saved.OneTimeOrderLimit == nil || !saved.OneTimeOrderLimit.Equal(d(1000)) || *saved.OneTimeOrderLimitCurrency != "USD" {
		t.Fatalf("save limit: %+v %v", saved, err)
	}
	if _, _, err := s.CreateOrder(ctx, oneTimeOrder(le, 1500, "k-1500")); problemCode(t, err) != CodeOneTimeLimit {
		t.Fatalf("1,500 is over the saved 1,000: %v", err)
	}
	eur := oneTimeOrder(le, 10, "k-eur")
	eur.Currency = "EUR"
	if _, _, err := s.CreateOrder(ctx, eur); problemCode(t, err) != CodeOneTimeLimit {
		t.Fatalf("a limit stated in USD does not cover EUR: %v", err)
	}
	order, _, err := s.CreateOrder(ctx, oneTimeOrder(le, 900, "k-900"))
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2027, 3, 2, 9, 0, 0, 0, time.UTC)
	price := d(1200)
	raised := ChangeRequest{Reason: "bigger", Lines: []LineChange{{LineNo: 1, UnitPrice: &price}}}
	if _, err := s.ChangeOrder(ctx, order.ID, order.Version, raised, "sales-rep", day); problemCode(t, err) != CodeOneTimeLimit {
		t.Fatalf("a change over the limit: %v", err)
	}

	// A credit limit the order fails: confirm places a CREDIT hold.
	credit := domain.CreditProfile{Limit: d(100), Currency: "USD", LimitType: domain.LimitBalance, TolerancePct: d(0), ToleranceAmount: d(0), OldestItemDaysMax: 30, MaxDunningLevel: 3}
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: order.CustomerID, CreditProfile: credit}, 0); err != nil {
		t.Fatal(err)
	}
	held, err := s.ConfirmOrder(ctx, order.ID, order.Version, "sales-rep", day)
	if err != nil || held.Confirmed || len(held.Order.Holds) != 1 {
		t.Fatalf("confirm must place a CREDIT hold: %+v %v", held.Confirmed, err)
	}

	// The limit is lowered while the order waits: neither path confirms it.
	limit = d(500)
	if _, err := s.PutParameters(ctx, SalesParameters{LegalEntityID: le, Parameters: params}, saved.Version, "admin"); err != nil {
		t.Fatal(err)
	}
	released, err := s.ReleaseHold(principalCtx("credit-manager"), order.ID, held.Order.Holds[0].ID, held.Order.Version, "credit-manager", day)
	if err != nil || released.Confirmed || len(released.Order.Warnings) != 1 || released.Order.Warnings[0].Code != CodeOneTimeLimit ||
		released.Order.Holds[0].Status != domain.HoldReleased {
		t.Fatalf("release over the lowered limit: confirmed=%v warnings=%+v err=%v", released.Confirmed, released.Order.Warnings, err)
	}
	if _, err := s.ConfirmOrder(ctx, order.ID, released.Order.Version, "sales-rep", day); problemCode(t, err) != CodeOneTimeLimit {
		t.Fatalf("confirm re-checks the limit: %v", err)
	}
	if n := countWhere(t, pool, `select count(*) from sales_order where id = $1 and status = 'CONFIRMED'`, order.ID); n != 0 {
		t.Fatal("the refused confirm must not confirm")
	}
}
