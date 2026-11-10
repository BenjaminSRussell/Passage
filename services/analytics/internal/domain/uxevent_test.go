package domain

import (
	"strings"
	"testing"
	"time"
)

var uxNow = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func step(flow, name string) UXEvent {
	return UXEvent{SessionID: "sess-1234abcd", Release: "2026.11.1", Type: UXFunnelStep, Flow: flow, Step: name}
}

func TestPrepareUXEvents_AcceptsEachType(t *testing.T) {
	in := []UXEvent{
		step("procure_to_pay", "requisition_created"),
		{SessionID: "sess-1234abcd", Release: "2026.11.1", Type: UXValidationError, Route: "/purchasing/requisitions/new", Field: "lines[0].qty"},
		{SessionID: "sess-1234abcd", Release: "2026.11.1", Type: UXSearchNoResults, Query: "  Hydraulic Hose "},
		{SessionID: "sess-1234abcd", Release: "2026.11.1", Type: UXFormAbandoned, Route: "/purchasing/requisitions/new"},
	}
	out, dropped, err := PrepareUXEvents(in, uxNow)
	if err != nil || dropped != 0 || len(out) != 4 {
		t.Fatalf("out %d dropped %d err %v", len(out), dropped, err)
	}
	if out[2].Query != "hydraulic hose" {
		t.Fatalf("query %q, want trimmed and lower-cased", out[2].Query)
	}
	if !out[0].OccurredAt.Equal(uxNow) {
		t.Fatalf("occurredAt %v, want server time when the client sent none", out[0].OccurredAt)
	}
}

func TestPrepareUXEvents_RejectsWithTheFailingIndex(t *testing.T) {
	bad := func(mut func(*UXEvent)) UXEvent {
		e := step("procure_to_pay", "receipt_posted")
		mut(&e)
		return e
	}
	cases := map[string]UXEvent{
		"short session":       bad(func(e *UXEvent) { e.SessionID = "abc" }),
		"session with space":  bad(func(e *UXEvent) { e.SessionID = "abcd efgh ijkl" }),
		"empty release":       bad(func(e *UXEvent) { e.Release = "" }),
		"unknown flow":        bad(func(e *UXEvent) { e.Flow = "payroll" }),
		"step of other flow":  bad(func(e *UXEvent) { e.Step = "product_created" }),
		"unknown type":        bad(func(e *UXEvent) { e.Type = "click" }),
		"route with query":    bad(func(e *UXEvent) { e.Route = "/purchasing?vendor=a@b.c" }),
		"validation no field": {SessionID: "sess-1234abcd", Release: "r1", Type: UXValidationError, Route: "/x"},
		"field is a value":    {SessionID: "sess-1234abcd", Release: "r1", Type: UXValidationError, Route: "/x", Field: "john smith"},
		"abandoned no route":  {SessionID: "sess-1234abcd", Release: "r1", Type: UXFormAbandoned},
		"search no query":     {SessionID: "sess-1234abcd", Release: "r1", Type: UXSearchNoResults},
	}
	for name, e := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := PrepareUXEvents([]UXEvent{step("onboarding", "site_created"), e}, uxNow)
			if err == nil || err.Fields["events[1]"] == "" || err.Fields["events[0]"] != "" {
				t.Fatalf("err %+v, want a problem on events[1] only", err)
			}
		})
	}
}

func TestPrepareUXEvents_BatchSize(t *testing.T) {
	if _, _, err := PrepareUXEvents(nil, uxNow); err == nil {
		t.Fatal("empty batch accepted")
	}
	big := make([]UXEvent, MaxUXBatch+1)
	for i := range big {
		big[i] = step("onboarding", "site_created")
	}
	if _, _, err := PrepareUXEvents(big, uxNow); err == nil {
		t.Fatal("oversized batch accepted")
	}
	if out, _, err := PrepareUXEvents(big[:MaxUXBatch], uxNow); err != nil || len(out) != MaxUXBatch {
		t.Fatalf("full batch: %d %v", len(out), err)
	}
}

func TestScrubQuery(t *testing.T) {
	for _, q := range []string{"jane@example.com", "invoice 1234567", "+1 555 0100 2233 99", "   "} {
		if _, ok := ScrubQuery(q); ok {
			t.Errorf("%q kept, want dropped", q)
		}
	}
	if got, ok := ScrubQuery("PO 12345"); !ok || got != "po 12345" {
		t.Errorf("short number: %q %v", got, ok)
	}
	long, ok := ScrubQuery(strings.Repeat("é", 200))
	if !ok || len([]rune(long)) != MaxQueryLen {
		t.Errorf("long query cut to %d runes (ok %v), want %d", len([]rune(long)), ok, MaxQueryLen)
	}
}

func TestPrepareUXEvents_DropsPersonalSearchWithoutFailingTheBatch(t *testing.T) {
	in := []UXEvent{
		step("onboarding", "site_created"),
		{SessionID: "sess-1234abcd", Release: "r1", Type: UXSearchNoResults, Query: "ann@corp.example"},
	}
	out, dropped, err := PrepareUXEvents(in, uxNow)
	if err != nil || len(out) != 1 || dropped != 1 {
		t.Fatalf("out %d dropped %d err %v", len(out), dropped, err)
	}
}

func TestPrepareUXEvents_ClientTimeOnlyWhenRecent(t *testing.T) {
	recent, old, future := uxNow.Add(-time.Hour), uxNow.Add(-48*time.Hour), uxNow.Add(time.Hour)
	mk := func(at time.Time) UXEvent {
		e := step("onboarding", "site_created")
		e.OccurredAt = &at
		return e
	}
	out, _, err := PrepareUXEvents([]UXEvent{mk(recent), mk(old), mk(future)}, uxNow)
	if err != nil {
		t.Fatal(err)
	}
	if !out[0].OccurredAt.Equal(recent) || !out[1].OccurredAt.Equal(uxNow) || !out[2].OccurredAt.Equal(uxNow) {
		t.Fatalf("times %v %v %v", out[0].OccurredAt, out[1].OccurredAt, out[2].OccurredAt)
	}
}

func TestBuildFunnel_ConversionFromPreviousStep(t *testing.T) {
	steps := Funnels["procure_to_pay"]
	secs := 90.0
	f := BuildFunnel("procure_to_pay", "2026-11-01", "2026-11-10", "", map[string]int{steps[0]: 10, steps[1]: 5, steps[2]: 6}, &secs)
	if len(f.Steps) != len(steps) {
		t.Fatalf("%d steps, want %d", len(f.Steps), len(steps))
	}
	if f.Steps[0].ConversionFromPrevious != 1 || f.Steps[1].ConversionFromPrevious != 0.5 {
		t.Fatalf("conversion %+v", f.Steps[:2])
	}
	// A later step with more sessions than the one before (a session that skipped a step) is capped at 1.
	if f.Steps[2].ConversionFromPrevious != 1 {
		t.Fatalf("step 3 conversion %v, want capped at 1", f.Steps[2].ConversionFromPrevious)
	}
	// No sessions before a step: conversion is 0, not a division error.
	if f.Steps[4].Sessions != 0 || f.Steps[4].ConversionFromPrevious != 0 {
		t.Fatalf("step 5 %+v", f.Steps[4])
	}
	if f.SecondsToLastStep == nil || *f.SecondsToLastStep != 90 {
		t.Fatal("median seconds lost")
	}
}

func TestFunnelsAreDefinedForProcureToPayAndOnboarding(t *testing.T) {
	for _, name := range FunnelNames() {
		if len(Funnels[name]) < 3 {
			t.Errorf("%s has %d steps", name, len(Funnels[name]))
		}
	}
	if len(FunnelNames()) != len(Funnels) {
		t.Error("FunnelNames and Funnels disagree")
	}
	if last := Funnels["onboarding"]; last[len(last)-1] != "first_transaction" {
		t.Errorf("onboarding must end at first_transaction, ends at %s", last[len(last)-1])
	}
}

func TestValidateUXPeriod(t *testing.T) {
	if _, _, err := ValidateUXPeriod("2026-11-01", "2026-11-10", "2026.11.1"); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][3]string{{"x", "2026-11-10", ""}, {"2026-11-10", "2026-11-01", ""}, {"2025-01-01", "2026-11-10", ""}, {"2026-11-01", "2026-11-10", "bad release!"}} {
		if _, _, err := ValidateUXPeriod(c[0], c[1], c[2]); err == nil {
			t.Errorf("%v accepted", c)
		}
	}
}
