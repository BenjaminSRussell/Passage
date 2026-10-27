package domain

// Codes of the standard definitions shipped with the service (FR-15.5, FR-15.14).
const (
	CodeTBStd  = "TB-STD"
	CodeBSStd  = "BS-STD"
	CodePLStd  = "PL-STD"
	CodePLFunc = "PL-FUNC"
	CodeCFStd  = "CF-STD"
)

// Cash flow tags (BR-15.5, PRD §2.15.4 row.cash_flow_tag).
const (
	tagOperating  = "operating_adjustment"
	tagWorkingCap = "working_capital"
	tagInvesting  = "investing"
	tagFinancing  = "financing"
	tagCash       = "cash"
)

// PLFuncCheckRow is the check row of PL-FUNC: expense lines without FUNC_AREA.
const PLFuncCheckRow = "CHK_FA"

// Default chart ranges used by the standard definitions. ASSUMPTION: they stand
// in for MainAccountCategory from the chart of accounts (wave 11) until that
// feed exists; the ranges follow the first digit of the account number.
const (
	rangeAssetsFrom    = "100000"
	rangeAssetsTo      = "199999"
	rangeLiabFrom      = "200000"
	rangeLiabTo        = "299999"
	rangeEquityFrom    = "300000"
	rangeEquityTo      = "399999"
	rangeRevenueFrom   = "400000"
	rangeRevenueTo     = "499999"
	rangeExpenseFrom   = "500000"
	rangeExpenseTo     = "699999"
	funcAreaDimension  = "FUNC_AREA"
	funcAreaCostOfSale = "COS"
)

// StandardDefinitions returns the designs of TB-STD, BS-STD, PL-STD, PL-FUNC
// and CF-STD. They are unpublished templates; the install action publishes them
// as version 1. Columns leave the currency type empty, so every run chooses it
// (FR-15.5: standard statements are parameterized by ledger, period and
// currency type).
func StandardDefinitions() []Definition {
	return []Definition{trialBalanceStd(), balanceSheetStd(), incomeStatementStd(), incomeStatementByFunction(), cashFlowStd()}
}

// cashFlowStd is the indirect cash flow of FR-15.5 and BR-15.5 for the run
// period and the year to date. Cash flow statements are not cumulative, so each
// account row shows the movement of its accounts, and closing entries are left
// out. Balance-sheet rows are credit-normal, so they show minus the movement:
// the cash effect (an increase in an asset uses cash, an increase in a
// liability provides it). Cash is debit-normal and shows the change in cash.
// The ranges together cover every balance-sheet account of the default chart
// once; CheckCashFlow enforces the reconciliation on every run and CF_CHK shows
// it (must be 0.00).
func cashFlowStd() Definition {
	tagged := func(r Row, tag string) Row { r.CashFlowTag = tag; return r }
	total := func(code, label string, refs ...string) Row {
		return Row{Code: code, Kind: RowTotal, Label: label, NormalBalance: "credit", References: refs}
	}
	return Definition{Code: CodeCFStd, Name: "Cash flow statement (indirect)", Kind: KindCashFlow,
		Rows: []Row{
			account("REV", "Revenue", rangeRevenueFrom, rangeRevenueTo, "credit"),
			account("EXP", "Expenses", rangeExpenseFrom, rangeExpenseTo, "debit"),
			{Code: "NI", Kind: RowCalculation, Label: "Net income", Expression: "REV-EXP", NormalBalance: "credit"},
			tagged(account("ADJ_DEP", "Accumulated depreciation and amortisation", "155000", "159999", "credit"), tagOperating),
			tagged(account("ADJ_PROV", "Provisions", "230000", "239999", "credit"), tagOperating),
			total("CF_ADJ", "Adjustments for non-cash items", "ADJ_DEP", "ADJ_PROV"),
			tagged(account("WC_REC", "Receivables and inventories", "110000", "149999", "credit"), tagWorkingCap),
			tagged(account("WC_PAY", "Trade payables", "200000", "219999", "credit"), tagWorkingCap),
			tagged(account("WC_ACC", "Accruals and other liabilities", "240000", "299999", "credit"), tagWorkingCap),
			total("CF_WC", "Working capital changes", "WC_REC", "WC_PAY", "WC_ACC"),
			{Code: "CF_OP", Kind: RowCalculation, Label: "Cash flow from operating activities", Expression: "NI+CF_ADJ+CF_WC", NormalBalance: "credit"},
			tagged(account("INV_PPE", "Property, plant and equipment", "150000", "154999", "credit"), tagInvesting),
			tagged(account("INV_OTH", "Other non-current assets", "160000", "199999", "credit"), tagInvesting),
			total("CF_INV", "Cash flow from investing activities", "INV_PPE", "INV_OTH"),
			tagged(account("FIN_DEBT", "Borrowings", "220000", "229999", "credit"), tagFinancing),
			tagged(account("FIN_EQ", "Equity", rangeEquityFrom, rangeEquityTo, "credit"), tagFinancing),
			total("CF_FIN", "Cash flow from financing activities", "FIN_DEBT", "FIN_EQ"),
			{Code: "CF_NET", Kind: RowCalculation, Label: "Net change in cash", Expression: "CF_OP+CF_INV+CF_FIN", NormalBalance: "credit"},
			tagged(account("CASH", "Change in cash and cash equivalents", "100000", "109999", "debit"), tagCash),
			{Code: "CF_CHK", Kind: RowCalculation, Label: "Check: net change in cash less change in cash (must be 0.00)",
				Expression: "CF_NET-CASH", NormalBalance: "debit"},
		},
		Columns: []Column{
			{Code: "P", Label: "Period", PeriodExpr: "P"},
			{Code: "YTD", Label: "Year to date", PeriodExpr: "YTD"},
		},
	}
}

func account(code, label, from, to, normal string) Row {
	return Row{Code: code, Kind: RowAccountRange, Label: label, AccountFrom: from, AccountTo: to, NormalBalance: normal}
}

func periodColumns() []Column {
	return []Column{
		{Code: "P", Label: "Period", PeriodExpr: "P"},
		{Code: "YTD", Label: "Year to date", PeriodExpr: "YTD"},
		{Code: "PY", Label: "Prior year", PeriodExpr: "PY"},
	}
}

// trialBalanceStd: opening (cumulative through P-1), closing (cumulative through
// P) and movement, per account class in raw net (debit minus credit). The total
// row is zero for a balanced ledger.
func trialBalanceStd() Definition {
	return Definition{Code: CodeTBStd, Name: "Trial balance", Kind: KindTrialBalance,
		Rows: []Row{
			account("ASSETS", "Assets", rangeAssetsFrom, rangeAssetsTo, "debit"),
			account("LIAB", "Liabilities", rangeLiabFrom, rangeLiabTo, "debit"),
			account("EQUITY", "Equity", rangeEquityFrom, rangeEquityTo, "debit"),
			account("REVENUE", "Revenue", rangeRevenueFrom, rangeRevenueTo, "debit"),
			account("EXPENSE", "Expenses", rangeExpenseFrom, rangeExpenseTo, "debit"),
			{Code: "TOTAL", Kind: RowTotal, Label: "Total (must be 0.00)", NormalBalance: "debit",
				References: []string{"ASSETS", "LIAB", "EQUITY", "REVENUE", "EXPENSE"}},
		},
		Columns: []Column{
			{Code: "OPEN", Label: "Opening", PeriodExpr: "P-1"},
			{Code: "CLOSE", Label: "Closing", PeriodExpr: "P"},
			{Code: "MOVE", Label: "Movement", PeriodExpr: "CALC", Expression: "CLOSE-OPEN"},
		},
	}
}

// balanceSheetStd: balances are cumulative (BR-15.2). The current period result
// is the P&L accounts' net; the check row must be zero (AC-15.1).
func balanceSheetStd() Definition {
	return Definition{Code: CodeBSStd, Name: "Balance sheet", Kind: KindBalanceSheet,
		Rows: []Row{
			account("ASSETS", "Total assets", rangeAssetsFrom, rangeAssetsTo, "debit"),
			account("LIAB", "Liabilities", rangeLiabFrom, rangeLiabTo, "credit"),
			account("EQUITY", "Equity", rangeEquityFrom, rangeEquityTo, "credit"),
			account("RESULT", "Current period result", rangeRevenueFrom, rangeExpenseTo, "credit"),
			{Code: "LE", Kind: RowTotal, Label: "Liabilities and equity", NormalBalance: "credit",
				References: []string{"LIAB", "EQUITY", "RESULT"}},
			{Code: "CHK", Kind: RowCalculation, Label: "Check: assets less liabilities and equity (must be 0.00)",
				Expression: "ASSETS-LE", NormalBalance: "debit"},
		},
		Columns: []Column{
			{Code: "CUR", Label: "Current", PeriodExpr: "P"},
			{Code: "PY", Label: "Prior year", PeriodExpr: "PY"},
			{Code: "VAR", Label: "Variance", PeriodExpr: "CALC", Expression: "CUR-PY"},
		},
	}
}

func incomeStatementStd() Definition {
	return Definition{Code: CodePLStd, Name: "Income statement", Kind: KindIncomeStatement,
		Rows: []Row{
			account("REV", "Revenue", rangeRevenueFrom, rangeRevenueTo, "credit"),
			account("EXP", "Expenses", rangeExpenseFrom, rangeExpenseTo, "debit"),
			{Code: "RES", Kind: RowCalculation, Label: "Net result", Expression: "REV-EXP", NormalBalance: "credit"},
		},
		Columns: periodColumns(),
	}
}

// incomeStatementByFunction (FR-15.14) replaces the expense account ranges by
// FUNC_AREA dimension rows. Each functional row is restricted to expense accounts
// so balance sheet lines with the same attribute cannot leak in. The check row
// lists expense lines without FUNC_AREA and must be zero.
func incomeStatementByFunction() Definition {
	fn := func(code, label, area string) Row {
		r := account(code, label, rangeExpenseFrom, rangeExpenseTo, "debit")
		r.DimensionFilter = map[string][]string{funcAreaDimension: {area}}
		return r
	}
	return Definition{Code: CodePLFunc, Name: "Income statement by function of expense", Kind: KindIncomeStatement,
		Rows: []Row{
			account("REV", "Revenue", rangeRevenueFrom, rangeRevenueTo, "credit"),
			fn("COS", "Cost of sales", funcAreaCostOfSale),
			fn("DIST", "Distribution costs", "DIST"),
			fn("ADMIN", "Administrative expenses", "ADMIN"),
			fn("OTHX", "Other operating expenses", "OTHER"),
			fn("FINX", "Financial expenses", "FIN"),
			{Code: "EXPF", Kind: RowTotal, Label: "Total expenses by function", NormalBalance: "debit",
				References: []string{"COS", "DIST", "ADMIN", "OTHX", "FINX"}},
			{Code: "RES", Kind: RowCalculation, Label: "Net result", Expression: "REV-EXPF", NormalBalance: "credit"},
			{Code: PLFuncCheckRow, Kind: RowAccountRange, Label: "Check: expense lines without FUNC_AREA (must be 0.00)",
				AccountFrom: rangeExpenseFrom, AccountTo: rangeExpenseTo, NormalBalance: "debit",
				DimensionFilter: map[string][]string{funcAreaDimension: {""}}},
		},
		Columns: periodColumns(),
	}
}

// PLFuncCheck returns REPORT-0015 when the check row of a PL-FUNC result is not
// zero in any column (FR-15.14). A nil result means the check passes.
func PLFuncCheck(res Result) *Error {
	for _, c := range res.Cells {
		if c.Row == PLFuncCheckRow && !c.Blank && !c.Value.IsZero() {
			return errf(CodePLFuncMissing, "P&L lines totaling %s have no functional area.", c.Value.StringFixed(2))
		}
	}
	return nil
}
