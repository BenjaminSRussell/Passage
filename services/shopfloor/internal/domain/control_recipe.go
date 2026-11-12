package domain

import (
	"fmt"
	"math"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
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
	// Materials is the structured form of a material-list instruction, scaled to the batch: the PI sheet
	// shows each product by name with its quantity. Text stays required as the plain-language summary.
	Materials []InstructionMaterial `json:"materials,omitempty"`
	// Meaning is what a signature-request step's signature means (21 CFR 11.50(a)(3), FR-24.22): performed,
	// verified, reviewed or approved. The kiosk shows it to the signer and records it with the signature; a step
	// without one is signed as performed.
	Meaning string `json:"meaning,omitempty"`
}

// Signature meanings of a signature-request step.
var SignatureMeanings = []string{"performed", "verified", "reviewed", "approved"}

// ValidMeaning reports whether m is a signature meaning ("" is the default, performed).
func ValidMeaning(m string) bool {
	if m == "" {
		return true
	}
	for _, x := range SignatureMeanings {
		if x == m {
			return true
		}
	}
	return false
}

// InstructionMaterial is one line of a material-list instruction.
type InstructionMaterial struct {
	ItemID string `json:"itemId"`
	Qty    string `json:"qty"`
	Unit   string `json:"unit,omitempty"`
	Role   string `json:"role,omitempty"` // ingredient, co-product or by-product
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
	switch {
	case in.Meaning != "" && in.Category != InstSignature:
		f[path+".meaning"] = "applies to a signature-request step only"
	case !ValidMeaning(in.Meaning):
		f[path+".meaning"] = "must be performed, verified, reviewed or approved"
	}
	switch in.Category {
	case InstText, InstSignature, InstMaterials, InstSequence, InstCalculation:
		if strings.TrimSpace(in.Text) == "" {
			f[path+".text"] = "is required for this category"
		}
		if in.Category == InstMaterials {
			checkMaterials(f, path, in.Materials)
		} else if len(in.Materials) > 0 {
			f[path+".materials"] = "applies to a material-list instruction only"
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

const maxMaterials = 200

func checkMaterials(f map[string]string, path string, ms []InstructionMaterial) {
	if len(ms) > maxMaterials {
		f[path+".materials"] = fmt.Sprintf("holds %d lines; at most %d", len(ms), maxMaterials)
		return
	}
	for i, m := range ms {
		p := fmt.Sprintf("%s.materials[%d]", path, i)
		if !ids.Valid(m.ItemID) {
			f[p+".itemId"] = "must be a product UUID"
		}
		if q, err := decimal.NewFromString(strings.TrimSpace(m.Qty)); err != nil || q.IsNegative() {
			f[p+".qty"] = "must be a decimal quantity, zero or more"
		}
		if len(m.Unit) > 20 {
			f[p+".unit"] = "must be at most 20 characters"
		}
		if m.Role != "" && m.Role != "ingredient" && m.Role != "co-product" && m.Role != "by-product" {
			f[p+".role"] = "must be ingredient, co-product or by-product"
		}
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

// Node types of a process control system a readback tolerance is chosen by (OPC-UA built-in data types).
var integerNodeTypes = map[string]bool{"SByte": true, "Byte": true, "Int16": true, "UInt16": true, "Int32": true, "UInt32": true, "Int64": true, "UInt64": true}

// DefaultReadbackTolerance is the absolute tolerance of a Double node or of a node whose type the connector does not
// report (an MQTT topic).
const DefaultReadbackTolerance = 0.001

// KnownNodeType reports whether t is a node type the readback rule distinguishes ("" is unknown and allowed).
func KnownNodeType(t string) bool {
	return t == "" || t == "Double" || t == "Float" || t == "Boolean" || integerNodeTypes[t]
}

// ReadbackTolerance is how far the value read back may be from the value written for a node of nodeType before the
// write is a deviation (BR-25.15): a Double within DefaultReadbackTolerance, a Float also within the rounding of a
// 32-bit float (2^-23 of the value), an integer or Boolean exactly. The connector applies the same rule.
func ReadbackTolerance(nodeType string, written decimal.Decimal) decimal.Decimal {
	def := decimal.NewFromFloat(DefaultReadbackTolerance)
	switch {
	case nodeType == "Float":
		rel := written.Abs().Div(decimal.NewFromInt(1 << 23))
		if rel.GreaterThan(def) {
			return rel
		}
		return def
	case nodeType == "Boolean", integerNodeTypes[nodeType]:
		return decimal.Zero
	}
	return def
}

// CodeSignatureStep is a signature in a process message that does not match the step it signs: no such signature
// step in the recipe, or a meaning other than the step's (21 CFR 11.50). 422.
const CodeSignatureStep = "SF-0186"

// SignatureStep finds the instruction a kiosk step key names: "unit procedure/operation/phase/seq", as the kiosk builds
// it. ok is false when the recipe has no such instruction.
func (d RecipeDoc) SignatureStep(key string) (Instruction, bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 4 {
		return Instruction{}, false
	}
	for _, up := range d.Procedure.UnitProcedures {
		if up.Name != parts[0] {
			continue
		}
		for _, op := range up.Operations {
			if op.Name != parts[1] {
				continue
			}
			for _, ph := range op.Phases {
				if ph.Name != parts[2] {
					continue
				}
				for _, in := range ph.Instructions {
					if fmt.Sprint(in.Seq) == parts[3] {
						return in, true
					}
				}
			}
		}
	}
	return Instruction{}, false
}

// MeaningOf is the meaning a signature step's signature carries: the step's own, or performed.
func MeaningOf(in Instruction) string {
	if in.Meaning == "" {
		return "performed"
	}
	return in.Meaning
}
