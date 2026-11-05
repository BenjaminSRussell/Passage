package carrier_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/carrier"
)

const (
	tenantID = "0190a5f2-7b1c-7d3e-8a4f-1234567890ab"
	entityID = "0190a5f2-7b1c-7d3e-8a4f-1234567890ac"
	// vectorFile is read by services/carrier-gateway-ts/test/goVector.test.ts: a token minted by the Go signer
	// that the TypeScript verifier must accept.
	vectorFile = "../../../carrier-gateway-ts/testdata/go-minted-token.json"
)

func principalCtx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: entityID, UserID: "packer-1"})
}

type gatewayCall struct {
	method, path, key string
	duties            []string
	body              map[string]any
}

func newClient(t *testing.T, handler http.HandlerFunc) (*carrier.Client, *[]gatewayCall) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("fulfillment", priv)
	if err != nil {
		t.Fatal(err)
	}
	verifier := tenant.NewServiceVerifier("carrier-gateway", map[string]ed25519.PublicKey{"fulfillment": pub})
	var calls []gatewayCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, err := verifier.Verify(r.Header.Get(tenant.HeaderServiceToken))
		if err != nil {
			t.Errorf("token refused: %v", err)
		}
		c := gatewayCall{method: r.Method, path: r.URL.EscapedPath(), key: r.Header.Get("Idempotency-Key"), duties: claims.Duties}
		_ = json.NewDecoder(r.Body).Decode(&c.body)
		calls = append(calls, c)
		if claims.Le != entityID || claims.Tid != tenantID || claims.Act != "packer-1" {
			t.Errorf("claims %+v", claims)
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	c, err := carrier.New(srv.URL, signer, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c, &calls
}

func reply(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

var parcel = carrier.Shipment{
	ShipFrom: carrier.Address{Street1: "1 Dock Rd", City: "Columbus", PostalCode: "43004", Country: "US"},
	ShipTo:   carrier.Address{Street1: "200 Main St", City: "Austin", PostalCode: "73301", Country: "US"},
	Parcels:  []carrier.Parcel{{WeightKg: "2.5", LengthCm: "30", WidthCm: "20", HeightCm: "10"}},
}

func TestClient_SignsEachCallWithOnlyItsDuty(t *testing.T) {
	handler := reply(http.StatusOK, `{"items":[{"carrier":"sandbox","service":"GROUND","amount":"8.63","currency":"USD","transitDays":3,"billableWeightKg":"2.500"}],"failures":[]}`)
	c, calls := newClient(t, func(w http.ResponseWriter, r *http.Request) { handler(w, r) })
	rates, err := c.Rates(principalCtx(), entityID, carrier.RateQuery{Services: []string{"GROUND"}, Shipment: parcel})
	if err != nil || len(rates.Items) != 1 || rates.Items[0].Amount != "8.63" {
		t.Fatalf("rates %+v %v", rates, err)
	}
	handler = reply(http.StatusCreated, `{"carrier":"sandbox","service":"GROUND","trackingNumber":"SBX1","amount":"8.63","currency":"USD","format":"ZPL","document":"XlhB","createdAt":"2026-11-05T12:00:00.000Z","created":true}`)
	label, err := c.Label(principalCtx(), entityID, "ship-1:k", carrier.LabelOrder{Carrier: "sandbox", Service: "GROUND", Reference: "SH-1", Shipment: parcel})
	if err != nil || label.TrackingNumber != "SBX1" || !label.Created || !label.CreatedAt.Equal(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("label %+v %v", label, err)
	}
	handler = reply(http.StatusOK, `{"carrier":"sandbox","trackingNumber":"SBX 1/2","status":"IN_TRANSIT","events":[{"eventKey":"e1","status":"IN_TRANSIT","description":"Picked up","eventTime":"2026-11-05T16:00:00Z"}]}`)
	tr, err := c.Track(principalCtx(), entityID, "sandbox", "SBX 1/2")
	if err != nil || tr.Status != "IN_TRANSIT" || len(tr.Events) != 1 {
		t.Fatalf("track %+v %v", tr, err)
	}
	want := []struct{ method, path, key, duty string }{
		{"POST", "/v1/carrier/rates", "", carrier.DutyRate},
		{"POST", "/v1/carrier/labels", "ship-1:k", carrier.DutyLabel},
		{"GET", "/v1/carrier/tracking/sandbox/SBX%201%2F2", "", carrier.DutyTrack},
	}
	for i, w := range want {
		got := (*calls)[i]
		if got.method != w.method || got.path != w.path || got.key != w.key || len(got.duties) != 1 || got.duties[0] != w.duty {
			t.Errorf("call %d = %+v, want %+v", i, got, w)
		}
	}
	if b := (*calls)[1].body; b["carrier"] != "sandbox" || b["service"] != "GROUND" || b["reference"] != "SH-1" || b["shipTo"] == nil {
		t.Errorf("label body %v", b)
	}
}

func TestClient_MapsRefusalsAndOutages(t *testing.T) {
	handler := reply(http.StatusBadRequest, `{"status":400,"code":"CGW-0400","detail":"the request is not valid","errors":{"parcels[0].weightKg":"must be a decimal"}}`)
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) { handler(w, r) })
	_, err := c.Rates(principalCtx(), entityID, carrier.RateQuery{Shipment: parcel})
	var ge *carrier.Error
	if !errors.As(err, &ge) || ge.Status != 400 || ge.Errors["parcels[0].weightKg"] == "" {
		t.Fatalf("400: %v", err)
	}
	handler = reply(http.StatusBadGateway, `oops`)
	if _, err := c.Rates(principalCtx(), entityID, carrier.RateQuery{Shipment: parcel}); !errors.Is(err, carrier.ErrUnavailable) {
		t.Fatalf("502: %v", err)
	}
	handler = reply(http.StatusUnauthorized, `not json`)
	if _, err := c.Rates(principalCtx(), entityID, carrier.RateQuery{Shipment: parcel}); !errors.As(err, &ge) || ge.Code != "CGW-UNKNOWN" || ge.Status != 401 {
		t.Fatalf("401: %v", err)
	}
	if _, err := c.Rates(context.Background(), entityID, carrier.RateQuery{Shipment: parcel}); err == nil {
		t.Fatal("no principal must fail")
	}
}

func TestFromEnv(t *testing.T) {
	g, err := carrier.FromEnv(func(string) string { return "" })
	if err != nil || g != nil {
		t.Fatalf("unset: %v %v", g, err)
	}
	if _, err := carrier.FromEnv(func(k string) string {
		if k == "CARRIER_GATEWAY_URL" {
			return "http://carrier-gateway:8080"
		}
		return ""
	}); err == nil {
		t.Fatal("a URL without a signing key must fail")
	}
}

type vector struct {
	Issuer    string `json:"issuer"`
	PublicKey string `json:"publicKey"`
	NowMs     int64  `json:"nowMs"`
	Token     string `json:"token"`
}

// The checked-in token minted by the Go signer is accepted by the Go verifier at its clock; the TypeScript test
// accepts the same token. CARRIER_GATEWAY_WRITE_VECTOR=1 mints a new one.
func TestGoMintedVector(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	if os.Getenv("CARRIER_GATEWAY_WRITE_VECTOR") == "1" {
		priv := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
		signer, err := tenant.NewServiceSigner("fulfillment", priv)
		if err != nil {
			t.Fatal(err)
		}
		token, err := signer.WithClock(func() time.Time { return now }).MintDuties("carrier-gateway",
			tenant.Principal{TenantID: tenantID, LegalEntityID: entityID, UserID: "packer-1"}, entityID, []string{carrier.DutyLabel})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.MarshalIndent(vector{Issuer: "fulfillment", PublicKey: base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)), NowMs: now.UnixMilli(), Token: token}, "", "  ")
		if err := os.WriteFile(vectorFile, append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(vectorFile)
	if err != nil {
		t.Fatal(err)
	}
	var v vector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	pub, err := base64.StdEncoding.DecodeString(v.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	verifier := tenant.NewServiceVerifier("carrier-gateway", map[string]ed25519.PublicKey{v.Issuer: pub}).WithClock(func() time.Time { return time.UnixMilli(v.NowMs) })
	claims, err := verifier.Verify(v.Token)
	if err != nil || claims.Duties[0] != carrier.DutyLabel {
		t.Fatalf("Go verifier refused the vector: %v %+v", err, claims)
	}
}
