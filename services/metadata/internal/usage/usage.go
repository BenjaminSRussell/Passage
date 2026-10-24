// Package usage collects licensing inputs without enforcing licenses.
package usage

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strconv"
	"sync"
	"time"
)

type Principal struct {
	TenantID      string
	LegalEntityID string
	Role          string
	UserID        string
	NamedUser     bool
}

type EntityCount struct {
	TenantID string
	Entity   string
	Rows     int64
}

type Source interface {
	Principals(context.Context, time.Time) ([]Principal, error)
	EntityCounts(context.Context, time.Time) ([]EntityCount, error)
}

type Row struct {
	Day           time.Time
	TenantID      string
	LegalEntityID string
	Role          string
	Entity        string
	NamedUsers    int64
	Principals    int64
	Requests      int64
	RowsRead      int64
	RowsStored    int64
}

type requestKey struct {
	tenant string
	entity string
}

type requestCount struct {
	requests int64
	rows     int64
}

type Meter struct {
	source Source
	mu     sync.Mutex
	counts map[requestKey]requestCount
}

func NewMeter(source Source) *Meter {
	return &Meter{source: source, counts: map[requestKey]requestCount{}}
}

func (meter *Meter) RecordRequest(tenantID, entity string, rowsRead int64) {
	meter.mu.Lock()
	defer meter.mu.Unlock()
	key := requestKey{tenant: tenantID, entity: entity}
	count := meter.counts[key]
	count.requests++
	count.rows += rowsRead
	meter.counts[key] = count
}

func (meter *Meter) Collect(ctx context.Context, day time.Time) ([]Row, error) {
	principals, err := meter.source.Principals(ctx, day)
	if err != nil {
		return nil, fmt.Errorf("collect principals: %w", err)
	}
	entities, err := meter.source.EntityCounts(ctx, day)
	if err != nil {
		return nil, fmt.Errorf("collect entities: %w", err)
	}
	rows := map[string]*Row{}
	named := map[string]map[string]struct{}{}
	all := map[string]map[string]struct{}{}
	for _, principal := range principals {
		key := principal.TenantID + "\x00" + principal.LegalEntityID + "\x00" + principal.Role
		if rows[key] == nil {
			rows[key] = &Row{Day: day, TenantID: principal.TenantID, LegalEntityID: principal.LegalEntityID, Role: principal.Role}
			named[key] = map[string]struct{}{}
			all[key] = map[string]struct{}{}
		}
		all[key][principal.UserID] = struct{}{}
		if principal.NamedUser {
			named[key][principal.UserID] = struct{}{}
		}
	}
	for key, row := range rows {
		row.NamedUsers = int64(len(named[key]))
		row.Principals = int64(len(all[key]))
	}
	meter.mu.Lock()
	counts := meter.counts
	meter.counts = map[requestKey]requestCount{}
	meter.mu.Unlock()
	stored := map[requestKey]int64{}
	for _, entity := range entities {
		stored[requestKey{tenant: entity.TenantID, entity: entity.Entity}] = entity.Rows
	}
	keys := map[requestKey]struct{}{}
	for key := range counts {
		keys[key] = struct{}{}
	}
	for key := range stored {
		keys[key] = struct{}{}
	}
	for key := range keys {
		count := counts[key]
		mapKey := key.tenant + "\x00\x00\x00" + key.entity
		rows[mapKey] = &Row{Day: day, TenantID: key.tenant, Entity: key.entity, Requests: count.requests, RowsRead: count.rows, RowsStored: stored[key]}
	}
	out := make([]Row, 0, len(rows))
	for _, row := range rows {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		left := out[i].TenantID + "\x00" + out[i].LegalEntityID + "\x00" + out[i].Role + "\x00" + out[i].Entity
		right := out[j].TenantID + "\x00" + out[j].LegalEntityID + "\x00" + out[j].Role + "\x00" + out[j].Entity
		return left < right
	})
	return out, nil
}

func WriteCSV(writer io.Writer, rows []Row) error {
	csvWriter := csv.NewWriter(writer)
	if err := csvWriter.Write([]string{"day", "tenant_id", "legal_entity_id", "role", "entity", "named_users", "principals", "api_requests", "rows_read", "rows_stored"}); err != nil {
		return err
	}
	for _, row := range rows {
		values := []string{
			row.Day.UTC().Format(time.DateOnly), row.TenantID, row.LegalEntityID, row.Role, row.Entity,
			strconv.FormatInt(row.NamedUsers, 10), strconv.FormatInt(row.Principals, 10),
			strconv.FormatInt(row.Requests, 10), strconv.FormatInt(row.RowsRead, 10), strconv.FormatInt(row.RowsStored, 10),
		}
		if err := csvWriter.Write(values); err != nil {
			return err
		}
	}
	csvWriter.Flush()
	return csvWriter.Error()
}
