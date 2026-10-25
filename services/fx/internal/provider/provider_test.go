package provider_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/fx/internal/provider"
	"github.com/BenjaminSRussell/passage/services/fx/internal/store"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "providers", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type served struct {
	*httptest.Server
	requests atomic.Int32
	paths    chan string
}

func serve(t *testing.T, status int, body []byte) *served {
	t.Helper()
	s := &served{paths: make(chan string, 8)}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		s.paths <- r.URL.RequestURI()
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}

func sum(s string) [32]byte { return sha256.Sum256([]byte(s)) }

func day(s string) time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestProviderNamesAreStorePrioritySources(t *testing.T) {
	for _, p := range []provider.Provider{provider.NewECB(nil, ""), provider.NewOXR(nil, "", ""), &provider.CSV{}} {
		if _, ok := store.Priority(p.Name()); !ok {
			t.Fatalf("provider name %q has no store priority", p.Name())
		}
	}
}

func TestECBParsesRecordedDailyFile(t *testing.T) {
	srv := serve(t, http.StatusOK, fixture(t, "ecb-daily.xml"))
	points, err := provider.NewECB(srv.Client(), srv.URL).Fetch(context.Background(), "CLOSING", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ to, rate, hash string }{
		{"USD", "1.0834", `<Cube currency="USD" rate="1.0834"/>`},
		{"JPY", "162.45", `<Cube currency="JPY" rate="162.45"/>`},
		{"GBP", "0.8671", `<Cube currency="GBP" rate="0.8671"/>`},
		{"CHF", "0.9532", `<Cube currency="CHF" rate="0.9532"/>`},
	}
	if len(points) != len(want) {
		t.Fatalf("points = %d, want %d", len(points), len(want))
	}
	for i, w := range want {
		p := points[i]
		if p.From != "EUR" || p.To != w.to || p.Rate.String() != w.rate || !p.ValidFrom.Equal(day("2026-10-23")) || p.ProviderTimestamp != nil || p.PayloadHash != sum(w.hash) {
			t.Fatalf("point %d = %+v, want EUR->%s %s", i, p, w.to, w.rate)
		}
	}
}

func TestECBRejectsBadPayloads(t *testing.T) {
	cube := func(inner string) string {
		return `<Envelope xmlns="x"><Cube><Cube time="2026-10-23">` + inner + `</Cube></Cube></Envelope>`
	}
	cases := map[string]struct{ body, names string }{
		"recorded malformed file": {string(fixture(t, "ecb-malformed.xml")), "JPY"},
		"non numeric rate":        {cube(`<Cube currency="GBP" rate="abc"/>`), "GBP"},
		"zero rate":               {cube(`<Cube currency="CHF" rate="0"/>`), "CHF"},
		"lower case currency":     {cube(`<Cube currency="usd" rate="1.1"/>`), "usd"},
		"no currencies":           {cube(``), "no rates"},
		"bad date":                {strings.Replace(cube(`<Cube currency="USD" rate="1.1"/>`), "2026-10-23", "2026-13-45", 1), "2026-13-45"},
		"not xml":                 {`<Envelope`, "xml"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			srv := serve(t, http.StatusOK, []byte(c.body))
			points, err := provider.NewECB(srv.Client(), srv.URL).Fetch(context.Background(), "CLOSING", time.Time{})
			if !errors.Is(err, provider.ErrMalformed) || !strings.Contains(err.Error(), c.names) || points != nil {
				t.Fatalf("points=%v err=%v, want ErrMalformed naming %q", points, err, c.names)
			}
		})
	}
	srv := serve(t, http.StatusInternalServerError, nil)
	if _, err := provider.NewECB(srv.Client(), srv.URL).Fetch(context.Background(), "CLOSING", time.Time{}); err == nil || errors.Is(err, provider.ErrMalformed) || !strings.Contains(err.Error(), "500") {
		t.Fatalf("http 500 err = %v", err)
	}
}

func TestOXRParsesRecordedLatestFile(t *testing.T) {
	srv := serve(t, http.StatusOK, fixture(t, "oxr-latest.json"))
	oxr := provider.NewOXR(srv.Client(), srv.URL+"/", "app-id-1")
	if !oxr.Enabled() {
		t.Fatal("adapter with an app id must be enabled")
	}
	points, err := oxr.Fetch(context.Background(), "CLOSING", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got := <-srv.paths; got != "/latest.json?app_id=app-id-1" {
		t.Fatalf("request = %s", got)
	}
	want := []struct{ to, rate, hash string }{
		{"EUR", "0.923", `"EUR":0.923`},
		{"GBP", "0.8004", `"GBP":0.8004`},
		{"JPY", "149.9", `"JPY":149.9`},
	}
	if len(points) != len(want) {
		t.Fatalf("points = %d, want %d (USD base row skipped)", len(points), len(want))
	}
	stamp := time.Unix(1792756800, 0).UTC()
	for i, w := range want {
		p := points[i]
		if p.From != "USD" || p.To != w.to || p.Rate.String() != w.rate || !p.ValidFrom.Equal(day("2026-10-23")) || p.ProviderTimestamp == nil || !p.ProviderTimestamp.Equal(stamp) || p.PayloadHash != sum(w.hash) {
			t.Fatalf("point %d = %+v, want USD->%s %s", i, p, w.to, w.rate)
		}
	}
}

func TestOXRHistoricalPathForDatedFetch(t *testing.T) {
	srv := serve(t, http.StatusOK, fixture(t, "oxr-latest.json"))
	if _, err := provider.NewOXR(srv.Client(), srv.URL, "k").Fetch(context.Background(), "CLOSING", day("2026-10-20")); err != nil {
		t.Fatal(err)
	}
	if got := <-srv.paths; got != "/historical/2026-10-20.json?app_id=k" {
		t.Fatalf("request = %s", got)
	}
}

func TestOXRDisabledWithoutAppIDMakesNoRequest(t *testing.T) {
	srv := serve(t, http.StatusOK, fixture(t, "oxr-latest.json"))
	oxr := provider.NewOXR(srv.Client(), srv.URL, "")
	points, err := oxr.Fetch(context.Background(), "CLOSING", time.Time{})
	if oxr.Enabled() || !errors.Is(err, provider.ErrDisabled) || points != nil || srv.requests.Load() != 0 {
		t.Fatalf("enabled=%v err=%v points=%v requests=%d", oxr.Enabled(), err, points, srv.requests.Load())
	}
}

func TestOXRRejectsBadPayloads(t *testing.T) {
	cases := map[string]struct{ body, names string }{
		"recorded malformed file": {string(fixture(t, "oxr-malformed.json")), "EUR"},
		"missing timestamp":       {`{"base":"USD","rates":{"EUR":0.9}}`, "timestamp"},
		"string timestamp":        {`{"timestamp":"now","base":"USD","rates":{"EUR":0.9}}`, "timestamp"},
		"wrong base":              {`{"timestamp":1792756800,"base":"EUR","rates":{"GBP":0.9}}`, "base"},
		"empty rates":             {`{"timestamp":1792756800,"base":"USD","rates":{}}`, "no rates"},
		"zero rate":               {`{"timestamp":1792756800,"base":"USD","rates":{"CHF":0}}`, "CHF"},
		"null rate":               {`{"timestamp":1792756800,"base":"USD","rates":{"CHF":null}}`, "CHF"},
		"not json":                {`{`, "json"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			srv := serve(t, http.StatusOK, []byte(c.body))
			points, err := provider.NewOXR(srv.Client(), srv.URL, "k").Fetch(context.Background(), "CLOSING", time.Time{})
			if !errors.Is(err, provider.ErrMalformed) || !strings.Contains(err.Error(), c.names) || points != nil {
				t.Fatalf("points=%v err=%v, want ErrMalformed naming %q", points, err, c.names)
			}
		})
	}
	srv := serve(t, http.StatusUnauthorized, nil)
	if _, err := provider.NewOXR(srv.Client(), srv.URL, "k").Fetch(context.Background(), "CLOSING", time.Time{}); err == nil || errors.Is(err, provider.ErrMalformed) || !strings.Contains(err.Error(), "401") {
		t.Fatalf("http 401 err = %v", err)
	}
}

func TestOXRErrorsNeverCarryTheAppID(t *testing.T) {
	srv := serve(t, http.StatusOK, nil)
	client, base := srv.Client(), srv.URL
	srv.Close()
	_, err := provider.NewOXR(client, base, "super-secret-app-id").Fetch(context.Background(), "CLOSING", time.Time{})
	if err == nil || strings.Contains(err.Error(), "super-secret-app-id") {
		t.Fatalf("err = %v", err)
	}
}

func TestCSVParsesRecordedFile(t *testing.T) {
	c, err := provider.NewCSV(strings.NewReader(string(fixture(t, "rates.csv"))))
	if err != nil {
		t.Fatal(err)
	}
	points, err := c.Fetch(context.Background(), "CLOSING", time.Time{})
	if err != nil || len(points) != 2 {
		t.Fatalf("points=%v err=%v", points, err)
	}
	p := points[0]
	if p.From != "EUR" || p.To != "USD" || p.Rate.String() != "1.085" || !p.ValidFrom.Equal(day("2026-10-22")) || p.ProviderTimestamp != nil ||
		p.PayloadHash != sum("CLOSING,EUR,USD,2026-10-22,1.0850000000") {
		t.Fatalf("point = %+v", p)
	}
	if other, _ := c.Fetch(context.Background(), "AVERAGE", time.Time{}); len(other) != 1 || other[0].Rate.String() != "1.079" {
		t.Fatalf("average points = %+v", other)
	}
	if none, _ := c.Fetch(context.Background(), "MISSING", time.Time{}); len(none) != 0 {
		t.Fatalf("unknown type points = %+v", none)
	}
}

func TestCSVRejectsBadRowsNamingTheLine(t *testing.T) {
	const header = "type,from,to,valid_from,rate\n"
	cases := map[string]struct{ body, want string }{
		"recorded malformed file": {string(fixture(t, "rates-malformed.csv")), "line 3: rate has more than 10 decimals"},
		"eleven decimals":         {header + "CLOSING,EUR,USD,2026-10-22,1.12345678901\n", "line 2: rate has more than 10 decimals"},
		"zero rate":               {header + "CLOSING,EUR,USD,2026-10-22,1.1\nCLOSING,EUR,GBP,2026-10-22,0\n", "line 3: rate must be greater than zero"},
		"negative rate":           {header + "CLOSING,EUR,USD,2026-10-22,-1\n", "line 2: rate must be greater than zero"},
		"huge rate":               {header + "CLOSING,EUR,USD,2026-10-22,1e400\n", "line 2: rate is out of range"},
		"text rate":               {header + "CLOSING,EUR,USD,2026-10-22,abc\n", "line 2:"},
		"bad date":                {header + "CLOSING,EUR,USD,22/10/2026,1.1\n", "line 2: valid_from"},
		"lower case currency":     {header + "CLOSING,eur,USD,2026-10-22,1.1\n", "line 2: currencies"},
		"missing type":            {header + ",EUR,USD,2026-10-22,1.1\n", "line 2: type is required"},
		"short row":               {header + "CLOSING,EUR,USD\n", "line 2: 3 fields"},
		"unknown column":          {"type,from,to,valid_from,rate,extra\nCLOSING,EUR,USD,2026-10-22,1.1,x\n", `line 1: unknown column "extra"`},
		"missing column":          {"type,from,to,rate\nCLOSING,EUR,USD,1.1\n", `line 1: missing column "valid_from"`},
		"duplicate column":        {"type,from,from,valid_from,rate\n", `line 1: duplicate column "from"`},
		"empty file":              {"", "line 1: header"},
		"header only":             {header, "line 1: no data rows"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rows, err := provider.ParseCSV(strings.NewReader(c.body))
			if !errors.Is(err, provider.ErrMalformed) || !strings.Contains(err.Error(), c.want) || rows != nil {
				t.Fatalf("rows=%v err=%v, want ErrMalformed containing %q", rows, err, c.want)
			}
		})
	}
}

func TestCSVAcceptsAnyColumnOrderAndCRLF(t *testing.T) {
	rows, err := provider.ParseCSV(strings.NewReader("rate,valid_from,to,from,type\r\n1.1,2026-10-22,USD,EUR,CLOSING\r\n"))
	if err != nil || len(rows) != 1 || rows[0].RateType != "CLOSING" || rows[0].To != "USD" || rows[0].Rate.String() != "1.1" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}
