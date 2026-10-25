package domain

import (
	"errors"
	"fmt"
)

type Error struct {
	Code   string
	Detail string
	Cause  error
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

func (e *Error) Unwrap() error { return e.Cause }

func errf(code, format string, args ...any) *Error {
	return &Error{Code: code, Detail: fmt.Sprintf(format, args...)}
}

func HasCode(err error, code string) bool {
	var de *Error
	return errors.As(err, &de) && de.Code == code
}

const (
	CodeBomCycle        = "ENG-BOM-CYCLE"
	CodeBomOverlap      = "ENG-BOM-OVERLAP"
	CodeBomNoVersion    = "ENG-BOM-NOVERSION"
	CodeBomLevels       = "ENG-BOM-LEVELS"
	CodeBomQty          = "ENG-BOM-QTY"
	CodeBomLine         = "ENG-BOM-LINE"
	CodeBomPhantomNoBom = "ENG-BOM-PHANTOM-NOBOM"
	CodeRouteQty        = "ENG-ROUTE-QTY"
	CodeRouteNextMiss   = "ENG-ROUTE-NEXT-MISSING"
	CodeRouteNoPrimary  = "ENG-ROUTE-SECONDARY-NOPRIMARY"
	CodeRouteCycle      = "ENG-ROUTE-CYCLE"
	CodeEcoState        = "ENG-ECO-STATE"
	CodeEcoNotApproved  = "ENG-ECO-NOT-APPROVED"
	CodeEcoDryRun       = "ENG-ECO-DRYRUN"
	CodeEcoDate         = "ENG-ECO-DATE"

	CodeBomTooLarge            = "ENG-BOM-TOOLARGE"
	CodeBomEcoRequired         = "ENG-BOM-ECO-REQUIRED"
	CodeEcoApproverUnavailable = "ENG-ECO-APPROVER-UNAVAILABLE"
)
