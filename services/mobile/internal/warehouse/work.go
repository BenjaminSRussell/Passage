package warehouse

import (
	"encoding/json"
	"strconv"
)

// Work is the part of a warehouse work the mobile service reads.
type Work struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Version int    `json:"version"`
	Lines   []struct {
		LineNo int    `json:"lineNo"`
		ItemID string `json:"itemId"`
		Qty    string `json:"qty"`
		Status string `json:"status"`
	} `json:"lines"`
}

// WorkFrom reads a work from a get-work response or from an assign response,
// which wraps it as {"work": {...}} and holds null when nothing is assignable.
func WorkFrom(body []byte) (Work, bool) {
	var wrapped struct {
		Work *Work `json:"work"`
	}
	if json.Unmarshal(body, &wrapped) == nil && wrapped.Work != nil && wrapped.Work.ID != "" {
		return *wrapped.Work, true
	}
	var w Work
	if json.Unmarshal(body, &w) == nil && w.ID != "" {
		return w, true
	}
	return Work{}, false
}

// Values are what a flow run reads from the work: its id and version, and the
// first open line.
func (w Work) Values() map[string]string {
	v := map[string]string{"work_id": w.ID, "work_version": strconv.Itoa(w.Version), "work_status": w.Status}
	for _, l := range w.Lines {
		if l.Status == "OPEN" {
			v["line_no"], v["line_qty"], v["item_id"] = strconv.Itoa(l.LineNo), l.Qty, l.ItemID
			break
		}
	}
	return v
}
