package target

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

var partyFields = []string{"legalName", "tradeName", "registrationNumber", "industryCode", "givenName", "middleName", "familyName", "prefix", "suffix", "dob", "gender", "languageCode", "timeZone", "ownerUserId", "ownerTeamId"}

func idemHeader(it domain.LoadItem) map[string]string {
	return map[string]string{"Idempotency-Key": it.IdempotencyKey}
}

// accepted turns a successful reply into the row's result.
func accepted(r reply, fallbackID string) domain.LoadResult {
	rc := parseReceipt(r.body)
	if rc.ID == "" {
		rc.ID = fallbackID
	}
	return domain.LoadResult{TargetID: rc.ID, TargetVersion: rc.Version}
}

// loadParty creates a party under the id the pipeline reserved, so the legacy key keeps one Passage id, or patches
// the one an earlier run created. The party number comes from numbering in the party service.
func (l *Loader) loadParty(ctx context.Context, c *call, it domain.LoadItem) (domain.LoadResult, error) {
	le, bad := l.legalEntity(it.Fields)
	if bad != nil {
		return *bad, nil
	}
	body := map[string]any{}
	pick(body, it.Fields, partyFields...)
	if it.Existing {
		if it.TargetVersion < 1 {
			return domain.LoadResult{Code: CodeVersionUnknown, Detail: "the party was loaded without a recorded version; it cannot be patched"}, nil
		}
		if s := text(it.Fields, "status"); s != "" {
			body["status"] = s
		}
		r, err := c.do(ctx, http.MethodPatch, "/v1/party/parties/"+url.PathEscape(it.TargetID), le, map[string]string{"If-Match": etag(it.TargetVersion)}, body)
		if err != nil {
			return domain.LoadResult{}, err
		}
		if !r.ok() {
			return r.updateRefusal(), nil
		}
		return accepted(r, it.TargetID), nil
	}
	partyType := strings.ToLower(text(it.Fields, "partyType"))
	switch {
	case partyType == "" && text(it.Fields, "legalName") != "":
		partyType = "organization"
	case partyType == "" && (text(it.Fields, "givenName") != "" || text(it.Fields, "familyName") != ""):
		partyType = "person"
	case partyType == "":
		return missingResult([]string{"partyType (or legalName, or givenName and familyName)"}), nil
	}
	body["partyType"], body["id"] = partyType, it.TargetID
	r, err := c.do(ctx, http.MethodPost, "/v1/party/parties", le, idemHeader(it), body)
	if err != nil {
		return domain.LoadResult{}, err
	}
	if !r.ok() {
		return r.refusal(), nil
	}
	return accepted(r, it.TargetID), nil
}

// loadRole creates a customer or vendor role on a loaded party. The party service has no import route to change a
// role, so a row that changed since its first load is a reject, not a silent skip.
func (l *Loader) loadRole(ctx context.Context, c *call, it domain.LoadItem, role string, need []string, optional []string) (domain.LoadResult, error) {
	if it.Existing {
		return domain.LoadResult{Code: CodeUpdateUnavailable, Detail: "the party service has no import route to change a " + role + " role after it is created; change it in Passage or load it as a new key"}, nil
	}
	le, bad := l.legalEntity(it.Fields)
	if bad != nil {
		return *bad, nil
	}
	if missing := required(it.Fields, need...); len(missing) > 0 {
		return missingResult(missing), nil
	}
	partyID, bad := l.ref(ctx, it.Fields, "partyId", "partyKey", "party")
	if bad != nil {
		return *bad, nil
	}
	body := map[string]any{}
	pick(body, it.Fields, need...)
	pick(body, it.Fields, optional...)
	if v, ok := it.Fields["creditHold"].(bool); ok {
		body["creditHold"] = v
	}
	if role == "customer" {
		if _, ok := it.Fields["invoiceAccountPartyId"]; !ok && text(it.Fields, "invoiceAccountPartyKey") != "" {
			id, bad := l.ref(ctx, it.Fields, "invoiceAccountPartyId", "invoiceAccountPartyKey", "party")
			if bad != nil {
				return *bad, nil
			}
			body["invoiceAccountPartyId"] = id
		}
	}
	r, err := c.do(ctx, http.MethodPost, "/v1/party/parties/"+url.PathEscape(partyID)+"/"+role, le, idemHeader(it), body)
	if err != nil {
		return domain.LoadResult{}, err
	}
	if !r.ok() {
		return r.refusal(), nil
	}
	return accepted(r, ""), nil
}

var (
	customerNeed     = []string{"accountNumber", "customerGroupId", "paymentTermId", "postingProfileId", "currencyCode"}
	customerOptional = []string{"deliveryTermId", "deliveryModeId", "invoiceAccountPartyId", "creditLimit", "creditCurrency", "creditRating", "blocked"}
	vendorNeed       = []string{"accountNumber", "vendorGroupId", "currencyCode"}
	vendorOptional   = []string{"paymentTermId"}
)

func (l *Loader) loadCustomer(ctx context.Context, c *call, it domain.LoadItem) (domain.LoadResult, error) {
	return l.loadRole(ctx, c, it, "customer", customerNeed, customerOptional)
}

func (l *Loader) loadVendor(ctx context.Context, c *call, it domain.LoadItem) (domain.LoadResult, error) {
	return l.loadRole(ctx, c, it, "vendor", vendorNeed, vendorOptional)
}
