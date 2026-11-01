package recon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const (
	cubeLoadPath   = "/cubejs-api/v1/load"
	cubeRevenue    = "fin_journal_line.revenue"
	cubeEntity     = "fin_journal_line.legal_entity_id"
	cubePostedOn   = "fin_journal_line.posting_date"
	maxCubeBody    = 8 << 20
	cubeHTTPBudget = 30 * time.Second
)

type CubeReader struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewCubeReader(baseURL, token string, client *http.Client) *CubeReader {
	if client == nil {
		client = &http.Client{Timeout: cubeHTTPBudget}
	}
	return &CubeReader{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: client}
}

func (c *CubeReader) Revenue(ctx context.Context, day string) (map[string]*decimal.Decimal, error) {
	query := map[string]any{"query": map[string]any{
		"measures":       []string{cubeRevenue},
		"dimensions":     []string{cubeEntity},
		"timeDimensions": []map[string]any{{"dimension": cubePostedOn, "dateRange": []string{day, day}}},
	}}
	body, err := json.Marshal(query)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+cubeLoadPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cube: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCubeBody))
	if err != nil {
		return nil, fmt.Errorf("cube: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cube: unexpected status %d", resp.StatusCode)
	}
	var out struct {
		Error string                       `json:"error"`
		Data  []map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("cube: %w", err)
	}
	if out.Error != "" {
		return nil, fmt.Errorf("cube: %s", out.Error)
	}
	rows := make(map[string]*decimal.Decimal, len(out.Data))
	for _, row := range out.Data {
		var entity string
		if err := json.Unmarshal(row[cubeEntity], &entity); err != nil || entity == "" {
			return nil, fmt.Errorf("cube: row has no legal entity")
		}
		value, err := nullableDecimal(row[cubeRevenue])
		if err != nil {
			return nil, fmt.Errorf("cube: revenue of %s: %w", entity, err)
		}
		rows[entity] = value
	}
	return rows, nil
}

func nullableDecimal(raw json.RawMessage) (*decimal.Decimal, error) {
	text := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if text == "" || text == "null" {
		return nil, nil
	}
	d, err := decimal.NewFromString(text)
	if err != nil {
		return nil, err
	}
	return &d, nil
}
