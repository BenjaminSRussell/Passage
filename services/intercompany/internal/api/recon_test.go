package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/api"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/store"
	"github.com/BenjaminSRussell/passage/services/intercompany/migrations"
)

type reconRunOut struct {
	ID                               string
	Matched, Timing, Mismatch, Open  int
	Blocking                         bool
	Period, Currency, FXTolerancePct string
}

type reconItemOut struct {
	ID, Ref, Status, Resolution, Note, ResolvedBy, Difference string
}

type reconDetail struct {
	reconRunOut
	Items []reconItemOut
}

func reconAPI(t *testing.T) http.Handler {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool)
	return router(api.New(st).WithReconciliation(st))
}

// TestReconRun_FlagsEveryPlantedMismatch is the stored form of AC-40.5: the
// fixture plants 50 mismatches (missing mirror, wrong amount, wrong period)
// among 100 pairs; the run must flag every one and no clean pair.
func TestReconRun_FlagsEveryPlantedMismatch(t *testing.T) {
	raw, err := os.ReadFile("testdata/ic-recon-planted-50.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Request  json.RawMessage   `json:"request"`
		Expected map[string]string `json:"expected"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	h := reconAPI(t)
	rec := postAs(h, http.MethodPost, "/v1/interco/reconciliations/run", "r1", string(fixture.Request))
	if rec.Code != http.StatusCreated {
		t.Fatalf("run = %d %s", rec.Code, rec.Body.String())
	}
	run := decode[reconRunOut](t, rec)
	if run.Mismatch != 50 || run.Timing != 10 || run.Matched != 40 || run.Open != 60 || !run.Blocking {
		t.Fatalf("run = %+v", run)
	}
	detail := decode[reconDetail](t, postAs(h, http.MethodGet, "/v1/interco/reconciliations/"+run.ID, "", ""))
	if len(detail.Items) != 100 {
		t.Fatalf("items = %d", len(detail.Items))
	}
	for _, it := range detail.Items {
		if want := fixture.Expected[it.Ref]; it.Status != want {
			t.Errorf("%s classified %s, want %s", it.Ref, it.Status, want)
		}
		if (it.Status == "Matched") != (it.Resolution == "none") {
			t.Errorf("%s: status %s with resolution %s", it.Ref, it.Status, it.Resolution)
		}
	}
	flagged := decode[reconDetail](t, postAs(h, http.MethodGet, "/v1/interco/reconciliations/"+run.ID+"?status=Mismatch", "", ""))
	if len(flagged.Items) != 50 {
		t.Fatalf("mismatch filter = %d items", len(flagged.Items))
	}

	replay := postAs(h, http.MethodPost, "/v1/interco/reconciliations/run", "r1", string(fixture.Request))
	if replay.Header().Get("Idempotent-Replayed") != "true" || decode[reconRunOut](t, replay).ID != run.ID {
		t.Fatalf("replay = %d", replay.Code)
	}
	list := decode[struct{ Items []reconRunOut }](t, postAs(h, http.MethodGet, "/v1/interco/reconciliations", "", ""))
	if len(list.Items) != 1 || list.Items[0].Open != 60 {
		t.Fatalf("list = %+v", list)
	}
}

func TestReconRun_ResolveAMismatchOnce(t *testing.T) {
	h := reconAPI(t)
	body := `{"period":"2026-11","currency":"USD","items":[
	  {"icPairId":"P-1","entityA":"A","entityB":"B","amountA":"100.00","amountB":"100.00"},
	  {"icPairId":"P-2","entityA":"A","entityB":"B","amountA":"250.00","amountB":"0.00"}]}`
	run := decode[reconRunOut](t, postAs(h, http.MethodPost, "/v1/interco/reconciliations/run", "r2", body))
	if run.Matched != 1 || run.Mismatch != 1 || run.Open != 1 {
		t.Fatalf("run = %+v", run)
	}
	detail := decode[reconDetail](t, postAs(h, http.MethodGet, "/v1/interco/reconciliations/"+run.ID, "", ""))
	var matched, mismatch reconItemOut
	for _, it := range detail.Items {
		if it.Ref == "P-1" {
			matched = it
		} else {
			mismatch = it
		}
	}
	resolve := func(id, note string) (int, string) {
		rec := postAs(h, http.MethodPost, "/v1/interco/reconciliations/items/"+id+"/resolve", "", `{"note":`+quote(note)+`}`)
		return rec.Code, rec.Body.String()
	}
	if code, body := resolve(matched.ID, "nothing to do"); code != http.StatusConflict || !strings.Contains(body, "IC-0852") {
		t.Errorf("resolving a matched item = %d %s", code, body)
	}
	if code, _ := resolve(mismatch.ID, "  "); code != http.StatusBadRequest {
		t.Errorf("empty note = %d", code)
	}
	rec := postAs(h, http.MethodPost, "/v1/interco/reconciliations/items/"+mismatch.ID+"/resolve", "", `{"note":"B booked it in December; credit note raised"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve = %d %s", rec.Code, rec.Body.String())
	}
	got := decode[reconItemOut](t, rec)
	if got.Resolution != "resolved" || got.ResolvedBy != "user-7" || got.Note == "" || got.Status != "Mismatch" || got.Difference != "250.00" {
		t.Fatalf("resolved = %+v", got)
	}
	if code, body := resolve(mismatch.ID, "again"); code != http.StatusConflict || !strings.Contains(body, "IC-0852") {
		t.Errorf("resolving twice = %d %s", code, body)
	}
	after := decode[reconRunOut](t, postAs(h, http.MethodGet, "/v1/interco/reconciliations/"+run.ID, "", ""))
	if after.Open != 0 || !after.Blocking {
		t.Errorf("after resolving: open %d blocking %v (the run still has its mismatch)", after.Open, after.Blocking)
	}
	if code, body := resolve("01890a5d-ac96-774b-bcce-b302099a8057", "x"); code != http.StatusNotFound || !strings.Contains(body, "IC-0851") {
		t.Errorf("unknown item = %d %s", code, body)
	}
}

func TestReconRun_Validation(t *testing.T) {
	h := reconAPI(t)
	for name, body := range map[string]string{
		"no items":   `{"period":"2026-11","currency":"USD","items":[]}`,
		"bad period": `{"period":"Nov","currency":"USD","items":[{"entityA":"A","entityB":"B","amountA":"1","amountB":"1"}]}`,
		"same side":  `{"period":"2026-11","currency":"USD","items":[{"entityA":"A","entityB":"A","amountA":"1","amountB":"1"}]}`,
		"tolerance":  `{"period":"2026-11","currency":"USD","fxTolerancePct":"9","items":[{"entityA":"A","entityB":"B","amountA":"1","amountB":"1"}]}`,
	} {
		if rec := postAs(h, http.MethodPost, "/v1/interco/reconciliations/run", "v-"+name, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := postAs(h, http.MethodPost, "/v1/interco/reconciliations/run", "", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("no key = %d", rec.Code)
	}
	if rec := postAs(h, http.MethodGet, "/v1/interco/reconciliations/"+"01890a5d-ac96-774b-bcce-b302099a8057", "", ""); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "IC-0850") {
		t.Errorf("unknown run = %d %s", rec.Code, rec.Body.String())
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
