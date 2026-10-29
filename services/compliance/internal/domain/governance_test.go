package domain

import (
	"reflect"
	"testing"
)

func TestDecideExport(t *testing.T) {
	const reason = "monthly audit pack for the external auditor"
	cases := []struct {
		name        string
		req         ExportRequest
		wantAllowed bool
		wantActions []ExportAction
		wantWM      bool
		wantReason  bool
		code        string
	}{
		{name: "public grid export is free", req: ExportRequest{Classification: ClassPublic, Channel: ChannelGrid, Rows: 50}, wantAllowed: true, wantActions: []ExportAction{ActionAllow}},
		{name: "financial export is audited", req: ExportRequest{Classification: ClassFinancial, Channel: ChannelReport, Rows: 50}, wantAllowed: true, wantActions: []ExportAction{ActionAudit}},
		{name: "PII needs reason and watermark", req: ExportRequest{Classification: ClassPII, Channel: ChannelExplorer, Rows: 5, Reason: reason}, wantAllowed: true, wantActions: []ExportAction{ActionReason, ActionWatermark}, wantWM: true, wantReason: true},
		{name: "PII without reason refused", req: ExportRequest{Classification: ClassPII, Channel: ExportChannel("GRID"), Rows: 5, Reason: "short"}, code: CodeExportReason},
		{name: "deny override refused", req: ExportRequest{Classification: ClassConfidential, Channel: ChannelGrid, Rows: 1, Policy: map[ExportClassification][]ExportAction{ClassConfidential: {ActionDeny}}}, code: CodeExportDenied},
		{name: "approval override holds download", req: ExportRequest{Classification: ClassFinancial, Channel: ChannelReport, Rows: 1, Policy: map[ExportClassification][]ExportAction{ClassFinancial: {ActionApproval}}}, wantActions: []ExportAction{ActionApproval}},
		{name: "bulk API read above 10k holds for approval", req: ExportRequest{Classification: ClassFinancial, Channel: ChannelAPIBulk, Rows: 10001}, wantActions: []ExportAction{ActionApproval, ActionAudit}},
		{name: "bulk at exactly 10k passes", req: ExportRequest{Classification: ClassFinancial, Channel: ChannelAPIBulk, Rows: 10000}, wantAllowed: true, wantActions: []ExportAction{ActionAudit}},
		{name: "public bulk never needs approval", req: ExportRequest{Classification: ClassPublic, Channel: ChannelMCPBulk, Rows: 20000}, wantAllowed: true, wantActions: []ExportAction{ActionAllow}},
		{name: "unknown classification", req: ExportRequest{Classification: "SECRET", Channel: ChannelGrid}, code: CodeExportChannel},
		{name: "unknown channel", req: ExportRequest{Classification: ClassPublic, Channel: "FAX"}, code: CodeExportChannel},
		{name: "unknown override action", req: ExportRequest{Classification: ClassFinancial, Channel: ChannelGrid, Policy: map[ExportClassification][]ExportAction{ClassFinancial: {"SHRUG"}}}, code: CodeExportChannel},
		{name: "negative rows", req: ExportRequest{Classification: ClassPublic, Channel: ChannelGrid, Rows: -1}, code: CodeExportRows},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := DecideExport(c.req)
			if c.code != "" {
				expectPrivacyCode(t, err, c.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if out.Allowed != c.wantAllowed || !reflect.DeepEqual(out.Actions, c.wantActions) {
				t.Fatalf("got allowed=%v actions=%v, want allowed=%v actions=%v", out.Allowed, out.Actions, c.wantAllowed, c.wantActions)
			}
			if out.Watermark != c.wantWM || out.RequiresReason != c.wantReason || !out.Audit {
				t.Fatalf("flags wrong: %+v", out)
			}
		})
	}
}

func TestClassifyRead(t *testing.T) {
	fields := []string{"hr.worker.salary", "hr.worker.name", "party.national_id", "hr.worker.salary"}
	out, err := ClassifyRead(fields, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"hr.worker.salary", "party.national_id"}
	if !out.Logged || !reflect.DeepEqual(out.Fields, want) || out.RetentionDays != 730 {
		t.Fatalf("got %+v, want fields %v and 730 days", out, want)
	}

	none, err := ClassifyRead([]string{"hr.worker.name"}, nil, 0)
	if err != nil || none.Logged || len(none.Fields) != 0 {
		t.Fatalf("name is not a read-logged field: %+v %v", none, err)
	}

	exact, err := ClassifyRead([]string{"crm.contact.fax", "crm.contact.email"}, []ReadRule{{FieldFQN: "crm.contact.fax", Classification: ClassPII}}, 90)
	if err != nil || !exact.Logged || !reflect.DeepEqual(exact.Fields, []string{"crm.contact.fax"}) || exact.RetentionDays != 90 {
		t.Fatalf("custom exact rule: %+v %v", exact, err)
	}

	for _, days := range []int{89, 3651} {
		if _, err := ClassifyRead(fields, nil, days); err == nil {
			t.Fatalf("retention %d should be refused", days)
		} else {
			expectPrivacyCode(t, err, CodeReadLogRetain)
		}
	}
}
