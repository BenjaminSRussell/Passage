package domain_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
)

const (
	pumpID  = "0190f0a0-0000-7000-8000-0000000000a1"
	valveID = "0190f0a0-0000-7000-8000-0000000000a2"
)

func movement(ref, flow, date, partner string, lines ...domain.MovementLine) domain.Movement {
	return domain.Movement{ID: "m-" + ref, Flow: flow, SourceRef: ref, MovementDate: domain.Day(date), PartnerCountry: partner, Lines: lines}
}

func mline(no int, product, qty, value string) domain.MovementLine {
	return domain.MovementLine{LineNo: no, ProductID: product, Qty: domain.D(qty), Value: domain.D(value)}
}

func TestMovementLines_TakesEUMovementsOfTheMonthAndNamesWhatLacksAMapping(t *testing.T) {
	products := map[string]domain.IntrastatProduct{
		pumpID: {ProductID: pumpID, CommodityCode: "84137030", OriginCountry: "US", NetMassPerUnit: domain.D("12.5"), SupplementaryPerUnit: domain.D("1")},
	}
	month := domain.Day("2026-03-01").Time
	movements := []domain.Movement{
		movement("SHIP-1", "dispatch", "2026-03-04", "FR", mline(1, pumpID, "4", "2000.005")),
		movement("SHIP-2", "dispatch", "2026-03-09", "DE", mline(1, pumpID, "1", "500"), mline(2, valveID, "2", "80")), // the valve has no mapping
		movement("SHIP-3", "dispatch", "2026-03-10", "US", mline(1, pumpID, "1", "500")),                               // not in the EU
		movement("SHIP-4", "dispatch", "2026-03-11", "DE", mline(1, pumpID, "1", "500")),                               // own country (home DE)
		movement("SHIP-5", "dispatch", "2026-04-01", "FR", mline(1, pumpID, "1", "500")),                               // another month
		movement("RCV-1", "arrival", "2026-03-05", "FR", mline(1, pumpID, "1", "500")),                                 // another flow
	}
	// The entity sits in Germany, so DE movements are domestic.
	lines, unmapped, complete, ignored := domain.MovementLines("dispatch", "DE", month, movements, products, domain.MovementDefaults{TransactionCode: "11", TransportMode: "3"})
	if len(lines) != 1 || lines[0].SourceRef != "SHIP-1:1" || lines[0].CommodityCode != "84137030" || lines[0].OriginCountry != "US" ||
		lines[0].NetMass.String() != "50" || lines[0].SupplementaryQty.String() != "4" || lines[0].InvoiceValue.String() != "2000.01" || lines[0].TransactionCode != "11" || lines[0].TransportMode != "3" || lines[0].DeliveryTerms != "" {
		t.Fatalf("lines = %+v", lines)
	}
	if len(complete) != 1 || complete[0] != "m-SHIP-1" {
		t.Errorf("complete = %v", complete)
	}
	if len(ignored) != 3 {
		t.Errorf("ignored = %v, want the US and the two German movements", ignored)
	}
	if len(unmapped) != 0 {
		t.Errorf("unmapped = %v", unmapped)
	}
	// With the entity in France the German movement is Intrastat business and its valve line has no mapping.
	lines, unmapped, complete, _ = domain.MovementLines("dispatch", "FR", month, movements, products, domain.MovementDefaults{})
	if len(unmapped) != 1 || unmapped[0].SourceRef != "SHIP-2" || unmapped[0].LineNo != 2 || unmapped[0].ProductID != valveID {
		t.Fatalf("unmapped = %+v", unmapped)
	}
	if len(lines) != 2 || lines[0].SourceRef != "SHIP-2:1" || len(complete) != 1 || complete[0] != "m-SHIP-4" {
		t.Errorf("lines = %+v complete = %v: the movement with an unmapped line must not be complete", lines, complete)
	}
	// A movement's own codes win over the defaults.
	own := movement("SHIP-6", "dispatch", "2026-03-12", "FR", mline(1, pumpID, "1", "10"))
	own.TransactionCode, own.DeliveryTerms = "21", "DAP"
	got, _, _, _ := domain.MovementLines("dispatch", "DE", month, []domain.Movement{own}, products, domain.MovementDefaults{TransactionCode: "11", DeliveryTerms: "EXW"})
	if got[0].TransactionCode != "21" || got[0].DeliveryTerms != "DAP" {
		t.Errorf("own codes lost: %+v", got[0])
	}
}

func TestValidateMovementAndProduct(t *testing.T) {
	ok := movement("S1", "dispatch", "2026-03-04", "FR", mline(1, pumpID, "1", "10"))
	if err := domain.ValidateMovement(ok); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*domain.Movement){
		"flow":      func(m *domain.Movement) { m.Flow = "sideways" },
		"ref":       func(m *domain.Movement) { m.SourceRef = "" },
		"date":      func(m *domain.Movement) { m.MovementDate = domain.Date{} },
		"partner":   func(m *domain.Movement) { m.PartnerCountry = "fr" },
		"no lines":  func(m *domain.Movement) { m.Lines = nil },
		"line no":   func(m *domain.Movement) { m.Lines[0].LineNo = 0 },
		"product":   func(m *domain.Movement) { m.Lines[0].ProductID = "x" },
		"quantity":  func(m *domain.Movement) { m.Lines[0].Qty = domain.D("0") },
		"value":     func(m *domain.Movement) { m.Lines[0].Value = domain.D("-1") },
		"duplicate": func(m *domain.Movement) { m.Lines = append(m.Lines, m.Lines[0]) },
	}
	for name, mutate := range bad {
		m := movement("S1", "dispatch", "2026-03-04", "FR", mline(1, pumpID, "1", "10"))
		mutate(&m)
		if err := domain.ValidateMovement(m); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	good := domain.IntrastatProduct{ProductID: pumpID, CommodityCode: "84137030", OriginCountry: "US", NetMassPerUnit: domain.D("1.5")}
	if err := domain.ValidateIntrastatProduct(good); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]domain.IntrastatProduct{
		"id":        {ProductID: "x", CommodityCode: "84137030", OriginCountry: "US"},
		"commodity": {ProductID: pumpID, CommodityCode: "8413", OriginCountry: "US"},
		"origin":    {ProductID: pumpID, CommodityCode: "84137030", OriginCountry: "us"},
		"mass":      {ProductID: pumpID, CommodityCode: "84137030", OriginCountry: "US", NetMassPerUnit: domain.D("-1")},
	} {
		if err := domain.ValidateIntrastatProduct(p); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
