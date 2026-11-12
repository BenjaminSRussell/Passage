package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/services/iot-edge/internal/domain"
)

// Client talks to the shopfloor service over HTTP: it is the Transport (machine events) and the
// ControlPlane (configuration and heartbeat). The HTTP client carries the connector's identity
// (the mTLS client certificate once enrollment is built, or a bearer token for dev); Client adds
// nothing but the optional Authorization header.
type Client struct {
	BaseURL string // for example https://shopfloor.plant.example
	EdgeID  string // the shopfloor edge node id (UUID)
	Token   string // optional bearer token
	HTTP    *http.Client
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, rd)
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	return res, b, err
}

// problemText reads the detail of an application/problem+json answer, or the status.
func problemText(res *http.Response, body []byte) string {
	var p struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &p) == nil && p.Detail != "" {
		return fmt.Sprintf("%d %s: %s", res.StatusCode, p.Code, p.Detail)
	}
	return res.Status
}

type wireEvent struct {
	MachineID   string              `json:"machineId"`
	Seq         int64               `json:"seq"`
	Kind        string              `json:"kind"`
	TS          time.Time           `json:"ts"`
	GoodCount   int64               `json:"goodCount"`
	RejectCount int64               `json:"rejectCount"`
	State       string              `json:"state"`
	ReasonCode  *string             `json:"reasonCode"`
	Measurement *domain.Measurement `json:"measurement"`
	JobID       *string             `json:"jobId"`
}

// Send posts up to 500 events to POST /v1/shopfloor/machine-events. A 4xx answer other than 401,
// 403, 408 and 429 is the server saying the batch is invalid: ErrRejected, so the runtime drops it
// instead of retrying it forever. Anything else that is not a 200 is an error and the events stay
// buffered.
func (c *Client) Send(ctx context.Context, events []domain.MachineEvent) (SendResult, error) {
	items := make([]wireEvent, 0, len(events))
	for _, e := range events {
		w := wireEvent{MachineID: e.MachineID, Seq: e.Seq, Kind: e.Kind, TS: e.TS, State: e.State,
			ReasonCode: e.ReasonCode, Measurement: e.Measurement, JobID: e.JobID}
		if e.GoodCount != nil {
			w.GoodCount = *e.GoodCount
		}
		if e.RejectCount != nil {
			w.RejectCount = *e.RejectCount
		}
		items = append(items, w)
	}
	res, body, err := c.do(ctx, http.MethodPost, "/v1/shopfloor/machine-events", map[string]any{"events": items})
	if err != nil {
		return SendResult{}, err
	}
	switch {
	case res.StatusCode == http.StatusOK:
		var out struct {
			Accepted     int `json:"accepted"`
			Dropped      int `json:"dropped"`
			StaleDropped int `json:"staleDropped"`
			Gaps         []struct {
				From int64 `json:"from"`
				To   int64 `json:"to"`
			} `json:"gaps"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return SendResult{}, fmt.Errorf("ingest answer: %w", err)
		}
		r := SendResult{Accepted: out.Accepted, Dropped: out.Dropped + out.StaleDropped}
		for _, g := range out.Gaps {
			r.Gaps = append(r.Gaps, domain.Gap{From: g.From, To: g.To})
		}
		return r, nil
	case res.StatusCode >= 400 && res.StatusCode < 500 && res.StatusCode != 401 && res.StatusCode != 403 &&
		res.StatusCode != 408 && res.StatusCode != 429:
		return SendResult{}, fmt.Errorf("%w: %s", ErrRejected, problemText(res, body))
	default:
		return SendResult{}, fmt.Errorf("ingest: %s", problemText(res, body))
	}
}

// PullConfig reads GET /v1/shopfloor/edge-nodes/{id}/config and returns the document.
func (c *Client) PullConfig(ctx context.Context) (domain.EdgeConfig, error) {
	res, body, err := c.do(ctx, http.MethodGet, "/v1/shopfloor/edge-nodes/"+url.PathEscape(c.EdgeID)+"/config", nil)
	if err != nil {
		return domain.EdgeConfig{}, err
	}
	if res.StatusCode != http.StatusOK {
		return domain.EdgeConfig{}, fmt.Errorf("configuration: %s", problemText(res, body))
	}
	var v struct {
		Version  int             `json:"version"`
		Document json.RawMessage `json:"document"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return domain.EdgeConfig{}, fmt.Errorf("configuration answer: %w", err)
	}
	var cfg domain.EdgeConfig
	if err := json.Unmarshal(v.Document, &cfg); err != nil {
		return domain.EdgeConfig{}, fmt.Errorf("configuration document: %w", err)
	}
	if cfg.Version == 0 {
		cfg.Version = v.Version
	}
	return cfg, nil
}

// Heartbeat posts POST /v1/shopfloor/edge-nodes/{id}:heartbeat.
func (c *Client) Heartbeat(ctx context.Context, hb Heartbeat) (HeartbeatReply, error) {
	type status struct {
		MachineCode string `json:"machineCode"`
		State       string `json:"state"`
	}
	machines := make([]status, 0, len(hb.Machines))
	for code, state := range hb.Machines {
		machines = append(machines, status{code, state})
	}
	body := map[string]any{"connectorVersion": hb.ConnectorVersion, "bufferFillPct": fmt.Sprintf("%.2f", hb.BufferFillPct), "machines": machines}
	if hb.CertSerial != "" {
		body["certSerial"] = hb.CertSerial
		body["certExpiresAt"] = hb.CertExpiresAt.UTC().Format(time.RFC3339)
	}
	if hb.AppliedConfigVersion > 0 {
		body["appliedConfigVersion"] = hb.AppliedConfigVersion
	}
	res, b, err := c.do(ctx, http.MethodPost, "/v1/shopfloor/edge-nodes/"+url.PathEscape(c.EdgeID)+":heartbeat", body)
	if err != nil {
		return HeartbeatReply{}, err
	}
	if res.StatusCode != http.StatusOK {
		return HeartbeatReply{}, fmt.Errorf("heartbeat: %s", problemText(res, b))
	}
	var out struct {
		LatestConfigVersion      *int `json:"latestConfigVersion"`
		HeartbeatIntervalSeconds int  `json:"heartbeatIntervalSeconds"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return HeartbeatReply{}, fmt.Errorf("heartbeat answer: %w", err)
	}
	r := HeartbeatReply{IntervalSeconds: out.HeartbeatIntervalSeconds}
	if out.LatestConfigVersion != nil {
		r.LatestConfigVersion = *out.LatestConfigVersion
	}
	return r, nil
}

func decodeJSON(b []byte, v any) error { return json.Unmarshal(b, v) }
