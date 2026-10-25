package connectors

const (
	SystemAX2012 = "ax2012"
	SystemD365FO = "d365fo"
	axCursor     = "ModifiedDateTime"
)

func AX() []Entity {
	return []Entity{
		axEntity("party", "DirParty", []string{"PartyNumber"}, "PartyNumber", "Name", "RecId"),
		axEntity("customer", "CustTable", []string{"DataAreaId", "AccountNum"}, "DataAreaId", "AccountNum", "Party", "CustGroup", "Currency", "CreditMax", "Blocked"),
		axEntity("vendor", "VendTable", []string{"DataAreaId", "AccountNum"}, "DataAreaId", "AccountNum", "Party", "VendGroup", "Currency", "Blocked"),
		axEntity("item", "InventTable", []string{"DataAreaId", "ItemId"}, "DataAreaId", "ItemId", "ItemType", "Product", "NetWeight"),
		axEntity("product", "EcoResProduct", []string{"DisplayProductNumber"}, "DisplayProductNumber", "ProductType", "RecId"),
		axEntity("bom", "BOM", []string{"DataAreaId", "BOMId"}, "DataAreaId", "BOMId", "Name", "Approved"),
		axEntity("route", "Route", []string{"DataAreaId", "RouteId"}, "DataAreaId", "RouteId", "Name", "Approved"),
	}
}

func axEntity(name, table string, key []string, columns ...string) Entity {
	return Entity{Name: name, System: SystemAX2012, Table: table, KeyColumns: key, Columns: append(columns, axCursor), CursorColumn: axCursor, Unverified: true}
}
