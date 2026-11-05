package domain

// CatalogEntry describes one pack for the migration factory workspace (FR-49.1): the legacy
// systems it covers, the control totals FR-49.2 compares for it, and whether this release runs it.
type CatalogEntry struct {
	Pack          Pack
	Systems       []SourceSystem
	ControlTotals string
	Available     bool
}

// availablePacks are the packs the service validates, loads and reconciles today.
var availablePacks = map[Pack]bool{PackOpenItems: true, PackGLBalances: true, PackProjectWIP: true, PackOnhand: true, PackBankBal: true, PackOpenOrders: true,
	PackWIPProd: true, PackFixedAssets: true, PackPayrollYTD: true}

// PackAvailable reports whether a pack run can be started in this release.
func PackAvailable(p Pack) bool { return availablePacks[p] }

// controlTotalsByPack is the FR-49.2 comparison per pack, as shown in the workspace.
var controlTotalsByPack = map[Pack]string{
	PackOpenItems:   "count, amount per currency, amount per (entity, account, currency)",
	PackGLBalances:  "balance per (entity, account, dimensions, currency), trial balance debit = credit, non-zero combinations",
	PackOnhand:      "quantity per (item, site, warehouse, location, batch, serial), value per (item, site), value per entity",
	PackOpenOrders:  "open quantity per (order type, item, unit), open value per (order type, currency), line and order count per order type",
	PackWIPProd:     "order count, WIP value per order, WIP value per entity and currency",
	PackFixedAssets: "count per book, acquisition cost, accumulated depreciation, NBV per (book, currency); next depreciation within 0.01 (BR-49.6)",
	PackProjectWIP:  "cost, sales, WIP cost and WIP sales per (project, category)",
	PackPayrollYTD:  "gross, tax, net per (worker, tax year, currency), worker count per tax year",
	PackBankBal:     "balance per (bank account, currency), balance per entity and currency, account count",
}

// PackCatalog lists the nine packs in FR-49.1 order. Every pack covers AX 2012, D365 F&O,
// SAP ECC and S/4HANA; each entry has its own copy of the systems.
func PackCatalog() []CatalogEntry {
	out := make([]CatalogEntry, 0, len(Packs))
	for _, p := range Packs {
		out = append(out, CatalogEntry{
			Pack:          p,
			Systems:       []SourceSystem{SourceAX2012, SourceD365FO, SourceSAPECC, SourceS4HANA},
			ControlTotals: controlTotalsByPack[p],
			Available:     PackAvailable(p),
		})
	}
	return out
}
