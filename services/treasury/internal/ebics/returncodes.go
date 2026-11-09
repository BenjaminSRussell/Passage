package ebics

import (
	"errors"
	"fmt"
	"time"
)

type Disposition int

const (
	Proceed Disposition = iota
	NoData
	BackOff
	Park
)

func (d Disposition) String() string {
	return [...]string{"proceed", "no-data", "back-off", "park"}[d]
}

const (
	CodeOK              = "000000"
	CodeDownloadDone    = "011000"
	CodeDownloadSkipped = "011001"
	CodeNoDownloadData  = "090005"
	CodeInternalError   = "061099"
	CodeTxRecoverySync  = "061101"
	CodeAuthFailed      = "061001"
	CodeUserUnknown     = "091003"
	CodeBankKeyUpdate   = "091008"
	CodeTxUnknown       = "091012"
	CodeTxAbort         = "091013"
	CodeTxReplay        = "091014"
	CodeSignatureFailed = "091118"
)

var dispositions = map[string]Disposition{
	CodeOK:              Proceed,
	CodeDownloadDone:    Proceed,
	CodeDownloadSkipped: Proceed,
	CodeNoDownloadData:  NoData,
	CodeInternalError:   BackOff,
	CodeTxRecoverySync:  BackOff,
	CodeTxUnknown:       BackOff,
	CodeTxAbort:         BackOff,
}

// Classify says what to do with a bank return code: carry on, treat as an empty
// download, back off and retry later, or park the subscriber for a person. Codes
// not listed park: an unknown refusal is never retried blindly. Authentication,
// signature, user-state, key-update and order-type refusals need a person (keys
// rotated, subscriber suspended, order not contracted) and park.
func Classify(code string) Disposition {
	if d, ok := dispositions[code]; ok {
		return d
	}
	return Park
}

// ReturnError is a bank refusal carrying its technical or business return code.
type ReturnError struct {
	Code        string
	Text        string
	Disposition Disposition
}

func (e *ReturnError) Error() string {
	return fmt.Sprintf("ebics bank refused: %s %s (%s)", e.Code, e.Text, e.Disposition)
}

func returnErr(code, text string) error {
	if d := Classify(code); d != Proceed && d != NoData {
		return &ReturnError{Code: code, Text: text, Disposition: d}
	}
	return nil
}

// ErrParked is returned while a subscriber is parked after a refusal that needs
// a person. Unpark clears it.
var ErrParked = errors.New("ebics subscriber is parked: a person must review the bank's refusal")

// ErrNoData is returned by a download when the bank has nothing to deliver.
var ErrNoData = errors.New("ebics: no download data available")

// BackoffPolicy bounds the retries after a back-off return code.
type BackoffPolicy struct {
	Attempts int
	Base     time.Duration
	Max      time.Duration
}

var DefaultBackoff = BackoffPolicy{Attempts: 4, Base: 2 * time.Second, Max: 30 * time.Second}

func (p BackoffPolicy) delay(attempt int) time.Duration {
	d := p.Base << attempt
	if d > p.Max || d <= 0 {
		return p.Max
	}
	return d
}
