package connectors

const (
	SystemSAPECC = "sap_ecc"
	SystemSAPS4  = "sap_s4"
)

func SAP() []Entity {
	return []Entity{
		sapEntity("customer", "KNA1", "ERDAT", []string{"MANDT", "KUNNR"}, "MANDT", "KUNNR", "NAME1", "LAND1", "KTOKD"),
		sapEntity("vendor", "LFA1", "ERDAT", []string{"MANDT", "LIFNR"}, "MANDT", "LIFNR", "NAME1", "LAND1", "KTOKK"),
		sapEntity("item", "MARA", "LAEDA", []string{"MANDT", "MATNR"}, "MANDT", "MATNR", "MTART", "MATKL", "MEINS"),
		sapEntity("bomLink", "MAST", "AEDAT", []string{"MANDT", "MATNR", "WERKS", "STLAN", "STLNR", "STLAL"}, "MANDT", "MATNR", "WERKS", "STLAN", "STLNR", "STLAL"),
		sapEntity("bom", "STKO", "AEDAT", []string{"MANDT", "STLTY", "STLNR", "STLAL"}, "MANDT", "STLTY", "STLNR", "STLAL", "STLST"),
		sapEntity("route", "PLKO", "AEDAT", []string{"MANDT", "PLNTY", "PLNNR", "PLNAL"}, "MANDT", "PLNTY", "PLNNR", "PLNAL", "KTEXT"),
	}
}

func sapEntity(name, table, cursor string, key []string, columns ...string) Entity {
	return Entity{Name: name, System: SystemSAPECC, Table: table, KeyColumns: key, Columns: append(columns, cursor), CursorColumn: cursor, Unverified: true}
}
