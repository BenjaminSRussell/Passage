package flows

import "github.com/BenjaminSRussell/passage/services/mobile/internal/gs1"

func to(id string) *string { return &id }

func scan(id, prompt string, next string, classes ...gs1.ScanClass) Step {
	return Step{ID: id, Type: StepScan, Prompt: prompt, ExpectedScan: classes, Next: map[string]*string{"default": to(next)}}
}

func step(id, typ, prompt string, next *string) Step {
	return Step{ID: id, Type: typ, Prompt: prompt, Next: map[string]*string{"default": next}}
}

func call(id, prompt, op string, params map[string]string) Step {
	return Step{ID: id, Type: StepCall, Prompt: prompt, Next: map[string]*string{"default": nil}, Call: &Call{Op: op, Params: params}}
}

// CoreFlows are the seven flows of FR-32.4. Pick, put-away and receive end in
// a call to the wave-31 work API; the others have no warehouse operation to
// call yet (see NOT-IMPLEMENTED.md). Work and line values are seeded from the
// session's assigned work.
func CoreFlows() []Draft {
	return []Draft{
		{Code: "RECEIVE", EntryStepID: "scan_receipt", Steps: []Step{
			scan("scan_receipt", "Scan the receipt", "enter_line", gs1.ClassAny),
			step("enter_line", StepInput, "Receipt line number", to("enter_qty")),
			step("enter_qty", StepInput, "Quantity received", to("confirm_receive")),
			step("confirm_receive", StepConfirm, "Confirm receipt", to("call_receive")),
			call("call_receive", "Posting the receipt", OpReceive, map[string]string{"receiptId": "scan_receipt", "lineNo": "enter_line", "qty": "enter_qty"}),
		}},
		{Code: "RECEIVE_PUTAWAY", EntryStepID: "scan_lp", Steps: []Step{
			scan("scan_lp", "Scan the license plate", "scan_location", gs1.ClassLP),
			scan("scan_location", "Scan the put-away location", "confirm_putaway", gs1.ClassLocation),
			step("confirm_putaway", StepConfirm, "Confirm put-away", to("call_confirm")),
			call("call_confirm", "Confirming put-away", OpConfirm, map[string]string{"workId": "work_id", "lineNo": "line_no", "qty": "line_qty"}),
		}},
		{Code: "PICK", EntryStepID: "scan_location", Steps: []Step{
			scan("scan_location", "Scan the pick location", "scan_item", gs1.ClassLocation),
			scan("scan_item", "Scan the item", "enter_qty", gs1.ClassGTIN),
			step("enter_qty", StepInput, "Quantity picked", to("qty_check")),
			{ID: "qty_check", Type: StepBranch, Prompt: "Was the full quantity picked?", Next: map[string]*string{"full": to("confirm_pick"), "short": to("short_reason")}},
			step("short_reason", StepSelect, "Short pick reason", to("call_short")),
			step("confirm_pick", StepConfirm, "Confirm pick", to("call_confirm")),
			call("call_confirm", "Confirming the pick", OpConfirm, map[string]string{"workId": "work_id", "lineNo": "line_no", "qty": "enter_qty"}),
			call("call_short", "Reporting the short pick", OpException, map[string]string{"workId": "work_id", "action": "=SHORT_PICK", "lineNo": "line_no", "qty": "enter_qty", "reason": "short_reason"}),
		}},
		{Code: "PICK_CLUSTER", EntryStepID: "scan_cart_lp", Steps: []Step{
			scan("scan_cart_lp", "Scan the cart position LP", "scan_item", gs1.ClassLP),
			scan("scan_item", "Scan the item", "enter_qty", gs1.ClassGTIN),
			step("enter_qty", StepInput, "Quantity for this position", to("confirm_position")),
			step("confirm_position", StepConfirm, "Confirm the position", to("more_positions")),
			{ID: "more_positions", Type: StepBranch, Prompt: "Another cart position?", Next: map[string]*string{"more": to("scan_cart_lp"), "done": nil}},
		}},
		{Code: "PACK", EntryStepID: "scan_container", Steps: []Step{
			scan("scan_container", "Scan the shipping container", "scan_item", gs1.ClassLP),
			scan("scan_item", "Scan the item to pack", "enter_qty", gs1.ClassGTIN),
			step("enter_qty", StepInput, "Quantity packed", to("confirm_pack")),
			step("confirm_pack", StepConfirm, "Confirm the pack", nil),
		}},
		{Code: "MOVE", EntryStepID: "scan_source_lp", Steps: []Step{
			scan("scan_source_lp", "Scan the license plate to move", "enter_qty", gs1.ClassLP),
			step("enter_qty", StepInput, "Quantity to move", to("scan_destination")),
			scan("scan_destination", "Scan the destination location", "confirm_move", gs1.ClassLocation),
			step("confirm_move", StepConfirm, "Confirm the move", nil),
		}},
		{Code: "COUNT", EntryStepID: "scan_location", Steps: []Step{
			scan("scan_location", "Scan the location to count", "scan_item", gs1.ClassLocation),
			scan("scan_item", "Scan the item counted", "enter_count", gs1.ClassGTIN),
			step("enter_count", StepInput, "Counted quantity", to("confirm_count")),
			step("confirm_count", StepConfirm, "Confirm the count", nil),
		}},
		{Code: "INQUIRY", EntryStepID: "scan_query", Steps: []Step{
			scan("scan_query", "Scan an item, location or LP", "show_result", gs1.ClassAny),
			step("show_result", StepDisplay, "Stock and status", nil),
		}},
	}
}
