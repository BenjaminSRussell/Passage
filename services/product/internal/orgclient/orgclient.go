package orgclient

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/services/product/internal/upstream"
)

const timeout = 2 * time.Second

var (
	ErrNotFound    = upstream.ErrNotFound
	ErrUnavailable = upstream.ErrUnavailable
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
}

func New(baseURL string) Checker {
	return &httpChecker{baseURL: strings.TrimRight(baseURL, "/"), client: &http.Client{Timeout: timeout}}
}

func (c *httpChecker) Site(ctx context.Context, id string) (Site, error) {
	var out Site
	err := upstream.GetJSON(ctx, c.client, c.baseURL+"/v1/org/sites/"+url.PathEscape(id), &out)
	return out, err
}

func (c *httpChecker) Warehouse(ctx context.Context, id string) (Warehouse, error) {
	var out Warehouse
	err := upstream.GetJSON(ctx, c.client, c.baseURL+"/v1/org/warehouses/"+url.PathEscape(id), &out)
	return out, err
}
