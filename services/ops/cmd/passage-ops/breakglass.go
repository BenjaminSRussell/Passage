package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"

	"github.com/BenjaminSRussell/passage/services/ops/internal/store"
)

func runBreakGlass(ctx context.Context, args []string, out io.Writer, getenv func(string) string, d deps) error {
	if len(args) == 0 {
		return errors.New("usage: passage-ops break-glass open|list|revoke|review")
	}
	c, err := newClient(getenv, d.httpClient)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("break-glass "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var reason, ticket, id, outcome, note string
	var minutes int
	var unreviewed bool
	fs.StringVar(&reason, "reason", "", "reason code: INCIDENT_RESPONSE, PRODUCTION_OUTAGE, REGION_LOSS, APPROVER_UNAVAILABLE, AUDIT_EVIDENCE_REQUEST")
	fs.StringVar(&ticket, "ticket", "", "incident or change ticket")
	fs.IntVar(&minutes, "minutes", 0, "window length, 15 to 120 (default 60)")
	fs.StringVar(&id, "id", "", "window id")
	fs.StringVar(&outcome, "outcome", "", "review outcome: APPROVED or FLAGGED")
	fs.StringVar(&note, "note", "", "review note (required to flag)")
	fs.BoolVar(&unreviewed, "unreviewed", false, "list only windows awaiting review")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	var g store.BreakGlass
	switch args[0] {
	case "open":
		if _, err := c.do(ctx, http.MethodPost, "/v1/ops/break-glass", nil, map[string]any{"reasonCode": reason, "ticketRef": ticket, "minutes": minutes}, &g); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "break-glass window %s open until %s; every use is logged and reviewed\n", g.ID, g.ExpiresAt.Format("15:04:05Z07:00"))
	case "revoke":
		if _, err := c.do(ctx, http.MethodPost, "/v1/ops/break-glass/"+esc(id)+":revoke", nil, nil, &g); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "window %s revoked\n", g.ID)
	case "review":
		if _, err := c.do(ctx, http.MethodPost, "/v1/ops/break-glass/"+esc(id)+":review", nil, map[string]any{"outcome": outcome, "note": note}, &g); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "window %s reviewed: %s\n", g.ID, g.ReviewOutcome)
	case "list":
		var list struct {
			Items []store.BreakGlass `json:"items"`
		}
		path := "/v1/ops/break-glass"
		if unreviewed {
			path += "?unreviewed=true"
		}
		if _, err := c.do(ctx, http.MethodGet, path, nil, nil, &list); err != nil {
			return err
		}
		for _, w := range list.Items {
			state := "open until " + w.ExpiresAt.Format("2006-01-02 15:04Z")
			if w.ReviewedBy != "" {
				state = "reviewed by " + w.ReviewedBy + ": " + w.ReviewOutcome
			}
			_, _ = fmt.Fprintf(out, "%s  %s  %s  %s  uses=%d  %s\n", w.ID, w.UserID, w.ReasonCode, w.TicketRef, w.Uses, state)
		}
	default:
		return fmt.Errorf("unknown break-glass command %q", args[0])
	}
	return nil
}
