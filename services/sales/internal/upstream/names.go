package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const dutyProductNames = "product.names.read"

// maxNamesPerCall is the product service's limit on one names read.
const maxNamesPerCall = 200

// ProductNames reads the display name of products from the product service (GET /v1/product/products:names, duty
// product.names.read): the customer portal labels order and return lines with it. A product the service does not know is
// absent from the answer.
func (c *Client) ProductNames(ctx context.Context, legalEntityID string, productIDs []string) (map[string]string, error) {
	if c == nil || c.productURL == "" {
		return nil, errors.New("product service is not configured")
	}
	out := make(map[string]string, len(productIDs))
	for start := 0; start < len(productIDs); start += maxNamesPerCall {
		end := min(start+maxNamesPerCall, len(productIDs))
		target := c.productURL + "/v1/product/products:names?" + url.Values{"ids": {strings.Join(productIDs[start:end], ",")}}.Encode()
		status, raw, err := c.do(ctx, http.MethodGet, target, productAudience, dutyProductNames, legalEntityID, "", nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("product responded with status %d", status)
		}
		var body struct {
			Items []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"items"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, fmt.Errorf("decode product names: %w", err)
		}
		for _, it := range body.Items {
			out[strings.ToLower(it.ID)] = it.Name
		}
	}
	return out, nil
}
