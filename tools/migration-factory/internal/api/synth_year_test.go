package api

// The synthetic legacy year of the six packs the Python reference (testdata/synth) does not cover:
// onhand, open_orders, bank_balances, wip_prod, fixed_assets and payroll_ytd. The year is simulated
// here, deterministically, rather than vendored (a full-scale year is about 1.1M AX and 1.3M SAP
// legacy rows): goods movements, sales and purchase orders with their deliveries, bank statement
// lines, production orders with their material, labor and overhead postings, asset acquisitions
// with every monthly depreciation run, and monthly payroll. The pack extracts are the legacy state
// at the 2026 cutoff, in AX 2012 or SAP ECC shape (keys, codes and units), and every expected
// control total is summed here from that state in integer minor units, by code that never calls the
// engine. PASSAGE_SYNTH_SCALE=full runs the PRD fixture size (fx-ax-synth-year, fx-sap-synth-year);
// the default is a 1/50 year with the same shape.

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// synthPackOrder is the order the six simulated packs are loaded and checked in.
var synthPackOrder = []string{"onhand", "open_orders", "bank_balances", "wip_prod", "fixed_assets", "payroll_ytd"}

// synthPackRun is one simulated pack extract of one entity: the request body, its record count
// and the control totals the run must report (totalKey -> source value with six decimals).
type synthPackRun struct {
	pack    string
	body    string
	records int
	totals  map[string]string
}

// synthYear is a system's simulated year: per entity, the six pack extracts, and the number of
// legacy rows the simulation produced (the fixture size the PRD counts).
type synthYear struct {
	packs   map[string]map[string]*synthPackRun // company -> pack -> run
	rawRows int
	byPack  map[string]int // legacy rows per pack, all entities
	records int
}

func synthPackPath(pack string) string { return "/api/v1/migration/packs/" + pack + "/runs" }

// synthScaleFromEnv reads PASSAGE_SYNTH_SCALE: "full" is the PRD fixture size, a number in (0, 1] a
// fraction of it; the default is 0.02.
func synthScaleFromEnv(t *testing.T) float64 {
	t.Helper()
	switch v := strings.TrimSpace(os.Getenv("PASSAGE_SYNTH_SCALE")); v {
	case "":
		return 0.02
	case "full":
		return 1
	default:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f <= 0 || f > 1 {
			t.Fatalf("PASSAGE_SYNTH_SCALE=%q: want full or a fraction in (0, 1]", v)
		}
		return f
	}
}

// minorDigits is the number of decimals a currency's amounts are booked in.
func minorDigits(cur string) int {
	if cur == "JPY" {
		return 0
	}
	return 2
}

// fixed renders v units of 10^-digits as a decimal with digits decimals (none for 0).
func fixed(v int64, digits int) string {
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	if digits == 0 {
		return sign + strconv.FormatInt(v, 10)
	}
	p := int64(1)
	for range digits {
		p *= 10
	}
	return fmt.Sprintf("%s%d.%0*d", sign, v/p, digits, v%p)
}

// six renders v units of 10^-digits with the six decimals control totals are reported with.
func six(v int64, digits int) string {
	s := fixed(v, digits)
	if digits == 0 {
		return s + ".000000"
	}
	return s + strings.Repeat("0", 6-digits)
}

// expect accumulates the expected totals of one run in integer units, then renders them.
type expect struct {
	sums   map[string]int64
	digits map[string]int
}

func newExpect() *expect { return &expect{sums: map[string]int64{}, digits: map[string]int{}} }

func (e *expect) add(name string, key map[string]string, v int64, digits int) {
	k := totalKey(name, key)
	e.sums[k] += v
	e.digits[k] = digits
}

func (e *expect) render() map[string]string {
	out := make(map[string]string, len(e.sums))
	for k, v := range e.sums {
		out[k] = six(v, e.digits[k])
	}
	return out
}

// synthShape holds the codes that make an extract look like AX 2012 or SAP ECC.
type synthShape struct {
	sap bool
	rec int64 // next AX RecId
}

func (s *synthShape) recID() string { s.rec++; return strconv.FormatInt(s.rec, 10) }

func (s *synthShape) item(i int) string {
	if s.sap {
		return fmt.Sprintf("%018d", 100000+i) // MATNR, zero-padded
	}
	return fmt.Sprintf("M%05d", i)
}

func (s *synthShape) unit(i int) string {
	units := []string{"ea", "kg", "pcs", "m"}
	if s.sap {
		units = []string{"EA", "KG", "ST", "M"}
	}
	return units[i%len(units)]
}

func (s *synthShape) sites() [][2]string { // site, warehouse
	if s.sap {
		return [][2]string{{"1000", "0001"}, {"1000", "0002"}, {"1100", "0001"}}
	}
	return [][2]string{{"1", "11"}, {"1", "12"}, {"2", "21"}}
}

func entityRand(system, company, pack string) *rand.Rand {
	var h uint64 = 1469598103934665603
	for _, b := range []byte(system + "/" + company + "/" + pack) {
		h = (h ^ uint64(b)) * 1099511628211
	}
	return rand.New(rand.NewPCG(h, 49))
}

func scaled(n int, scale float64) int { return max(1, int(float64(n)*scale+0.5)) }

func synthBody(system, company string, ds synthDataset, extra map[string]any, records []map[string]any) string {
	body := map[string]any{"sourceSystem": system, "sourceCompany": company, "cutoffTs": ds.CutoffTS, "freezeTs": ds.FreezeTS, "records": records}
	for k, v := range extra {
		body[k] = v
	}
	b, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// genSynthYear simulates the six packs' year for every entity of the dataset.
func genSynthYear(ds synthDataset, scale float64) synthYear {
	y := synthYear{packs: map[string]map[string]*synthPackRun{}, byPack: map[string]int{}}
	sap := ds.System == "SAPECC"
	// At scale 1 the year is the PRD fixture size: about 1.3M SAP rows, and 1.1M AX rows (fewer
	// documents per entity).
	scale *= 0.77
	if !sap {
		scale *= 0.85
	}
	for _, e := range ds.Entities {
		sh := &synthShape{sap: sap, rec: 5637144576}
		packs := map[string]*synthPackRun{}
		var raw int
		for _, g := range []struct {
			pack string
			gen  func(*synthShape, synthDataset, synthEntity, float64) (*synthPackRun, int)
		}{
			{"onhand", synthOnhand}, {"open_orders", synthOpenOrders}, {"bank_balances", synthBank},
			{"wip_prod", synthWIP}, {"fixed_assets", synthAssets}, {"payroll_ytd", synthPayroll},
		} {
			run, rows := g.gen(sh, ds, e, scale)
			run.pack = g.pack
			packs[g.pack] = run
			raw += rows
			y.byPack[g.pack] += rows
			y.records += run.records
		}
		y.packs[e.Company] = packs
		y.rawRows += raw
	}
	return y
}

// synthOnhand simulates a year of goods receipts and issues (AX InventTrans, SAP MSEG) per item and
// inventory dimension at moving average cost, plus serial-tracked units, and extracts the stock on
// hand at the cutoff (AX InventSum with InventDim, SAP MARD/MCHB) at its legacy value.
func synthOnhand(sh *synthShape, ds synthDataset, e synthEntity, scale float64) (*synthPackRun, int) {
	r := entityRand(ds.System, e.Company, "onhand")
	digits := minorDigits(e.Currency)
	nItems := scaled(4000, scale)
	type pos struct{ qty, value int64 }
	type dimKey struct{ item, site, wh, loc, batch, serial string }
	stock := map[dimKey]*pos{}
	var order []dimKey
	items := make([]string, 0, nItems)
	units := map[string]string{}
	raw := 0
	for i := 0; i < nItems; i++ {
		item := sh.item(i)
		items = append(items, item)
		units[item] = sh.unit(i)
		cost := int64(50 + r.IntN(20000)) // per unit, in minor units
		serialTracked := i%20 == 0
		batchTracked := !serialTracked && i%3 == 0
		places := sh.sites()
		if !serialTracked {
			places = places[:1+r.IntN(len(places))]
		}
		for _, pl := range places {
			loc := ""
			if !sh.sap {
				loc = fmt.Sprintf("BULK-%03d", 1+r.IntN(40))
			}
			if serialTracked {
				var held []dimKey
				for m := 1; m <= 12; m++ {
					for range 1 + r.IntN(2) { // receipts of serial units
						k := dimKey{item, pl[0], pl[1], loc, "", fmt.Sprintf("SN%s%06d", strings.ToUpper(e.Company[:1]), len(order)+1)}
						stock[k] = &pos{1, cost + int64(r.IntN(100))}
						order = append(order, k)
						held = append(held, k)
						raw++
					}
					if len(held) > 0 && r.IntN(3) == 0 { // an issue of one unit
						j := r.IntN(len(held))
						delete(stock, held[j])
						held = append(held[:j], held[j+1:]...)
						raw++
					}
				}
				continue
			}
			batches := []string{""}
			if batchTracked {
				batches = nil
				for b := range 1 + r.IntN(3) {
					batches = append(batches, fmt.Sprintf("B26%04d%d", i%10000, b))
				}
			}
			for _, batch := range batches {
				k := dimKey{item, pl[0], pl[1], loc, batch, ""}
				p := &pos{}
				for m := 1; m <= 12; m++ {
					for range 1 + r.IntN(3) {
						q := int64(1 + r.IntN(200))
						if r.IntN(2) == 0 || p.qty == 0 { // receipt at this month's cost
							p.qty += q
							p.value += q * (cost + int64(r.IntN(int(cost/10)+1)))
						} else { // issue at the moving average
							q = min(q, p.qty)
							if q == p.qty {
								p.value, p.qty = 0, 0
							} else {
								p.value -= p.value * q / p.qty
								p.qty -= q
							}
						}
						raw++
					}
				}
				if p.qty > 0 {
					stock[k] = p
					order = append(order, k)
				}
			}
		}
	}
	ex := newExpect()
	var recs []map[string]any
	for _, k := range order {
		p, ok := stock[k]
		if !ok {
			continue
		}
		id := sh.recID()
		if sh.sap {
			id = "MARD:" + k.site + ":" + k.wh + ":" + k.item
			if k.batch != "" {
				id = "MCHB:" + k.site + ":" + k.wh + ":" + k.item + ":" + k.batch
			}
			if k.serial != "" {
				id = "EQUI:" + k.item + ":" + k.serial
			}
		}
		recs = append(recs, map[string]any{"legacyId": id, "item": k.item, "site": k.site, "warehouse": k.wh, "location": k.loc, "batch": k.batch,
			"serial": k.serial, "unit": units[k.item], "currency": e.Currency, "quantity": strconv.FormatInt(p.qty, 10), "value": fixed(p.value, digits)})
		ex.add("onhand.quantity", map[string]string{"company": e.Company, "item": k.item, "site": k.site, "warehouse": k.wh, "location": k.loc,
			"batch": k.batch, "serial": k.serial}, p.qty, 0)
		ex.add("onhand.value", map[string]string{"company": e.Company, "item": k.item, "site": k.site, "currency": e.Currency}, p.value, digits)
		ex.add("onhand.value_entity", map[string]string{"company": e.Company, "currency": e.Currency}, p.value, digits)
	}
	return &synthPackRun{body: synthBody(ds.System, e.Company, ds, map[string]any{"items": items}, recs), records: len(recs), totals: ex.render()}, raw
}

// synthOpenOrders simulates the year's sales and purchase orders (AX SalesLine/PurchLine, SAP
// VBAP/EKPO) with their deliveries and receipts, and extracts the lines still open at the cutoff.
func synthOpenOrders(sh *synthShape, ds synthDataset, e synthEntity, scale float64) (*synthPackRun, int) {
	r := entityRand(ds.System, e.Company, "open_orders")
	nItems := scaled(2000, scale)
	items := make([]string, nItems)
	for i := range items {
		items[i] = sh.item(i)
	}
	var customers, vendors []string
	for i := range scaled(1500, scale) {
		customers = append(customers, fmt.Sprintf("C%06d", i))
	}
	for i := range scaled(600, scale) {
		vendors = append(vendors, fmt.Sprintf("V%06d", i))
	}
	ex := newExpect()
	var recs []map[string]any
	raw := 0
	for _, typ := range []string{"SALES", "PURCHASE"} {
		n, parties := scaled(12000, scale), customers
		if typ == "PURCHASE" {
			n, parties = scaled(8000, scale), vendors
		}
		openOrders := map[string]bool{}
		var open int64
		for o := 1; o <= n; o++ {
			created := 1 + (o*364)/(n+1) // day of 2026 the order was entered
			number := fmt.Sprintf("SO-%s-%06d", e.Company, o)
			if typ == "PURCHASE" {
				number = fmt.Sprintf("PO-%s-%06d", e.Company, o)
			}
			if sh.sap {
				number = fmt.Sprintf("%010d", map[string]int{"SALES": 1, "PURCHASE": 45}[typ]*100000000+o)
			}
			party := parties[r.IntN(len(parties))]
			nLines := 1 + r.IntN(4)
			for l := 1; l <= nLines; l++ {
				raw++
				item := r.IntN(nItems)
				ordered := int64(1 + r.IntN(200))
				price := int64(1000 + r.IntN(5_000_000)) // in 1/10000 of the currency
				// The later in the year an order is entered, the less of it has shipped by the cutoff.
				var delivered int64
				for d := 0; d < 3 && delivered < ordered; d++ {
					if r.IntN(365) < 365-created+40 {
						delivered += min(ordered-delivered, int64(1+r.IntN(int(ordered))))
						raw++
					}
				}
				if delivered >= ordered {
					continue
				}
				open++
				openOrders[number] = true
				lineNo := strconv.Itoa(l)
				id := sh.recID()
				if sh.sap {
					lineNo = fmt.Sprintf("%06d", l*10)
					id = map[string]string{"SALES": "VBAP:", "PURCHASE": "EKPO:"}[typ] + number + ":" + lineNo
				}
				due := fmt.Sprintf("2027-%02d-%02d", 1+r.IntN(6), 1+r.IntN(28))
				recs = append(recs, map[string]any{"legacyId": id, "orderType": typ, "orderNumber": number, "lineNumber": lineNo, "item": items[item],
					"party": party, "unit": sh.unit(item), "currency": e.Currency, "orderedQty": strconv.FormatInt(ordered, 10),
					"deliveredQty": strconv.FormatInt(delivered, 10), "unitPrice": fixed(price, 4), "deliveryDate": due})
				openQty := ordered - delivered
				ex.add("open_orders.open_quantity", map[string]string{"company": e.Company, "order_type": typ, "item": items[item], "unit": sh.unit(item)}, openQty, 0)
				ex.add("open_orders.open_value", map[string]string{"company": e.Company, "order_type": typ, "currency": e.Currency}, openQty*price, 4)
			}
		}
		if open > 0 {
			ex.add("open_orders.line_count", map[string]string{"company": e.Company, "order_type": typ}, open, 0)
			ex.add("open_orders.order_count", map[string]string{"company": e.Company, "order_type": typ}, int64(len(openOrders)), 0)
		}
	}
	return &synthPackRun{body: synthBody(ds.System, e.Company, ds, map[string]any{"items": items, "customers": customers, "vendors": vendors}, recs),
		records: len(recs), totals: ex.render()}, raw
}

// synthBank simulates a year of bank statement lines (AX BankAccountTrans, SAP FEBEP) on the
// entity's house bank accounts, one of them in a second currency, and extracts the book balances.
func synthBank(sh *synthShape, ds synthDataset, e synthEntity, scale float64) (*synthPackRun, int) {
	r := entityRand(ds.System, e.Company, "bank_balances")
	type account struct{ id, cur string }
	second := "USD"
	if e.Currency == "USD" {
		second = "EUR"
	}
	var accounts []account
	for i := range 4 {
		id := fmt.Sprintf("%s-OPER-%d", e.Company, i+1)
		if sh.sap {
			id = fmt.Sprintf("%s%02d", map[int]string{0: "DEUBA", 1: "COBA", 2: "CITI", 3: "HSBC"}[i], i+1)
		}
		accounts = append(accounts, account{id, e.Currency})
	}
	accounts = append(accounts, account{e.Company + "-FX-" + second, second})
	if sh.sap {
		accounts[4].id = "FX" + second
	}
	perDay := scaled(12, scale)
	ex := newExpect()
	var recs []map[string]any
	var names []string
	raw := 0
	for _, a := range accounts {
		digits := minorDigits(a.cur)
		unit := int64(100)
		if digits == 0 {
			unit = 1
		}
		bal := int64(50_000+r.IntN(5_000_000)) * unit // opening balance at 1 January
		for range 365 {
			for range perDay {
				amt := int64(r.IntN(400_000)) * unit / 100
				if r.IntN(2) == 0 {
					amt = -amt * 9 / 10
				}
				bal += amt
				raw++
			}
		}
		names = append(names, a.id)
		recs = append(recs, map[string]any{"legacyId": sh.recID(), "bankAccount": a.id, "currency": a.cur, "balance": fixed(bal, digits)})
		ex.add("bank_balances.balance", map[string]string{"company": e.Company, "bank_account": a.id, "currency": a.cur}, bal, digits)
		ex.add("bank_balances.balance_entity", map[string]string{"company": e.Company, "currency": a.cur}, bal, digits)
	}
	if sh.sap {
		for i := range recs {
			recs[i]["legacyId"] = "T012K:" + e.Company + ":" + names[i]
		}
	}
	ex.add("bank_balances.count", map[string]string{"company": e.Company}, int64(len(recs)), 0)
	return &synthPackRun{body: synthBody(ds.System, e.Company, ds, map[string]any{"bankAccounts": names}, recs), records: len(recs), totals: ex.render()}, raw
}

// synthWIP simulates the year's production orders (AX ProdTable with ProdJournalBOM and route card
// journals, SAP AFKO/AUFK with goods issues and confirmations) and extracts the work in progress of
// the orders not yet received at the cutoff.
func synthWIP(sh *synthShape, ds synthDataset, e synthEntity, scale float64) (*synthPackRun, int) {
	r := entityRand(ds.System, e.Company, "wip_prod")
	digits := minorDigits(e.Currency)
	nItems := scaled(500, scale)
	items := make([]string, nItems)
	for i := range items {
		items[i] = sh.item(10000 + i)
	}
	n := scaled(3000, scale)
	ex := newExpect()
	var recs []map[string]any
	raw, open := 0, int64(0)
	for o := 1; o <= n; o++ {
		started := 1 + (o*364)/(n+1)
		var material, labor, overhead int64
		for range 4 + r.IntN(9) { // material issues
			material += int64(100 + r.IntN(50_000))
			raw++
		}
		for range 2 + r.IntN(5) { // route card or confirmation postings
			labor += int64(500 + r.IntN(20_000))
			raw++
		}
		overhead = labor * int64(20+r.IntN(30)) / 100 // costing sheet overhead on labor
		raw++
		if r.IntN(365) < 365-started+20 { // finished and received before the cutoff
			raw++
			continue
		}
		order := fmt.Sprintf("P%s-%06d", e.Company, o)
		id := sh.recID()
		if sh.sap {
			order = fmt.Sprintf("%012d", 1_000_000+o)
			id = "AUFK:" + order
		}
		item := items[r.IntN(nItems)]
		recs = append(recs, map[string]any{"legacyId": id, "productionOrder": order, "item": item, "currency": e.Currency,
			"material": fixed(material, digits), "labor": fixed(labor, digits), "overhead": fixed(overhead, digits)})
		open++
		wip := material + labor + overhead
		ex.add("wip_prod.wip_value", map[string]string{"company": e.Company, "production_order": order, "currency": e.Currency}, wip, digits)
		ex.add("wip_prod.wip_entity", map[string]string{"company": e.Company, "currency": e.Currency}, wip, digits)
	}
	if open > 0 {
		ex.add("wip_prod.order_count", map[string]string{"company": e.Company}, open, 0)
	}
	return &synthPackRun{body: synthBody(ds.System, e.Company, ds, map[string]any{"master": items}, recs), records: len(recs), totals: ex.render()}, raw
}

// divRound divides a non-negative a by b > 0, rounding half up.
func divRound(a, b int64) int64 { return (2*a + b) / (2 * b) }

// synthAssets simulates assets acquired from 2018 to 2026 in two depreciation books (AX AssetBook
// CURRENT straight line and TAX declining balance, SAP depreciation areas 01 and 15) with every
// monthly depreciation run through December 2026 (AX AssetTrans, SAP ANLP), and extracts each
// asset book at the cutoff with the legacy projection of the next month's depreciation.
func synthAssets(sh *synthShape, ds synthDataset, e synthEntity, scale float64) (*synthPackRun, int) {
	r := entityRand(ds.System, e.Company, "fixed_assets")
	digits := minorDigits(e.Currency)
	toHundredths := int64(1)
	if digits == 0 {
		toHundredths = 100
	}
	books := []struct{ name, method string }{{"CURRENT", "SL"}, {"TAX", "DB"}}
	if sh.sap {
		books = []struct{ name, method string }{{"01", "SL"}, {"15", "DB"}}
	}
	n := scaled(1200, scale)
	ex := newExpect()
	var recs []map[string]any
	raw := 0
	for a := 1; a <= n; a++ {
		asset := fmt.Sprintf("FA-%s-%05d", e.Company, a)
		if sh.sap {
			asset = fmt.Sprintf("%012d-0000", 1000+a) // ANLN1-ANLN2
		}
		acquired := r.IntN(107)                    // months since January 2018; 106 is November 2026
		cost := int64(1_000+r.IntN(500_000)) * 100 // whole currency units of a 2-decimal currency, or yen
		raw++                                      // the acquisition
		for _, b := range books {
			useful := []int{36, 60, 84, 120}[r.IntN(4)]
			if b.method == "DB" {
				useful = 60
			}
			salvage := int64(0)
			if b.method == "SL" {
				salvage = cost * 5 / 100
				if digits > 0 {
					salvage = salvage / 100 * 100
				}
			}
			acc, remaining := int64(0), int64(useful)
			for m := acquired + 1; m <= 107 && remaining > 0; m++ { // runs through December 2026 (month 107)
				nbv := cost - acc
				base := nbv - salvage
				var dep int64
				if base > 0 {
					if b.method == "DB" {
						dep = divRound(nbv*2, int64(useful))
						if remaining == 1 || dep > base {
							dep = base
						}
					} else {
						dep = divRound(base, remaining)
					}
				}
				acc += dep
				remaining--
				raw++
			}
			// The legacy projection of January 2027, in hundredths of the currency.
			nbvH, baseH := (cost-acc)*toHundredths, (cost-acc-salvage)*toHundredths
			var next int64
			if baseH > 0 && remaining > 0 {
				if b.method == "DB" {
					next = divRound(nbvH*2, int64(useful))
					if remaining == 1 || next > baseH {
						next = baseH
					}
				} else {
					next = divRound(baseH, remaining)
				}
			}
			id := sh.recID()
			if sh.sap {
				id = "ANLC:" + e.Company + ":" + asset + ":" + b.name + ":2026"
			}
			recs = append(recs, map[string]any{"legacyId": id, "asset": asset, "book": b.name, "currency": e.Currency, "method": b.method,
				"usefulLifeMonths": useful, "remainingLifeMonths": remaining, "acquisitionCost": fixed(cost, digits),
				"accumulatedDepreciation": fixed(acc, digits), "salvageValue": fixed(salvage, digits), "nextDepreciation": fixed(next, 2)})
			k := map[string]string{"company": e.Company, "book": b.name, "currency": e.Currency}
			ex.add("fixed_assets.count", map[string]string{"company": e.Company, "book": b.name}, 1, 0)
			ex.add("fixed_assets.acquisition_cost", k, cost, digits)
			ex.add("fixed_assets.accumulated_depreciation", k, acc, digits)
			ex.add("fixed_assets.nbv", k, cost-acc, digits)
		}
	}
	names := []string{books[0].name, books[1].name}
	return &synthPackRun{body: synthBody(ds.System, e.Company, ds, map[string]any{"master": names}, recs), records: len(recs), totals: ex.render()}, raw
}

// synthPayroll simulates the twelve monthly payroll runs of 2026 (AX PayrollStatementLine, SAP
// payroll results RT /101 gross, /401 tax, /5xx deductions, /560 net) for workers who join and
// leave during the year, and extracts each worker's year-to-date totals.
func synthPayroll(sh *synthShape, ds synthDataset, e synthEntity, scale float64) (*synthPackRun, int) {
	r := entityRand(ds.System, e.Company, "payroll_ytd")
	digits := minorDigits(e.Currency)
	unit := int64(100)
	if digits == 0 {
		unit = 1
	}
	n := scaled(400, scale)
	ex := newExpect()
	var recs []map[string]any
	var workers []string
	raw := 0
	for w := 1; w <= n; w++ {
		worker := fmt.Sprintf("%s-%06d", e.Company, w)
		if sh.sap {
			worker = fmt.Sprintf("%08d", 10000000+w) // PERNR
		}
		workers = append(workers, worker)
		from, to := 1, 12
		if r.IntN(5) == 0 {
			from = 1 + r.IntN(12)
		}
		if r.IntN(8) == 0 {
			to = from + r.IntN(13-from)
		}
		monthly := int64(2_000+r.IntN(9_000)) * unit
		if digits == 0 {
			monthly *= 150
		}
		var gross, tax, ded int64
		for m := from; m <= to; m++ {
			g := monthly + int64(r.IntN(500))*unit/10
			t := g * int64(15+r.IntN(20)) / 100
			d := g * int64(r.IntN(9)) / 100
			gross, tax, ded = gross+g, tax+t, ded+d
			raw += 4
		}
		net := gross - tax - ded
		id := sh.recID()
		if sh.sap {
			id = "PCL2:" + worker + ":2026"
		}
		recs = append(recs, map[string]any{"legacyId": id, "worker": worker, "taxYear": "2026", "currency": e.Currency,
			"gross": fixed(gross, digits), "tax": fixed(tax, digits), "deductions": fixed(ded, digits), "net": fixed(net, digits)})
		k := map[string]string{"company": e.Company, "worker": worker, "tax_year": "2026", "currency": e.Currency}
		ex.add("payroll_ytd.gross", k, gross, digits)
		ex.add("payroll_ytd.tax", k, tax, digits)
		ex.add("payroll_ytd.net", k, net, digits)
	}
	ex.add("payroll_ytd.worker_count", map[string]string{"company": e.Company, "tax_year": "2026"}, int64(n), 0)
	sort.Strings(workers)
	return &synthPackRun{body: synthBody(ds.System, e.Company, ds, map[string]any{"master": workers}, recs), records: len(recs), totals: ex.render()}, raw
}
