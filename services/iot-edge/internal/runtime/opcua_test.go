package runtime

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"

	"github.com/BenjaminSRussell/passage/services/iot-edge/internal/domain"
)

// plc is an in-process OPC-UA server (the gopcua server package) with a few plant tags. The live proof
// against a real vendor server stays in #213.
type plc struct {
	srv *server.Server
	ns  *server.MapNamespace
	url string
}

func startPLC(t *testing.T) *plc {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	s := server.New(
		server.EnableSecurity("None", ua.MessageSecurityModeNone),
		server.EnableAuthMode(ua.UserTokenTypeAnonymous),
		server.EndPoint("127.0.0.1", port),
	)
	ns := server.NewMapNamespace(s, "Plant")
	ns.Data["Press1.Count"] = uint32(100)
	ns.Data["Press1.State"] = "RUNNING"
	ns.Data["Reactor1.TempSP"] = float64(70)
	ns.Data["Reactor1.Level"] = int32(40)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &plc{srv: s, ns: ns, url: fmt.Sprintf("opc.tcp://127.0.0.1:%d", port)}
}

func (p *plc) node(name string) string { return fmt.Sprintf("ns=%d;s=%s", p.ns.ID(), name) }

type readings struct {
	mu sync.Mutex
	rs []Reading
}

func (r *readings) add(rd Reading) { r.mu.Lock(); r.rs = append(r.rs, rd); r.mu.Unlock() }
func (r *readings) all() []Reading {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Reading(nil), r.rs...)
}

func TestOPCUASourceEmitsValueChanges(t *testing.T) {
	p := startPLC(t)
	conn := &OPCUAConn{Config: OPCUAConfig{Endpoint: p.url, Interval: 50 * time.Millisecond, Timeout: 5 * time.Second}}
	src := &OPCUASource{Conn: conn, BackoffMin: 20 * time.Millisecond, BackoffMax: 50 * time.Millisecond}
	var got readings
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- src.Run(ctx, got.add) }()
	src.SetTags([]domain.TagMapping{
		{Address: p.node("Press1.Count"), MachineID: "PRESS-1", Kind: domain.KindCount},
		{Address: p.node("Press1.State"), MachineID: "PRESS-1", Kind: domain.KindState},
	})
	slowly(t, "the first readings", func() bool { return len(got.all()) >= 2 })
	first := got.all()
	byAddr := map[string]Reading{}
	for _, r := range first {
		byAddr[r.Address] = r
	}
	if c := byAddr[p.node("Press1.Count")]; c.Number != 100 {
		t.Fatalf("count reading %+v", c)
	}
	if s := byAddr[p.node("Press1.State")]; s.Text != "RUNNING" {
		t.Fatalf("state reading %+v", s)
	}

	p.ns.SetValue("Press1.Count", uint32(107))
	slowly(t, "the changed count", func() bool {
		for _, r := range got.all() {
			if r.Address == p.node("Press1.Count") && r.Number == 107 {
				return true
			}
		}
		return false
	})
	// A new configuration watches its own nodes without a restart.
	src.SetTags([]domain.TagMapping{{Address: p.node("Reactor1.Level"), MachineID: "R-1", Kind: domain.KindMeasurement}})
	slowly(t, "the reading of the new tag", func() bool {
		for _, r := range got.all() {
			if r.Address == p.node("Reactor1.Level") && r.Number == 40 {
				return true
			}
		}
		return false
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestOPCUAWriterWritesInTheNodesTypeAndReadsBack(t *testing.T) {
	p := startPLC(t)
	conn := &OPCUAConn{Config: OPCUAConfig{Endpoint: p.url, Timeout: 5 * time.Second}}
	defer conn.Close(context.Background())
	ctx := context.Background()

	if err := conn.Write(ctx, p.node("Reactor1.TempSP"), 72.5); err != nil {
		t.Fatal(err)
	}
	if v, ok := p.ns.GetValue("Reactor1.TempSP").(float64); !ok || v != 72.5 {
		t.Fatalf("server holds %#v", p.ns.GetValue("Reactor1.TempSP"))
	}
	if back, err := conn.Read(ctx, p.node("Reactor1.TempSP")); err != nil || back != 72.5 {
		t.Fatalf("readback %v %v", back, err)
	}
	// An Int32 node takes a whole number in its range and refuses a fraction.
	if err := conn.Write(ctx, p.node("Reactor1.Level"), 55); err != nil {
		t.Fatal(err)
	}
	if v, ok := p.ns.GetValue("Reactor1.Level").(int32); !ok || v != 55 {
		t.Fatalf("server holds %#v", p.ns.GetValue("Reactor1.Level"))
	}
	if err := conn.Write(ctx, p.node("Reactor1.Level"), 55.5); err == nil || !strings.Contains(err.Error(), "integer") {
		t.Fatalf("a fraction for an integer node: %v", err)
	}
	if err := conn.Write(ctx, p.node("Press1.State"), 1); err == nil {
		t.Fatal("a text node takes no numeric setpoint")
	}
	if err := conn.Write(ctx, p.node("No.Such.Node"), 1); err == nil {
		t.Fatal("an unknown node must fail")
	}
}

// BR-25.15 end to end: a pcs-opcua recipe is written through the OPC-UA writer, the value is read back from
// the server, and a node that is not on the allowlist is never touched.
func TestControlRecipeThroughTheOPCUAWriter(t *testing.T) {
	p := startPLC(t)
	conn := &OPCUAConn{Config: OPCUAConfig{Endpoint: p.url, Timeout: 5 * time.Second}}
	defer conn.Close(context.Background())
	sp := p.node("Reactor1.TempSP")
	rec := &fakeRecipes{recipes: []Recipe{{ID: "r1", OrderID: "o1", Destination: "pcs-opcua", Status: "sent",
		Setpoints: []Setpoint{{PhasePath: "Batch/R/Heat/Ramp", Node: sp, Value: 75}, {PhasePath: "Batch/R/Heat/Hold", Node: p.node("Reactor1.Level"), Value: 5}}}}}
	r, _ := simRuntime(t, newServer(), 1, Options{Recipes: rec, Writer: conn})
	cfg, _ := SimConfig(2, 1, func(int) string { return resA })
	cfg.WriteRules = []domain.WriteRule{{Node: sp, Min: 60, Max: 90}}
	if _, err := r.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ExecuteRecipes(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := p.ns.GetValue("Reactor1.TempSP"); v != float64(75) {
		t.Fatalf("setpoint on the server: %#v", v)
	}
	if v := p.ns.GetValue("Reactor1.Level"); v != int32(40) {
		t.Fatalf("a node off the allowlist was written: %#v", v)
	}
	if len(rec.writes) != 2 || !rec.writes[0].Written || rec.writes[0].Readback == nil || *rec.writes[0].Readback != 75 || rec.writes[1].Written {
		t.Fatalf("writes %+v", rec.writes)
	}
}

func TestOPCUAConfigValidation(t *testing.T) {
	for _, c := range []struct {
		cfg  OPCUAConfig
		want string
	}{
		{OPCUAConfig{}, "endpoint"},
		{OPCUAConfig{Endpoint: "opc.tcp://h:4840", SecurityPolicy: "Basic256Sha256"}, "both"},
		{OPCUAConfig{Endpoint: "opc.tcp://h:4840", SecurityPolicy: "Basic256Sha256", SecurityMode: "Sign"}, "certificate"},
		{OPCUAConfig{Endpoint: "opc.tcp://h:4840", Password: "x"}, "user name"},
		{OPCUAConfig{Endpoint: "opc.tcp://h:4840", Username: "op", Password: "x"}, "secured channel"},
		{OPCUAConfig{Endpoint: "opc.tcp://h:4840", SecurityPolicy: "None", SecurityMode: "None", Username: "op"}, "secured channel"},
	} {
		if err := c.cfg.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v", c.cfg, err)
		}
	}
	if err := (OPCUAConfig{Endpoint: "opc.tcp://h:4840"}).Validate(); err != nil {
		t.Fatal(err)
	}
	secured := OPCUAConfig{Endpoint: "opc.tcp://h:4840", SecurityPolicy: "Basic256Sha256", SecurityMode: "SignAndEncrypt", CertFile: "c.pem", KeyFile: "k.pem", Username: "op", Password: "x"}
	if err := secured.Validate(); err != nil || secured.Insecure() {
		t.Fatalf("a user name over a secured channel: %v, insecure %v", err, secured.Insecure())
	}
	if !(OPCUAConfig{Endpoint: "opc.tcp://h:4840"}).Insecure() {
		t.Fatal("the default policy None is not reported insecure")
	}
}

func TestTypedValue(t *testing.T) {
	if v, err := typedValue(float32(1), 2.5); err != nil || v != float32(2.5) {
		t.Fatal(v, err)
	}
	if _, err := typedValue(uint8(1), 300); err == nil {
		t.Fatal("300 does not fit a Byte")
	}
	if _, err := typedValue(true, 2); err == nil {
		t.Fatal("2 is not a Boolean")
	}
	if _, err := typedValue(float64(1), nan()); err == nil {
		t.Fatal("NaN must be refused")
	}
}

func nan() float64 { var z float64; return z / z }

// slowly waits for the in-process OPC-UA server, which publishes data changes about every two seconds.
func slowly(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
