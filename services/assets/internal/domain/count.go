package domain

// CodeCountUnknown is the AST-0525 warning for a scanned barcode that is not
// in the register (FR-37.11).
const CodeCountUnknown = "AST-0525"

// Count outcomes of a physical inventory line.
const (
	CountFound   = "Found"
	CountMissing = "Missing"
	CountUnknown = "Unknown"
)

// CountAsset is an asset expected at a count: an Open asset with a barcode.
type CountAsset struct {
	AssetID string
	Barcode string
}

// CountLine is one reconciled barcode. AssetID is empty for an Unknown scan.
type CountLine struct {
	Barcode string
	Outcome string
	AssetID string
}

// CountResult classifies one count session against the expected register.
// Found and Missing follow the order of expected, Unknown the scan order.
// Duplicates counts the repeated scans of a barcode already seen.
type CountResult struct {
	Found      []CountLine
	Missing    []CountLine
	Unknown    []CountLine
	Duplicates int
}

// ReconcileCount classifies scanned barcodes against the expected assets
// (FR-37.11). A barcode scanned more than once counts once; a scan that
// matches no expected asset is Unknown; an expected asset whose barcode was
// not scanned is Missing.
func ReconcileCount(expected []CountAsset, scanned []string) CountResult {
	scannedSet := make(map[string]bool, len(scanned))
	known := make(map[string]bool, len(expected))
	for _, a := range expected {
		known[a.Barcode] = true
	}
	res := CountResult{Found: []CountLine{}, Missing: []CountLine{}, Unknown: []CountLine{}}
	for _, code := range scanned {
		if scannedSet[code] {
			res.Duplicates++
			continue
		}
		scannedSet[code] = true
		if !known[code] {
			res.Unknown = append(res.Unknown, CountLine{Barcode: code, Outcome: CountUnknown})
		}
	}
	for _, a := range expected {
		if scannedSet[a.Barcode] {
			res.Found = append(res.Found, CountLine{Barcode: a.Barcode, Outcome: CountFound, AssetID: a.AssetID})
		} else {
			res.Missing = append(res.Missing, CountLine{Barcode: a.Barcode, Outcome: CountMissing, AssetID: a.AssetID})
		}
	}
	return res
}
