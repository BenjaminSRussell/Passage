package replay

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

// The subledger half of the parallel-run replay (49-4). Each area is posted through its own subledger's posting API,
// behind a port, so the replay does not know how receivables, payables, inventory or fixed assets are reached; the
// rest of the replay (ordering, idempotency keys, rejected documents, the period read-back and the diff) is the same
// for every area and is built here.

var (
	// ErrNoSubledgerPort is returned when documents of an area are replayed but no port posts that area.
	ErrNoSubledgerPort = errors.New("no subledger port for the area")
	// ErrSubledgerUnavailable is returned when a subledger fails in a way a retry may fix; the replay stops and can be
	// run again, since every document is posted under a fixed idempotency key.
	ErrSubledgerUnavailable = errors.New("subledger unavailable")
	// ErrRefused is the error a port returns for a document the subledger refuses (a 4xx such as an unknown party or a
	// closed period); use Refusal to give the subledger's code.
	ErrRefused = errors.New("subledger refused the document")
)

// Refusal is a port's answer for a document the subledger refused, with the subledger's code.
type Refusal struct{ Code string }

func (r Refusal) Error() string { return "subledger refused the document: " + r.Code }

// Is makes errors.Is(err, ErrRefused) true for a Refusal.
func (r Refusal) Is(target error) bool { return target == ErrRefused }

// Post is one document sent to a subledger: the legal entity it belongs to, the fixed idempotency key a replay run
// twice posts under once, and the legacy document.
type Post struct {
	LegalEntityID  string
	IdempotencyKey string
	Doc            domain.SubledgerDocument
}

// Activity asks a subledger for the net movement of one key in a period.
type Activity struct {
	LegalEntityID string
	Key           string
	Currency      string
	Period        string
}

// Port is one subledger as the replay sees it. Post returns the id the subledger gave the document; Movement reads
// the net movement of the key in the period, in the sign of the documents (an invoice raises it, a payment lowers it).
type Port interface {
	Post(ctx context.Context, in Post) (string, error)
	Movement(ctx context.Context, q Activity) (decimal.Decimal, error)
}

// Ports maps an area to the port that posts it.
type Ports map[domain.Area]Port

// SubledgerResult is the outcome of the subledger replay: the Passage-side balances to diff, the ids by document, and
// the documents a subledger refused.
type SubledgerResult struct {
	Passage  []domain.SubledgerBalance
	Posted   map[string]string
	Rejected []Rejected
}

// RunSubledger posts the documents area by area in their legacy sequence (posting date, then Seq), each under a fixed
// idempotency key, then reads back, for every (area, key, currency) the documents or the legacy balances name, the
// movement the subledger holds for the period. A refused document is recorded and the replay goes on, its amount
// missing from the Passage side and showing as a variance; any other failure stops it with ErrSubledgerUnavailable,
// and a missing port is ErrNoSubledgerPort before anything is posted.
func RunSubledger(ctx context.Context, ports Ports, in Request) (SubledgerResult, error) {
	res := SubledgerResult{Posted: map[string]string{}}
	if len(in.Subledger) == 0 {
		return res, nil
	}
	if err := domain.ValidateSubledger(in.Period, in.Subledger); err != nil {
		return res, err
	}
	missing := map[domain.Area]bool{}
	for _, d := range in.Subledger {
		if ports[d.Area] == nil {
			missing[d.Area] = true
		}
	}
	if len(missing) > 0 {
		areas := make([]string, 0, len(missing))
		for a := range missing {
			areas = append(areas, string(a))
		}
		sort.Strings(areas)
		return res, fmt.Errorf("%w: %v", ErrNoSubledgerPort, areas)
	}
	for _, d := range domain.OrderSubledger(in.Subledger) {
		id, err := ports[d.Area].Post(ctx, Post{LegalEntityID: in.LegalEntityID, IdempotencyKey: domain.SubledgerReplayKey(in.System, in.Period, d.Area, d.DocumentID), Doc: d})
		switch {
		case err == nil:
			res.Posted[string(d.Area)+"/"+d.DocumentID] = id
		case errors.Is(err, ErrRefused):
			var ref Refusal
			code := "SUBLEDGER_REFUSED"
			if errors.As(err, &ref) && ref.Code != "" {
				code = ref.Code
			}
			res.Rejected = append(res.Rejected, Rejected{DocumentID: d.DocumentID, Seq: d.Seq, Code: code, Area: string(d.Area)})
		default:
			return SubledgerResult{}, fmt.Errorf("%w: %s document %s: %v", ErrSubledgerUnavailable, d.Area, d.DocumentID, err)
		}
	}
	type key struct {
		area     domain.Area
		key, cur string
	}
	wanted := map[key]bool{}
	for _, d := range in.Subledger {
		wanted[key{d.Area, d.Key, d.Currency}] = true
	}
	for _, b := range in.LegacySubledger {
		if ports[b.Area] != nil {
			wanted[key{b.Area, b.Key, b.Currency}] = true
		}
	}
	keys := make([]key, 0, len(wanted))
	for k := range wanted {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].area != keys[j].area {
			return keys[i].area < keys[j].area
		}
		if keys[i].key != keys[j].key {
			return keys[i].key < keys[j].key
		}
		return keys[i].cur < keys[j].cur
	})
	for _, k := range keys {
		net, err := ports[k.area].Movement(ctx, Activity{LegalEntityID: in.LegalEntityID, Key: k.key, Currency: k.cur, Period: in.Period})
		if err != nil {
			return SubledgerResult{}, fmt.Errorf("%w: %s movement of %s: %v", ErrSubledgerUnavailable, k.area, k.key, err)
		}
		if !net.IsZero() {
			res.Passage = append(res.Passage, domain.SubledgerBalance{Area: k.area, Key: k.key, Currency: k.cur, Net: net})
		}
	}
	return res, nil
}
