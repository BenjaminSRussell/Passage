package domain

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"pgregory.net/rapid"
)

// TestSchemaEvolutionMatrix runs the fx-schema-evolution fixture: 12 source
// changes, 8 applied automatically and 4 that pause the pipeline (FR-46.3).
func TestSchemaEvolutionMatrix(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/fx-schema-evolution.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name   string  `json:"name"`
		Change Change  `json:"change"`
		Expect Verdict `json:"expect"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 12 {
		t.Fatalf("fixture has %d scripts, want 12", len(cases))
	}
	auto, pause := 0, 0
	for _, c := range cases {
		got, err := Classify(c.Change)
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		if got != c.Expect {
			t.Errorf("%s: got %s, want %s", c.Name, got, c.Expect)
		}
		if got == AutoApply {
			auto++
		} else {
			pause++
		}
	}
	if auto != 8 || pause != 4 {
		t.Fatalf("auto %d pause %d, want 8 and 4", auto, pause)
	}
}

func TestClassify_UnknownKindAndBadType(t *testing.T) {
	if _, err := Classify(Change{Kind: "truncate"}); err == nil {
		t.Fatal("unknown change kind must be an error")
	}
	if _, err := Classify(Change{Kind: WidenType, From: "jsonb", To: "text"}); err == nil {
		t.Fatal("unsupported type must be an error")
	}
}

func TestPipeline_LSNNeverDecreases(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := NewPipeline("ledger", "journal_line")
		var high int64
		for _, lsn := range rapid.SliceOfN(rapid.Int64Range(0, 1_000_000), 1, 50).Draw(t, "lsns") {
			err := p.CommitLSN(lsn)
			switch {
			case lsn < high:
				if !errors.Is(err, ErrLSNRegression) {
					t.Fatalf("lsn %d after %d: err = %v, want regression", lsn, high, err)
				}
			case err != nil:
				t.Fatalf("lsn %d: unexpected %v", lsn, err)
			default:
				high = lsn
			}
			if p.LastLSN != high {
				t.Fatalf("last_lsn %d, want %d", p.LastLSN, high)
			}
		}
	})
}

func TestPipeline_SchemaPauseAndApprove(t *testing.T) {
	p := NewPipeline("sales", "sales_line")
	v, err := p.ApplySchemaChange(Change{Kind: AddColumn, Nullable: true})
	if err != nil || v != AutoApply || p.Status != PipelineRunning || p.SchemaVersion != 2 {
		t.Fatalf("nullable add: %s %v %+v", v, err, p)
	}
	v, _ = p.ApplySchemaChange(Change{Kind: NarrowType, From: "decimal(28,6)", To: "decimal(18,4)"})
	if v != Pause || p.Status != PipelinePausedSchema {
		t.Fatalf("narrowing must pause: %s %+v", v, p)
	}
	if _, err := p.ApplySchemaChange(Change{Kind: DropColumn}); !errors.Is(err, ErrWrongState) {
		t.Fatalf("changes while paused: err = %v", err)
	}
	if err := p.ApproveMapping(); err != nil || p.Status != PipelineRunning {
		t.Fatalf("approve: %v %+v", err, p)
	}
}

func TestPipeline_FiveFailuresPauseAndReplayResumes(t *testing.T) {
	p := NewPipeline("ledger", "journal_line")
	if err := p.CommitLSN(500); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxConsecutiveFailures-1; i++ {
		p.SinkFailed()
	}
	if p.Status != PipelineRunning {
		t.Fatalf("after %d failures status %s, want RUNNING", MaxConsecutiveFailures-1, p.Status)
	}
	p.SinkFailed()
	if p.Status != PipelinePausedError {
		t.Fatalf("after %d failures status %s, want PAUSED_ERROR", MaxConsecutiveFailures, p.Status)
	}
	if err := p.CommitLSN(501); !errors.Is(err, ErrWrongState) {
		t.Fatalf("commit while paused: err = %v", err)
	}
	if err := p.Replay(); err != nil || p.Status != PipelineRunning || p.LastLSN != 500 {
		t.Fatalf("replay: %v %+v; must resume from last_lsn 500", err, p)
	}
}
