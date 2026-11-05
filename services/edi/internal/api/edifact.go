package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

// receiveEdifact accepts one raw EDIFACT interchange from a partner, posted
// by an operator or an integration, the EDIFACT counterpart of receiveX12
// (the AS2 endpoint and the SFTP poller detect the syntax and hand EDIFACT
// to the same pipeline). It returns the CONTRL text when the partner gets
// one.
func (a *API) receiveEdifact(w http.ResponseWriter, r *http.Request) {
	a.receiveRoute(w, r, domain.SyntaxEdifact)
}

// edifactKey names a message inside an interchange: refs are unique only
// inside their group.
func edifactKey(group, ref string) string { return group + "\x00" + ref }

// receiveEdifactInterchange is the inbound EDIFACT pipeline, parallel to the
// X12 one: read the interchange (UNA, UNB, optional UNG..UNE groups, UNH..UNT
// messages, UNZ, with every count and reference checked), resolve the partner
// by UNB sender, map ORDERS, DESADV and INVOIC onto the canonical documents
// the 850, 856 and 810 use, and record everything with the CONTRL answer (when
// the partner wants one) in one transaction, with message.received and
// message.failed events. A message whose UNT count or reference is wrong is
// recorded FAILED and rejected in the CONTRL while the rest is processed; any
// other envelope error refuses the interchange.
func (a *API) receiveEdifactInterchange(ctx context.Context, body []byte, o receiveOptions) (receiveResponse, error) {
	ic, broken, err := domain.ReadEdifact(string(body))
	if err != nil {
		return receiveResponse{}, httpx.NewProblem(http.StatusBadRequest, CodeSyntax, err.Error())
	}
	partner, err := a.store.FindPartnerByEdifactSender(ctx, ic.Sender, o.Environment)
	if errors.Is(err, store.ErrNotFound) {
		return receiveResponse{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeUnknownSender,
			fmt.Sprintf("interchange sender %s (UNB S002) is not configured for any partner", edifactParty(ic.Sender)))
	}
	if err != nil {
		return receiveResponse{}, err
	}
	if o.PartnerID != "" && partner.ID != o.PartnerID {
		return receiveResponse{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeUnknownSender,
			fmt.Sprintf("interchange sender %s (UNB S002) is not the partner this transport belongs to", edifactParty(ic.Sender)))
	}
	brokenBy := map[string]*domain.EdifactError{}
	for _, e := range broken {
		brokenBy[edifactKey(e.Group, e.Message)] = e
	}
	var (
		all      []domain.TxOutcome
		outcomes []domain.EdifactOutcome
		messages []store.Message
	)
	// The messages the CONTRL answers: a received CONTRL is never acknowledged.
	acked := ic
	acked.Messages = nil
	for _, m := range ic.Messages {
		if m.Type == "CONTRL" {
			msg, err := contrlMessage(m)
			if err != nil {
				return receiveResponse{}, err
			}
			messages = append(messages, msg)
			continue
		}
		var (
			res txResult
			eo  domain.EdifactOutcome
		)
		if e := brokenBy[edifactKey(m.Group, m.Ref)]; e != nil {
			res = txResult{outcome: domain.TxOutcome{SetID: m.Type, Control: m.Ref, Code: domain.AckRejected}, status: "FAILED",
				errText: CodeSyntax + ": " + e.Detail}
			eo = domain.EdifactOutcome{Group: m.Group, Ref: m.Ref, ErrorCode: e.Code, SegmentPosition: e.Segment,
				ElementPosition: e.Element, ComponentPosition: e.Component}
		} else if res, eo, err = mapEdifactMessage(m); err != nil {
			return receiveResponse{}, err
		}
		all = append(all, res.outcome)
		outcomes = append(outcomes, eo)
		acked.Messages = append(acked.Messages, m)
		messages = append(messages, store.Message{ID: ids.New(), TransactionSet: m.Type, STControl: m.Ref,
			Status: res.status, Canonical: res.canonical, Error: res.errText})
	}
	if len(messages) == 0 {
		return receiveResponse{}, httpx.NewProblem(http.StatusBadRequest, CodeSyntax, "interchange carries no messages")
	}
	// An interchange of CONTRLs only is accepted; the store downgrades it
	// when a CONTRL fails.
	status := domain.StatusAccepted
	if len(all) > 0 {
		status = domain.SummarizeOutcomes(all)
	}
	wants := len(outcomes) > 0 && partner.Edifact() != nil && partner.Edifact().WantsContrl(ic.AckRequest)
	at := a.now().UTC()
	buildAck := func(next func() (int64, error)) (string, error) {
		if !wants {
			return "", nil
		}
		n, err := next()
		if err != nil {
			return "", err
		}
		c, err := domain.BuildCONTRL(acked, outcomes, strconv.FormatInt(n, 10), at)
		if err != nil {
			return "", err
		}
		return domain.SerializeEdifact(c, false)
	}
	res, err := a.store.ReceiveInterchange(ctx, store.Inbound{
		PartnerID:  partner.ID,
		Standard:   domain.SyntaxEdifact,
		Version:    ic.Syntax + ":" + ic.SyntaxVersion,
		ISAControl: ic.Control,
		Raw:        string(body),
		AckStatus:  string(status),
		Messages:   messages,
		BuildAck:   buildAck,
		Source:     o.Source,
	})
	if errors.Is(err, store.ErrDuplicate) {
		return receiveResponse{}, httpx.NewProblem(http.StatusConflict, CodeDuplicateControl,
			fmt.Sprintf("duplicate interchange reference %s (UNB 0020) inside the duplicate window", ic.Control))
	}
	if err != nil {
		return receiveResponse{}, err
	}
	return receiveResponse{
		InterchangeID: res.InterchangeID,
		AckStatus:     res.AckStatus,
		Messages:      messageViews(res.Messages),
		Contrl:        res.AckPayload,
	}, nil
}

func edifactParty(p domain.EdifactParty) string {
	if p.Qualifier == "" {
		return p.ID
	}
	return p.ID + ":" + p.Qualifier
}

// mapEdifactMessage maps one received EDIFACT message onto the canonical
// document of its X12 counterpart: ORDERS like the 850 (with its issues and
// AK5 codes), DESADV held to the 856 rules and INVOIC to the 810 rules. It
// derives the message status, the 997-style outcome for the interchange
// status, and the CONTRL outcome (UCM 4 with UCS and UCD where the failing
// segment is known). Other message types are rejected as not supported.
func mapEdifactMessage(m domain.EdifactMessage) (txResult, domain.EdifactOutcome, error) {
	res := txResult{outcome: domain.TxOutcome{SetID: m.Type, Control: m.Ref, Code: domain.AckAccepted}, status: "MAPPED"}
	eo := domain.EdifactOutcome{Group: m.Group, Ref: m.Ref, Accepted: true}
	reject := func(text string, place domain.ContrlPlace) {
		res.outcome.Code, res.status, res.errText = domain.AckRejected, "FAILED", text
		eo = domain.EdifactOutcome{Group: m.Group, Ref: m.Ref, ErrorCode: place.Code, SegmentPosition: place.Segment,
			ElementPosition: place.Element, ComponentPosition: place.Component}
	}
	var (
		doc any
		err error
	)
	switch m.Type {
	case "ORDERS":
		order, issues := domain.MapORDERS(m)
		doc, res.order, res.issues = order, &order, issues
		var details []string
		var first *domain.Issue
		for i, is := range issues {
			details = append(details, is.Code+": "+is.Detail)
			switch {
			case is.Reject && first == nil:
				first = &issues[i]
			case !is.Reject && res.outcome.Code == domain.AckAccepted:
				res.outcome.Code = domain.AckAcceptedWithError
			}
		}
		res.errText = strings.Join(details, "; ")
		switch {
		case first != nil:
			place := first.Contrl
			if place.Code == "" {
				place.Code = domain.ContrlErrUnspecified
			}
			reject(res.errText, place)
		case res.outcome.Code != domain.AckAccepted:
			res.status = "FAILED"
		}
	case "DESADV":
		if doc, err = domain.MapDESADV(m); err != nil {
			reject(domain.CodeMalformed+": "+err.Error(), domain.ContrlPlace{Code: domain.ContrlErrUnspecified})
			doc = nil
		}
	case "INVOIC":
		if doc, err = domain.MapINVOIC(m); err != nil {
			reject(domain.CodeMalformed+": "+err.Error(), domain.ContrlPlace{Code: domain.ContrlErrUnspecified})
			doc = nil
		}
	default:
		// UNH S009 0065, element 2 component 1 of segment 1.
		reject(CodeUnsupportedSet+": message type "+m.Type+" is not supported", domain.ContrlPlace{Code: domain.ContrlErrInvalidValue, Segment: 1, Element: 2, Component: 1})
	}
	if doc != nil {
		canonical, err := json.Marshal(doc)
		if err != nil {
			return txResult{}, domain.EdifactOutcome{}, err
		}
		res.canonical = canonical
	}
	return res, eo, nil
}

// contrlMessage records a received CONTRL. Outbound EDIFACT interchanges are
// not generated yet, so no CONTRL can answer one of ours: it is stored with
// its reading and FAILED, like a 997 that matches nothing.
func contrlMessage(m domain.EdifactMessage) (store.Message, error) {
	msg := store.Message{ID: ids.New(), TransactionSet: m.Type, STControl: m.Ref, Status: "FAILED"}
	ack, err := domain.ParseCONTRL(m)
	if err != nil {
		msg.Error = CodeSyntax + ": " + err.Error()
		return msg, nil
	}
	canonical, err := json.Marshal(ack)
	if err != nil {
		return store.Message{}, err
	}
	msg.Canonical = canonical
	msg.Error = fmt.Sprintf("%s: no outbound EDIFACT interchange %s to this partner answers this CONTRL", domain.CodeUnmatchedAck, ack.InterchangeRef)
	return msg, nil
}

// refuse answers a payload a transport brought from an authenticated partner
// that the receipt refused for its syntax or its sender, with a negative 997
// (X12) or CONTRL (EDIFACT) when one can be built from what the envelope
// says: the refused interchange is recorded with the acknowledgment, its
// source is parked with it and the acknowledgment is queued over the
// partner's transport (store.RecordRefusal). Nothing is built for a payload
// over the size limit, one whose ISA or UNB cannot be read, a CONTRL the
// partner does not want, or an X12 interchange without a group. Only the
// transports call it, after the partner was authenticated (AS2 signature,
// pinned SFTP host key); it returns an error only for a failure worth
// retrying.
func (a *API) refuse(ctx context.Context, partner store.Partner, src store.InboundSource, payload []byte, p *httpx.Problem) error {
	if len(payload) > maxInbound || (p.Code != CodeSyntax && p.Code != CodeUnknownSender) {
		return nil
	}
	at := a.now().UTC()
	r := store.Refusal{PartnerID: partner.ID, Raw: string(payload), Reason: p.Code + ": " + p.Detail, Source: src}
	log := func(what string, err error) {
		slog.Warn("edi refusal acknowledgment not built", "partner", partner.ID, "ref", src.Ref, "ack", what, "err", err)
	}
	switch domain.DetectSyntax(payload) {
	case domain.SyntaxX12:
		ic, err := domain.Parse(string(payload))
		groups := domain.RefusedGroups(ic)
		ok := err == nil && len(groups) > 0
		if err != nil {
			ic, groups, ok = domain.ReadRefusedX12(string(payload))
		}
		if !ok {
			return nil
		}
		r.Standard, r.Version, r.Control = domain.SyntaxX12, ic.Version, ic.Control
		r.BuildAck = func(next func() (int64, error)) (string, error) {
			parts := make([]string, 0, len(groups))
			for _, g := range groups {
				n, err := next()
				if err != nil {
					return "", err
				}
				ack, err := domain.Reject997(ic, g, fmt.Sprintf("%09d", n), strconv.FormatInt(n, 10), at)
				if err == nil {
					var raw string
					if raw, err = domain.Serialize(ack); err == nil {
						parts = append(parts, raw)
						continue
					}
				}
				log("997", err)
				return "", nil
			}
			return strings.Join(parts, "\n"), nil
		}
	case domain.SyntaxEdifact:
		ic, _, err := domain.ReadEdifact(string(payload))
		if ic.Control == "" {
			return nil
		}
		ident := partner.Edifact()
		if ident == nil {
			ident = &domain.EdifactIdentity{Contrl: domain.ContrlRequested}
		}
		if !ident.WantsContrl(ic.AckRequest) {
			return nil
		}
		var cause *domain.EdifactError
		switch {
		case p.Code == CodeUnknownSender:
			cause = &domain.EdifactError{Code: domain.ContrlErrUnknownSender}
		case errors.As(err, &cause):
		default:
			cause = &domain.EdifactError{Code: domain.ContrlErrEmpty}
		}
		r.Standard, r.Version, r.Control = domain.SyntaxEdifact, ic.Syntax+":"+ic.SyntaxVersion, ic.Control
		r.BuildAck = func(next func() (int64, error)) (string, error) {
			n, err := next()
			if err != nil {
				return "", err
			}
			c, err := domain.RefusalCONTRL(ic, cause, strconv.FormatInt(n, 10), at)
			if err == nil {
				var raw string
				if raw, err = domain.SerializeEdifact(c, false); err == nil {
					return raw, nil
				}
			}
			log("CONTRL", err)
			return "", nil
		}
	default:
		return nil
	}
	_, err := a.store.RecordRefusal(ctx, r)
	if errors.Is(err, store.ErrNoAck) {
		return nil
	}
	return err
}
