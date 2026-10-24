package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/metadata/internal/domain"
)

var ErrNotFound = errors.New("metadata definition not found")

type Version struct {
	Number  int             `json:"version"`
	Entity  *domain.Entity  `json:"entity"`
	Changes []domain.Change `json:"changes"`
}

type Store interface {
	Publish(context.Context, string, *domain.Entity) (Version, error)
	Current(context.Context, string, string) (*domain.Entity, error)
	Extend(context.Context, string, domain.Extension) (Version, error)
	CreateRecord(context.Context, string, string, map[string]any) (map[string]any, error)
	ListRecords(context.Context, string, string, int) ([]map[string]any, error)
	Ping(context.Context) error
}

type Memory struct {
	mu      sync.RWMutex
	history map[string][]Version
	records map[string][]map[string]any
}

func NewMemory() *Memory {
	return &Memory{history: map[string][]Version{}, records: map[string][]map[string]any{}}
}

func (store *Memory) Ping(context.Context) error { return nil }

func registryKey(tenantID, entity string) string { return tenantID + "\x00" + entity }

func (store *Memory) Publish(_ context.Context, tenantID string, entity *domain.Entity) (Version, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.publishLocked(tenantID, entity)
}

func (store *Memory) publishLocked(tenantID string, entity *domain.Entity) (Version, error) {
	if tenantID == "" || entity == nil {
		return Version{}, fmt.Errorf("tenant and entity are required")
	}
	if violations := domain.ValidateEffective(entity); len(violations) > 0 {
		errs := make([]error, len(violations))
		for i, violation := range violations {
			errs[i] = violation
		}
		return Version{}, errors.Join(errs...)
	}
	key := registryKey(tenantID, entity.Name)
	history := store.history[key]
	var previous *domain.Entity
	if len(history) > 0 {
		previous = history[len(history)-1].Entity
		if err := domain.CompareVersions(previous, entity); err != nil {
			return Version{}, err
		}
	}
	version := Version{
		Number: len(history) + 1, Entity: clone(entity),
		Changes: domain.Diff(previous, entity),
	}
	store.history[key] = append(history, version)
	return cloneVersion(version), nil
}

func (store *Memory) Current(_ context.Context, tenantID, entity string) (*domain.Entity, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	history := store.history[registryKey(tenantID, entity)]
	if len(history) == 0 {
		return nil, ErrNotFound
	}
	return clone(history[len(history)-1].Entity), nil
}

func (store *Memory) Extend(_ context.Context, tenantID string, extension domain.Extension) (Version, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	history := store.history[registryKey(tenantID, extension.Entity)]
	if len(history) == 0 {
		return Version{}, ErrNotFound
	}
	effective, err := domain.ApplyExtension(history[len(history)-1].Entity, extension)
	if err != nil {
		return Version{}, err
	}
	return store.publishLocked(tenantID, effective)
}

func (store *Memory) CreateRecord(_ context.Context, tenantID, entity string, input map[string]any) (map[string]any, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	history := store.history[registryKey(tenantID, entity)]
	if len(history) == 0 {
		return nil, ErrNotFound
	}
	record, err := validateRecord(history[len(history)-1].Entity, input)
	if err != nil {
		return nil, err
	}
	record["id"] = ids.New()
	record["tenantId"] = tenantID
	record["version"] = 1
	key := registryKey(tenantID, entity)
	store.records[key] = append(store.records[key], record)
	return cloneMap(record), nil
}

func (store *Memory) ListRecords(_ context.Context, tenantID, entity string, limit int) ([]map[string]any, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	if len(store.history[registryKey(tenantID, entity)]) == 0 {
		return nil, ErrNotFound
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	source := store.records[registryKey(tenantID, entity)]
	if len(source) > limit {
		source = source[:limit]
	}
	result := make([]map[string]any, len(source))
	for i, record := range source {
		result[i] = cloneMap(record)
	}
	return result, nil
}

func validateRecord(entity *domain.Entity, input map[string]any) (map[string]any, error) {
	fields := map[string]domain.Field{}
	for _, field := range entity.Fields {
		fields[wireName(field.Name)] = field
	}
	var errs []error
	record := map[string]any{}
	for name := range input {
		if _, ok := fields[name]; !ok {
			errs = append(errs, domain.Violation{Path: name, Code: "MET-0401", Msg: "unknown field"})
		}
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		field := fields[name]
		value, exists := input[name]
		if !exists && field.Default != "" {
			value, exists = field.Default, true
		}
		if !exists {
			if field.Required && field.Computed == "" {
				errs = append(errs, domain.Violation{Path: name, Code: "MET-0401", Msg: "required field is missing"})
			}
			continue
		}
		if field.Computed != "" {
			errs = append(errs, domain.Violation{Path: name, Code: "MET-0401", Msg: "computed field is read-only"})
			continue
		}
		if err := validateFieldValue(entity, field, value); err != nil {
			errs = append(errs, domain.Violation{Path: name, Code: "MET-0401", Msg: err.Error()})
			continue
		}
		record[name] = value
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return record, nil
}

func validateFieldValue(entity *domain.Entity, field domain.Field, value any) error {
	switch field.Type {
	case "string", "text", "date", "datetime", "time", "uuid", "money", "decimal":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("must be a string")
		}
		if field.Type == "string" && len(text) > field.Length {
			return fmt.Errorf("must be at most %d bytes", field.Length)
		}
		if field.Type == "decimal" || field.Type == "money" {
			if _, err := strconv.ParseFloat(text, 64); err != nil {
				return fmt.Errorf("must be a decimal string")
			}
		}
	case "enum":
		text, ok := value.(string)
		if !ok || !contains(entity.EnumNames(field.Enum), text) {
			return fmt.Errorf("must be a declared %s value", field.Enum)
		}
	case "bool":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("must be a boolean")
		}
	case "int32", "int64":
		if _, ok := value.(float64); !ok {
			return fmt.Errorf("must be an integer")
		}
	case "json":
	case "bytes":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("must be base64 text")
		}
	}
	return nil
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func wireName(name string) string {
	parts := strings.Split(name, "_")
	for i := 1; i < len(parts); i++ {
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

func clone(entity *domain.Entity) *domain.Entity {
	data, _ := json.Marshal(entity)
	var result domain.Entity
	_ = json.Unmarshal(data, &result)
	return &result
}

func cloneMap(record map[string]any) map[string]any {
	data, _ := json.Marshal(record)
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	return result
}

func cloneVersion(version Version) Version {
	version.Entity = clone(version.Entity)
	version.Changes = append([]domain.Change(nil), version.Changes...)
	return version
}
