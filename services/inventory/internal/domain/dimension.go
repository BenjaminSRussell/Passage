// Package domain holds the pure inventory rules: dimension sets, the status
// model, on-hand arithmetic, journal validation, posting plans and FEFO batch
// selection. Nothing here performs I/O.
package domain

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Dimension names. The order of dimensionOrder is part of the hash contract
// shared with other services: changing it changes every DimensionSet key.
const (
	DimSite         = "site"
	DimWarehouse    = "warehouse"
	DimLocation     = "location"
	DimBatch        = "batch"
	DimSerial       = "serial"
	DimStatus       = "inventory_status"
	DimConfig       = "config"
	DimSize         = "size"
	DimColor        = "color"
	DimStyle        = "style"
	DimLicensePlate = "license_plate"
	DimOwner        = "owner"
)

var dimensionOrder = []string{DimSite, DimWarehouse, DimLocation, DimBatch, DimSerial, DimStatus,
	DimConfig, DimSize, DimColor, DimStyle, DimLicensePlate, DimOwner}

// DimensionNames returns the dimension names in canonical order.
func DimensionNames() []string { return slices.Clone(dimensionOrder) }

const maxDimensionValue = 60

// Dimensions is an inventory dimension set. Absent and blank entries mean
// "not set" and do not contribute to the hash.
type Dimensions map[string]string

// Normalize returns a copy with blank values dropped and surrounding space
// trimmed. Unknown names and over-long values are errors.
func (d Dimensions) Normalize() (Dimensions, error) {
	out := Dimensions{}
	for name, v := range d {
		if !slices.Contains(dimensionOrder, name) {
			return nil, fmt.Errorf("unknown dimension %q", name)
		}
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if utf8.RuneCountInString(v) > maxDimensionValue {
			return nil, fmt.Errorf("dimension %s is longer than %d characters", name, maxDimensionValue)
		}
		out[name] = v
	}
	return out, nil
}

// Canonical is the byte string hashed into a DimensionSet key: for each
// populated dimension in canonical order, name=len:value; . Length prefixes
// keep separators unambiguous. Callers must Normalize first.
func (d Dimensions) Canonical() string {
	var b strings.Builder
	for _, name := range dimensionOrder {
		if v := d[name]; v != "" {
			b.WriteString(name)
			b.WriteByte('=')
			b.WriteString(strconv.Itoa(len(v)))
			b.WriteByte(':')
			b.WriteString(v)
			b.WriteByte(';')
		}
	}
	return b.String()
}

// Hash is the DimensionSet key: SHA-256 of Canonical (32 bytes, stored as bytea).
func (d Dimensions) Hash() []byte {
	sum := sha256.Sum256([]byte(d.Canonical()))
	return sum[:]
}
