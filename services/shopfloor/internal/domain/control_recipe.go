package domain

import (
	"fmt"
	"math"
	"strings"
)

// Control recipes (FR-25.15, BR-25.11, BR-25.15). A control recipe is the ISA-88 structure of one order
// for one destination: procedure, unit procedures, operations and phases, each phase holding ordered
// process instructions. It is generated when the order is released and then sent: to the kiosk (rendered
// as a PI sheet) or to a process control system through the edge connector.

// Destinations of a control recipe.
const (
	DestKiosk    = "kiosk"
	DestPCSOPCUA = "pcs-opcua"
	DestPCSMQTT  = "pcs-mqtt"
)

// ValidDestination reports whether d is a destination.
func ValidDestination(d string) bool { return d == DestKiosk || d == DestPCSOPCUA || d == DestPCSMQTT }

// Process instruction categories (FR-25.15).
const (
	InstText        = "instruction-text"
	InstSetpoint    = "process-parameter-setpoint"
	InstDataRequest = "process-data-request"
	InstCalculation = "calculation"
	InstInspection  = "inspection-characteristic"
	InstMaterials   = "material-list"
	InstSignature   = "signature-request"
	InstSequence    = "sequence-definition"
)

var instructionCategories = map[string]bool{InstText: true, InstSetpoint: true, InstDataRequest: true, InstCalculation: true,
	InstInspection: true, InstMaterials: true, InstSignature: true, InstSequence: true}

// Control recipe statuses.
const (
	RecipeSent        = "sent"
	RecipeReceived    = "received"
	RecipeInExecution = "in-execution"
	RecipeCompleted   = "completed"
	RecipeError       = "error"
)

// MaxResends is how often a recipe in error may be sent again (BR-25.11).
const MaxResends = 5

// Size bounds of a recipe document.
const (
	maxRecipeBytes    = 256 << 10
	maxUnitProcedures = 50
	maxOperations     = 100
	maxPhases         = 200
	maxInstructions   = 200
	maxSetpoints      = 500
)

// Characteristic definition of an instruction: the value type, unit and limits a setpoint, data request
// or inspection is checked against. Values are decimal text, as everywhere in the service.
type InstructionCharacteristic struct {
	Name string   `json:"name"`
	Type string   `json:"type"` // numeric, text, boolean
	Unit string   `json:"unit,omitempty"`
	Min  *float64 `json:"min,omitempty"`
	Max  *float64 `json:"max,omitempty"`
}

// Instruction is one ordered process instruction of a phase.
type Instruction struct {
	Seq      int                        `json:"seq"`
	Category string                     `json:"category"`
	Text     string                     `json:"text,omitempty"`
	Char     *InstructionCharacteristic `json:"characteristic,omitempty"`
	// Value is the setpoint a process-parameter-setpoint instruction carries, scaled to the batch.
	Value *float64 `json:"value,omitempty"`
	// Node is the OPC-UA node id (pcs-opcua) or topic (pcs-mqtt) a setpoint is written to or a data
	// request reads from.
	Node string `json:"node,omitempty"`
}

// Phase, Operation, UnitProcedure and Procedure are the ISA-88 levels.
type Phase struct {
	Name         string        `json:"name"`
	Instructions []Instruction `json:"instructions"`
}

type Operation struct {
	Name   string  `json:"name"`
	Phases []Phase `json:"phases"`
}

type UnitProcedure struct {
	Name       string      `json:"name"`
	Operations []Operation `json:"operations"`
}

type Procedure struct {
	Name           string          `json:"name"`
	UnitProcedures []UnitProcedure `json:"unitProcedures"`
}

// RecipeDoc is the stored recipe structure.
type RecipeDoc struct {
	Procedure Procedure `json:"procedure"`
}

func finiteNum(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// FieldErrors validates a recipe for its destination: the structure has names at every level (unique
// within the level) and at least one phase with an instruction; instruction sequence numbers count up
// within the phase; a setpoint carries a numeric characteristic, a finite value inside its limits and, for
// a process control destination, the node or topic it is written to; a data request names its
// characteristic (and a node for a process control destination); instruction text is required for text,
// signature and material instructions. Keys are JSON paths.
func (d RecipeDoc) FieldErrors(destination string) map[string]string {
	f := map[string]string{}
	p := d.Procedure
	if n := strings.TrimSpace(p.Name); n == "" || len(n) > 120 {
		f["recipe.procedure.name"] = "must be 1 to 120 characters"
	}
	if len(p.UnitProcedures) == 0 || len(p.UnitProcedures) > maxUnitProcedures {
		f["recipe.procedure.unitProcedures"] = fmt.Sprintf("must hold 1 to %d unit procedures", maxUnitProcedures)
		return f
	}
	instructions, setpoints := 0, 0
	seenUP := map[string]bool{}
	for ui, up := range p.UnitProcedures {
		upPath := fmt.Sprintf("recipe.procedure.unitProcedures[%d]", ui)
		checkName(f, upPath, up.Name, seenUP)
		if len(up.Operations) == 0 || len(up.Operations) > maxOperations {
			f[upPath+".operations"] = fmt.Sprintf("must hold 1 to %d operations", maxOperations)
			continue
		}
		seenOp := map[string]bool{}
		for oi, op := range up.Operations {
			opPath := fmt.Sprintf("%s.operations[%d]", upPath, oi)
			checkName(f, opPath, op.Name, seenOp)
			if len(op.Phases) == 0 || len(op.Phases) > maxPhases {
				f[opPath+".phases"] = fmt.Sprintf("must hold 1 to %d phases", maxPhases)
				continue
			}
			seenPh := map[string]bool{}
			for pi, ph := range op.Phases {
				phPath := fmt.Sprintf("%s.phases[%d]", opPath, pi)
				checkName(f, phPath, ph.Name, seenPh)
				if len(ph.Instructions) == 0 || len(ph.Instructions) > maxInstructions {
					f[phPath+".instructions"] = fmt.Sprintf("must hold 1 to %d instructions", maxInstructions)
					continue
				}
				for ii, in := range ph.Instructions {
					instructions++
					if in.Category == InstSetpoint {
						setpoints++
					}
					checkInstruction(f, fmt.Sprintf("%s.instructions[%d]", phPath, ii), ii+1, in, destination)
				}
			}
		}
	}
	if setpoints > maxSetpoints {
		f["recipe"] = fmt.Sprintf("holds %d setpoints; at most %d", setpoints, maxSetpoints)
	}
	if instructions == 0 {
		f["recipe"] = "must hold at least one instruction"
	}
	return f
}

func checkName(f map[string]string, path, name string, seen map[string]bool) {
	n := strings.TrimSpace(name)
	switch {
	case n == "" || len(n) > 120:
		f[path+".name"] = "must be 1 to 120 characters"
	case seen[n]:
		f[path+".name"] = "is used twice at this level"
	}
	seen[n] = true
}

func checkInstruction(f map[string]string, path string, want int, in Instruction, destination string) {
	if in.Seq != want {
		f[path+".seq"] = fmt.Sprintf("must be %d: instructions are numbered 1, 2, 3 in order", want)
	}
	if !instructionCategories[in.Category] {
		f[path+".category"] = "must be instruction-text, process-parameter-setpoint, process-data-request, calculation, inspection-characteristic, material-list, signature-request or sequence-definition"
		return
	}
	if len(in.Text) > 4000 {
		f[path+".text"] = "must be at most 4000 characters"
	}
	switch in.Category {
	case InstText, InstSignature, InstMaterials, InstSequence, InstCalculation:
		if strings.TrimSpace(in.Text) == "" {
			f[path+".text"] = "is required for this category"
		}
	case InstSetpoint, InstDataRequest, InstInspection:
		c := in.Char
		if c == nil || strings.TrimSpace(c.Name) == "" || len(c.Name) > 120 {
			f[path+".characteristic"] = "needs a name of 1 to 120 characters"
			return
		}
		if c.Type != "numeric" && c.Type != "text" && c.Type != "boolean" {
			f[path+".characteristic.type"] = "must be numeric, text or boolean"
		}
		if (c.Min != nil && !finiteNum(*c.Min)) || (c.Max != nil && !finiteNum(*c.Max)) || (c.Min != nil && c.Max != nil && *c.Min > *c.Max) {
			f[path+".characteristic"] = "limits must be finite with min not above max"
		}
		if in.Category == InstSetpoint {
			checkSetpoint(f, path, in, destination)
		}
		if in.Category == InstDataRequest && destination != DestKiosk && strings.TrimSpace(in.Node) == "" {
			f[path+".node"] = "is required to read the value from a process control system"
		}
	}
	if len(in.Node) > 300 {
		f[path+".node"] = "must be at most 300 characters"
	}
}

func checkSetpoint(f map[string]string, path string, in Instruction, destination string) {
	c := in.Char
	switch {
	case c.Type != "numeric":
		f[path+".characteristic.type"] = "a setpoint must be numeric"
	case in.Value == nil || !finiteNum(*in.Value):
		f[path+".value"] = "is required: the setpoint, a finite number"
	case (c.Min != nil && *in.Value < *c.Min) || (c.Max != nil && *in.Value > *c.Max):
		f[path+".value"] = "is outside the characteristic's limits"
	}
	if destination != DestKiosk && strings.TrimSpace(in.Node) == "" {
		f[path+".node"] = "is required: the node or topic the setpoint is written to"
	}
}

// Setpoint is one value a process control destination is asked to write.
type Setpoint struct {
	PhasePath string  `json:"phasePath"`
	Node      string  `json:"node"`
	Value     float64 `json:"value"`
	Unit      string  `json:"unit,omitempty"`
}

// Setpoints lists the setpoints of the recipe in order, with the path of the phase that holds each
// (procedure/unit procedure/operation/phase). A kiosk recipe has none that are written.
func (d RecipeDoc) Setpoints() []Setpoint {
	var out []Setpoint
	for _, up := range d.Procedure.UnitProcedures {
		for _, op := range up.Operations {
			for _, ph := range op.Phases {
				for _, in := range ph.Instructions {
					if in.Category == InstSetpoint && in.Value != nil && in.Char != nil {
						out = append(out, Setpoint{PhasePath: strings.Join([]string{d.Procedure.Name, up.Name, op.Name, ph.Name}, "/"),
							Node: in.Node, Value: *in.Value, Unit: in.Char.Unit})
					}
				}
			}
		}
	}
	return out
}

// RecipeTransition is the status a report moves a recipe to, or an error naming why it may not.
// received, in-execution and completed follow in that order; error can be reported from any of the
// active statuses; a recipe in error is sent again by resend, not by a report.
func RecipeTransition(from, to string) error {
	allowed := map[string][]string{
		RecipeSent:        {RecipeReceived, RecipeError},
		RecipeReceived:    {RecipeInExecution, RecipeError},
		RecipeInExecution: {RecipeCompleted, RecipeError},
	}
	for _, t := range allowed[from] {
		if t == to {
			return nil
		}
	}
	return fmt.Errorf("a control recipe %s cannot become %s", from, to)
}

// ResendCheck applies BR-25.11 to a resend. Only a recipe in error is resent, at most MaxResends times
// (resends counted after the first send); once a process message for the order has been received the PCS
// may have partially executed the recipe, so the resend needs force and a reason.
func ResendCheck(status string, sendCount int, messagesForOrder bool, force bool, reason string) (code string, err error) {
	switch {
	case status != RecipeError:
		return "transition", fmt.Errorf("only a control recipe in error can be sent again; this one is %s", status)
	case sendCount-1 >= MaxResends:
		return "limit", fmt.Errorf("this control recipe was already sent again %d times, the most allowed", MaxResends)
	case messagesForOrder && (!force || strings.TrimSpace(reason) == ""):
		return "force", fmt.Errorf("process messages were already received for this order, so the process control system may have started it; resend with force and a reason")
	}
	return "", nil
}

// PhasePaths lists the path of every phase of the recipe in order (procedure/unit procedure/operation/phase),
// the form a process message names a phase in.
func (d RecipeDoc) PhasePaths() []string {
	var out []string
	for _, up := range d.Procedure.UnitProcedures {
		for _, op := range up.Operations {
			for _, ph := range op.Phases {
				out = append(out, strings.Join([]string{d.Procedure.Name, up.Name, op.Name, ph.Name}, "/"))
			}
		}
	}
	return out
}

// Process message values a process control system sends to end a recipe it runs (FR-25.17, 25-5).
const (
	// StatusCharacteristic is the characteristic of an order-status message that carries the outcome.
	StatusCharacteristic = "status"
	// StatusMessageCharacteristic optionally carries the reason of an aborted or failed batch.
	StatusMessageCharacteristic = "message"
)

// RecipeEnd decides whether a process message ends a recipe that is in-execution on a process control
// system. Two messages do: an order-status message whose status is completed (the recipe completes) or
// aborted or error (the recipe goes to error, with the message characteristic as the reason), and a
// phase-end message for the last phase of the recipe (the recipe completes). Every other message leaves the
// recipe as it is. It returns the status the recipe moves to and the error text for the error status.
func RecipeEnd(category, phasePath string, values []Characteristic, doc RecipeDoc) (to, errText string, ends bool) {
	switch category {
	case "order-status":
		status, reason := "", ""
		for _, v := range values {
			switch v.Name {
			case StatusCharacteristic:
				status = strings.ToLower(strings.TrimSpace(v.Value))
			case StatusMessageCharacteristic:
				reason = strings.TrimSpace(v.Value)
			}
		}
		switch status {
		case "completed":
			return RecipeCompleted, "", true
		case "aborted", "error", "failed":
			if reason == "" {
				reason = "the process control system reported the batch as " + status
			}
			return RecipeError, reason, true
		}
	case "phase-end":
		paths := doc.PhasePaths()
		if len(paths) > 0 && phasePath == paths[len(paths)-1] {
			return RecipeCompleted, "", true
		}
	}
	return "", "", false
}
