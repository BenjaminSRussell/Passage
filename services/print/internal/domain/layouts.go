package domain

// Compact layouts for 1 in long label media, 2 x 1 and 3 x 1 in (FR-32.7,
// FR-32.13). The standard layouts in template.go are authored for 4 x 6 in and
// also fit 4 x 2 in; their bar codes start below 1 in, so a 1 in label needs its
// own layout. These use the compact bar code helpers (50 dot bars without the
// human-readable line, which is printed as its own text field) and start every
// bar code 20 dots in, the Code 128 quiet zone at 203 dpi. Like every layout they
// are authored at LayoutDpi and sized per device by SizeLabel, which still refuses
// a label whose bar code is too wide for the device (an item label with lot,
// expiry and serial has a bar code about 3.5 in wide), so the queue fails over.
//
// The GS1 carton label has no compact layout: it is a 4 x 6 in label (§6.32.15),
// and its SSCC, PO and postal code bar code is wider than 3 in.
var compactLayouts = map[string]string{
	TemplateLP: "^XA\n^CI28\n^FO20,6^A0N,26,26^FDLP {{lp}}^FS\n^FO20,36^A0N,20,20^FD{{item}}^FS\n" +
		"^FO20,60^A0N,20,20^FDLOT {{lot}} EXP {{expiry}}^FS\n^FO20,88{{ssccsm sscc}}\n" +
		"^FO20,146^A0N,20,20^FD(00) {{sscc}}^FS\n^XZ\n",
	TemplateLocation: "^XA\n^CI28\n^FO20,8^A0N,50,50^FD{{location}}^FS\n^FO20,70{{bc128sm location}}\n^XZ\n",
	TemplateItem:     "^XA\n^CI28\n^FO20,8^A0N,28,28^FDGTIN {{gtin}}^FS\n^FO20,48{{gs1sm gs1}}\n^XZ\n",
}

// CompactLayout returns the layout of a built-in template for 1 in long media.
// ok is false for other media and for templates without a compact layout, which
// print from their standard layout.
func CompactLayout(code, media string) (string, bool) {
	if media != Media2x1In && media != Media3x1In {
		return "", false
	}
	tpl, ok := compactLayouts[code]
	return tpl, ok
}

// LabelForDevice returns the ZPL a device receives for a new label job: the
// compact layout rendered from the job's template data on 1 in media, otherwise
// the standard rendering. data is nil for a reprint, which reuses the stored
// payload. The result is sized for the device by SizeForDevice.
func LabelForDevice(d Printer, template, standard string, data map[string]string) (string, error) {
	if data == nil || d.DeviceType != DeviceZPL {
		return standard, nil
	}
	tpl, ok := CompactLayout(template, d.MediaSize)
	if !ok {
		return standard, nil
	}
	return Render(tpl, data)
}
