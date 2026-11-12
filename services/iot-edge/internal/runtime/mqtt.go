package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BenjaminSRussell/passage/services/iot-edge/internal/domain"
	"github.com/BenjaminSRussell/passage/services/iot-edge/internal/mqtt"
)

// TagAware is implemented by a Source that must know which tags the applied configuration maps: the
// runtime calls SetTags each time a configuration is applied.
type TagAware interface {
	SetTags(tags []domain.TagMapping)
}

// MQTTSource reads machine data published to an MQTT broker (FR-25.9). A tag's address is the topic. A
// message is one reading:
//
//   - a JSON object {"value": <number or text>, "ts": "<RFC 3339>"}, where ts is optional, or
//   - a bare number or bare text.
//
// The value is a counter, measurement or state according to the tag's kind. Topics no tag maps are
// ignored. The broker connection is the Session's: it reconnects, and subscribes again, by itself.
type MQTTSource struct {
	Session *mqtt.Session
	Now     func() time.Time

	mu   sync.Mutex
	tags map[string]bool
}

// SetTags subscribes to the topics of the tags and forgets the ones no longer mapped.
func (s *MQTTSource) SetTags(tags []domain.TagMapping) {
	m := make(map[string]bool, len(tags))
	var filters []string
	for _, t := range tags {
		m[t.Address] = true
		if mqtt.ValidFilter(t.Address) {
			filters = append(filters, t.Address)
		}
	}
	s.mu.Lock()
	s.tags = m
	s.mu.Unlock()
	_ = s.Session.Subscribe(context.Background(), filters...)
}

// Run connects to the broker and emits a reading for every message on a mapped topic until ctx ends.
func (s *MQTTSource) Run(ctx context.Context, emit func(Reading)) error {
	now := s.Now
	if now == nil {
		now = time.Now
	}
	s.Session.SetHandler(func(m mqtt.Message) {
		s.mu.Lock()
		mapped := s.tags[m.Topic]
		s.mu.Unlock()
		if !mapped {
			return
		}
		if rd, err := ParseMQTTReading(m.Topic, m.Payload, now()); err == nil {
			emit(rd)
		}
	})
	err := s.Session.Run(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// ParseMQTTReading reads one message payload into a Reading stamped with the message's own ts, or now.
func ParseMQTTReading(topic string, payload []byte, now time.Time) (Reading, error) {
	p := strings.TrimSpace(string(payload))
	if p == "" {
		return Reading{}, errors.New("empty payload")
	}
	rd := Reading{Address: topic, TS: now}
	if p[0] == '{' {
		var doc struct {
			Value json.RawMessage `json:"value"`
			TS    string          `json:"ts"`
		}
		if err := json.Unmarshal([]byte(p), &doc); err != nil || len(doc.Value) == 0 {
			return Reading{}, errors.New("payload is not {\"value\": ...}")
		}
		if doc.TS != "" {
			ts, err := time.Parse(time.RFC3339Nano, doc.TS)
			if err != nil {
				return Reading{}, fmt.Errorf("ts is not RFC 3339: %w", err)
			}
			rd.TS = ts
		}
		p = strings.TrimSpace(string(doc.Value))
		if strings.HasPrefix(p, `"`) {
			var text string
			if err := json.Unmarshal(doc.Value, &text); err != nil {
				return Reading{}, err
			}
			p = text
		}
	}
	rd.Text = p
	if f, err := strconv.ParseFloat(p, 64); err == nil {
		rd.Number = f
	}
	return rd, nil
}

// ErrNoReadback is what a Writer returns from Read when its protocol has no way to read a value back.
var ErrNoReadback = errors.New("this protocol has no readback; the broker's acknowledgement is the confirmation")

// MQTTWriter publishes setpoints to the process control system's broker (destination pcs-mqtt, FR-25.15).
// The node of a setpoint is its topic. The message is {"value": v, "ts": "..."}, at QoS 1 and retained, so
// the controller that connects later still finds the last setpoint. The PUBACK is the confirmation; MQTT
// has no readback, so Read reports ErrNoReadback and the write audit says so.
type MQTTWriter struct {
	Session *mqtt.Session
	Now     func() time.Time
}

// Write publishes the value to the topic and waits for the broker's acknowledgement.
func (w *MQTTWriter) Write(ctx context.Context, node string, value float64) error {
	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	body, err := json.Marshal(map[string]any{"value": value, "ts": now().UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return err
	}
	if err := w.Session.Publish(ctx, node, body, 1, true); err != nil {
		return fmt.Errorf("publish setpoint to %s: %w", node, err)
	}
	return nil
}

// Read has no answer over MQTT.
func (w *MQTTWriter) Read(context.Context, string) (float64, error) { return 0, ErrNoReadback }

// PublishRecipe publishes the recipe document to the recipe's topic at QoS 1.
func (w *MQTTWriter) PublishRecipe(ctx context.Context, topic string, doc []byte) error {
	if err := w.Session.Publish(ctx, topic, doc, 1, false); err != nil {
		return fmt.Errorf("publish recipe to %s: %w", topic, err)
	}
	return nil
}
