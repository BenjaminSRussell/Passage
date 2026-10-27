package store_test

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// FR-17.19 and BR-17.16 against Postgres: a sealed WEIGHTED_SCORE RFQ keeps
// its criteria, refuses a late reply outright, takes scores only after the
// deadline (a re-score replaces the earlier one and bumps the version), and
// logs the first opening after the deadline once.
func TestRFQ_SealedScoringAndOpening(t *testing.T) {
	s := store.New(migrated(t))
	ctx := principalContext(70)
	in := draftRFQ(t)
	in.Sealed, in.RankingRule = true, domain.RankWeightedScore
	in.Criteria = []domain.ScoringCriterion{{Name: "Quality", Weight: dec("2")}, {Name: "Delivery", Weight: dec("1.5")}}
	r, err := s.CreateRFQ(ctx, in, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Sealed || r.RankingRule != domain.RankWeightedScore || len(r.Criteria) != 2 || r.Criteria[1].Seq != 2 ||
		r.Criteria[1].Name != "Delivery" || !r.Criteria[1].Weight.Equal(dec("1.5")) || r.OpenedAt != nil {
		t.Fatalf("created %+v", r)
	}
	list, err := s.ListRFQs(ctx, store.RFQFilter{}, "", 0)
	if err != nil || len(list.Items) != 1 || !list.Items[0].Sealed {
		t.Fatalf("list = %+v, %v", list, err)
	}
	vendorA, vendorB := ids.New(), ids.New()
	r, err = s.SendRFQ(ctx, r.ID, 1, []string{vendorA, vendorB}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	onTime := rfqDeadline.Add(-time.Hour)
	if r, err = s.RecordRFQReplies(ctx, r.ID, vendorA, []domain.RFQReply{replyTo(r.Lines[0].ID, "10", 5, onTime)}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	late := []domain.RFQReply{replyTo(r.Lines[0].ID, "9", 5, rfqDeadline.Add(time.Second))}
	if _, err := s.RecordRFQReplies(ctx, r.ID, vendorB, late, store.Idem{}); problemCode(err) != domain.CodeRFQSealedLate {
		t.Fatalf("late reply to a sealed RFQ = %v, want %s", err, domain.CodeRFQSealedLate)
	}
	if cur, _ := s.GetRFQ(ctx, r.ID); len(cur.Replies) != 1 || cur.Version != r.Version {
		t.Fatalf("the refused late reply left %d replies, version %d", len(cur.Replies), cur.Version)
	}

	quality, delivery := r.Criteria[0].ID, r.Criteria[1].ID
	scores := []domain.VendorScore{{CriterionID: quality, Score: 8}, {CriterionID: delivery, Score: 3}}
	if _, err := s.SetRFQScores(ctx, r.ID, r.Version, vendorA, scores, rfqDeadline.Add(-time.Minute)); problemCode(err) != domain.CodeRFQSealed {
		t.Fatalf("scoring before the deadline = %v", err)
	}
	if _, err := s.SetRFQScores(ctx, r.ID, r.Version-1, vendorA, scores, rfqDeadline.Add(time.Hour)); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version = %v", err)
	}
	after := rfqDeadline.Add(time.Hour)
	scored, err := s.SetRFQScores(ctx, r.ID, r.Version, vendorA, scores, after)
	if err != nil {
		t.Fatal(err)
	}
	if scored.Version != r.Version+1 || len(scored.Scores) != 2 {
		t.Fatalf("scored = v%d %+v", scored.Version, scored.Scores)
	}
	rescored, err := s.SetRFQScores(ctx, r.ID, scored.Version, vendorA, []domain.VendorScore{{CriterionID: quality, Score: 9}}, after)
	if err != nil {
		t.Fatal(err)
	}
	// Quality is now 9, delivery stays 3: (2×9 + 1.5×3) / 3.5 = 6.4286.
	weighted := domain.WeightedScores(rescored.Criteria, rescored.Scores)
	if len(rescored.Scores) != 2 || weighted[vendorA].String() != "6.4286" {
		t.Fatalf("after re-score: %+v, weighted %s", rescored.Scores, weighted[vendorA])
	}
	if _, err := s.SetRFQScores(ctx, r.ID, rescored.Version, ids.New(), scores, after); problemCode(err) != domain.CodeRFQNotInvited {
		t.Fatalf("uninvited vendor = %v", err)
	}

	if early, err := s.OpenSealedRFQ(ctx, r.ID, ids.New(), rfqDeadline.Add(-time.Second)); err != nil || early.OpenedAt != nil {
		t.Fatalf("opening before the deadline = %v, %v", early.OpenedAt, err)
	}
	buyer := ids.New()
	at := rfqDeadline.Add(time.Second)
	opened, err := s.OpenSealedRFQ(ctx, r.ID, buyer, at)
	if err != nil || opened.OpenedAt == nil || !opened.OpenedAt.Equal(at) || opened.OpenedBy != buyer || opened.Version != rescored.Version {
		t.Fatalf("opened = %v by %s v%d, %v", opened.OpenedAt, opened.OpenedBy, opened.Version, err)
	}
	again, err := s.OpenSealedRFQ(ctx, r.ID, ids.New(), at.Add(time.Hour))
	if err != nil || !again.OpenedAt.Equal(at) || again.OpenedBy != buyer {
		t.Fatalf("the first opening must stay logged: %v by %s, %v", again.OpenedAt, again.OpenedBy, err)
	}
	if _, err := s.GetRFQ(principalContext(71), r.ID); problemCode(err) != store.CodeRFQNotFound {
		t.Fatalf("other tenant = %v", err)
	}
}
