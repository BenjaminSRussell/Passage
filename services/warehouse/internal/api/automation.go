package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/store"
)

// Duties of RFID ingest and warehouse automation (wave 32, FR-32.9, FR-32.12). A reader or portal posts its reads with
// the ingest duty; the iot-edge connector of a PLC pulls transport orders, reports them and posts the PLC's events
// with the automation duty. Neither is a person's duty.
const (
	dutyRFIDIngest = "warehouse.rfid.ingest"
	dutyAutomation = "warehouse.automation.execute"
)

const (
	maxRFIDBatch       = 500
	maxAutomationBatch = 200
)

var stationName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,8}$`)

// linkSession is the connector's name for one link session to a PLC; the PLC's sequence numbers restart with each.
var linkSession = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

// registerAutomation mounts the RFID read and automation routes inside the tenant-checked group.
func (a *API) registerAutomation(r chi.Router) {
	r.With(authz.RequireDuty(dutyRFIDIngest)).Post("/v1/warehouse/rfid-reads:ingest", a.ingestRFID)
	r.With(authz.RequireDuty(dutyInquiry)).Get("/v1/warehouse/rfid-reads", a.listRFID)
	r.With(authz.RequireDuty(dutyWorkSupervise)).Post("/v1/warehouse/automation/transport-orders", a.createTransportOrder)
	r.With(authz.Require(anyDuty{dutyInquiry, dutyAutomation}, dutyInquiry+" or "+dutyAutomation, "")).Get("/v1/warehouse/automation/transport-orders", a.listTransportOrders)
	r.With(authz.RequireDuty(dutyAutomation)).Post("/v1/warehouse/automation/transport-orders/{id}:report", a.reportTransportOrder)
	r.With(authz.RequireDuty(dutyAutomation)).Post("/v1/warehouse/automation/events", a.ingestAutomationEvents)
}

type rfidReadInput struct {
	ReaderID     string `json:"readerId"`
	Antenna      int    `json:"antenna"`
	LocationCode string `json:"locationCode"`
	EPC          string `json:"epc"`
	RSSI         *int   `json:"rssi"`
	ReadAt       string `json:"readAt"`
}

type rfidIngestInput struct {
	Reads []rfidReadInput `json:"reads"`
}

// ingestRFID stores the reads of a reader or portal. A read whose tag cannot be decoded is reported with its position
// and does not stop the rest; a tag read again within seconds at the same reader is counted as a duplicate.
func (a *API) ingestRFID(w http.ResponseWriter, r *http.Request) {
	var in rfidIngestInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if n := len(in.Reads); n == 0 || n > maxRFIDBatch {
		fields["reads"] = fmt.Sprintf("must hold 1 to %d reads", maxRFIDBatch)
	}
	reads := make([]store.RFIDRead, 0, len(in.Reads))
	for i, rd := range in.Reads {
		p := fmt.Sprintf("reads[%d]", i)
		reader := strings.TrimSpace(rd.ReaderID)
		if reader == "" || len(reader) > 64 {
			fields[p+".readerId"] = "is required, at most 64 characters"
		}
		if rd.Antenna < 0 || rd.Antenna > 255 {
			fields[p+".antenna"] = "must be 0 to 255"
		}
		if len(rd.LocationCode) > 60 {
			fields[p+".locationCode"] = "must be at most 60 characters"
		}
		if rd.RSSI != nil && (*rd.RSSI > 0 || *rd.RSSI < -200) {
			fields[p+".rssi"] = "must be a dBm value from -200 to 0"
		}
		at, err := time.Parse(time.RFC3339Nano, rd.ReadAt)
		switch {
		case err != nil:
			fields[p+".readAt"] = "must be a time, RFC 3339"
		case at.After(time.Now().Add(5 * time.Minute)):
			fields[p+".readAt"] = "is in the future; check the reader's clock"
		}
		reads = append(reads, store.RFIDRead{ReaderID: reader, Antenna: rd.Antenna, LocationCode: strings.TrimSpace(rd.LocationCode), EPC: rd.EPC, RSSI: rd.RSSI, ReadAt: at.UTC()})
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.st.IngestRFID(r.Context(), reads)
	if err != nil {
		fail(w, r, err, codeReceiptNotFound)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listRFID(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fields := map[string]string{}
	limit := pageLimit(r, fields)
	f := store.RFIDFilter{Key: q.Get("key"), ReaderID: q.Get("readerId"), Limit: limit}
	for name, dst := range map[string]*time.Time{"from": &f.From, "to": &f.To} {
		if raw := q.Get(name); raw != "" {
			t, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				fields[name] = "must be a time, RFC 3339"
			}
			*dst = t.UTC()
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.st.ListRFIDReads(r.Context(), f)
	if err != nil {
		fail(w, r, err, codeReceiptNotFound)
		return
	}
	httpx.JSON(w, http.StatusOK, page[store.RFIDReadRow]{Items: out})
}

type transportOrderInput struct {
	WorkID   string `json:"workId"`
	LineNo   int    `json:"lineNo"`
	Station  string `json:"station"`
	LoadUnit string `json:"loadUnit"`
	// FromLocation and ToLocation name the PLC's infeed or outfeed point when the line has only one location.
	FromLocation string `json:"fromLocation"`
	ToLocation   string `json:"toLocation"`
}

// createTransportOrder gives one line of a work to a PLC station as a MOVE order.
func (a *API) createTransportOrder(w http.ResponseWriter, r *http.Request) {
	var in transportOrderInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(in.WorkID) {
		fields["workId"] = "must be a UUID"
	}
	if in.LineNo < 1 {
		fields["lineNo"] = "must be a positive line number"
	}
	if !stationName.MatchString(in.Station) {
		fields["station"] = "must be the PLC's station name, 1 to 8 letters, digits, '.', '_' or '-'"
	}
	if in.LoadUnit != "" && !domain.ValidSSCC(in.LoadUnit) {
		fields["loadUnit"] = "must be an 18-digit SSCC with a valid check digit"
	}
	for name, v := range map[string]string{"fromLocation": in.FromLocation, "toLocation": in.ToLocation} {
		if len(strings.TrimSpace(v)) > 12 {
			fields[name] = "must be at most 12 characters, the width of a telegram's location"
		}
	}
	who := user(r, fields)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.st.CreateAutomationOrder(r.Context(), store.OrderInput{WorkID: strings.ToLower(in.WorkID), LineNo: in.LineNo, Station: in.Station, LoadUnit: in.LoadUnit,
		FromLocation: in.FromLocation, ToLocation: in.ToLocation, User: who, Now: time.Now()})
	if err != nil {
		automationFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) listTransportOrders(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fields := map[string]string{}
	limit := pageLimit(r, fields)
	switch s := q.Get("status"); s {
	case "", store.OrderPending, store.OrderSent, store.OrderRefused, store.OrderArrived, store.OrderDiverted, store.OrderFault, store.OrderCancelled:
	default:
		fields["status"] = "must be PENDING, SENT, REFUSED, ARRIVED, DIVERTED, FAULT or CANCELLED"
	}
	if s := q.Get("station"); s != "" && !stationName.MatchString(s) {
		fields["station"] = "must be a station name, 1 to 8 characters"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.st.ListAutomationOrders(r.Context(), store.OrderFilter{Station: q.Get("station"), Status: q.Get("status"), Limit: limit})
	if err != nil {
		automationFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, page[store.AutomationOrder]{Items: out})
}

type orderReportInput struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func (a *API) reportTransportOrder(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in orderReportInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeAutoOrderNotFound, "automation order not found"))
		return
	}
	if in.Status != store.OrderSent && in.Status != store.OrderRefused {
		fields["status"] = "must be SENT or REFUSED"
	}
	if len(in.Reason) > 500 {
		fields["reason"] = "must be at most 500 characters"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.st.ReportAutomationOrder(r.Context(), strings.ToLower(id), in.Status, in.Reason, time.Now())
	if err != nil {
		automationFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type automationEventInput struct {
	Kind     string `json:"kind"`
	Session  string `json:"session"`
	Seq      int    `json:"seq"`
	LoadUnit string `json:"loadUnit"`
	Location string `json:"location"`
	Lane     string `json:"lane"`
	Code     string `json:"code"`
	Text     string `json:"text"`
	At       string `json:"at"`
}

type automationEventsInput struct {
	Station string                 `json:"station"`
	Events  []automationEventInput `json:"events"`
}

// ingestAutomationEvents takes the events a PLC reported (arrived, diverted, fault), once each by station, link
// session and sequence. An arrival of a load unit that has an order sent to the station confirms the order's work line.
func (a *API) ingestAutomationEvents(w http.ResponseWriter, r *http.Request) {
	var in automationEventsInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !stationName.MatchString(in.Station) {
		fields["station"] = "must be the PLC's station name, 1 to 8 characters"
	}
	if n := len(in.Events); n == 0 || n > maxAutomationBatch {
		fields["events"] = fmt.Sprintf("must hold 1 to %d events", maxAutomationBatch)
	}
	events := make([]store.AutomationEvent, 0, len(in.Events))
	for i, e := range in.Events {
		p := fmt.Sprintf("events[%d]", i)
		if e.Kind != "arrived" && e.Kind != "diverted" && e.Kind != "fault" {
			fields[p+".kind"] = "must be arrived, diverted or fault"
		}
		if !linkSession.MatchString(e.Session) {
			fields[p+".session"] = "must name the connector's link session to the PLC, 1 to 64 letters, digits, '.', '_', ':' or '-'"
		}
		if e.Seq < 0 {
			fields[p+".seq"] = "must not be negative"
		}
		if e.LoadUnit != "" && !domain.ValidSSCC(e.LoadUnit) {
			fields[p+".loadUnit"] = "must be an 18-digit SSCC with a valid check digit"
		}
		if len(e.Location) > 60 || len(e.Lane) > 20 || len(e.Code) > 8 || len(e.Text) > 40 {
			fields[p] = "a field is longer than the telegram allows"
		}
		at, err := time.Parse(time.RFC3339Nano, e.At)
		if err != nil {
			fields[p+".at"] = "must be a time, RFC 3339"
		}
		events = append(events, store.AutomationEvent{Kind: e.Kind, Session: e.Session, Seq: e.Seq, LoadUnit: e.LoadUnit, Location: e.Location, Lane: e.Lane, Code: e.Code, Text: e.Text, At: at.UTC()})
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.st.IngestAutomationEvents(r.Context(), in.Station, events, time.Now())
	if err != nil {
		automationFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// automationFail maps the automation codes, then the common failures.
func automationFail(w http.ResponseWriter, r *http.Request, err error) {
	var de *domain.Error
	if errors.As(err, &de) {
		switch de.Code {
		case store.CodeAutoOrderNotFound:
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, de.Code, de.Detail))
			return
		case store.CodeAutoOrderExists, store.CodeAutoOrderState:
			httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, de.Code, de.Detail))
			return
		}
	}
	fail(w, r, err, codeReceiptNotFound)
}
