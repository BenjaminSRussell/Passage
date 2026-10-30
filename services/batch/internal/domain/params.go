package domain

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

const (
	HandlerProductExplode     = "product.explode"
	HandlerProductGroupChange = "product.group-change"

	maxParamsBytes    = 64 << 10
	maxSelectedAxes   = 64
	maxSelectedValues = 500
	maxSelectedLen    = 128
)

type ExplodeParams struct {
	MasterID string              `json:"masterId"`
	Selected map[string][]string `json:"selected"`
}

func IsProductHandler(handler string) bool {
	return handler == HandlerProductExplode || handler == HandlerProductGroupChange
}

func noParams(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

// ValidateRunParams checks a run's params against the schema of its job's handler
// and returns field errors keyed under "params".
func ValidateRunParams(handler string, raw json.RawMessage) map[string]string {
	fields := map[string]string{}
	if len(raw) > maxParamsBytes {
		fields["params"] = fmt.Sprintf("must be at most %d bytes", maxParamsBytes)
		return fields
	}
	switch handler {
	case HandlerProductExplode:
		validateExplode(raw, fields)
	case HandlerProductGroupChange:
		if !noParams(raw) && !bytes.Equal(bytes.TrimSpace(raw), []byte("{}")) {
			fields["params"] = "product.group-change takes no params"
		}
	default:
		if !noParams(raw) {
			fields["params"] = "this handler takes no params"
		}
	}
	return fields
}

func validateExplode(raw json.RawMessage, fields map[string]string) {
	var in ExplodeParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if noParams(raw) || dec.Decode(&in) != nil {
		fields["params"] = `must be an object {"masterId": uuid, "selected": {axis: [values]}}`
		return
	}
	if !ids.Valid(in.MasterID) {
		fields["params.masterId"] = "must be a UUID"
	}
	if len(in.Selected) > maxSelectedAxes {
		fields["params.selected"] = fmt.Sprintf("at most %d axes", maxSelectedAxes)
		return
	}
	for axis, values := range in.Selected {
		if axis == "" || len(axis) > maxSelectedLen || len(values) == 0 || len(values) > maxSelectedValues {
			fields["params.selected"] = fmt.Sprintf("each axis needs a name and 1 to %d values", maxSelectedValues)
			return
		}
		for _, v := range values {
			if v == "" || len(v) > maxSelectedLen {
				fields["params.selected"] = fmt.Sprintf("values must be 1 to %d characters", maxSelectedLen)
				return
			}
		}
	}
}
