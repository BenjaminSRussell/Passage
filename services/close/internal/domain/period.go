package domain

import "errors"

type Module string

const (
	ModuleAll        Module = "all"
	ModuleGL         Module = "gl"
	ModuleAP         Module = "ap"
	ModuleAR         Module = "ar"
	ModuleInventory  Module = "inventory"
	ModuleAssets     Module = "assets"
	ModuleCash       Module = "cash"
	ModuleProduction Module = "production"
	ModuleProjects   Module = "projects"
	ModuleTax        Module = "tax"
	ModuleBank       Module = "bank"
	ModulePayroll    Module = "payroll"
)

type Status string

const (
	StatusOpen              Status = "open"
	StatusOnHold            Status = "on_hold"
	StatusClosed            Status = "closed"
	StatusPermanentlyClosed Status = "permanently_closed"
	StatusNeverOpened       Status = "never_opened"
)

type Row struct {
	Module         Module
	Status         Status
	AllowedGroupID string
}

var sourceModules = map[string]Module{
	"manual": ModuleGL, "gl": ModuleGL, "ap": ModuleAP, "ar": ModuleAR, "inv": ModuleInventory,
	"fa": ModuleAssets, "proj": ModuleProjects, "tax": ModuleTax, "bank": ModuleBank,
	"payroll": ModulePayroll, "cash": ModuleCash, "prod": ModuleProduction,
}

func ModuleForSource(system string) Module {
	if m, ok := sourceModules[system]; ok {
		return m
	}
	return ModuleAll
}

func Resolve(rows []Row, module Module) Row {
	var fallback *Row
	for i := range rows {
		switch rows[i].Module {
		case module:
			return rows[i]
		case ModuleAll:
			fallback = &rows[i]
		}
	}
	if fallback != nil {
		return *fallback
	}
	return Row{Module: module, Status: StatusNeverOpened}
}

func Decide(rows []Row, module Module, group string) (Status, error) {
	if module != ModuleAll {
		return decideRow(Resolve(rows, module), module, group)
	}
	candidates := rows
	if len(candidates) == 0 {
		candidates = []Row{{Module: ModuleAll, Status: StatusNeverOpened}}
	}
	result := StatusOpen
	var held error
	for _, row := range candidates {
		status, err := decideRow(row, module, group)
		var de *Error
		switch {
		case errors.As(err, &de) && de.Code == CodePeriodClosed:
			return status, err
		case err != nil:
			held, result = err, status
		case status == StatusOnHold && held == nil:
			result = status
		}
	}
	return result, held
}

func decideRow(row Row, module Module, group string) (Status, error) {
	switch row.Status {
	case StatusOpen:
		return row.Status, nil
	case StatusOnHold:
		if group != "" && group == row.AllowedGroupID {
			return row.Status, nil
		}
		return row.Status, fail(CodePeriodOnHold, "period is on hold for module "+string(module))
	default:
		return row.Status, fail(CodePeriodClosed, "period is "+string(row.Status)+" for module "+string(module))
	}
}

func ValidateTransition(rows []Row, to Status, module Module) error {
	var current Status = StatusNeverOpened
	for _, r := range rows {
		if r.Module == module {
			current = r.Status
		}
	}
	switch {
	case to != StatusOpen && to != StatusOnHold && to != StatusClosed && to != StatusPermanentlyClosed:
		return fail(CodeInvalidTransition, "unknown target status "+string(to))
	case current == StatusPermanentlyClosed:
		return fail(CodeInvalidTransition, "permanently closed periods cannot change")
	case current == StatusClosed && to != StatusPermanentlyClosed:
		return fail(CodeInvalidTransition, "closed periods change only through the reopen workflow")
	case current == StatusNeverOpened && to != StatusOpen:
		return fail(CodeInvalidTransition, "a never opened period must be opened first")
	case current == to:
		return fail(CodeInvalidTransition, "period is already "+string(to))
	}
	if (to == StatusClosed || to == StatusPermanentlyClosed) && (module == ModuleGL || module == ModuleAll) {
		for _, r := range rows {
			if r.Module != ModuleGL && r.Module != ModuleAll && r.Status != StatusClosed && r.Status != StatusPermanentlyClosed {
				return fail(CodeInvalidTransition, "module "+string(r.Module)+" is still "+string(r.Status)+"; close it before the general ledger")
			}
		}
	}
	return nil
}
