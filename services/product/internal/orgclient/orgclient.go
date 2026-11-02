package orgclient

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/product/internal/upstream"
)

const timeout = 2 * time.Second

// readDuty is the org duty a product-signed token asks for; the grant
// product -> org (org.read) is a platform handoff (NOT-IMPLEMENTED.md).
const readDuty = "org.read"

var (
	ErrNotFound      = upstream.ErrNotFound
	ErrUnavailable   = upstream.ErrUnavailable
	ErrServiceCaller = upstream.ErrServiceCaller
)

type Site struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId"`
}

type Warehouse struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId"`
	SiteID        string `json:"siteId"`
}

type Checker interface {
	Site(ctx context.Context, id string) (Site, error)
	Warehouse(ctx context.Context, id string) (Warehouse, error)
}

type httpChecker struct {
	baseURL string
	client  *http.Client
	signing *upstream.Signing
}

func New(baseURL string) Checker { return NewSigned(baseURL, nil) }

// NewSigned is New with product's own signer for service callers (the
// migration loader): their lookups go out with a product token for org instead
// of their forwarded headers. A nil signer makes such lookups fail with
// ErrServiceCaller before any request is sent.
func NewSigned(baseURL string, signer *tenant.ServiceSigner) Checker {
	return &httpChecker{
		baseURL: strings.TrimRight(baseURL, "/"), client: &http.Client{Timeout: timeout},
		signing: &upstream.Signing{Signer: signer, Audience: "org", Duties: []string{readDuty}},
	}
}

func (c *httpChecker) Site(ctx context.Context, id string) (Site, error) {
	var out Site
	err := upstream.GetJSONSigned(ctx, c.client, c.baseURL+"/v1/org/sites/"+url.PathEscape(id), &out, c.signing)
	return out, err
}

func (c *httpChecker) Warehouse(ctx context.Context, id string) (Warehouse, error) {
	var out Warehouse
	err := upstream.GetJSONSigned(ctx, c.client, c.baseURL+"/v1/org/warehouses/"+url.PathEscape(id), &out, c.signing)
	return out, err
}
