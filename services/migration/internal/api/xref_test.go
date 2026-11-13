package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

const xrefPath = "/v1/integration/xref"

type xrefPage struct {
	Items []struct {
		SourceSystem string `json:"sourceSystem"`
		Entity       string `json:"entity"`
		SourceKey    string `json:"sourceKey"`
		TargetID     string `json:"targetId"`
	} `json:"items"`
	NextCursor string `json:"nextCursor"`
}

func seedXref(t *testing.T, s *store.Mem, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		k := domain.XrefKey{SourceSystem: "ax2012", Entity: "customer", SourceKey: fmt.Sprintf("C%03d", i)}
		if err := s.Xref().Put(context.Background(), k, domain.XrefEntry{TargetID: fmt.Sprintf("00000000-0000-7000-8000-%012d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.Xref().Put(context.Background(), domain.XrefKey{SourceSystem: "sap_ecc", Entity: "vendor", SourceKey: "V1"}, domain.XrefEntry{TargetID: "00000000-0000-7000-8000-0000000000aa"})
}

func getXref(t *testing.T, r http.Handler, query string) (int, xrefPage, string) {
	t.Helper()
	rec := do(r, http.MethodGet, xrefPath+query, "", duties, true)
	var out xrefPage
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func TestXref_PagesByCursorAndFilters(t *testing.T) {
	s := store.NewMem()
	seedXref(t, s, 5)
	r := router(s, rowsExtractor{}, domain.NewMemLoader())

	var seen []string
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		code, page, raw := getXref(t, r, "?limit=2&cursor="+cursor)
		if code != http.StatusOK {
			t.Fatalf("got %d %s", code, raw)
		}
		for _, it := range page.Items {
			seen = append(seen, it.SourceKey)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if strings.Join(seen, ",") != "C000,C001,C002,C003,C004,V1" {
		t.Fatalf("pages walked %v", seen)
	}
	if _, page, _ := getXref(t, r, "?sourceKey=C003"); len(page.Items) != 1 || page.Items[0].TargetID != "00000000-0000-7000-8000-000000000003" {
		t.Fatalf("exact key lookup = %+v", page)
	}
	if _, page, _ := getXref(t, r, "?targetId=00000000-0000-7000-8000-0000000000aa"); len(page.Items) != 1 || page.Items[0].SourceKey != "V1" {
		t.Fatalf("reverse lookup = %+v", page)
	}
	if _, page, _ := getXref(t, r, "?sourceSystem=sap_ecc&entity=vendor&keyPrefix=V"); len(page.Items) != 1 {
		t.Fatalf("prefix filter = %+v", page)
	}
	if _, page, _ := getXref(t, r, "?limit=6"); page.NextCursor != "" || len(page.Items) != 6 {
		t.Fatalf("an exactly full last page must not carry a cursor: %+v", page)
	}
}

func TestXref_RejectsBadInputAndRequiresDuty(t *testing.T) {
	r := router(store.NewMem(), rowsExtractor{}, domain.NewMemLoader())
	for _, q := range []string{"?sourceSystem=oracle", "?targetId=nope", "?cursor=!!", "?limit=0"} {
		if code, _, raw := getXref(t, r, q); code != http.StatusBadRequest || !strings.Contains(raw, "COM-0001") {
			t.Fatalf("%s: got %d %s", q, code, raw)
		}
	}
	if rec := do(r, http.MethodGet, xrefPath, "", "org.read", true); rec.Code != http.StatusForbidden {
		t.Fatalf("missing duty: %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, xrefPath, "", "", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no principal: %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, xrefPath+"/summary", "", duties, true, "X-Passage-Legal-Entity", "00000000-0000-7000-8000-000000000007"); rec.Code != http.StatusForbidden {
		t.Fatalf("legal-entity scoped caller: %d", rec.Code)
	}
}

func TestXrefSummary_CountsPerSystemAndEntity(t *testing.T) {
	s := store.NewMem()
	seedXref(t, s, 3)
	r := router(s, rowsExtractor{}, domain.NewMemLoader())
	rec := do(r, http.MethodGet, xrefPath+"/summary", "", duties, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `{"sourceSystem":"ax2012","entity":"customer","count":3}`) || !strings.Contains(rec.Body.String(), `"sap_ecc"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

// A loading service resolves legacy keys to the Passage ids this service loaded (#49, H-S8-6); keys it does not hold are left out.
func TestXrefResolve_AnswersTheHeldKeysOnly(t *testing.T) {
	s := store.NewMem()
	seedXref(t, s, 3)
	r := router(s, rowsExtractor{}, domain.NewMemLoader())
	rec := do(r, http.MethodGet, xrefPath+":resolve?sourceSystem=ax2012&entity=customer&keys=C001,NOPE,C002", "", "migration.xref.read", true)
	var out struct {
		Items []struct{ SourceKey, TargetID string } `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK || len(out.Items) != 2 ||
		out.Items[0].SourceKey != "C001" || out.Items[0].TargetID != "00000000-0000-7000-8000-000000000001" || out.Items[1].SourceKey != "C002" {
		t.Fatalf("resolve: %d %s", rec.Code, rec.Body)
	}
	for _, q := range []string{"?entity=customer&keys=C1", "?sourceSystem=ax2012&keys=C1", "?sourceSystem=ax2012&entity=customer", "?sourceSystem=ax2012&entity=customer&keys=" + strings.Repeat("k,", 501)} {
		if rec := do(r, http.MethodGet, xrefPath+":resolve"+q, "", "migration.xref.read", true); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", q, rec.Code, rec.Body)
		}
	}
	if rec := do(r, http.MethodGet, xrefPath+":resolve?sourceSystem=ax2012&entity=customer&keys=C001", "", duties, true); rec.Code != http.StatusForbidden {
		t.Fatalf("the run duty does not resolve: %d", rec.Code)
	}
}
