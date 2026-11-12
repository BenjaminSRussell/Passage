package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/BenjaminSRussell/passage/services/iot-edge/internal/mfs"
)

// Warehouse automation on the edge (wave 32, FR-32.12). The connector dials the plant's PLC (an AS/RS, a
// conveyor or a sorter) over the telegram profile in package mfs, sends the MOVE telegrams the warehouse's
// transport work asks for, and forwards what the PLC reports (arrived, diverted, fault) to the warehouse.

// TransportOrder is one move the warehouse wants a PLC to make: its id in the warehouse and the order.
type TransportOrder struct {
	ID string
	mfs.TransportOrder
}

// Order statuses the connector reports back.
const (
	OrderSent    = "SENT"
	OrderRefused = "REFUSED"
)

// AutomationPlane is the connector's side of the warehouse automation API.
type AutomationPlane interface {
	PullOrders(ctx context.Context, station string) ([]TransportOrder, error)
	ReportOrder(ctx context.Context, id, status, reason string) error
	PostEvents(ctx context.Context, station string, events []mfs.Event) error
}

// Automation runs the link to one PLC.
type Automation struct {
	Plane AutomationPlane
	// Station is the PLC's station name (the receiver of the link and the sender of its telegrams); the
	// warehouse keys its transport orders by it.
	Station string
	// Dial opens the connection to the PLC.
	Dial func(ctx context.Context) (net.Conn, error)
	// Link tunes the telegram session; Sender, Receiver and Handler are set here.
	Link      mfs.Config
	PollEvery time.Duration // how often the warehouse is asked for orders; default 2 s
	// BackoffMin and BackoffMax bound the wait before dialling again (default 1 s and 30 s).
	BackoffMin, BackoffMax time.Duration
	Now                    func() time.Time
	Log                    *slog.Logger

	ordersSent, ordersRefused, eventsForwarded, linkDrops atomic.Int64
}

// AutomationStats are the counters of an Automation.
type AutomationStats struct{ OrdersSent, OrdersRefused, EventsForwarded, LinkDrops int64 }

// Stats returns the counters.
func (a *Automation) Stats() AutomationStats {
	return AutomationStats{a.ordersSent.Load(), a.ordersRefused.Load(), a.eventsForwarded.Load(), a.linkDrops.Load()}
}

func (a *Automation) log() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}

// Run keeps the link up until ctx ends: it dials, runs a session, and dials again after the link fails.
func (a *Automation) Run(ctx context.Context) error {
	lo, hi := a.BackoffMin, a.BackoffMax
	if lo <= 0 {
		lo = time.Second
	}
	if hi <= 0 {
		hi = 30 * time.Second
	}
	wait := lo
	for ctx.Err() == nil {
		start := time.Now()
		err := a.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		a.linkDrops.Add(1)
		a.log().Warn("PLC link ended; dialling again", "station", a.Station, "err", err, "retryIn", wait)
		if time.Since(start) > 30*time.Second {
			wait = lo // it was healthy for a while
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		wait = min(wait*2, hi)
	}
	return nil
}

func (a *Automation) session(ctx context.Context) error {
	conn, err := a.Dial(ctx)
	if err != nil {
		return fmt.Errorf("dial PLC: %w", err)
	}
	defer conn.Close()
	now := a.Now
	if now == nil {
		now = time.Now
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// The PLC numbers its telegrams per link session, from 1 again on this connection: the session name
	// travels with each event so the warehouse does not take a reused number for a repeat.
	session, err := newLinkSession(now())
	if err != nil {
		return err
	}
	cfg := a.Link
	cfg.Receiver = a.Station
	cfg.Handler = func(t mfs.Telegram) error {
		ev, err := mfs.EventFrom(t, now())
		if err != nil {
			return nil // a telegram that is no event (a stray MOVE) is acknowledged and ignored
		}
		ev.Session = session
		// A failure NACKs busy: the PLC holds the telegram and sends it again, so nothing is lost.
		if err := a.Plane.PostEvents(ctx, a.Station, []mfs.Event{ev}); err != nil {
			a.log().Warn("automation event not delivered to the warehouse; the PLC will resend", "kind", ev.Kind, "err", err)
			return err
		}
		a.eventsForwarded.Add(1)
		return nil
	}
	link := mfs.NewLink(conn, cfg)
	linkErr := make(chan error, 1)
	go func() { linkErr <- link.Run(ctx) }()

	every := a.PollEvery
	if every <= 0 {
		every = 2 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := a.dispatch(ctx, link); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-linkErr:
			return err
		case <-t.C:
		}
	}
}

// dispatch sends the pending orders as MOVE telegrams. An order the PLC acknowledges is reported SENT; one
// it refuses is REFUSED with the code, and the warehouse decides what to do. An unanswered MOVE ends the
// session (the link is unusable) and the order stays pending for the next session.
func (a *Automation) dispatch(ctx context.Context, link *mfs.Link) error {
	orders, err := a.Plane.PullOrders(ctx, a.Station)
	if err != nil {
		a.log().Warn("transport orders not read", "err", err)
		return nil // the warehouse being away does not take the PLC link down
	}
	for _, o := range orders {
		tg, err := mfs.MoveFrom(o.TransportOrder)
		if err != nil {
			a.refuse(ctx, o, err.Error())
			continue
		}
		_, err = link.Send(ctx, tg)
		switch {
		case err == nil:
			if err := a.Plane.ReportOrder(ctx, o.ID, OrderSent, ""); err != nil {
				a.log().Warn("order sent but not reported; it will be offered again", "order", o.ID, "err", err)
				continue
			}
			a.ordersSent.Add(1)
		case errors.Is(err, mfs.ErrNacked):
			a.refuse(ctx, o, err.Error())
		default:
			return err
		}
	}
	return nil
}

// newLinkSession names one link session: the time it started and a random part, so two connectors of one
// station, or a restart within the same clock tick, never share a name.
func newLinkSession(start time.Time) (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("link session name: %w", err)
	}
	return strconv.FormatInt(start.UTC().Unix(), 36) + "-" + hex.EncodeToString(b[:]), nil
}

func (a *Automation) refuse(ctx context.Context, o TransportOrder, reason string) {
	if err := a.Plane.ReportOrder(ctx, o.ID, OrderRefused, reason); err != nil {
		a.log().Warn("refused order not reported", "order", o.ID, "err", err)
		return
	}
	a.ordersRefused.Add(1)
}

// The warehouse side over HTTP. A Client whose BaseURL is the warehouse service implements AutomationPlane.

const automationPath = "/v1/warehouse/automation"

// PullOrders reads the PENDING transport orders of a station.
func (c *Client) PullOrders(ctx context.Context, station string) ([]TransportOrder, error) {
	res, body, err := c.do(ctx, http.MethodGet, automationPath+"/transport-orders?status=PENDING&station="+url.QueryEscape(station), nil)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("transport orders: %s", problemText(res, body))
	}
	var out struct {
		Items []struct {
			ID       string `json:"id"`
			LoadUnit string `json:"loadUnit"`
			From     string `json:"fromLocation"`
			To       string `json:"toLocation"`
			Priority int    `json:"priority"`
		} `json:"items"`
	}
	if err := decodeJSON(body, &out); err != nil {
		return nil, fmt.Errorf("transport orders answer: %w", err)
	}
	orders := make([]TransportOrder, 0, len(out.Items))
	for _, i := range out.Items {
		orders = append(orders, TransportOrder{ID: i.ID, TransportOrder: mfs.TransportOrder{LoadUnit: i.LoadUnit, From: i.From, To: i.To, Priority: i.Priority}})
	}
	return orders, nil
}

// ReportOrder posts POST transport-orders/{id}:report.
func (c *Client) ReportOrder(ctx context.Context, id, status, reason string) error {
	body := map[string]any{"status": status}
	if reason != "" {
		body["reason"] = reason
	}
	res, b, err := c.do(ctx, http.MethodPost, automationPath+"/transport-orders/"+url.PathEscape(id)+":report", body)
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return errors.New("report transport order: " + problemText(res, b))
	}
	return nil
}

// PostEvents posts automation events to the warehouse ingest.
func (c *Client) PostEvents(ctx context.Context, station string, events []mfs.Event) error {
	res, b, err := c.do(ctx, http.MethodPost, automationPath+"/events", map[string]any{"station": station, "events": events})
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return errors.New("post automation events: " + problemText(res, b))
	}
	return nil
}
