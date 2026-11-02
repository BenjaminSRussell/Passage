package domain

import (
	"encoding/hex"
	"math"
	"strconv"
	"time"
)

// Delta sync (integration track S4).
const (
	SourceLegacy  = "legacy"
	SourcePassage = "passage"

	SyncOK        = "succeeded"
	SyncPartial   = "partial"
	SyncFailed    = "failed"
	SyncAbandoned = "abandoned"

	ConflictOpen     = "open"
	ConflictKept     = "kept"
	ConflictAccepted = "accepted"

	ResolveAcceptLegacy = "accept_legacy"
	ResolveKeepPassage  = "keep_passage"

	// BackoffAfter is the number of consecutive failures after which a binding's next run is pushed out.
	BackoffAfter = 3
	// BackoffBase is the first delay once BackoffAfter failures are reached; each further failure doubles it.
	BackoffBase = 15 * time.Minute
	// BackoffCap is the longest delay a failing binding waits.
	BackoffCap = 24 * time.Hour
)

// SyncLookback is how far before its timestamp watermark a scheduled delta reads again. The extractors return rows
// strictly after the value they are given; reading from a little earlier also picks up rows that share the
// watermark's timestamp but were committed after the last read, and rows a long transaction committed late with an
// earlier timestamp. Rows read twice are unchanged by content hash and are not loaded again.
const SyncLookback = 5 * time.Minute

// DeltaSince is the value a scheduled delta passes to the extractor for a stored watermark: SyncLookback earlier for a
// timestamp, the predecessor for an integer or row version (so a row equal to the watermark is read again), and ""
// (read everything) for "" or a watermark that is not of the cursor type.
func DeltaSince(typ, watermark string) string {
	if watermark == "" {
		return ""
	}
	switch typ {
	case CursorTimestamp:
		t, err := time.Parse(time.RFC3339Nano, watermark)
		if err != nil {
			return ""
		}
		return t.Add(-SyncLookback).UTC().Format(cursorLayout)
	case CursorInt:
		n, err := strconv.ParseInt(watermark, 10, 64)
		if err != nil {
			return ""
		}
		if n == math.MinInt64 {
			return watermark
		}
		return strconv.FormatInt(n-1, 10)
	case CursorRowversion:
		b, err := hex.DecodeString(watermark)
		if err != nil || len(b) == 0 {
			return ""
		}
		for i := len(b) - 1; i >= 0; i-- {
			if b[i] > 0 {
				b[i]--
				break
			}
			if i == 0 {
				return watermark // all zero: nothing is smaller
			}
			b[i] = 0xff
		}
		return hex.EncodeToString(b)
	}
	return ""
}

// ValidSourceOfTruth reports whether s names the system that wins when both changed a record.
func ValidSourceOfTruth(s string) bool { return s == SourceLegacy || s == SourcePassage }

// SyncBackoff is how long a binding waits after its failures-th consecutive failure before it may run again, on top
// of its schedule: none below BackoffAfter, then 15 minutes doubling per failure up to 24 hours.
func SyncBackoff(failures int) time.Duration {
	if failures < BackoffAfter {
		return 0
	}
	d := BackoffBase
	for i := BackoffAfter; i < failures && d < BackoffCap; i++ {
		d *= 2
	}
	return min(d, BackoffCap)
}
