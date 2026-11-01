package kafka

import (
	"errors"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/BenjaminSRussell/passage/platform/eventing"
)

func TestFromKgoLowercasesHeadersAndKeepsHandle(t *testing.T) {
	raw := &kgo.Record{Topic: "passage.receiving.receipt", Partition: 2, Offset: 41, Key: []byte("k"), Value: []byte("v"),
		Headers: []kgo.RecordHeader{{Key: "TenantId", Value: []byte("t1")}}}
	r := fromKgo(raw)
	if r.Headers["tenantid"] != "t1" || r.Partition != 2 || r.Offset != 41 || r.Raw != raw {
		t.Fatalf("record = %+v", r)
	}
}

func TestDeadLetterRecordKeepsOriginAndReason(t *testing.T) {
	r := eventing.Record{Topic: "a", Partition: 3, Offset: 9, Key: []byte("k"), Value: []byte("v"), Headers: map[string]string{"tenantid": "t1"}}
	out := deadLetterRecord("a.dlq", r, "decode")
	got := map[string]string{}
	for _, h := range out.Headers {
		got[h.Key] = string(h.Value)
	}
	if out.Topic != "a.dlq" || string(out.Value) != "v" || got[HeaderDeadLetterReason] != "decode" || got[HeaderOriginTopic] != "a" ||
		got[HeaderOriginPartition] != "3" || got[HeaderOriginOffset] != "9" || got["tenantid"] != "t1" {
		t.Fatalf("dead-letter record = %+v headers %v", out, got)
	}
}

func TestNewRequiresBrokersGroupAndTopics(t *testing.T) {
	if _, err := New(Config{Group: "g", Topics: []string{"t"}}); !errors.Is(err, eventing.ErrInvalidArgument) {
		t.Fatalf("err = %v", err)
	}
}

func TestCommitNeedsTheKafkaHandle(t *testing.T) {
	if err := (&Client{}).Commit(t.Context(), eventing.Record{}); !errors.Is(err, eventing.ErrInvalidArgument) {
		t.Fatalf("err = %v", err)
	}
}
