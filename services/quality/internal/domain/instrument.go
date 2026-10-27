package domain

import (
	"fmt"
	"regexp"
	"time"
)

// WarnDays is the calibration warning horizon (BR-33.8, instrument_warn_days).
const WarnDays = 14

// Calibration states shown on an instrument.
const (
	CalibrationOK      = "IN_CALIBRATION"
	CalibrationDueSoon = "DUE_SOON"
	CalibrationOverdue = "OVERDUE"
)

// Instrument is a measuring device with a calibration schedule (BR-33.8).
type Instrument struct {
	ID                      string    `json:"id"`
	Code                    string    `json:"code"`
	Name                    string    `json:"name"`
	CalibrationIntervalDays int       `json:"calibrationIntervalDays"`
	LastCalibratedAt        time.Time `json:"lastCalibratedAt"`
	DueAt                   time.Time `json:"dueAt"`
	CalibrationStatus       string    `json:"calibrationStatus,omitempty"`
}

// CalibrationStatus is OVERDUE past due_at, DUE_SOON within WarnDays of it
// (the QM-0103 warning horizon), IN_CALIBRATION otherwise.
func CalibrationStatus(i Instrument, now time.Time) string {
	switch {
	case i.DueAt.Before(now):
		return CalibrationOverdue
	case !i.DueAt.After(now.AddDate(0, 0, WarnDays)):
		return CalibrationDueSoon
	}
	return CalibrationOK
}

var instrumentCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{1,31}$`)

// NewInstrument validates an instrument and computes due_at = last + interval.
func NewInstrument(code, name string, intervalDays int, lastCalibrated time.Time) (Instrument, error) {
	if !instrumentCode.MatchString(code) {
		return Instrument{}, invalid("code must be 2 to 32 characters of A-Z, 0-9, _ or -")
	}
	if name == "" || len(name) > 120 {
		return Instrument{}, invalid("name must be 1 to 120 characters")
	}
	if intervalDays < 1 || intervalDays > 3650 {
		return Instrument{}, invalid("calibrationIntervalDays must be 1 to 3650")
	}
	return Instrument{Code: code, Name: name, CalibrationIntervalDays: intervalDays,
		LastCalibratedAt: lastCalibrated.UTC(), DueAt: lastCalibrated.UTC().AddDate(0, 0, intervalDays)}, nil
}

// CheckInstrument rejects a result recorded after the instrument's due date (BR-33.8).
func CheckInstrument(i Instrument, recordedAt time.Time) error {
	if i.DueAt.Before(recordedAt) {
		return &Error{Code: CodeInstrumentOverdue, Reason: "INSTRUMENT_OVERDUE",
			Detail: fmt.Sprintf("Instrument %s is overdue for calibration since %s.", i.Code, i.DueAt.Format("2006-01-02"))}
	}
	return nil
}

// Recalibrate records a calibration at the given time and moves due_at on by
// the interval (BR-33.8). A calibration cannot lie in the future or before the
// one already recorded.
func (i Instrument) Recalibrate(at, now time.Time) (Instrument, error) {
	at = at.UTC()
	if at.After(now) {
		return i, invalid("calibratedAt must not be in the future")
	}
	if at.Before(i.LastCalibratedAt) {
		return i, invalid("calibratedAt must not be before the last recorded calibration")
	}
	i.LastCalibratedAt, i.DueAt = at, at.AddDate(0, 0, i.CalibrationIntervalDays)
	return i, nil
}
