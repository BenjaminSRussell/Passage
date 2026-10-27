package domain

import (
	"errors"
	"fmt"
	"sort"
)

// Picking workbench group keys (FR-28.13).
const (
	GroupCustomer = "CUSTOMER"
	GroupCarrier  = "CARRIER"
	GroupZone     = "ZONE"
	GroupProduct  = "PRODUCT"
)

// UnassignedGroup is the group key of a candidate without a value for the key.
const UnassignedGroup = "UNASSIGNED"

// Wave errors. Codes are the FUL catalog entries.
var (
	ErrWaveLimits   = errors.New("FUL-0032: wave limits must be positive")
	ErrGroupBy      = errors.New("FUL-0033: unknown workbench group key")
	ErrWaveTooLarge = errors.New("FUL-0034: one shipment has more lines than a wave may hold")
)

// WaveCandidate is a released shipment whose unpicked lines the workbench may
// group. Lines of one shipment always land in one wave (BR-28.14).
type WaveCandidate struct {
	ShipmentID    string
	OrderID       string
	LegalEntityID string
	Customer      string
	Carrier       string
	Zone          string // pick zone of the shipment's first zoned line; the ZONE group key
	ProductID     string // product of the shipment's first line; the PRODUCT group key
	LineIDs       []string
}

// WaveLimits are WAVE_MAX_LINES and WAVE_MAX_ORDERS (FR-28.2).
type WaveLimits struct {
	MaxLines  int
	MaxOrders int
}

// WavePlan is one wave the workbench would create.
type WavePlan struct {
	GroupKey      string
	LegalEntityID string
	ShipmentIDs   []string
	LineIDs       []string
	OrderIDs      []string
}

// GroupKey returns the value of the candidate for the group key groupBy.
func GroupKey(c WaveCandidate, groupBy string) (string, error) {
	var v string
	switch groupBy {
	case GroupCustomer:
		v = c.Customer
	case GroupCarrier:
		v = c.Carrier
	case GroupZone:
		v = c.Zone
	case GroupProduct:
		v = c.ProductID
	default:
		return "", fmt.Errorf("%w: %q", ErrGroupBy, groupBy)
	}
	if v == "" {
		return UnassignedGroup, nil
	}
	return v, nil
}

// PlanWaves groups candidates by groupBy and fills waves in deterministic order.
// A wave closes before it would exceed MaxLines or MaxOrders, and a new wave
// starts when the group key or the legal entity changes. A shipment is never
// split, so a single shipment larger than MaxLines is rejected.
func PlanWaves(cands []WaveCandidate, groupBy string, lim WaveLimits) ([]WavePlan, error) {
	if lim.MaxLines < 1 || lim.MaxOrders < 1 {
		return nil, ErrWaveLimits
	}
	if _, err := GroupKey(WaveCandidate{}, groupBy); err != nil {
		return nil, err
	}
	type keyed struct {
		key string
		c   WaveCandidate
	}
	items := make([]keyed, 0, len(cands))
	for _, c := range cands {
		if len(c.LineIDs) == 0 {
			continue
		}
		if len(c.LineIDs) > lim.MaxLines {
			return nil, fmt.Errorf("%w: shipment %s has %d lines", ErrWaveTooLarge, c.ShipmentID, len(c.LineIDs))
		}
		k, err := GroupKey(c, groupBy)
		if err != nil {
			return nil, err
		}
		items = append(items, keyed{key: k, c: c})
	}
	// Group key, then legal entity (a wave never mixes entities), then shipment,
	// so a preview and the create that follows plan the same waves.
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.key != b.key {
			return a.key < b.key
		}
		if a.c.LegalEntityID != b.c.LegalEntityID {
			return a.c.LegalEntityID < b.c.LegalEntityID
		}
		return a.c.ShipmentID < b.c.ShipmentID
	})

	plans := []WavePlan{}
	var orders map[string]bool
	for _, it := range items {
		// cur is the index of the wave being filled; -1 before the first wave exists.
		cur := len(plans) - 1
		fresh := cur < 0 || plans[cur].GroupKey != it.key || plans[cur].LegalEntityID != it.c.LegalEntityID ||
			len(plans[cur].LineIDs)+len(it.c.LineIDs) > lim.MaxLines
		if !fresh && !orders[it.c.OrderID] && len(plans[cur].OrderIDs)+1 > lim.MaxOrders {
			fresh = true
		}
		if fresh {
			plans = append(plans, WavePlan{GroupKey: it.key, LegalEntityID: it.c.LegalEntityID})
			orders = map[string]bool{}
			cur = len(plans) - 1
		}
		w := &plans[cur]
		w.ShipmentIDs = append(w.ShipmentIDs, it.c.ShipmentID)
		w.LineIDs = append(w.LineIDs, it.c.LineIDs...)
		if !orders[it.c.OrderID] {
			orders[it.c.OrderID] = true
			w.OrderIDs = append(w.OrderIDs, it.c.OrderID)
		}
	}
	return plans, nil
}
