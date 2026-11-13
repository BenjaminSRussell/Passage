package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/BenjaminSRussell/passage/services/iot-edge/internal/domain"
)

// Control recipes on the edge (FR-25.15, FR-25.19, BR-25.15). The connector pulls the recipes sent to its
// node, acknowledges them, and writes their setpoints to the allowlisted nodes of the process control
// system through a Writer, auditing every write with the value read back. A write the allowlist, the limits
// or the recipe's state do not permit is refused and reported, never made.

// Recipe is a control recipe as the connector needs it.
type Recipe struct {
	ID          string
	OrderID     string
	Destination string
	Status      string
	Topic       string          // pcs-mqtt: the topic the recipe document is published to
	Document    json.RawMessage // the recipe document as the server holds it
	Setpoints   []Setpoint
}

// Setpoint is one value to write: the phase that holds it, the node and the value.
type Setpoint struct {
	PhasePath string
	Node      string
	Value     float64
}

// WriteReport is one write the connector made or refused.
type WriteReport struct {
	PhasePath string
	Node      string
	Value     float64
	Readback  *float64
	// NodeType is the OPC-UA data type of the node (Double, Float, Int32, Boolean, ...), "" when the protocol does not
	// say; the readback tolerance depends on it.
	NodeType string
	Written  bool
	Reason   string
	At       time.Time
}

// RecipeReport is the status and writes the connector reports for one recipe.
type RecipeReport struct {
	Status string
	Error  string
	Writes []WriteReport
}

// RecipePlane is the connector's side of the control recipe API.
type RecipePlane interface {
	PullRecipes(ctx context.Context) ([]Recipe, error)
	ReportRecipe(ctx context.Context, id string, rep RecipeReport) error
}

// Writer writes and reads the nodes of a process control system. The OPC-UA adapter and the MQTT publisher
// are two; Sim is the stand-in used for the demo plant and tests.
type Writer interface {
	Write(ctx context.Context, node string, value float64) error
	Read(ctx context.Context, node string) (float64, error)
}

// NodeTyper is implemented by a Writer that can tell the data type of a node (the OPC-UA adapter), so the readback
// is compared with the tolerance of that type.
type NodeTyper interface {
	NodeType(ctx context.Context, node string) (string, error)
}

// RecipePublisher is implemented by a Writer whose destination also takes the recipe document itself: the
// MQTT writer publishes it to the recipe's topic before the setpoints.
type RecipePublisher interface {
	PublishRecipe(ctx context.Context, topic string, doc []byte) error
}

// Destinations of control recipes that run on a connector.
const (
	DestOPCUA = "pcs-opcua"
	DestMQTT  = "pcs-mqtt"
)

// writerFor returns the writer for a destination: the one registered for it, or the default Writer for
// pcs-opcua (the option's first form).
func (r *Runtime) writerFor(dest string) Writer {
	if w := r.o.Writers[dest]; w != nil {
		return w
	}
	if dest == DestOPCUA {
		return r.o.Writer
	}
	return nil
}

func (r *Runtime) hasWriters() bool {
	if r.o.Writer != nil {
		return true
	}
	for _, w := range r.o.Writers {
		if w != nil {
			return true
		}
	}
	return false
}

// SimWriter is an in-memory process control system: it keeps what was written, can read back a different
// value (Drift) and can refuse a node.
type SimWriter struct {
	mu     sync.Mutex
	Values map[string]float64
	Drift  map[string]float64 // added to the value on read
	Fail   map[string]bool
	Writes []string // "node=value" in order
}

// Write stores the value.
func (w *SimWriter) Write(_ context.Context, node string, value float64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.Fail[node] {
		return fmt.Errorf("node %s is not writable", node)
	}
	if w.Values == nil {
		w.Values = map[string]float64{}
	}
	w.Values[node] = value
	w.Writes = append(w.Writes, fmt.Sprintf("%s=%v", node, value))
	return nil
}

// Read returns the stored value plus the node's drift.
func (w *SimWriter) Read(_ context.Context, node string) (float64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	v, ok := w.Values[node]
	if !ok {
		return 0, fmt.Errorf("node %s has no value", node)
	}
	return v + w.Drift[node], nil
}

// WriteLog returns the writes made so far.
func (w *SimWriter) WriteLog() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.Writes...)
}

// ExecuteRecipes pulls the node's recipes and works each: it acknowledges a sent recipe (received), starts
// it (in-execution) and, once, writes its setpoints under BR-25.15 and reports the writes. A recipe for
// a destination with no writer is reported as an error. With AutoComplete the recipe is
// reported completed after its writes (the demo plant); otherwise it stays in-execution until the process
// control system's own messages end it. It returns the number of recipes it advanced.
func (r *Runtime) ExecuteRecipes(ctx context.Context) (int, error) {
	if r.o.Recipes == nil || !r.hasWriters() {
		return 0, nil
	}
	recipes, err := r.o.Recipes.PullRecipes(ctx)
	if err != nil {
		return 0, fmt.Errorf("pull control recipes: %w", err)
	}
	advanced := 0
	for _, rc := range recipes {
		done, err := r.executeRecipe(ctx, rc)
		if err != nil {
			r.log.Warn("control recipe not advanced", "recipe", rc.ID, "err", err)
			continue
		}
		if done {
			advanced++
		}
	}
	return advanced, nil
}

func (r *Runtime) executeRecipe(ctx context.Context, rc Recipe) (bool, error) {
	writer := r.writerFor(rc.Destination)
	if writer == nil {
		return true, r.o.Recipes.ReportRecipe(ctx, rc.ID, RecipeReport{Status: "error",
			Error: "this connector is not configured for destination " + rc.Destination})
	}
	status := rc.Status
	if status == "sent" {
		if err := r.o.Recipes.ReportRecipe(ctx, rc.ID, RecipeReport{Status: "received"}); err != nil {
			return false, err
		}
		status = "received"
	}
	if status == "received" {
		if err := r.o.Recipes.ReportRecipe(ctx, rc.ID, RecipeReport{Status: "in-execution"}); err != nil {
			return false, err
		}
		status = "in-execution"
	}
	r.mu.Lock()
	already := r.written[rc.ID]
	cfg := r.cfg
	r.mu.Unlock()
	if status != "in-execution" || already {
		return false, nil
	}
	rep := RecipeReport{}
	refused := 0
	if pub, ok := writer.(RecipePublisher); ok && rc.Topic != "" && len(rc.Document) > 0 {
		if err := pub.PublishRecipe(ctx, rc.Topic, rc.Document); err != nil {
			rep.Status, rep.Error = "error", "the recipe was not published to "+rc.Topic+": "+err.Error()
			if err := r.o.Recipes.ReportRecipe(ctx, rc.ID, rep); err != nil {
				return false, err
			}
			r.mu.Lock()
			r.written[rc.ID] = true
			r.mu.Unlock()
			return true, nil
		}
	}
	for _, sp := range rc.Setpoints {
		wr := WriteReport{PhasePath: sp.PhasePath, Node: sp.Node, Value: sp.Value, At: r.o.Now().UTC()}
		// The recipe is in-execution here: the server accepts a write only in that state.
		if err := cfg.CheckWrite(domain.WriteRequest{Node: sp.Node, Value: sp.Value, RecipeInExecution: true}); err != nil {
			wr.Reason = err.Error()
			refused++
		} else if err := writer.Write(ctx, sp.Node, sp.Value); err != nil {
			wr.Reason = "write failed: " + err.Error()
			refused++
		} else {
			wr.Written = true
			if back, err := writer.Read(ctx, sp.Node); errors.Is(err, ErrNoReadback) {
				wr.Reason = "no readback: " + err.Error()
			} else if err != nil {
				wr.Reason = "readback failed: " + err.Error()
			} else {
				wr.Readback = &back
				if typer, ok := writer.(NodeTyper); ok {
					if t, err := typer.NodeType(ctx, sp.Node); err == nil && domain.KnownNodeType(t) {
						wr.NodeType = t
					}
				}
				// The server decides the deviation with the same tolerance by node type (BR-25.15).
				if !domain.ReadbackMatches(sp.Value, back, domain.ReadbackTolerance(wr.NodeType, sp.Value)) {
					r.deviations.Add(1)
					r.log.Warn("setpoint readback differs from the value written (BR-25.15)", "node", sp.Node, "written", sp.Value, "readback", back)
				}
			}
		}
		rep.Writes = append(rep.Writes, wr)
	}
	switch {
	case refused > 0:
		rep.Status, rep.Error = "error", fmt.Sprintf("%d of %d setpoints were refused or failed; see the write audit", refused, len(rc.Setpoints))
	case r.o.AutoComplete:
		rep.Status = "completed"
	}
	if err := r.o.Recipes.ReportRecipe(ctx, rc.ID, rep); err != nil {
		return false, err
	}
	r.mu.Lock()
	r.written[rc.ID] = true
	r.mu.Unlock()
	r.recipesExecuted.Add(1)
	return true, nil
}

func (r *Runtime) recipeLoop(ctx context.Context) error {
	t := time.NewTicker(r.o.RecipeEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
		if _, err := r.ExecuteRecipes(ctx); err != nil && ctx.Err() == nil {
			r.log.Warn("control recipes", "err", err)
		}
	}
}

type wireRecipe struct {
	ID          string          `json:"id"`
	OrderID     string          `json:"orderId"`
	Destination string          `json:"destination"`
	Status      string          `json:"status"`
	Topic       *string         `json:"topic"`
	Recipe      json.RawMessage `json:"recipe"`
}

// setpointsOf reads the setpoints out of a recipe document, in order, with the path of their phase.
func setpointsOf(doc json.RawMessage) ([]Setpoint, error) {
	var d struct {
		Procedure struct {
			Name           string `json:"name"`
			UnitProcedures []struct {
				Name       string `json:"name"`
				Operations []struct {
					Name   string `json:"name"`
					Phases []struct {
						Name         string `json:"name"`
						Instructions []struct {
							Category string   `json:"category"`
							Node     string   `json:"node"`
							Value    *float64 `json:"value"`
						} `json:"instructions"`
					} `json:"phases"`
				} `json:"operations"`
			} `json:"unitProcedures"`
		} `json:"procedure"`
	}
	if err := json.Unmarshal(doc, &d); err != nil {
		return nil, err
	}
	var out []Setpoint
	for _, up := range d.Procedure.UnitProcedures {
		for _, op := range up.Operations {
			for _, ph := range op.Phases {
				for _, in := range ph.Instructions {
					if in.Category == "process-parameter-setpoint" && in.Value != nil {
						out = append(out, Setpoint{PhasePath: strings.Join([]string{d.Procedure.Name, up.Name, op.Name, ph.Name}, "/"), Node: in.Node, Value: *in.Value})
					}
				}
			}
		}
	}
	return out, nil
}

// PullRecipes reads GET edge-nodes/{id}/control-recipes.
func (c *Client) PullRecipes(ctx context.Context) ([]Recipe, error) {
	res, body, err := c.do(ctx, http.MethodGet, "/v1/shopfloor/edge-nodes/"+url.PathEscape(c.EdgeID)+"/control-recipes", nil)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("control recipes: %s", problemText(res, body))
	}
	var out struct {
		Items []wireRecipe `json:"items"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("control recipes answer: %w", err)
	}
	recipes := make([]Recipe, 0, len(out.Items))
	for _, w := range out.Items {
		sp, err := setpointsOf(w.Recipe)
		if err != nil {
			return nil, fmt.Errorf("control recipe %s: %w", w.ID, err)
		}
		rc := Recipe{ID: w.ID, OrderID: w.OrderID, Destination: w.Destination, Status: w.Status, Document: w.Recipe, Setpoints: sp}
		if w.Topic != nil {
			rc.Topic = *w.Topic
		}
		recipes = append(recipes, rc)
	}
	return recipes, nil
}

// ReportRecipe posts the status and writes of one recipe.
func (c *Client) ReportRecipe(ctx context.Context, id string, rep RecipeReport) error {
	type wire struct {
		PhasePath string   `json:"phasePath"`
		Node      string   `json:"node"`
		Value     float64  `json:"value"`
		Readback  *float64 `json:"readback,omitempty"`
		NodeType  string   `json:"nodeType,omitempty"`
		Written   bool     `json:"written"`
		Reason    string   `json:"reason,omitempty"`
		At        string   `json:"at,omitempty"`
	}
	body := map[string]any{}
	if rep.Status != "" {
		body["status"] = rep.Status
	}
	if rep.Error != "" {
		body["error"] = rep.Error
	}
	if len(rep.Writes) > 0 {
		writes := make([]wire, 0, len(rep.Writes))
		for _, w := range rep.Writes {
			writes = append(writes, wire{w.PhasePath, w.Node, w.Value, w.Readback, w.NodeType, w.Written, w.Reason, w.At.UTC().Format(time.RFC3339Nano)})
		}
		body["writes"] = writes
	}
	res, b, err := c.do(ctx, http.MethodPost, "/v1/shopfloor/edge-nodes/"+url.PathEscape(c.EdgeID)+"/control-recipes/"+url.PathEscape(id)+":report", body)
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return errors.New("report control recipe: " + problemText(res, b))
	}
	return nil
}
