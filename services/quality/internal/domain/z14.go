package domain

import (
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

// ANSI/ASQ Z1.4 single sampling reference data (FR-33.2, BR-33.1): Table I
// (sample size code letters) for every inspection level and the master Tables
// II-A (normal), II-B (tightened) and II-C (reduced), transcribed cell by cell
// with their arrows from MIL-STD-105D, whose single sampling tables Z1.4
// reproduces. testdata/z14-tables.json is an independent reconstruction the
// tests compare every cell against.

// InspectionLevels are the Z1.4 inspection levels: special S-1 to S-4 and
// general I to III.
var InspectionLevels = []string{"S-1", "S-2", "S-3", "S-4", "I", "II", "III"}

// lotBands are the upper lot sizes of the Table I rows; the last row is open.
var lotBands = []int64{8, 15, 25, 50, 90, 150, 280, 500, 1200, 3200, 10000, 35000, 150000, 500000}

// tableI is Table I: the code letter of each lot band per inspection level.
var tableI = map[string]string{
	"S-1": "AAAABBBBCCCCDDD",
	"S-2": "AAABBBCCCDDDEEE",
	"S-3": "AABBCCDDEEFFGGH",
	"S-4": "AABCCDEEFGGHJJK",
	"I":   "AABCCDEFGHJKLMN",
	"II":  "ABCDEFGHJKLMNPQ",
	"III": "BCDEFGHJKLMNPQR",
}

// AQLColumns are the AQL columns of Tables II-A, II-B and II-C, in order.
var AQLColumns = []string{"0.010", "0.015", "0.025", "0.040", "0.065", "0.10", "0.15", "0.25", "0.40", "0.65",
	"1.0", "1.5", "2.5", "4.0", "6.5", "10", "15", "25", "40", "65", "100", "150", "250", "400", "650", "1000"}

var aqlColumnValues = func() []decimal.Decimal {
	out := make([]decimal.Decimal, len(AQLColumns))
	for i, s := range AQLColumns {
		out[i] = decimal.RequireFromString(s)
	}
	return out
}()

// aqlColumn returns the column of an AQL value, compared as a number (2.50 is 2.5).
func aqlColumn(aql decimal.Decimal) (int, bool) {
	for i, v := range aqlColumnValues {
		if aql.Equal(v) {
			return i, true
		}
	}
	return 0, false
}

// masterRows are the row letters of the master tables. S (3150) appears only
// in Table II-B, where an arrow leads to it.
const masterRows = "ABCDEFGHJKLMNPQRS"

var (
	sizeNormal  = []int64{2, 3, 5, 8, 13, 20, 32, 50, 80, 125, 200, 315, 500, 800, 1250, 2000, 3150}
	sizeReduced = []int64{2, 2, 2, 3, 5, 8, 13, 20, 32, 50, 80, 125, 200, 315, 500, 800}
)

// Cell tokens: "v" use the first plan below the arrow, "^" the first plan
// above it, "ac/re" a plan, "." blank. One row per code letter, one token per
// AQL column from 0.010 to 1000.
var tableIIA = []string{
	"A: v v v v v v v v v v v v v v 0/1 v v 1/2 2/3 3/4 5/6 7/8 10/11 14/15 21/22 30/31",
	"B: v v v v v v v v v v v v v 0/1 ^ v 1/2 2/3 3/4 5/6 7/8 10/11 14/15 21/22 30/31 44/45",
	"C: v v v v v v v v v v v v 0/1 ^ v 1/2 2/3 3/4 5/6 7/8 10/11 14/15 21/22 30/31 44/45 ^",
	"D: v v v v v v v v v v v 0/1 ^ v 1/2 2/3 3/4 5/6 7/8 10/11 14/15 21/22 30/31 44/45 ^ ^",
	"E: v v v v v v v v v v 0/1 ^ v 1/2 2/3 3/4 5/6 7/8 10/11 14/15 21/22 30/31 44/45 ^ ^ ^",
	"F: v v v v v v v v v 0/1 ^ v 1/2 2/3 3/4 5/6 7/8 10/11 14/15 21/22 ^ ^ ^ ^ ^ ^",
	"G: v v v v v v v v 0/1 ^ v 1/2 2/3 3/4 5/6 7/8 10/11 14/15 21/22 ^ ^ ^ ^ ^ ^ ^",
	"H: v v v v v v v 0/1 ^ v 1/2 2/3 3/4 5/6 7/8 10/11 14/15 21/22 ^ ^ ^ ^ ^ ^ ^ ^",
	"J: v v v v v v 0/1 ^ v 1/2 2/3 3/4 5/6 7/8 10/11 14/15 21/22 ^ ^ ^ ^ ^ ^ ^ ^ ^",
	"K: v v v v v 0/1 ^ v 1/2 2/3 3/4 5/6 7/8 10/11 14/15 21/22 ^ ^ ^ ^ ^ ^ ^ ^ ^ ^",
	"L: v v v v 0/1 ^ v 1/2 2/3 3/4 5/6 7/8 10/11 14/15 21/22 ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^",
	"M: v v v 0/1 ^ v 1/2 2/3 3/4 5/6 7/8 10/11 14/15 21/22 ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^",
	"N: v v 0/1 ^ v 1/2 2/3 3/4 5/6 7/8 10/11 14/15 21/22 ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^",
	"P: v 0/1 ^ v 1/2 2/3 3/4 5/6 7/8 10/11 14/15 21/22 ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^",
	"Q: 0/1 ^ v 1/2 2/3 3/4 5/6 7/8 10/11 14/15 21/22 ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^",
	"R: ^ ^ 1/2 2/3 3/4 5/6 7/8 10/11 14/15 21/22 ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^",
}

var tableIIB = []string{
	"A: v v v v v v v v v v v v v v v v v v 1/2 2/3 3/4 5/6 8/9 12/13 18/19 27/28",
	"B: v v v v v v v v v v v v v v 0/1 v v 1/2 2/3 3/4 5/6 8/9 12/13 18/19 27/28 41/42",
	"C: v v v v v v v v v v v v v 0/1 v v 1/2 2/3 3/4 5/6 8/9 12/13 18/19 27/28 41/42 ^",
	"D: v v v v v v v v v v v v 0/1 v v 1/2 2/3 3/4 5/6 8/9 12/13 18/19 27/28 41/42 ^ ^",
	"E: v v v v v v v v v v v 0/1 v v 1/2 2/3 3/4 5/6 8/9 12/13 18/19 27/28 41/42 ^ ^ ^",
	"F: v v v v v v v v v v 0/1 v v 1/2 2/3 3/4 5/6 8/9 12/13 18/19 ^ ^ ^ ^ ^ ^",
	"G: v v v v v v v v v 0/1 v v 1/2 2/3 3/4 5/6 8/9 12/13 18/19 ^ ^ ^ ^ ^ ^ ^",
	"H: v v v v v v v v 0/1 v v 1/2 2/3 3/4 5/6 8/9 12/13 18/19 ^ ^ ^ ^ ^ ^ ^ ^",
	"J: v v v v v v v 0/1 v v 1/2 2/3 3/4 5/6 8/9 12/13 18/19 ^ ^ ^ ^ ^ ^ ^ ^ ^",
	"K: v v v v v v 0/1 v v 1/2 2/3 3/4 5/6 8/9 12/13 18/19 ^ ^ ^ ^ ^ ^ ^ ^ ^ ^",
	"L: v v v v v 0/1 v v 1/2 2/3 3/4 5/6 8/9 12/13 18/19 ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^",
	"M: v v v v 0/1 v v 1/2 2/3 3/4 5/6 8/9 12/13 18/19 ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^",
	"N: v v v 0/1 v v 1/2 2/3 3/4 5/6 8/9 12/13 18/19 ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^",
	"P: v v 0/1 v v 1/2 2/3 3/4 5/6 8/9 12/13 18/19 ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^",
	"Q: v 0/1 v v 1/2 2/3 3/4 5/6 8/9 12/13 18/19 ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^",
	"R: 0/1 ^ v 1/2 2/3 3/4 5/6 8/9 12/13 18/19 ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^",
	"S: . . 1/2 . . . . . . . . . . . . . . . . . . . . . . .",
}

var tableIIC = []string{
	"A: v v v v v v v v v v v v v v 0/1 v v 1/2 2/3 3/4 5/6 7/8 10/11 14/15 21/22 30/31",
	"B: v v v v v v v v v v v v v 0/1 ^ v 0/2 1/3 2/4 3/5 5/6 7/8 10/11 14/15 21/22 30/31",
	"C: v v v v v v v v v v v v 0/1 ^ v 0/2 1/3 1/4 2/5 3/6 5/8 7/10 10/13 14/17 21/24 ^",
	"D: v v v v v v v v v v v 0/1 ^ v 0/2 1/3 1/4 2/5 3/6 5/8 7/10 10/13 14/17 21/24 ^ ^",
	"E: v v v v v v v v v v 0/1 ^ v 0/2 1/3 1/4 2/5 3/6 5/8 7/10 10/13 14/17 21/24 ^ ^ ^",
	"F: v v v v v v v v v 0/1 ^ v 0/2 1/3 1/4 2/5 3/6 5/8 7/10 10/13 ^ ^ ^ ^ ^ ^",
	"G: v v v v v v v v 0/1 ^ v 0/2 1/3 1/4 2/5 3/6 5/8 7/10 10/13 ^ ^ ^ ^ ^ ^ ^",
	"H: v v v v v v v 0/1 ^ v 0/2 1/3 1/4 2/5 3/6 5/8 7/10 10/13 ^ ^ ^ ^ ^ ^ ^ ^",
	"J: v v v v v v 0/1 ^ v 0/2 1/3 1/4 2/5 3/6 5/8 7/10 10/13 ^ ^ ^ ^ ^ ^ ^ ^ ^",
	"K: v v v v v 0/1 ^ v 0/2 1/3 1/4 2/5 3/6 5/8 7/10 10/13 ^ ^ ^ ^ ^ ^ ^ ^ ^ ^",
	"L: v v v v 0/1 ^ v 0/2 1/3 1/4 2/5 3/6 5/8 7/10 10/13 ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^",
	"M: v v v 0/1 ^ v 0/2 1/3 1/4 2/5 3/6 5/8 7/10 10/13 ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^",
	"N: v v 0/1 ^ v 0/2 1/3 1/4 2/5 3/6 5/8 7/10 10/13 ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^",
	"P: v 0/1 ^ v 0/2 1/3 1/4 2/5 3/6 5/8 7/10 10/13 ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^",
	"Q: 0/1 ^ v 0/2 1/3 1/4 2/5 3/6 5/8 7/10 10/13 ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^",
	"R: ^ ^ 0/2 1/3 1/4 2/5 3/6 5/8 7/10 10/13 ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^ ^",
}

// z14Cell is one master table cell: a plan, an arrow or a blank.
type z14Cell struct {
	arrow  int // +1 down, -1 up, 0 a plan (or blank when blank is set)
	blank  bool
	ac, re int
}

type masterTable struct {
	rows  [][]z14Cell
	sizes []int64
}

var masterTables = map[Severity]masterTable{
	SeverityNormal:    {parseMaster(tableIIA), sizeNormal},
	SeverityTightened: {parseMaster(tableIIB), sizeNormal},
	SeverityReduced:   {parseMaster(tableIIC), sizeReduced},
}

// parseMaster reads a transcribed table; a malformed row is a programming
// error and stops the service at start.
func parseMaster(lines []string) [][]z14Cell {
	rows := make([][]z14Cell, len(lines))
	for i, line := range lines {
		letter, body, ok := strings.Cut(line, ": ")
		if !ok || letter != string(masterRows[i]) {
			panic("z14: row " + strconv.Itoa(i) + " is not " + string(masterRows[i]))
		}
		tokens := strings.Fields(body)
		if len(tokens) != len(AQLColumns) {
			panic("z14: row " + letter + " has " + strconv.Itoa(len(tokens)) + " cells")
		}
		rows[i] = make([]z14Cell, len(tokens))
		for j, tok := range tokens {
			rows[i][j] = parseCell(letter, tok)
		}
	}
	return rows
}

func parseCell(letter, tok string) z14Cell {
	switch tok {
	case "v":
		return z14Cell{arrow: 1}
	case "^":
		return z14Cell{arrow: -1}
	case ".":
		return z14Cell{blank: true}
	}
	a, r, ok := strings.Cut(tok, "/")
	ac, err1 := strconv.Atoi(a)
	re, err2 := strconv.Atoi(r)
	if !ok || err1 != nil || err2 != nil || ac >= re {
		panic("z14: row " + letter + " has a bad cell " + tok)
	}
	return z14Cell{ac: ac, re: re}
}

// Z14Plan is the single sampling plan a code letter and AQL resolve to.
type Z14Plan struct {
	SampleLetter string // the row whose plan applies, after following arrows
	SampleSize   int64
	AcceptNo     int
	RejectNo     int
}

// Z14CodeLetter returns the Table I code letter of a lot at an inspection level.
func Z14CodeLetter(level string, lot int64) (string, bool) {
	letters, ok := tableI[level]
	if !ok || lot < 2 {
		return "", false
	}
	for i, upTo := range lotBands {
		if lot <= upTo {
			return string(letters[i]), true
		}
	}
	return string(letters[len(lotBands)]), true
}

// Z14Resolve follows the arrows of the master table of sev from the cell of a
// code letter and AQL column to its plan ("use the first sampling plan below /
// above the arrow").
func Z14Resolve(sev Severity, letter string, aql decimal.Decimal) (Z14Plan, bool) {
	t, ok := masterTables[sev]
	col, okCol := aqlColumn(aql)
	row := strings.Index(masterRows, letter)
	if !ok || !okCol || row < 0 || row >= len(t.rows) || len(letter) != 1 {
		return Z14Plan{}, false
	}
	step := t.rows[row][col].arrow
	for i := row; i >= 0 && i < len(t.rows); i += step {
		c := t.rows[i][col]
		switch {
		case c.blank:
			return Z14Plan{}, false
		case c.arrow == 0:
			return Z14Plan{SampleLetter: string(masterRows[i]), SampleSize: t.sizes[i], AcceptNo: c.ac, RejectNo: c.re}, true
		case c.arrow != step:
			return Z14Plan{}, false
		}
	}
	return Z14Plan{}, false
}
