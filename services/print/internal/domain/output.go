package domain

import (
	"fmt"
	"sort"
	"strings"
)

// Output routing (#46-2): where the original and the copies of a business document go.
// A rule names a document type, optionally a legal entity and an account (customer or
// vendor), the kind of copy and a destination channel. The plan for one document is the
// set of rules of the most specific scope that matches, per copy kind and channel, so an
// account can have its invoices e-mailed while everyone else's original goes to a printer
// and the copy to the archive.

const (
	CopyOriginal = "ORIGINAL"
	CopyCopy     = "COPY"

	ChannelPrinter  = "PRINTER"  // a print queue of the site print agent
	ChannelEmail    = "EMAIL"    // an address
	ChannelEInvoice = "EINVOICE" // the e-invoice network (no destination)
	ChannelArchive  = "ARCHIVE"  // the document archive (no destination)
	ChannelPortal   = "PORTAL"   // the customer or supplier portal (no destination)

	CodeOutputRuleNotFound = "PRN-0015" // output rule not found (service-local)
)

var channelOrder = map[string]int{ChannelPrinter: 1, ChannelEmail: 2, ChannelEInvoice: 3, ChannelArchive: 4, ChannelPortal: 5}

// OutputRule is one destination of one kind of copy.
type OutputRule struct {
	ID            string `json:"id"`
	DocumentType  string `json:"documentType"`
	LegalEntityID string `json:"legalEntityId,omitempty"`
	AccountID     string `json:"accountId,omitempty"`
	CopyKind      string `json:"copyKind"`
	Channel       string `json:"channel"`
	Destination   string `json:"destination,omitempty"`
	Copies        int    `json:"copies"`
	Sequence      int    `json:"sequence"`
}

// OutputDocument is what is being routed.
type OutputDocument struct {
	Type          string
	LegalEntityID string
	AccountID     string
}

// scope orders rules by specificity: 1 entity and account, 2 account, 3 entity, 4 default.
func (r OutputRule) scope() int {
	switch {
	case r.LegalEntityID != "" && r.AccountID != "":
		return 1
	case r.AccountID != "":
		return 2
	case r.LegalEntityID != "":
		return 3
	}
	return 4
}

func (r OutputRule) matches(d OutputDocument) bool {
	return r.DocumentType == d.Type &&
		(r.LegalEntityID == "" || r.LegalEntityID == d.LegalEntityID) &&
		(r.AccountID == "" || r.AccountID == d.AccountID)
}

// Validate returns field-level errors for a rule, or none.
func (r OutputRule) Validate() map[string]string {
	f := map[string]string{}
	known := false
	for _, d := range DocumentTypes() {
		known = known || d == r.DocumentType
	}
	if !known {
		f["documentType"] = "must be a document type the renderer knows"
	}
	if r.CopyKind != CopyOriginal && r.CopyKind != CopyCopy {
		f["copyKind"] = "must be ORIGINAL or COPY"
	}
	if _, ok := channelOrder[r.Channel]; !ok {
		f["channel"] = "must be PRINTER, EMAIL, EINVOICE, ARCHIVE or PORTAL"
	}
	switch r.Channel {
	case ChannelPrinter:
		if !ValidCode(r.Destination) {
			f["destination"] = "must be a queue code"
		}
	case ChannelEmail:
		at := strings.IndexByte(r.Destination, '@')
		if at < 1 || at == len(r.Destination)-1 || strings.ContainsAny(r.Destination, " \t\r\n,;") || strings.Count(r.Destination, "@") != 1 || len(r.Destination) > 254 {
			f["destination"] = "must be one e-mail address"
		}
	default:
		if r.Destination != "" {
			f["destination"] = fmt.Sprintf("is not used by the %s channel", r.Channel)
		}
	}
	if r.Channel == ChannelPrinter {
		if r.Copies < 1 || r.Copies > 9 {
			f["copies"] = "must be between 1 and 9"
		}
	} else if r.Copies != 1 {
		f["copies"] = "must be 1 for a channel that is not a printer"
	}
	if r.Sequence < 1 {
		f["sequence"] = "must be at least 1"
	}
	for name, v := range map[string]string{"legalEntityId": r.LegalEntityID, "accountId": r.AccountID} {
		if len(v) > 64 {
			f[name] = "is longer than 64 characters"
		}
	}
	return f
}

// ResolveOutputs returns the destinations for doc: for each copy kind and channel, every
// rule of the most specific scope that matches. The result is ordered original first, then
// by channel and sequence, so it is stable.
func ResolveOutputs(rules []OutputRule, doc OutputDocument) []OutputRule {
	best := map[string]int{}
	for _, r := range rules {
		if !r.matches(doc) {
			continue
		}
		k := r.CopyKind + "/" + r.Channel
		if b, ok := best[k]; !ok || r.scope() < b {
			best[k] = r.scope()
		}
	}
	out := []OutputRule{}
	for _, r := range rules {
		if r.matches(doc) && r.scope() == best[r.CopyKind+"/"+r.Channel] {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.CopyKind != b.CopyKind {
			return a.CopyKind == CopyOriginal
		}
		if a.Channel != b.Channel {
			return channelOrder[a.Channel] < channelOrder[b.Channel]
		}
		if a.Sequence != b.Sequence {
			return a.Sequence < b.Sequence
		}
		return a.ID < b.ID
	})
	return out
}
