package domain

import "fmt"

// MaxMasterImport bounds one bulk import of master data (the CN8 table is about ten thousand rows; load it in batches).
const MaxMasterImport = 5000

type seedRow struct{ code, description string }

// The standard lists below are reference data seeded into a tenant's master data on request. They are a starting point, not a
// substitute for the national statistics office's current tables: the commodity list is a small starter set of CN8 codes, and the
// full Combined Nomenclature is loaded through the bulk import. A reviewer confirms the lists against the current regulation (#512).
var (
	seedTransactionCodes = []seedRow{
		{"1", "Transactions involving transfer of ownership against compensation"},
		{"11", "Outright purchase or sale"},
		{"12", "Supply for sale on approval, for consignment or through a commission agent"},
		{"13", "Barter trade"},
		{"14", "Personal purchases by travellers"},
		{"15", "Financial leasing"},
		{"2", "Return and replacement of goods"},
		{"21", "Return of goods"},
		{"22", "Replacement for returned goods"},
		{"23", "Replacement for goods not returned"},
		{"3", "Transfer of ownership without financial compensation"},
		{"4", "Goods for processing under contract"},
		{"41", "Goods for processing, expected to return to the initial member state"},
		{"42", "Goods for processing, not expected to return to the initial member state"},
		{"5", "Goods after processing under contract"},
		{"51", "Goods returning to the initial member state after processing"},
		{"52", "Goods not returning to the initial member state after processing"},
		{"7", "Operations under joint defence projects"},
		{"8", "Supply of building materials and technical equipment under a construction contract"},
		{"9", "Other transactions"},
		{"91", "Hire, loan and operational leasing of more than 24 months"},
		{"99", "Other"},
	}
	seedTransportModes = []seedRow{
		{"1", "Sea transport"}, {"2", "Rail transport"}, {"3", "Road transport"}, {"4", "Air transport"}, {"5", "Postal consignment"},
		{"7", "Fixed transport installations"}, {"8", "Inland waterway transport"}, {"9", "Own propulsion"},
	}
	seedDeliveryTerms = []seedRow{
		{"EXW", "Ex Works"}, {"FCA", "Free Carrier"}, {"CPT", "Carriage Paid To"}, {"CIP", "Carriage and Insurance Paid To"},
		{"DAP", "Delivered at Place"}, {"DPU", "Delivered at Place Unloaded"}, {"DDP", "Delivered Duty Paid"},
		{"FAS", "Free Alongside Ship"}, {"FOB", "Free on Board"}, {"CFR", "Cost and Freight"}, {"CIF", "Cost, Insurance and Freight"},
		{"DAT", "Delivered at Terminal (Incoterms 2010)"},
	}
	// The intrastat code is the ISO code except for Greece (EL on the VAT register, GR for Intrastat) and Northern Ireland (XI).
	seedEUCountries = []seedRow{
		{"AT", "Austria"}, {"BE", "Belgium"}, {"BG", "Bulgaria"}, {"HR", "Croatia"}, {"CY", "Cyprus"}, {"CZ", "Czechia"},
		{"DK", "Denmark"}, {"EE", "Estonia"}, {"FI", "Finland"}, {"FR", "France"}, {"DE", "Germany"}, {"GR", "Greece"},
		{"HU", "Hungary"}, {"IE", "Ireland"}, {"IT", "Italy"}, {"LV", "Latvia"}, {"LT", "Lithuania"}, {"LU", "Luxembourg"},
		{"MT", "Malta"}, {"NL", "Netherlands"}, {"PL", "Poland"}, {"PT", "Portugal"}, {"RO", "Romania"}, {"SK", "Slovakia"},
		{"SI", "Slovenia"}, {"ES", "Spain"}, {"SE", "Sweden"},
	}
	seedOtherCountries = []seedRow{
		{"CA", "Canada"}, {"CH", "Switzerland"}, {"CN", "China"}, {"GB", "United Kingdom"}, {"IN", "India"}, {"JP", "Japan"},
		{"MX", "Mexico"}, {"NO", "Norway"}, {"TR", "Turkey"}, {"US", "United States"},
	}
	seedPorts = []struct{ code, description, country string }{
		{"DEHAM", "Hamburg", "DE"}, {"DEBRV", "Bremerhaven", "DE"}, {"NLRTM", "Rotterdam", "NL"}, {"BEANR", "Antwerp", "BE"},
		{"FRLEH", "Le Havre", "FR"}, {"ESVLC", "Valencia", "ES"}, {"ITGOA", "Genoa", "IT"}, {"PLGDN", "Gdansk", "PL"},
		{"SEGOT", "Gothenburg", "SE"}, {"GRPIR", "Piraeus", "GR"}, {"DEFRA", "Frankfurt airport", "DE"}, {"NLAMS", "Amsterdam", "NL"},
	}
	// A starter set of CN8 codes (the supplementary unit is p/st for pieces, kg for weight only).
	seedCommodities = []struct{ code, description, unit string }{
		{"84131100", "Pumps for dispensing fuel or lubricants, of the type used in filling stations", "p/st"},
		{"84818099", "Taps, cocks, valves and similar appliances, other", "p/st"},
		{"84811099", "Pressure-reducing valves", "p/st"},
		{"84713000", "Portable automatic data-processing machines, not over 10 kg", "p/st"},
		{"85171300", "Smartphones", "p/st"},
		{"39239000", "Articles for the conveyance or packing of goods, of plastics, other", ""},
		{"73181595", "Screws and bolts of iron or steel, other", ""},
	}
)

// SeedEntries returns the standard master data of the given kinds (all kinds when none are named). Every entry is valid master data.
func SeedEntries(kinds []string) ([]MasterEntry, error) {
	if len(kinds) == 0 {
		kinds = MasterKinds
	}
	var out []MasterEntry
	seen := map[string]bool{}
	for _, kind := range kinds {
		if !ValidMasterKind(kind) {
			return nil, invalid("kinds", "unknown master data kind %q", kind)
		}
		if seen[kind] {
			continue
		}
		seen[kind] = true
		add := func(rows []seedRow) {
			for _, r := range rows {
				out = append(out, MasterEntry{Kind: kind, Code: r.code, Description: r.description, Attrs: map[string]string{}})
			}
		}
		switch kind {
		case MasterTransactionCode:
			add(seedTransactionCodes)
		case MasterTransportMode:
			add(seedTransportModes)
		case MasterDeliveryTerms:
			add(seedDeliveryTerms)
		case MasterCountry:
			for _, r := range seedEUCountries {
				out = append(out, MasterEntry{Kind: kind, Code: r.code, Description: r.description, Attrs: map[string]string{"eu_member": "true", "intrastat_code": r.code}})
			}
			for _, r := range seedOtherCountries {
				out = append(out, MasterEntry{Kind: kind, Code: r.code, Description: r.description, Attrs: map[string]string{"eu_member": "false", "intrastat_code": r.code}})
			}
		case MasterPort:
			for _, p := range seedPorts {
				out = append(out, MasterEntry{Kind: kind, Code: p.code, Description: p.description, Attrs: map[string]string{"country": p.country}})
			}
		case MasterCommodity:
			for _, c := range seedCommodities {
				attrs := map[string]string{}
				if c.unit != "" {
					attrs["supplementary_unit"] = c.unit
				}
				out = append(out, MasterEntry{Kind: kind, Code: c.code, Description: c.description, Attrs: attrs})
			}
		}
	}
	for _, e := range out {
		if err := ValidateMasterEntry(e); err != nil {
			return nil, fmt.Errorf("seed %s %s: %w", e.Kind, e.Code, err)
		}
	}
	return out, nil
}

// ValidateMasterImport checks a bulk import: at most MaxMasterImport entries, each valid and none repeated.
func ValidateMasterImport(entries []MasterEntry) error {
	if len(entries) == 0 || len(entries) > MaxMasterImport {
		return invalid("entries", "entries must hold between 1 and %d rows", MaxMasterImport)
	}
	seen := map[string]bool{}
	for i, e := range entries {
		if err := ValidateMasterEntry(e); err != nil {
			de := err.(*Error)
			return invalid(fmt.Sprintf("entries[%d].%s", i, de.Field), "%s", de.Detail)
		}
		key := e.Kind + "|" + e.Code
		if seen[key] {
			return invalid(fmt.Sprintf("entries[%d].code", i), "%s %s appears twice", e.Kind, e.Code)
		}
		seen[key] = true
	}
	return nil
}
