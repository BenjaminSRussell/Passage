package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
)

// toleranceDTO is one configured control-total tolerance (FR-49.2).
type toleranceDTO struct {
	Pack      string    `json:"pack"`
	Total     string    `json:"total"`
	Amount    string    `json:"amount"`
	Reason    string    `json:"reason"`
	Version   int       `json:"version"`
	UpdatedBy string    `json:"updatedBy"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func toToleranceDTO(t domain.ToleranceRule) toleranceDTO {
	return toleranceDTO{Pack: string(t.Pack), Total: t.Total, Amount: t.Amount.String(), Reason: t.Reason, Version: t.Version, UpdatedBy: t.UpdatedBy, UpdatedAt: t.UpdatedAt}
}

// listTolerances returns the tolerances configured for the tenant. A total without a row compares at 0.00.
func (s *Server) listTolerances(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	rules, err := s.store.ListTolerances(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]toleranceDTO, len(rules))
	for i, t := range rules {
		items[i] = toToleranceDTO(t)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

type toleranceRequest struct {
	Pack   string `json:"pack"`
	Total  string `json:"total"`
	Amount string `json:"amount"`
	Reason string `json:"reason"`
	// Version is the stored version of the tolerance being changed; omitted for a new one.
	Version int `json:"version"`
}

// putTolerance sets the tolerance of a (pack, total). A positive amount needs a reason; an amount of 0
// returns the total to an exact comparison.
func (s *Server) putTolerance(w http.ResponseWriter, r *http.Request) {
	var req toleranceRequest
	idem, err := readCommand(r, &req, 1<<16)
	if err != nil {
		fail(w, r, err)
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	pack, err := domain.ParsePack(req.Pack)
	if err != nil {
		fail(w, r, err)
		return
	}
	amount, err := decimal.NewFromString(strings.TrimSpace(req.Amount))
	if err != nil {
		fail(w, r, invalid("amount must be a decimal number"))
		return
	}
	if req.Version < 0 {
		fail(w, r, invalid("version must be >= 1 when given"))
		return
	}
	rule := domain.ToleranceRule{Pack: pack, Total: strings.TrimSpace(req.Total), Amount: amount, Reason: strings.TrimSpace(req.Reason)}
	if err := rule.Validate(); err != nil {
		fail(w, r, err)
		return
	}
	out, replayed, err := s.store.PutTolerance(r.Context(), idem, store.ToleranceChange{Rule: rule, ExpectedVersion: req.Version, By: p.UserID})
	if err != nil {
		fail(w, r, err)
		return
	}
	respond(w, http.StatusOK, replayed, toToleranceDTO(out))
}
