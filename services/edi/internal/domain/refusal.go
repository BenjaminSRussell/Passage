package domain

import (
	"strconv"
	"strings"
	"time"
)

// 997 error codes for a refused interchange: AK502 (transaction set syntax
// error, element 718) and AK905 (functional group syntax error, element 716).
const (
	AK5TrailerMissing  = "2" // transaction set trailer missing
	AK5ControlMismatch = "3" // ST02 and SE02 do not match
	AK5CountMismatch   = "4" // SE01 does not match the segment count
	AK9TrailerMissing  = "3" // functional group trailer missing
	AK9ControlMismatch = "4" // GS06 and GE02 do not agree
	AK9CountMismatch   = "5" // GE01 does not match the transaction set count
)

// RefusedGroup is one functional group of an X12 interchange the receipt
// refused, as far as it could be read: the GS header with the ST headers of
// its transactions (Body is empty), the AK502 code of each transaction
// (same index; "" when the transaction itself was readable) and the AK905
// code of the group ("" when the group itself was readable).
type RefusedGroup struct {
	Group   Group
	TxCodes []string
	Code    string
}

// ReadRefusedX12 reads what a 997 for a refused X12 interchange needs: the
// ISA header and every GS group other than 997 groups (FA) with its ST
// headers, noting the trailer errors it meets. ok is false when the ISA
// header itself cannot be read or no group could be found, so no 997 can
// answer the payload. Nothing here decides whether the interchange is
// accepted: the caller calls it for a payload it already refused.
func ReadRefusedX12(raw string) (ic Interchange, groups []RefusedGroup, ok bool) {
	ic, body, err := readISA(raw)
	if err != nil {
		return Interchange{}, nil, false
	}
	var (
		cur   *RefusedGroup
		tx    = -1 // index of the open transaction in cur
		count = 0  // segments of the open transaction, ST included
	)
	closeTx := func(code string) {
		if cur != nil && tx >= 0 {
			if cur.TxCodes[tx] == "" {
				cur.TxCodes[tx] = code
			}
			tx = -1
		}
	}
	closeGroup := func(code string) {
		if cur == nil {
			return
		}
		closeTx(AK5TrailerMissing)
		if cur.Code == "" {
			cur.Code = code
		}
		if cur.Group.FunctionalID != "FA" {
			groups = append(groups, *cur)
		}
		cur = nil
	}
	for _, piece := range strings.Split(body, string(ic.Delims.Segment)) {
		piece = strings.Trim(piece, "\r\n\t")
		if piece == "" {
			continue
		}
		f := strings.Split(piece, string(ic.Delims.Element))
		s := Segment{ID: f[0], Elements: f[1:]}
		switch s.ID {
		case "GS":
			closeGroup(AK9TrailerMissing)
			cur = &RefusedGroup{Group: Group{FunctionalID: element(s, 0), Sender: element(s, 1), Receiver: element(s, 2),
				Date: element(s, 3), Time: element(s, 4), Control: element(s, 5), Version: element(s, 7)}}
		case "ST":
			if cur == nil {
				continue
			}
			closeTx(AK5TrailerMissing)
			cur.Group.Transactions = append(cur.Group.Transactions, Transaction{SetID: element(s, 0), Control: element(s, 1)})
			cur.TxCodes = append(cur.TxCodes, "")
			tx, count = len(cur.TxCodes)-1, 1
		case "SE":
			if cur == nil || tx < 0 {
				continue
			}
			count++
			code := ""
			if element(s, 1) != cur.Group.Transactions[tx].Control {
				code = AK5ControlMismatch
			} else if element(s, 0) != strconv.Itoa(count) {
				code = AK5CountMismatch
			}
			cur.TxCodes[tx] = code
			tx = -1
		case "GE":
			if cur == nil {
				continue
			}
			closeTx(AK5TrailerMissing)
			code := ""
			if element(s, 1) != cur.Group.Control {
				code = AK9ControlMismatch
			} else if element(s, 0) != strconv.Itoa(len(cur.Group.Transactions)) {
				code = AK9CountMismatch
			}
			cur.Code = code
			closeGroup("")
		case "IEA":
			closeGroup(AK9TrailerMissing)
		default:
			if tx >= 0 {
				count++
			}
		}
	}
	closeGroup(AK9TrailerMissing)
	return ic, groups, len(groups) > 0
}

// RefusedGroups lists the groups of an interchange that parsed but was
// refused (an unknown or wrong sender) for Reject997: every group but the
// 997 groups, with its transactions and no error codes.
func RefusedGroups(ic Interchange) []RefusedGroup {
	var out []RefusedGroup
	for _, g := range ic.Groups {
		if g.FunctionalID == "FA" {
			continue
		}
		rg := RefusedGroup{Group: g, TxCodes: make([]string, len(g.Transactions))}
		rg.Group.Transactions = nil
		for _, t := range g.Transactions {
			rg.Group.Transactions = append(rg.Group.Transactions, Transaction{SetID: t.SetID, Control: t.Control})
		}
		out = append(out, rg)
	}
	return out
}

// Reject997 builds the 997 that rejects one group of a refused interchange:
// AK1 with the group's GS01 and GS06, an AK2 and AK5 R (with the AK502 code
// when there is one) for every transaction found, and AK9 R with the counts,
// none accepted, and the AK905 code when there is one. Like Ack997 it swaps
// sender and receiver and carries ourControl in ISA13 and groupControl in
// GS06.
func Reject997(received Interchange, g RefusedGroup, ourControl, groupControl string, at time.Time) (Interchange, error) {
	body := []Segment{{ID: "AK1", Elements: []string{g.Group.FunctionalID, g.Group.Control}}}
	for i, t := range g.Group.Transactions {
		ak5 := []string{AckRejected}
		if i < len(g.TxCodes) && g.TxCodes[i] != "" {
			ak5 = append(ak5, g.TxCodes[i])
		}
		body = append(body, Segment{ID: "AK2", Elements: []string{t.SetID, t.Control}}, Segment{ID: "AK5", Elements: ak5})
	}
	n := strconv.Itoa(len(g.Group.Transactions))
	ak9 := []string{AckRejected, n, n, "0"}
	if g.Code != "" {
		ak9 = append(ak9, g.Code)
	}
	body = append(body, Segment{ID: "AK9", Elements: ak9})
	at = at.UTC()
	return Interchange{
		Delims:   received.Delims,
		Sender:   received.Receiver,
		Receiver: received.Sender,
		Date:     at.Format("060102"),
		Time:     at.Format("1504"),
		Version:  received.Version,
		Control:  ourControl,
		Usage:    received.Usage,
		Groups: []Group{{
			FunctionalID: "FA",
			Sender:       received.Receiver.ID,
			Receiver:     received.Sender.ID,
			Date:         at.Format("20060102"),
			Time:         at.Format("1504"),
			Control:      groupControl,
			Version:      g.Group.Version,
			Transactions: []Transaction{{SetID: "997", Control: "0001", Body: body}},
		}},
	}, nil
}
