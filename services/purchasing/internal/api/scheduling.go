package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// Scheduling agreement request bodies (FR-17.15). Profile fields left out take
// the PRD 3.17.13 default (FRC 90, JIT 14, firm 14, trade-off 28 days, weekly).
type saProfileBody struct {
	FRCHorizonDays   *int   `json:"frcHorizonDays,omitempty"`
	JITHorizonDays   *int   `json:"jitHorizonDays,omitempty"`
	FirmZoneDays     *int   `json:"firmZoneDays,omitempty"`
	TradeoffZoneDays *int   `json:"tradeoffZoneDays,omitempty"`
	ReleaseFrequency string `json:"releaseFrequency,omitempty"`
}

type saLineBody struct {
	LineNo       int    `json:"lineNo,omitempty"`
	DeliveryDate string `json:"deliveryDate"`
	Qty          string `json:"qty"`
	Reason       string `json:"reason,omitempty"`
}

type saCreateBody struct {
	LegalEntityID string         `json:"legalEntityId"`
	VendorID      string         `json:"vendorId"`
	ProductID     string         `json:"productId"`
	SiteID        string         `json:"siteId"`
	Currency      string         `json:"currency"`
	Unit          string         `json:"unit"`
	UnitPrice     string         `json:"unitPrice"`
	TargetQty     string         `json:"targetQty,omitempty"`
	ValidFrom     string         `json:"validFrom"`
	ValidTo       string         `json:"validTo,omitempty"`
	Profile       *saProfileBody `json:"profile,omitempty"`
	Lines         []saLineBody   `json:"lines"`
}

type saScheduleBody struct {
	Lines []saLineBody `json:"lines"`
}

type saReleaseBody struct {
	Kind            string `json:"kind"`
	RunDate         string `json:"runDate,omitempty"`
	CumulativeReset bool   `json:"cumulativeReset,omitempty"`
}

// Wire forms.
type saProfileDTO struct {
	FRCHorizonDays   int    `json:"frcHorizonDays"`
	JITHorizonDays   int    `json:"jitHorizonDays"`
	FirmZoneDays     int    `json:"firmZoneDays"`
	TradeoffZoneDays int    `json:"tradeoffZoneDays"`
	ReleaseFrequency string `json:"releaseFrequency"`
}

type saLineDTO struct {
	LineNo          int        `json:"lineNo"`
	DeliveryDate    domain.Day `json:"deliveryDate"`
	Zone            string     `json:"zone"`
	Qty             string     `json:"qty"`
	ReceivedQty     string     `json:"receivedQty"`
	OpenQty         string     `json:"openQty"`
	ReductionReason string     `json:"reductionReason,omitempty"`
}

type saDTO struct {
	ID            string       `json:"id"`
	Number        string       `json:"number"`
	LegalEntityID string       `json:"legalEntityId"`
	VendorID      string       `json:"vendorId"`
	ProductID     string       `json:"productId"`
	SiteID        string       `json:"siteId"`
	Currency      string       `json:"currency"`
	Unit          string       `json:"unit"`
	UnitPrice     string       `json:"unitPrice"`
	TargetQty     string       `json:"targetQty"`
	ScheduledQty  string       `json:"scheduledQty"`
	ReceivedQty   string       `json:"receivedQty"`
	ValidFrom     domain.Day   `json:"validFrom"`
	ValidTo       *domain.Day  `json:"validTo,omitempty"`
	Profile       saProfileDTO `json:"profile"`
	ZonesAsOf     domain.Day   `json:"zonesAsOf"`
	Version       int          `json:"version"`
	Lines         []saLineDTO  `json:"lines"`
	Warnings      []string     `json:"warnings,omitempty"`
	CreatedAt     time.Time    `json:"createdAt"`
	UpdatedAt     time.Time    `json:"updatedAt"`
}

type saMonitorDTO struct {
	ID                string      `json:"id"`
	Number            string      `json:"number"`
	LegalEntityID     string      `json:"legalEntityId"`
	VendorID          string      `json:"vendorId"`
	ProductID         string      `json:"productId"`
	SiteID            string      `json:"siteId"`
	Unit              string      `json:"unit"`
	ValidFrom         domain.Day  `json:"validFrom"`
	ValidTo           *domain.Day `json:"validTo,omitempty"`
	ReleaseFrequency  string      `json:"releaseFrequency"`
	ScheduledQty      string      `json:"scheduledQty"`
	ReceivedQty       string      `json:"receivedQty"`
	OpenQty           string      `json:"openQty"`
	ReleaseCount      int         `json:"releaseCount"`
	LastReleaseNumber string      `json:"lastReleaseNumber,omitempty"`
	LastReleaseKind   string      `json:"lastReleaseKind,omitempty"`
	LastRunDate       *domain.Day `json:"lastRunDate,omitempty"`
	NextReleaseDue    *domain.Day `json:"nextReleaseDue,omitempty"`
	Version           int         `json:"version"`
}

type saReleaseLineDTO struct {
	LineNo             int        `json:"lineNo"`
	ScheduleLineNo     int        `json:"scheduleLineNo"`
	DeliveryDate       domain.Day `json:"deliveryDate"`
	Zone               string     `json:"zone"`
	InHorizon          bool       `json:"inHorizon"`
	Qty                string     `json:"qty"`
	CumulativeReleased string     `json:"cumulativeReleased"`
	CumulativeReceived string     `json:"cumulativeReceived"`
}

type saReleaseDTO struct {
	ID                 string             `json:"id"`
	AgreementID        string             `json:"agreementId"`
	ReleaseNo          int                `json:"releaseNo"`
	Number             string             `json:"number"`
	Kind               string             `json:"kind"`
	RunDate            domain.Day         `json:"runDate"`
	AgreementVersion   int                `json:"agreementVersion"`
	CumulativeReset    bool               `json:"cumulativeReset"`
	CumulativeReleased string             `json:"cumulativeReleased"`
	CumulativeReceived string             `json:"cumulativeReceived"`
	Lines              []saReleaseLineDTO `json:"lines,omitempty"`
}

func (a *API) createSchedulingAgreement(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body saCreateBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	sa, err := buildSchedulingAgreement(body)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := inScope(p, sa.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.CreateSchedulingAgreement(r.Context(), sa, store.Idem{Key: key, Hash: hashBody("scheduling-agreement.create", body)})
	if err != nil {
		fail(w, r, err)
		return
	}
	a.schedulingAgreementsCreated.Add(1)
	w.Header().Set("ETag", etag(out.Agreement.Version))
	httpx.JSON(w, http.StatusCreated, a.toSADTO(out, nil))
}

// listSchedulingAgreements is the "Monitor scheduling agreement releases"
// inquiry (FR-17.15, 3.17.14).
func (a *API) listSchedulingAgreements(w http.ResponseWriter, r *http.Request) {
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.SchedulingAgreementFilter{VendorID: q.Get("vendorId"), ProductID: q.Get("productId")}
	if err := uuidField("vendorId", f.VendorID, false); err != nil {
		fail(w, r, err)
		return
	}
	if err := uuidField("productId", f.ProductID, false); err != nil {
		fail(w, r, err)
		return
	}
	page, err := a.store.ListSchedulingAgreements(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]saMonitorDTO, 0, len(page.Items))
	for _, m := range page.Items {
		items = append(items, toSAMonitorDTO(m))
	}
	writePage(w, store.Page[saMonitorDTO]{Items: items, Next: page.Next})
}

func (a *API) getSchedulingAgreement(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.GetSchedulingAgreement(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Agreement.Version))
	httpx.JSON(w, http.StatusOK, a.toSADTO(out, nil))
}

// replaceSchedule replaces the release schedule (FR-17.15, BR-17.13) under
// If-Match. Rows with lineNo change that line (quantity 0 removes it with its
// reason), rows without one are added, and existing lines left out are
// removed. Zones are judged on today's date.
func (a *API) replaceSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body saScheduleBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	changes, err := buildScheduleChanges(body.Lines)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, warnings, err := a.store.ReplaceSchedule(r.Context(), id, version, a.today(), changes)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Agreement.Version))
	httpx.JSON(w, http.StatusOK, a.toSADTO(out, warnings))
}

// schedulingAgreementAction serves the ":release" custom method (SAP ME84): a
// release run snapshots the schedule as an FRC or JIT release. 201 when a
// release was created; 200 with the existing release when the schedule did not
// change since a run of the same kind on the same date, or on a replayed key.
func (a *API) schedulingAgreementAction(w http.ResponseWriter, r *http.Request) {
	id, action, found := strings.Cut(chi.URLParam(r, "id"), ":")
	if !found || action != "release" || !ids.Valid(id) {
		fail(w, r, httpx.NewProblem(http.StatusNotFound, codeUnknownAction, "unknown scheduling agreement action"))
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body saReleaseBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if body.Kind != domain.ReleaseFRC && body.Kind != domain.ReleaseJIT {
		fail(w, r, invalid("kind", "must be FRC or JIT"))
		return
	}
	runDate := a.today()
	if body.RunDate != "" {
		if runDate, err = dateField("runDate", body.RunDate); err != nil {
			fail(w, r, err)
			return
		}
	}
	idem := store.Idem{Key: key, Hash: hashBody("scheduling-agreement.release", map[string]any{"id": id, "body": body})}
	rel, created, err := a.store.RunSchedulingRelease(r.Context(), id, body.Kind, runDate, body.CumulativeReset, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		a.saReleasesCreated.Add(1)
		status = http.StatusCreated
	}
	httpx.JSON(w, status, toSAReleaseDTO(rel, true))
}

func (a *API) listSchedulingReleases(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	rels, err := a.store.ListSchedulingReleases(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]saReleaseDTO, 0, len(rels))
	for _, rel := range rels {
		items = append(items, toSAReleaseDTO(rel, false))
	}
	httpx.JSON(w, http.StatusOK, pageBody[saReleaseDTO]{Items: items})
}

// getSchedulingRelease is the release documentation (SAP ME86) of one release number.
func (a *API) getSchedulingRelease(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	no, err := strconv.Atoi(chi.URLParam(r, "releaseNo"))
	if err != nil || no < 1 {
		fail(w, r, invalid("releaseNo", "must be a release number from 1"))
		return
	}
	rel, err := a.store.GetSchedulingRelease(r.Context(), id, no)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toSAReleaseDTO(rel, true))
}

// buildSchedulingAgreement validates a create request's shape and returns the
// agreement; schedule rows are numbered in request order. The agreement rules
// (validity, zones, line dates) are the domain's.
func buildSchedulingAgreement(body saCreateBody) (domain.SchedulingAgreement, error) {
	for _, f := range []struct{ name, raw string }{
		{"legalEntityId", body.LegalEntityID}, {"vendorId", body.VendorID},
		{"productId", body.ProductID}, {"siteId", body.SiteID},
	} {
		if err := uuidField(f.name, f.raw, true); err != nil {
			return domain.SchedulingAgreement{}, err
		}
	}
	cur, err := currencyField(body.Currency)
	if err != nil {
		return domain.SchedulingAgreement{}, err
	}
	unit := strings.TrimSpace(body.Unit)
	if unit == "" {
		return domain.SchedulingAgreement{}, invalid("unit", "is required")
	}
	price, err := decimalField("unitPrice", body.UnitPrice, "")
	if err != nil {
		return domain.SchedulingAgreement{}, err
	}
	if price.IsNegative() {
		return domain.SchedulingAgreement{}, invalid("unitPrice", "must be zero or more")
	}
	target, err := decimalField("targetQty", body.TargetQty, "0")
	if err != nil {
		return domain.SchedulingAgreement{}, err
	}
	if target.IsNegative() {
		return domain.SchedulingAgreement{}, invalid("targetQty", "must be zero or more")
	}
	from, err := dateField("validFrom", body.ValidFrom)
	if err != nil {
		return domain.SchedulingAgreement{}, err
	}
	var to *domain.Day
	if body.ValidTo != "" {
		d, err := dateField("validTo", body.ValidTo)
		if err != nil {
			return domain.SchedulingAgreement{}, err
		}
		if d.Before(from.Time) {
			return domain.SchedulingAgreement{}, invalid("validTo", "must not be before validFrom")
		}
		to = &d
	}
	profile, err := buildProfile(body.Profile)
	if err != nil {
		return domain.SchedulingAgreement{}, err
	}
	if len(body.Lines) == 0 || len(body.Lines) > domain.MaxScheduleLines {
		return domain.SchedulingAgreement{}, invalid("lines", "must hold from 1 to "+strconv.Itoa(domain.MaxScheduleLines)+" schedule lines")
	}
	out := domain.SchedulingAgreement{
		LegalEntityID: body.LegalEntityID, VendorID: body.VendorID, ProductID: body.ProductID, SiteID: body.SiteID,
		Currency: cur, Unit: unit, UnitPrice: price, TargetQty: target, ValidFrom: from, ValidTo: to, Profile: profile,
	}
	for i, l := range body.Lines {
		field := func(n string) string { return "lines[" + strconv.Itoa(i) + "]." + n }
		date, err := dateField(field("deliveryDate"), l.DeliveryDate)
		if err != nil {
			return domain.SchedulingAgreement{}, err
		}
		qty, err := positiveQty(field("qty"), l.Qty)
		if err != nil {
			return domain.SchedulingAgreement{}, err
		}
		out.Lines = append(out.Lines, domain.ScheduleLine{LineNo: i + 1, DeliveryDate: date, Qty: qty})
	}
	return out, nil
}

// buildProfile fills the release profile from the request over the defaults.
func buildProfile(body *saProfileBody) (domain.ReleaseProfile, error) {
	p := domain.DefaultReleaseProfile()
	if body == nil {
		return p, nil
	}
	for _, f := range []struct {
		name string
		raw  *int
		dst  *int
	}{
		{"profile.frcHorizonDays", body.FRCHorizonDays, &p.FRCHorizonDays},
		{"profile.jitHorizonDays", body.JITHorizonDays, &p.JITHorizonDays},
		{"profile.firmZoneDays", body.FirmZoneDays, &p.FirmDays},
		{"profile.tradeoffZoneDays", body.TradeoffZoneDays, &p.TradeoffDays},
	} {
		if f.raw == nil {
			continue
		}
		if *f.raw < 0 || *f.raw > 3660 {
			return p, invalid(f.name, "must be from 0 to 3660 days")
		}
		*f.dst = *f.raw
	}
	if p.TradeoffDays < p.FirmDays {
		return p, invalid("profile.tradeoffZoneDays", "must not be shorter than the firm zone")
	}
	if body.ReleaseFrequency != "" {
		switch body.ReleaseFrequency {
		case domain.FrequencyDaily, domain.FrequencyWeekly, domain.FrequencyMonthly:
			p.ReleaseFrequency = body.ReleaseFrequency
		default:
			return p, invalid("profile.releaseFrequency", "must be DAILY, WEEKLY or MONTHLY")
		}
	}
	return p, nil
}

// buildScheduleChanges turns the request rows into schedule changes; the
// domain judges them against zones and receipts.
func buildScheduleChanges(lines []saLineBody) ([]domain.ScheduleChange, error) {
	if len(lines) == 0 || len(lines) > domain.MaxScheduleLines {
		return nil, invalid("lines", "must hold from 1 to "+strconv.Itoa(domain.MaxScheduleLines)+" schedule lines")
	}
	out := make([]domain.ScheduleChange, 0, len(lines))
	for i, l := range lines {
		field := func(n string) string { return "lines[" + strconv.Itoa(i) + "]." + n }
		if l.LineNo < 0 {
			return nil, invalid(field("lineNo"), "must be an existing line number, or left out for a new line")
		}
		date, err := dateField(field("deliveryDate"), l.DeliveryDate)
		if err != nil {
			return nil, err
		}
		qty, err := decimalField(field("qty"), l.Qty, "")
		if err == nil && l.Qty == "" {
			err = invalid(field("qty"), "is required")
		}
		if err != nil {
			return nil, err
		}
		// Zero removes an existing line (with its reason); a new line needs more.
		if qty.IsNegative() || (qty.IsZero() && l.LineNo == 0) {
			return nil, invalid(field("qty"), "must be greater than zero, or zero to remove an existing line")
		}
		reason := strings.TrimSpace(l.Reason)
		if len(reason) > 64 {
			return nil, invalid(field("reason"), "must be at most 64 characters")
		}
		out = append(out, domain.ScheduleChange{LineNo: l.LineNo, DeliveryDate: date, Qty: qty, Reason: reason})
	}
	return out, nil
}

// positiveQty parses a required quantity that must be above zero.
func positiveQty(field, raw string) (decimal.Decimal, error) {
	if raw == "" {
		return decimal.Zero, invalid(field, "is required")
	}
	q, err := decimalField(field, raw, "")
	if err != nil {
		return decimal.Zero, err
	}
	if !q.IsPositive() {
		return decimal.Zero, invalid(field, "must be greater than zero")
	}
	return q, nil
}

func (a *API) toSADTO(d store.SchedulingAgreementDetail, warnings []string) saDTO {
	sa := d.Agreement
	asOf := a.today()
	zones := sa.Profile.Zones()
	dto := saDTO{
		ID: sa.ID, Number: sa.Number, LegalEntityID: sa.LegalEntityID, VendorID: sa.VendorID, ProductID: sa.ProductID,
		SiteID: sa.SiteID, Currency: sa.Currency, Unit: sa.Unit, UnitPrice: sa.UnitPrice.String(), TargetQty: sa.TargetQty.String(),
		ValidFrom: sa.ValidFrom, ValidTo: sa.ValidTo, ZonesAsOf: asOf, Version: sa.Version, Warnings: warnings,
		Profile: saProfileDTO{
			FRCHorizonDays: sa.Profile.FRCHorizonDays, JITHorizonDays: sa.Profile.JITHorizonDays,
			FirmZoneDays: sa.Profile.FirmDays, TradeoffZoneDays: sa.Profile.TradeoffDays, ReleaseFrequency: sa.Profile.ReleaseFrequency,
		},
		Lines: make([]saLineDTO, 0, len(sa.Lines)), CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
	scheduled, received := decimal.Zero, decimal.Zero
	for _, l := range sa.Lines {
		scheduled, received = scheduled.Add(l.Qty), received.Add(l.ReceivedQty)
		dto.Lines = append(dto.Lines, saLineDTO{
			LineNo: l.LineNo, DeliveryDate: l.DeliveryDate, Zone: zones.ClassifyZone(asOf, l.DeliveryDate),
			Qty: l.Qty.String(), ReceivedQty: l.ReceivedQty.String(), OpenQty: l.Qty.Sub(l.ReceivedQty).String(),
			ReductionReason: l.ReductionReason,
		})
	}
	dto.ScheduledQty, dto.ReceivedQty = scheduled.String(), received.String()
	return dto
}

func toSAMonitorDTO(m store.SAMonitorRow) saMonitorDTO {
	return saMonitorDTO{
		ID: m.ID, Number: m.Number, LegalEntityID: m.LegalEntityID, VendorID: m.VendorID, ProductID: m.ProductID, SiteID: m.SiteID,
		Unit: m.Unit, ValidFrom: m.ValidFrom, ValidTo: m.ValidTo, ReleaseFrequency: m.ReleaseFrequency,
		ScheduledQty: m.Scheduled.String(), ReceivedQty: m.Received.String(), OpenQty: m.Open.String(),
		ReleaseCount: m.ReleaseCount, LastReleaseNumber: m.LastReleaseNumber, LastReleaseKind: m.LastReleaseKind,
		LastRunDate: m.LastRunDate, NextReleaseDue: m.NextReleaseDue, Version: m.Version,
	}
}

// toSAReleaseDTO renders a release; withLines false gives the header only.
func toSAReleaseDTO(rel domain.SARelease, withLines bool) saReleaseDTO {
	dto := saReleaseDTO{
		ID: rel.ID, AgreementID: rel.AgreementID, ReleaseNo: rel.ReleaseNo, Number: rel.Number, Kind: rel.Kind,
		RunDate: rel.RunDate, AgreementVersion: rel.AgreementVersion, CumulativeReset: rel.CumulativeReset,
		CumulativeReleased: rel.CumulativeReleased.String(), CumulativeReceived: rel.CumulativeReceived.String(),
	}
	if !withLines {
		return dto
	}
	dto.Lines = make([]saReleaseLineDTO, 0, len(rel.Lines))
	for _, l := range rel.Lines {
		dto.Lines = append(dto.Lines, saReleaseLineDTO{
			LineNo: l.LineNo, ScheduleLineNo: l.ScheduleLineNo, DeliveryDate: l.DeliveryDate, Zone: l.Zone, InHorizon: l.InHorizon,
			Qty: l.Qty.String(), CumulativeReleased: l.CumulativeReleased.String(), CumulativeReceived: l.CumulativeReceived.String(),
		})
	}
	return dto
}
