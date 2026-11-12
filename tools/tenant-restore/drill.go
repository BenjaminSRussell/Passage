package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// DrillOptions are the pass criteria of a restore drill (go-live checklist item 8, PRD 9.50.16).
type DrillOptions struct {
	Tenant     string
	Schema     string
	TargetTime string // recorded as given; the scratch cluster was recovered to it
	// RTO is the longest the restore may take; zero means no limit.
	RTO time.Duration
	// MinRows is the fewest rows the scratch database must hold for the tenant; a drill that restored nothing proves nothing.
	MinRows int
}

// DrillRecord is the evidence a drill leaves: what was restored, how long it took and whether it passed.
type DrillRecord struct {
	Tenant          string        `json:"tenant"`
	Schema          string        `json:"schema"`
	TargetTime      string        `json:"targetTime,omitempty"`
	StartedAt       time.Time     `json:"startedAt"`
	FinishedAt      time.Time     `json:"finishedAt"`
	DurationSeconds float64       `json:"durationSeconds"`
	RTOSeconds      float64       `json:"rtoSeconds,omitempty"`
	Status          string        `json:"status"`
	Failures        []string      `json:"failures,omitempty"`
	TablesChecked   int           `json:"tablesChecked"`
	RowsRestored    int           `json:"rowsRestored"`
	WouldInsert     int           `json:"wouldInsert"`
	WouldUpdate     int           `json:"wouldUpdate"`
	LiveOnly        int           `json:"liveOnly"`
	Tables          []TableResult `json:"tables"`
}

// RunDrill reads one tenant out of the recovered scratch database and runs the copy into the live database as a dry run, so
// every constraint and foreign key is exercised and nothing is written. It passes only if the copy succeeds, the tenant has at
// least MinRows restored rows and the run is inside the RTO. now is injected for tests.
func RunDrill(ctx context.Context, scratch, live *pgx.Conn, o DrillOptions, now func() time.Time) DrillRecord {
	rec := DrillRecord{Tenant: o.Tenant, Schema: o.Schema, TargetTime: o.TargetTime, StartedAt: now().UTC(), RTOSeconds: o.RTO.Seconds(), Tables: []TableResult{}}
	res, err := RestoreTenant(ctx, scratch, live, o.Schema, o.Tenant, false)
	rec.FinishedAt = now().UTC()
	rec.DurationSeconds = rec.FinishedAt.Sub(rec.StartedAt).Seconds()
	if err != nil {
		rec.Failures = append(rec.Failures, "restore failed: "+err.Error())
	}
	rec.Tables = append(rec.Tables, res.Tables...)
	rec.TablesChecked = len(res.Tables)
	for _, t := range res.Tables {
		rec.RowsRestored += t.Source
		rec.WouldInsert += t.Inserted
		rec.WouldUpdate += t.Updated
		rec.LiveOnly += t.LiveOnly
	}
	if err == nil && rec.RowsRestored < max(o.MinRows, 1) {
		rec.Failures = append(rec.Failures, fmt.Sprintf("only %d rows restored for the tenant, expected at least %d", rec.RowsRestored, max(o.MinRows, 1)))
	}
	if o.RTO > 0 && rec.FinishedAt.Sub(rec.StartedAt) > o.RTO {
		rec.Failures = append(rec.Failures, fmt.Sprintf("restore took %.0f s, over the RTO of %.0f s", rec.DurationSeconds, o.RTO.Seconds()))
	}
	rec.Status = "PASS"
	if len(rec.Failures) > 0 {
		rec.Status = "FAIL"
	}
	return rec
}
