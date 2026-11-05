package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

// Concurrency and scope codes of the partner update. 428 and 412 use the
// platform codes (docs/architecture.md, optimistic concurrency).
const (
	CodeIfMatchRequired = "COM-0428"
	CodeVersionConflict = "conflict_version"
	CodePartnerScope    = "EDI-0018"
)

// patchString is a JSON merge patch member: absent leaves the field as it
// is, null or "" clears it, a string sets it.
type patchString struct {
	set   bool
	value string
}

func (p *patchString) UnmarshalJSON(b []byte) error {
	p.set = true
	if string(b) == "null" {
		p.value = ""
		return nil
	}
	return json.Unmarshal(b, &p.value)
}

// partnerPatch is the body of PATCH /partners/{id} (JSON merge patch). The
// environment cannot change (an unknown field is refused): a partner moves
// from TEST to PROD as a new partner. Clearing edifactId removes the EDIFACT
// identity with its qualifier and CONTRL mode.
type partnerPatch struct {
	Name             *string     `json:"name"`
	ISAQualifier     *string     `json:"isaQualifier"`
	ISAID            *string     `json:"isaId"`
	AckSLAMinutes    *int        `json:"ackSlaMinutes"`
	Status           *string     `json:"status"`
	EdifactID        patchString `json:"edifactId"`
	EdifactQualifier patchString `json:"edifactQualifier"`
	EdifactContrl    patchString `json:"edifactContrl"`
}

// apply merges the patch onto the stored partner and returns the profile and
// status to store, with field errors.
func (p partnerPatch) apply(cur store.Partner) (domain.PartnerProfile, string, map[string]string) {
	prof := domain.PartnerProfile{Name: cur.Name, ISA: domain.Party{Qualifier: cur.ISAQualifier, ID: cur.ISAID},
		Environment: cur.Environment, AckSLAMinutes: cur.AckSLAMinutes, EDIFACT: cur.Edifact()}
	status := cur.Status
	if p.Name != nil {
		prof.Name = *p.Name
	}
	if p.ISAQualifier != nil {
		prof.ISA.Qualifier = *p.ISAQualifier
	}
	if p.ISAID != nil {
		prof.ISA.ID = *p.ISAID
	}
	fields := map[string]string{}
	if p.AckSLAMinutes != nil {
		if *p.AckSLAMinutes == 0 {
			fields["ackSlaMinutes"] = "must be 5 to 1440"
		}
		prof.AckSLAMinutes = *p.AckSLAMinutes
	}
	if p.Status != nil {
		status = *p.Status
		if status != "active" && status != "inactive" {
			fields["status"] = "must be active or inactive"
		}
	}
	if p.EdifactID.set || p.EdifactQualifier.set || p.EdifactContrl.set {
		e := domain.EdifactIdentity{}
		if prof.EDIFACT != nil {
			e = *prof.EDIFACT
		}
		if p.EdifactID.set {
			e.ID = p.EdifactID.value
		}
		if p.EdifactQualifier.set {
			e.Qualifier = p.EdifactQualifier.value
		}
		if p.EdifactContrl.set {
			e.Contrl = p.EdifactContrl.value
		}
		switch {
		case e.ID != "":
			prof.EDIFACT = &e
		case p.EdifactQualifier.set && e.Qualifier != "" || p.EdifactContrl.set && e.Contrl != "":
			fields["edifactId"] = "is required with edifactQualifier and edifactContrl"
		default:
			prof.EDIFACT = nil
		}
	}
	for k, v := range prof.Validate() {
		fields[k] = v
	}
	return prof, status, fields
}

// ifMatchVersion reads the required If-Match header carrying the version
// (the ETag of GET /partners/{id}, "3"; W/ and unquoted forms are read too).
func ifMatchVersion(r *http.Request) (int, *httpx.Problem) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		return 0, httpx.NewProblem(http.StatusPreconditionRequired, CodeIfMatchRequired, "If-Match header with the partner version (its ETag) is required")
	}
	v, err := strconv.Atoi(strings.Trim(strings.TrimPrefix(raw, "W/"), `"`))
	if err != nil || v < 1 {
		return 0, validation(map[string]string{"If-Match": `must be the partner's ETag, such as "3"`})
	}
	return v, nil
}

// patchPartner changes a partner (PATCH /partners/{id}, JSON merge patch)
// under If-Match against its version: name, ISA identity, ack SLA, EDIFACT
// identity and CONTRL mode, and status (inactive stops inbound and outbound
// traffic). A partner bound by its trade profile to a legal entity is
// changed only by callers scoped to that entity (403 EDI-0018), as its
// profile and transport are. The response carries the new version as ETag.
func (a *API) patchPartner(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	version, p := ifMatchVersion(r)
	if p != nil {
		httpx.Fail(w, r, p)
		return
	}
	var patch partnerPatch
	if err := httpx.Decode(r, &patch); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	cur, err := a.store.GetPartner(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if cur.Version != version {
		a.versionConflict(w, r, cur)
		return
	}
	prof, status, fields := patch.apply(cur)
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	updated, err := a.store.UpdatePartner(r.Context(), id, version, prof, status)
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(w, r, notFound("partner"))
	case errors.Is(err, store.ErrOutOfScope):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, CodePartnerScope, "the partner's trade profile binds it to a legal entity outside your scope"))
	case errors.Is(err, store.ErrVersionConflict):
		a.versionConflict(w, r, updated)
	case errors.Is(err, store.ErrDuplicate):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodePartnerExists, "a partner with this ISA or EDIFACT identity already exists in this environment"))
	case err != nil:
		httpx.Fail(w, r, err)
	default:
		w.Header().Set("ETag", fmt.Sprintf(`"%d"`, updated.Version))
		httpx.JSON(w, http.StatusOK, updated)
	}
}

func (a *API) versionConflict(w http.ResponseWriter, r *http.Request, cur store.Partner) {
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, cur.Version))
	httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict,
		fmt.Sprintf("If-Match does not match the partner's current version %d; read it again", cur.Version)))
}
