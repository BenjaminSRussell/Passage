package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"

	"github.com/BenjaminSRussell/passage/services/iot-edge/internal/domain"
)

// OPCUAConfig is how the connector reaches a plant's OPC-UA server (FR-25.9).
type OPCUAConfig struct {
	Endpoint string // opc.tcp://host:4840
	// SecurityPolicy and SecurityMode select the channel security. "None" (the default) is for the dev and
	// demo plant only (the connector refuses it outside PASSAGE_ENV=dev, see Insecure); a real plant uses a
	// signed and encrypted channel with CertFile and KeyFile.
	SecurityPolicy string // None, Basic256Sha256, Aes128_Sha256_RsaOaep, ...
	SecurityMode   string // None, Sign, SignAndEncrypt
	CertFile       string // PEM client certificate for a secured channel
	KeyFile        string // PEM private key of CertFile
	Username       string // anonymous when empty
	Password       string
	// Interval is the publishing and sampling interval of the subscription; default 1 s.
	Interval time.Duration
	// Timeout bounds the connect and each request; default 10 s.
	Timeout time.Duration
}

func (c OPCUAConfig) policy() string {
	if c.SecurityPolicy == "" {
		return "None"
	}
	return c.SecurityPolicy
}

func (c OPCUAConfig) mode() string {
	if c.SecurityMode == "" {
		return "None"
	}
	return c.SecurityMode
}

func (c OPCUAConfig) timeout() time.Duration {
	if c.Timeout <= 0 {
		return 10 * time.Second
	}
	return c.Timeout
}

// Validate checks the settings that must agree before any connection is tried.
func (c OPCUAConfig) Validate() error {
	switch {
	case c.Endpoint == "":
		return errors.New("an OPC-UA endpoint is required")
	case (c.policy() == "None") != (c.mode() == "None"):
		return errors.New("OPC-UA security policy and mode must both be None or both be set")
	case c.policy() != "None" && (c.CertFile == "" || c.KeyFile == ""):
		return errors.New("a secured OPC-UA channel needs a client certificate and key")
	case c.Password != "" && c.Username == "":
		return errors.New("an OPC-UA password needs a user name")
	case c.Insecure() && (c.Username != "" || c.Password != ""):
		// Over security policy None the user name token, password included, crosses the plant network unencrypted.
		return errors.New("an OPC-UA user name and password need a secured channel (security policy other than None)")
	}
	return nil
}

// Insecure reports a channel with security policy None: nothing is signed or encrypted. The connector allows it
// only when PASSAGE_ENV is dev.
func (c OPCUAConfig) Insecure() bool { return c.policy() == "None" }

func (c OPCUAConfig) options(ctx context.Context) ([]opcua.Option, error) {
	opts := []opcua.Option{opcua.RequestTimeout(c.timeout()), opcua.DialTimeout(c.timeout())}
	token := ua.UserTokenTypeAnonymous
	if c.Username != "" {
		token = ua.UserTokenTypeUserName
	}
	if c.policy() == "None" {
		opts = append(opts, opcua.SecurityMode(ua.MessageSecurityModeNone), opcua.SecurityPolicy("None"))
	} else {
		eps, err := opcua.GetEndpoints(ctx, c.Endpoint)
		if err != nil {
			return nil, fmt.Errorf("list OPC-UA endpoints: %w", err)
		}
		ep, err := opcua.SelectEndpoint(eps, c.policy(), ua.MessageSecurityModeFromString(c.mode()))
		if err != nil {
			return nil, fmt.Errorf("select OPC-UA endpoint: %w", err)
		}
		ep.EndpointURL = c.Endpoint
		opts = append(opts, opcua.SecurityFromEndpoint(ep, token), opcua.CertificateFile(c.CertFile), opcua.PrivateKeyFile(c.KeyFile))
	}
	if c.Username != "" {
		opts = append(opts, opcua.AuthUsername(c.Username, c.Password))
	} else {
		opts = append(opts, opcua.AuthAnonymous())
	}
	return opts, nil
}

// OPCUAConn is the connection to the OPC-UA server that the source (readings) and the writer (setpoints)
// share. It connects when first used and connects again after the server went away.
type OPCUAConn struct {
	Config OPCUAConfig

	mu sync.Mutex
	c  *opcua.Client
}

func (o *OPCUAConn) client(ctx context.Context) (*opcua.Client, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.c != nil && o.c.State() == opcua.Connected {
		return o.c, nil
	}
	o.dropLocked(ctx)
	opts, err := o.Config.options(ctx)
	if err != nil {
		return nil, err
	}
	c, err := opcua.NewClient(o.Config.Endpoint, opts...)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, o.Config.timeout())
	defer cancel()
	if err := c.Connect(cctx); err != nil {
		return nil, fmt.Errorf("connect to OPC-UA server %s: %w", o.Config.Endpoint, err)
	}
	o.c = c
	return c, nil
}

func (o *OPCUAConn) dropLocked(ctx context.Context) {
	if o.c != nil {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		_ = o.c.Close(cctx)
		cancel()
		o.c = nil
	}
}

// Close ends the session.
func (o *OPCUAConn) Close(ctx context.Context) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.dropLocked(ctx)
}

// Reset drops the connection so the next use dials again.
func (o *OPCUAConn) Reset(ctx context.Context) { o.Close(ctx) }

// readValue reads the current value of a node.
func (o *OPCUAConn) readValue(ctx context.Context, node string) (*ua.DataValue, error) {
	id, err := ua.ParseNodeID(node)
	if err != nil {
		return nil, fmt.Errorf("node id %q: %w", node, err)
	}
	c, err := o.client(ctx)
	if err != nil {
		return nil, err
	}
	res, err := c.Read(ctx, &ua.ReadRequest{MaxAge: 0, TimestampsToReturn: ua.TimestampsToReturnBoth,
		NodesToRead: []*ua.ReadValueID{{NodeID: id, AttributeID: ua.AttributeIDValue}}})
	if err != nil {
		o.Reset(ctx)
		return nil, err
	}
	if len(res.Results) != 1 {
		return nil, errors.New("the server answered a read with no result")
	}
	if st := res.Results[0].Status; st != ua.StatusOK {
		return nil, fmt.Errorf("read %s: %v", node, st)
	}
	return res.Results[0], nil
}

// Read implements Writer: the value of the node as a number, for the BR-25.15 readback.
func (o *OPCUAConn) Read(ctx context.Context, node string) (float64, error) {
	dv, err := o.readValue(ctx, node)
	if err != nil {
		return 0, err
	}
	n, _, ok := variantNumber(dv.Value.Value())
	if !ok {
		return 0, fmt.Errorf("node %s holds a %T, not a number", node, dv.Value.Value())
	}
	return n, nil
}

// NodeType implements NodeTyper: the OPC-UA built-in type of the node's current value (Double, Float, Int32, ...).
func (o *OPCUAConn) NodeType(ctx context.Context, node string) (string, error) {
	dv, err := o.readValue(ctx, node)
	if err != nil {
		return "", err
	}
	return opcuaTypeName(dv.Value.Value()), nil
}

func opcuaTypeName(v any) string {
	switch v.(type) {
	case float64:
		return "Double"
	case float32:
		return "Float"
	case int8:
		return "SByte"
	case uint8:
		return "Byte"
	case int16:
		return "Int16"
	case uint16:
		return "UInt16"
	case int32:
		return "Int32"
	case uint32:
		return "UInt32"
	case int64:
		return "Int64"
	case uint64:
		return "UInt64"
	case bool:
		return "Boolean"
	}
	return ""
}

// Write implements Writer: it writes value to the node in the node's own data type, read first, so a
// Float node gets a Float and an Int32 node an Int32. A value the type cannot hold exactly is refused.
func (o *OPCUAConn) Write(ctx context.Context, node string, value float64) error {
	id, err := ua.ParseNodeID(node)
	if err != nil {
		return fmt.Errorf("node id %q: %w", node, err)
	}
	cur, err := o.readValue(ctx, node)
	if err != nil {
		return fmt.Errorf("cannot write %s, its type is unknown: %w", node, err)
	}
	typed, err := typedValue(cur.Value.Value(), value)
	if err != nil {
		return fmt.Errorf("node %s: %w", node, err)
	}
	v, err := ua.NewVariant(typed)
	if err != nil {
		return err
	}
	c, err := o.client(ctx)
	if err != nil {
		return err
	}
	res, err := c.Write(ctx, &ua.WriteRequest{NodesToWrite: []*ua.WriteValue{{NodeID: id, AttributeID: ua.AttributeIDValue,
		Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: v}}}})
	if err != nil {
		o.Reset(ctx)
		return err
	}
	if len(res.Results) != 1 || res.Results[0] != ua.StatusOK {
		return fmt.Errorf("the server refused the write: %v", res.Results)
	}
	return nil
}

// variantNumber turns a value of a numeric OPC-UA type into a float64. isCount says it is an integer type.
func variantNumber(v any) (n float64, integer bool, ok bool) {
	switch x := v.(type) {
	case float64:
		return x, false, true
	case float32:
		return float64(x), false, true
	case int8:
		return float64(x), true, true
	case int16:
		return float64(x), true, true
	case int32:
		return float64(x), true, true
	case int64:
		return float64(x), true, true
	case uint8:
		return float64(x), true, true
	case uint16:
		return float64(x), true, true
	case uint32:
		return float64(x), true, true
	case uint64:
		return float64(x), true, true
	case bool:
		if x {
			return 1, true, true
		}
		return 0, true, true
	}
	return 0, false, false
}

// typedValue converts value to the Go type of sample, which is how the server's node is typed.
func typedValue(sample any, value float64) (any, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, errors.New("the value is not a finite number")
	}
	whole := func(lo, hi float64) (float64, error) {
		if value != math.Trunc(value) || value < lo || value > hi {
			return 0, fmt.Errorf("%v does not fit the node's integer type", value)
		}
		return value, nil
	}
	switch sample.(type) {
	case float64:
		return value, nil
	case float32:
		if math.Abs(value) > math.MaxFloat32 {
			return nil, fmt.Errorf("%v does not fit a Float", value)
		}
		return float32(value), nil
	case int8:
		v, err := whole(math.MinInt8, math.MaxInt8)
		return int8(v), err
	case int16:
		v, err := whole(math.MinInt16, math.MaxInt16)
		return int16(v), err
	case int32:
		v, err := whole(math.MinInt32, math.MaxInt32)
		return int32(v), err
	case int64:
		v, err := whole(-(1 << 53), 1<<53)
		return int64(v), err
	case uint8:
		v, err := whole(0, math.MaxUint8)
		return uint8(v), err
	case uint16:
		v, err := whole(0, math.MaxUint16)
		return uint16(v), err
	case uint32:
		v, err := whole(0, math.MaxUint32)
		return uint32(v), err
	case uint64:
		v, err := whole(0, 1<<53)
		return uint64(v), err
	case bool:
		if value != 0 && value != 1 {
			return nil, errors.New("a Boolean node takes 0 or 1")
		}
		return value == 1, nil
	}
	return nil, fmt.Errorf("a node of type %T takes no numeric setpoint", sample)
}

// OPCUASource subscribes to the nodes the applied configuration maps and emits a reading for every value
// change (FR-25.9). A tag's address is its node id ("ns=2;s=Press1.Count"). It dials again, with a growing
// wait, after the server or the network went away, and watches the nodes of a new configuration at once.
type OPCUASource struct {
	Conn *OPCUAConn
	Now  func() time.Time
	Log  *slog.Logger
	// BackoffMin and BackoffMax bound the wait between connection attempts (default 1 s and 30 s).
	BackoffMin, BackoffMax time.Duration

	mu      sync.Mutex
	nodes   []string
	changed chan struct{}
}

// SetTags tells the source which nodes to watch.
func (s *OPCUASource) SetTags(tags []domain.TagMapping) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes = s.nodes[:0]
	for _, t := range tags {
		s.nodes = append(s.nodes, t.Address)
	}
	if s.changed == nil {
		s.changed = make(chan struct{}, 1)
	}
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func (s *OPCUASource) snapshot() ([]string, chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.changed == nil {
		s.changed = make(chan struct{}, 1)
	}
	select { // a signal sent before this snapshot is already in it
	case <-s.changed:
	default:
	}
	return append([]string(nil), s.nodes...), s.changed
}

func (s *OPCUASource) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Run watches the nodes until ctx ends.
func (s *OPCUASource) Run(ctx context.Context, emit func(Reading)) error {
	lo, hi := s.BackoffMin, s.BackoffMax
	if lo <= 0 {
		lo = time.Second
	}
	if hi <= 0 {
		hi = 30 * time.Second
	}
	wait := lo
	for ctx.Err() == nil {
		err := s.watch(ctx, emit)
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			s.log().Warn("OPC-UA source interrupted; dialling again", "err", err, "retryIn", wait)
			s.Conn.Reset(ctx)
		} else {
			wait = lo
			continue // the nodes changed; watch the new set at once
		}
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
		wait = min(wait*2, hi)
	}
	s.Conn.Close(ctx)
	return nil
}

// watch runs one subscription until the set of nodes changes (nil), or it fails (the error).
func (s *OPCUASource) watch(ctx context.Context, emit func(Reading)) error {
	nodes, changed := s.snapshot()
	if len(nodes) == 0 {
		select { // nothing to watch until a configuration arrives
		case <-ctx.Done():
		case <-changed:
		}
		return nil
	}
	c, err := s.Conn.client(ctx)
	if err != nil {
		return err
	}
	interval := s.Conn.Config.Interval
	if interval <= 0 {
		interval = time.Second
	}
	notify := make(chan *opcua.PublishNotificationData, 1024)
	sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: interval}, notify)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		_ = sub.Cancel(cctx)
		cancel()
	}()
	var reqs []*ua.MonitoredItemCreateRequest
	byHandle := map[uint32]string{}
	for _, n := range nodes {
		id, err := ua.ParseNodeID(n)
		if err != nil {
			s.log().Warn("tag address is not an OPC-UA node id; not watched", "address", n, "err", err)
			continue
		}
		h := uint32(len(reqs) + 1)
		byHandle[h] = n
		reqs = append(reqs, opcua.NewMonitoredItemCreateRequestWithDefaults(id, ua.AttributeIDValue, h))
	}
	res, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth, reqs...)
	if err != nil {
		return fmt.Errorf("monitor nodes: %w", err)
	}
	for i, r := range res.Results {
		if r.StatusCode != ua.StatusOK {
			s.log().Warn("the server does not serve this node; not watched", "node", byHandle[uint32(i+1)], "status", r.StatusCode)
		}
	}
	now := s.Now
	if now == nil {
		now = time.Now
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-changed:
			return nil
		case res := <-notify:
			if res == nil {
				return errors.New("the subscription ended")
			}
			if res.Error != nil {
				return res.Error
			}
			dcn, ok := res.Value.(*ua.DataChangeNotification)
			if !ok {
				continue
			}
			for _, item := range dcn.MonitoredItems {
				addr, known := byHandle[item.ClientHandle]
				if !known || item.Value == nil || item.Value.Status != ua.StatusOK || item.Value.Value == nil {
					continue
				}
				ts := item.Value.SourceTimestamp
				if ts.IsZero() {
					ts = item.Value.ServerTimestamp
				}
				if ts.IsZero() || ts.Year() < 2000 {
					ts = now()
				}
				emit(readingOf(addr, ts, item.Value.Value.Value()))
			}
		}
	}
}

// readingOf turns an OPC-UA value into a Reading: numbers and booleans fill Number, strings and the
// text form of a number fill Text, so the tag's kind decides how it is read.
func readingOf(addr string, ts time.Time, v any) Reading {
	rd := Reading{Address: addr, TS: ts.UTC()}
	if n, _, ok := variantNumber(v); ok {
		rd.Number = n
		rd.Text = strconv.FormatFloat(n, 'f', -1, 64)
		return rd
	}
	rd.Text = fmt.Sprint(v)
	return rd
}
