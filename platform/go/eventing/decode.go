package eventing

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
)

const (
	HeaderTenantID       = "tenantid"
	headerBinaryVersion  = "ce_specversion"
	headerBinaryPrefix   = "ce_"
	defaultContentType   = "application/json"
	supportedSpecVersion = "1.0"
)

var ErrMalformedEvent = errors.New("malformed event")

type Record struct {
	Topic     string
	Partition int32
	Offset    int64
	Key       []byte
	Value     []byte
	Headers   map[string]string
	Raw       any
}

func Decode(r Record) (events.Event, error) {
	var evt events.Event
	if _, binary := r.Headers[headerBinaryVersion]; binary {
		evt = binaryEvent(r)
	} else if err := json.Unmarshal(r.Value, &evt); err != nil {
		return events.Event{}, fmt.Errorf("%w: structured json: %w", ErrMalformedEvent, err)
	}
	if err := validate(evt); err != nil {
		return events.Event{}, err
	}
	if header := r.Headers[HeaderTenantID]; header != "" && header != evt.TenantID {
		return events.Event{}, fmt.Errorf("%w: tenant header disagrees with the event", ErrMalformedEvent)
	}
	return evt, nil
}

func binaryEvent(r Record) events.Event {
	h := func(name string) string { return r.Headers[headerBinaryPrefix+name] }
	evt := events.Event{
		SpecVersion: h("specversion"), ID: h("id"), Source: h("source"), Type: h("type"), Subject: h("subject"),
		DataContentType: h("datacontenttype"), Data: json.RawMessage(r.Value),
		TenantID: h(HeaderTenantID), CorrelationID: h("correlationid"), CausationID: h("causationid"), Actor: h("actor"),
	}
	if evt.DataContentType == "" {
		evt.DataContentType = defaultContentType
	}
	evt.Time, _ = time.Parse(time.RFC3339Nano, h("time"))
	return evt
}

func validate(evt events.Event) error {
	switch {
	case evt.SpecVersion != supportedSpecVersion:
		return fmt.Errorf("%w: specversion %q", ErrMalformedEvent, evt.SpecVersion)
	case evt.ID == "":
		return fmt.Errorf("%w: missing id", ErrMalformedEvent)
	case !events.ValidType(evt.Type):
		return fmt.Errorf("%w: type %q", ErrMalformedEvent, evt.Type)
	case !ids.Valid(evt.TenantID):
		return fmt.Errorf("%w: tenantid is not a UUID", ErrMalformedEvent)
	case len(evt.Data) == 0:
		return fmt.Errorf("%w: missing data", ErrMalformedEvent)
	}
	return nil
}
