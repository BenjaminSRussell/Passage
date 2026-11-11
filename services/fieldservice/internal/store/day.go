package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
)

// CodeNoTechnician answers a day request from a user who is not a technician's worker.
const CodeNoTechnician = "SVC-TECH-006"

// DayTruck is the technician's truck as an inventory address (BR-45.7).
type DayTruck struct {
	SiteID      string  `json:"siteId"`
	WarehouseID string  `json:"warehouseId"`
	LocationID  *string `json:"locationId,omitempty"`
}

// DayTechnician is the technician a day belongs to.
type DayTechnician struct {
	ID       string    `json:"id"`
	Code     string    `json:"code"`
	Name     string    `json:"name"`
	TimeZone string    `json:"timeZone"`
	Truck    *DayTruck `json:"truck,omitempty"`
}

// DayLine is a work-order line as the technician sees it, with what has been consumed.
type DayLine struct {
	ID          string          `json:"id"`
	Kind        domain.LineKind `json:"kind"`
	Description string          `json:"description,omitempty"`
	ProductID   string          `json:"productId,omitempty"`
	Unit        string          `json:"unit,omitempty"`
	Qty         decimal.Decimal `json:"qty"`
	ConsumedQty decimal.Decimal `json:"consumedQty"`
}

// DayCheck is a checklist item with its state.
type DayCheck struct {
	domain.ChecklistItem
	Done bool `json:"done"`
}

// DayPhoto is a photo attached by an applied PHOTO action.
type DayPhoto struct {
	CaptureID  string    `json:"captureId"`
	Ref        string    `json:"ref"`
	CapturedAt time.Time `json:"capturedAt"`
}

// DaySignature says whether the customer has signed.
type DaySignature struct {
	Signed   bool       `json:"signed"`
	Exempt   bool       `json:"exempt"`
	SignedBy *string    `json:"signedBy,omitempty"`
	SignedAt *time.Time `json:"signedAt,omitempty"`
	Ref      *string    `json:"ref,omitempty"`
}

// DayVisit is one work order on a technician's route.
type DayVisit struct {
	ID                 string       `json:"id"`
	Number             string       `json:"number"`
	Status             string       `json:"status"`
	Version            int          `json:"version"`
	Sequence           int          `json:"sequence"`
	CarryOver          bool         `json:"carryOver"`
	CustomerPartyID    string       `json:"customerPartyId"`
	CaseID             *string      `json:"caseId,omitempty"`
	CaseNumber         *string      `json:"caseNumber,omitempty"`
	CaseDescription    string       `json:"caseDescription,omitempty"`
	Priority           *int         `json:"priority,omitempty"`
	ServiceObjectID    *string      `json:"serviceObjectId,omitempty"`
	ServiceObjectLabel string       `json:"serviceObjectLabel,omitempty"`
	Address            string       `json:"address"`
	Latitude           *float64     `json:"latitude,omitempty"`
	Longitude          *float64     `json:"longitude,omitempty"`
	AssignedStart      time.Time    `json:"assignedStart"`
	AssignedEnd        time.Time    `json:"assignedEnd"`
	WindowStart        *time.Time   `json:"windowStart,omitempty"`
	WindowEnd          *time.Time   `json:"windowEnd,omitempty"`
	ResolutionDue      *time.Time   `json:"resolutionDue,omitempty"`
	EstDurationMinutes int          `json:"estDurationMinutes"`
	RequiredSkills     []string     `json:"requiredSkills"`
	Lines              []DayLine    `json:"lines"`
	Checklist          []DayCheck   `json:"checklist"`
	Photos             []DayPhoto   `json:"photos"`
	Signature          DaySignature `json:"signature"`
	TechnicianNote     string       `json:"technicianNote"`
	CompletedAt        *time.Time   `json:"completedAt,omitempty"`
}

// DayTotals summarizes the route.
type DayTotals struct {
	Visits        int `json:"visits"`
	Completed     int `json:"completed"`
	Open          int `json:"open"`
	BookedMinutes int `json:"bookedMinutes"`
}

// Day is a technician's route for one local date: the published visits in order, with
// everything the device needs to work them offline (FR-45.8).
type Day struct {
	Technician  DayTechnician `json:"technician"`
	Date        string        `json:"date"`
	GeneratedAt time.Time     `json:"generatedAt"`
	Visits      []DayVisit    `json:"visits"`
	Totals      DayTotals     `json:"totals"`
}

// viewingFor checks that the caller may read technician t's day: the technician's own worker,
// or office staff who maintain work orders or dispatch.
func viewingFor(ctx context.Context, t Technician) error {
	p := caller(ctx)
	if t.WorkerID != nil && p.UserID != "" && strings.EqualFold(*t.WorkerID, p.UserID) {
		return nil
	}
	if p.HasDuty("fieldservice.work-order.maintain") || p.HasDuty("fieldservice.technician.dispatch") {
		return nil
	}
	return httpx.NewProblem(http.StatusForbidden, domain.CodeActionTechnician, "a technician's day can be read only by the technician's own worker or by office staff")
}

// TechnicianDay returns the route of the technician on a local date (YYYY-MM-DD in the
// technician's time zone): the work orders assigned to them that start that day and have
// been published, plus orders still being worked from an earlier day (carry-over). The id
// "me" names the technician whose worker is the caller. Visits are in start order.
func (s *Postgres) TechnicianDay(ctx context.Context, technicianID, date string, now time.Time) (Day, error) {
	tid := tenantOf(ctx)
	var out Day
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var t Technician
		var err error
		if technicianID == "me" {
			p := caller(ctx)
			if p.UserID == "" {
				return httpx.NewProblem(http.StatusNotFound, CodeNoTechnician, "no technician is linked to this user")
			}
			t, err = scanTechnician(tx.QueryRow(ctx, `select `+technicianColumns+` from service_technician where tenant_id = $1 and worker_id::text = lower($2) and active order by created_at limit 1`, tid, p.UserID))
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NewProblem(http.StatusNotFound, CodeNoTechnician, "no technician is linked to this user")
			}
		} else {
			t, err = getTechnician(ctx, tx, tid, technicianID, false)
		}
		if err != nil {
			return err
		}
		if err := viewingFor(ctx, t); err != nil {
			return err
		}
		loc, err := time.LoadLocation(t.TimeZone)
		if err != nil {
			return err
		}
		if date == "" {
			date = now.In(loc).Format("2006-01-02")
		}
		day, err := time.ParseInLocation("2006-01-02", date, loc)
		if err != nil {
			return err
		}
		from, to := day, day.AddDate(0, 0, 1)
		out = Day{Technician: DayTechnician{ID: t.ID, Code: t.Code, Name: t.Name, TimeZone: t.TimeZone}, Date: date, GeneratedAt: now.UTC(), Visits: []DayVisit{}}
		if t.TruckSiteID != nil && t.TruckWarehouseID != nil {
			out.Technician.Truck = &DayTruck{SiteID: *t.TruckSiteID, WarehouseID: *t.TruckWarehouseID, LocationID: t.TruckLocationID}
		}
		rows, err := tx.Query(ctx, `with w as (select `+workOrderColumns+` from service_work_order
				where tenant_id = $1 and technician_id = $2
					and ((assigned_start >= $3 and assigned_start < $4 and status in ('DISPATCHED', 'TRAVELING', 'IN_PROGRESS', 'ON_HOLD', 'COMPLETED', 'POSTED'))
						or (assigned_start < $3 and status in ('TRAVELING', 'IN_PROGRESS', 'ON_HOLD'))))
			select w.*, c.number, c.description, c.priority, c.service_object_id::text, c.sla_resolution_due, coalesce(o.serial, ''), (w.assigned_start < $3)
			from w
			left join service_case c on c.tenant_id = $1 and c.id::text = w.case_id
			left join service_object o on o.tenant_id = $1 and o.id = c.service_object_id
			order by w.assigned_start, w.id`, tid, t.ID, from.UTC(), to.UTC())
		if err != nil {
			return err
		}
		type pending struct {
			visit DayVisit
			wo    WorkOrder
		}
		var found []pending
		for rows.Next() {
			var wo WorkOrder
			var caseNumber, caseDesc, objectID, serial *string
			var priority *int
			var due *time.Time
			var carry bool
			var lines, posting, checklist []byte
			if err := rows.Scan(&wo.ID, &wo.Number, &wo.CaseID, &wo.AgreementID, &wo.CustomerPartyID, &wo.LegalEntityID, &wo.TerritoryID,
				&wo.IncidentTypeID, &wo.EstDurationMinutes, &wo.Currency, &wo.Status, &lines, &posting, &wo.Version, &wo.postKey,
				&wo.FollowUpOf, &wo.CompletedAt, &wo.FirstTimeFix, &wo.RequiredSkills, &wo.WindowStart, &wo.WindowEnd, &wo.Address, &wo.Latitude,
				&wo.Longitude, &wo.TechnicianID, &wo.AssignedStart, &wo.AssignedEnd, &wo.AssignmentPinned, &wo.SignatureExempt, &wo.SignedBy, &wo.SignedAt,
				&wo.SignatureRef, &wo.TechnicianNote, &wo.noteAt, &wo.SignedLatitude, &wo.SignedLongitude, &checklist,
				&caseNumber, &caseDesc, &priority, &objectID, &due, &serial, &carry); err != nil {
				rows.Close()
				return err
			}
			if err := json.Unmarshal(lines, &wo.Lines); err != nil {
				rows.Close()
				return err
			}
			if err := json.Unmarshal(checklist, &wo.Checklist); err != nil {
				rows.Close()
				return err
			}
			v := DayVisit{ID: wo.ID, Number: wo.Number, Status: wo.Status, Version: wo.Version, CarryOver: carry, CustomerPartyID: wo.CustomerPartyID,
				CaseID: wo.CaseID, CaseNumber: caseNumber, Priority: priority, ServiceObjectID: objectID, Address: wo.Address, Latitude: wo.Latitude,
				Longitude: wo.Longitude, AssignedStart: *wo.AssignedStart, AssignedEnd: *wo.AssignedEnd, WindowStart: wo.WindowStart, WindowEnd: wo.WindowEnd,
				ResolutionDue: due, EstDurationMinutes: wo.EstDurationMinutes, RequiredSkills: wo.RequiredSkills, TechnicianNote: wo.TechnicianNote,
				CompletedAt: wo.CompletedAt, Photos: []DayPhoto{}}
			if caseDesc != nil {
				v.CaseDescription = *caseDesc
			}
			if serial != nil && *serial != "" {
				v.ServiceObjectLabel = "Serial " + *serial
			}
			if v.RequiredSkills == nil {
				v.RequiredSkills = []string{}
			}
			v.Signature = DaySignature{Signed: wo.SignedAt != nil, Exempt: wo.SignatureExempt, SignedBy: wo.SignedBy, SignedAt: wo.SignedAt, Ref: wo.SignatureRef}
			found = append(found, pending{visit: v, wo: wo})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for i := range found {
			v, wo := &found[i].visit, found[i].wo
			consumed, err := consumedQty(ctx, tx, tid, wo.ID)
			if err != nil {
				return err
			}
			v.Lines = make([]DayLine, 0, len(wo.Lines))
			for _, l := range wo.Lines {
				v.Lines = append(v.Lines, DayLine{ID: l.ID, Kind: l.Kind, Description: l.Description, ProductID: l.ProductID, Unit: l.Unit, Qty: l.Qty, ConsumedQty: consumed[l.ID]})
			}
			st := mergeState{}
			if err := loadCaptureState(ctx, tx, tid, wo.ID, &st); err != nil {
				return err
			}
			done := st.checklistDone()
			v.Checklist = make([]DayCheck, 0, len(wo.Checklist))
			for _, it := range wo.Checklist {
				v.Checklist = append(v.Checklist, DayCheck{ChecklistItem: it, Done: done[it.ID]})
			}
			pr, err := tx.Query(ctx, `select a.device_time, c.id from service_wo_action a, jsonb_array_elements_text(a.payload -> 'captureIds') with ordinality as c(id, n)
				where a.tenant_id = $1 and a.work_order_id = $2 and a.kind = 'PHOTO' and a.outcome = 'APPLIED' order by a.device_time, a.seq, c.n`, tid, wo.ID)
			if err != nil {
				return err
			}
			for pr.Next() {
				var p DayPhoto
				if err := pr.Scan(&p.CapturedAt, &p.CaptureID); err != nil {
					pr.Close()
					return err
				}
				p.Ref = domain.CaptureRef(p.CaptureID)
				v.Photos = append(v.Photos, p)
			}
			pr.Close()
			if err := pr.Err(); err != nil {
				return err
			}
			v.Sequence = i + 1
			out.Visits = append(out.Visits, *v)
			out.Totals.Visits++
			out.Totals.BookedMinutes += v.EstDurationMinutes
			if v.Status == string(domain.WOCompleted) || v.Status == string(domain.WOPosted) {
				out.Totals.Completed++
			} else {
				out.Totals.Open++
			}
		}
		return nil
	})
	return out, err
}
