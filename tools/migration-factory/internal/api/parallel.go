package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
)

// parallelBodyLimit bounds a parallel-run start: both sides' tuples travel in the body.
const parallelBodyLimit = 16 << 20

type tupleDTO struct {
	Area   string `json:"area"`
	Key    string `json:"key"`
	Amount string `json:"amount"`
	// Documents is how many legacy documents the tuple sums; RND-001 allows 0.01 for each (BR-49.7).
	Documents int `json:"documents,omitempty"`
}

type parallelRequest struct {
	Period            string     `json:"period"`
	SourceSystem      string     `json:"sourceSystem"`
	DocumentsReplayed int        `json:"documentsReplayed"`
	Revenue           string     `json:"revenue"`
	ExplainedMaxPct   string     `json:"explainedMaxPct,omitempty"`
	Legacy            []tupleDTO `json:"legacy"`
	Passage           []tupleDTO `json:"passage"`
}

type varianceDTO struct {
	VarianceID  string     `json:"varianceId"`
	Area        string     `json:"area"`
	Key         string     `json:"key"`
	Legacy      string     `json:"legacy"`
	Passage     string     `json:"passage"`
	Delta       string     `json:"delta"`
	Documents   int        `json:"documents,omitempty"`
	Class       string     `json:"class"`
	RuleID      string     `json:"ruleId,omitempty"`
	Reason      string     `json:"reason,omitempty"`
	ExplainedBy string     `json:"explainedBy,omitempty"`
	ExplainedAt *time.Time `json:"explainedAt,omitempty"`
}

type parallelDTO struct {
	ParallelRunID     string         `json:"parallelRunId"`
	Period            string         `json:"period"`
	SourceSystem      string         `json:"sourceSystem"`
	DocumentsReplayed int            `json:"documentsReplayed"`
	Revenue           string         `json:"revenue"`
	ExplainedMaxPct   string         `json:"explainedMaxPct"`
	InputHash         string         `json:"inputHash"`
	State             string         `json:"state"`
	UnexplainedCount  int            `json:"unexplainedCount"`
	UnexplainedAmount string         `json:"unexplainedAmount"`
	ExplainedAmount   string         `json:"explainedAmount"`
	ExpectedByRule    map[string]int `json:"expectedByRule"`
	// CanSignOff reports whether the FR-49.5 exit criterion holds now; SignOffBlocker says why
	// not (MIG-1509 or the explained-amount limit).
	CanSignOff     bool          `json:"canSignOff"`
	SignOffBlocker string        `json:"signOffBlocker,omitempty"`
	StartedBy      string        `json:"startedBy,omitempty"`
	SignedOffBy    string        `json:"signedOffBy,omitempty"`
	SignedOffAt    *time.Time    `json:"signedOffAt,omitempty"`
	Version        int           `json:"version"`
	CreatedAt      time.Time     `json:"createdAt"`
	Variances      []varianceDTO `json:"variances,omitempty"`
}

func toParallelDTO(p domain.ParallelRun, withVariances bool) parallelDTO {
	out := parallelDTO{ParallelRunID: p.ID, Period: p.Period, SourceSystem: string(p.SourceSystem), DocumentsReplayed: p.DocumentsReplayed,
		Revenue: p.Revenue.StringFixed(6), ExplainedMaxPct: p.ExplainedMaxPct.String(), InputHash: p.InputHash, State: string(p.State),
		UnexplainedCount: p.Summary.UnexplainedCount, UnexplainedAmount: p.Summary.UnexplainedAmount.StringFixed(6),
		ExplainedAmount: p.Summary.ExplainedAmount.StringFixed(6), ExpectedByRule: map[string]int{}, StartedBy: p.StartedBy,
		SignedOffBy: p.SignedOffBy, SignedOffAt: p.SignedOffAt, Version: p.Version, CreatedAt: p.CreatedAt}
	for rule, n := range p.Summary.ByRule {
		out.ExpectedByRule[rule] = n
	}
	if p.State == domain.ParallelReview {
		if err := domain.SignOffGate(p.Summary, p.Revenue, p.ExplainedMaxPct); err != nil {
			out.SignOffBlocker = strings.TrimPrefix(err.Error(), domain.ErrInvalid.Error()+": ")
		} else {
			out.CanSignOff = true
		}
	}
	if withVariances {
		out.Variances = make([]varianceDTO, 0, len(p.Variances))
		for _, v := range p.Variances {
			out.Variances = append(out.Variances, varianceDTO{VarianceID: v.ID, Area: string(v.Area), Key: v.Key,
				Legacy: v.Legacy.StringFixed(6), Passage: v.Passage.StringFixed(6), Delta: v.Delta.StringFixed(6), Documents: v.Documents, Class: v.Class,
				RuleID: v.RuleID, Reason: v.Reason, ExplainedBy: v.ExplainedBy, ExplainedAt: v.ExplainedAt})
		}
	}
	return out
}

func toLines(in []tupleDTO, side string, documentsReplayed int) ([]domain.Line, error) {
	out := make([]domain.Line, 0, len(in))
	for i, t := range in {
		area, err := domain.ParseArea(strings.TrimSpace(t.Area))
		if err != nil {
			return nil, fmt.Errorf("%s tuple %d: %w", side, i+1, err)
		}
		amt, err := parseMoney(t.Amount)
		if err != nil {
			return nil, fmt.Errorf("%w: %s tuple %d amount %q %s", domain.ErrInvalid, side, i+1, t.Amount, err)
		}
		if t.Documents < 0 {
			return nil, fmt.Errorf("%w: %s tuple %d documents %d must be >= 0", domain.ErrInvalid, side, i+1, t.Documents)
		}
		if t.Documents > documentsReplayed {
			return nil, fmt.Errorf("%w: %s tuple %d sums %d documents but the run replayed %d; RND-001 scales with documents that exist",
				domain.ErrInvalid, side, i+1, t.Documents, documentsReplayed)
		}
		line := domain.Line{Area: area, Key: strings.TrimSpace(t.Key), Documents: t.Documents}
		if side == "legacy" {
			line.Legacy = amt
		} else {
			line.Passage = amt
		}
		out = append(out, line)
	}
	return out, nil
}

// startParallelRun diffs a replayed period against the legacy period and stores the variances
// for review (FR-49.5). The caller supplies the legacy tuples and the Passage tuples the replay
// produced. Starting is idempotent by (period, system): the same input returns the stored run
// with Idempotent-Replayed: true, and other input for a stored (period, system) is MIG-0009.
func (s *Server) startParallelRun(w http.ResponseWriter, r *http.Request) {
	var req parallelRequest
	idem, err := readCommand(r, &req, parallelBodyLimit)
	if err != nil {
		fail(w, r, err)
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	sys, err := domain.ParseSourceSystem(req.SourceSystem)
	if err != nil {
		fail(w, r, err)
		return
	}
	revenue, err := parseMoney(req.Revenue)
	if err != nil {
		fail(w, r, invalid(fmt.Sprintf("revenue %q %s", req.Revenue, err)))
		return
	}
	maxPct, err := domain.ParseExplainedMaxPct(req.ExplainedMaxPct)
	if err != nil {
		fail(w, r, err)
		return
	}
	legacy, err := toLines(req.Legacy, "legacy", req.DocumentsReplayed)
	if err != nil {
		fail(w, r, err)
		return
	}
	passage, err := toLines(req.Passage, "passage", req.DocumentsReplayed)
	if err != nil {
		fail(w, r, err)
		return
	}
	in := domain.ParallelInput{Period: strings.TrimSpace(req.Period), SourceSystem: sys, DocumentsReplayed: req.DocumentsReplayed,
		Revenue: revenue, ExplainedMaxPct: maxPct, Legacy: legacy, Passage: passage}
	run, err := domain.StartParallelRun(ids.New(), in, domain.DefaultRules(), p.UserID, ids.New)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, replayed, err := s.store.StartParallelRun(r.Context(), idem, run)
	if err != nil {
		fail(w, r, err)
		return
	}
	respond(w, http.StatusCreated, replayed, toParallelDTO(out, true))
}

func (s *Server) listParallelRuns(w http.ResponseWriter, r *http.Request) {
	f := store.ParallelFilter{State: r.URL.Query().Get("state")}
	switch domain.ParallelState(f.State) {
	case "", domain.ParallelReplaying, domain.ParallelDiffing, domain.ParallelReview, domain.ParallelSignedOff:
	default:
		fail(w, r, invalid("state is not a parallel run state"))
		return
	}
	var err error
	if f.Limit, err = listLimit(r); err != nil {
		fail(w, r, err)
		return
	}
	if f.After, err = after(r); err != nil {
		fail(w, r, err)
		return
	}
	runs, err := s.store.ListParallelRuns(r.Context(), f)
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]parallelDTO, 0, len(runs))
	for _, run := range runs {
		items = append(items, toParallelDTO(run, false))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getParallelRun(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	run, err := s.store.GetParallelRun(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toParallelDTO(run, true))
}

type explainRequest struct {
	Reason  string `json:"reason"`
	Version int    `json:"version,omitempty"`
}

// explainVariance attaches a reason to one UNEXPLAINED variance (9.49.5). The reviewer must not
// be the person who started the run (MIG-001).
func (s *Server) explainVariance(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	vid := chi.URLParam(r, "v")
	if !ids.Valid(vid) {
		fail(w, r, invalid("variance id must be a UUID"))
		return
	}
	var req explainRequest
	idem, err := readCommand(r, &req, 1<<16)
	if err != nil {
		fail(w, r, err)
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if req.Version < 0 {
		fail(w, r, invalid("version must be >= 1 when given"))
		return
	}
	run, replayed, err := s.store.ExplainVariance(r.Context(), idem, store.ExplainVariance{
		ParallelRunID: id, VarianceID: vid, Reason: req.Reason, ExpectedVersion: req.Version, By: p.UserID,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	respond(w, http.StatusOK, replayed, toParallelDTO(run, true))
}

type signOffRequest struct {
	Version int `json:"version,omitempty"`
}

// signOffParallelRun signs the run off as the calling reviewer (FR-49.5 step 6). It is refused
// with MIG-1509 while any variance is unexplained.
func (s *Server) signOffParallelRun(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var req signOffRequest
	idem, err := readCommand(r, &req, 1<<16)
	if err != nil {
		fail(w, r, err)
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if req.Version < 0 {
		fail(w, r, invalid("version must be >= 1 when given"))
		return
	}
	run, replayed, err := s.store.SignOffParallelRun(r.Context(), idem, store.SignOff{ParallelRunID: id, ExpectedVersion: req.Version, By: p.UserID})
	if err != nil {
		fail(w, r, err)
		return
	}
	respond(w, http.StatusOK, replayed, toParallelDTO(run, true))
}
