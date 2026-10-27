package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Label geometry (FR-32.7, FR-32.13, §6.32.13 label_default_dpi). Templates are
// authored once, in dots at LayoutDpi. At dispatch each ZPL label is rendered for
// the printer that receives it: coordinates, font and bar code sizes are scaled
// to the device's dpi, ^PW and ^LL are set from its media, and a label whose
// fields do not fit the media is refused so the queue fails over.

// LayoutDpi is the resolution the built-in templates are laid out at.
const LayoutDpi = 203

// defaultModuleWidth is the ZPL ^BY bar module width (dots) the templates rely on.
const defaultModuleWidth = 2

// quietZoneModules is the clear space Code 128 needs on each side of the bars
// (ISO/IEC 15417 §4.4.3, GS1 General Specifications §5.4.4: 10 modules).
const quietZoneModules = 10

// interpretationLine is the height (dots at LayoutDpi) allowed for the human-readable
// line that ^BC prints under a bar code when its interpretation flag is Y.
const interpretationLine = 30

type labelInches struct{ w, h int }

var labelMedia = map[string]labelInches{
	Media2x1In: {2, 1},
	Media3x1In: {3, 1},
	Media4x2In: {4, 2},
	Media4x6In: {4, 6},
}

// LabelDots returns the label width and length in dots for a ZPL media size at
// a supported dpi. ok is false for sheet media or an unsupported dpi.
func LabelDots(media string, dpi int) (w, h int, ok bool) {
	if dpi != 203 && dpi != 300 && dpi != 600 {
		return 0, 0, false
	}
	s, found := labelMedia[media]
	if !found {
		return 0, 0, false
	}
	return s.w * dpi, s.h * dpi, true
}

var (
	// The template's own label geometry, replaced by the device's.
	geometryCmd = regexp.MustCompile(`\^(?:PW|LL)\d+\n?`)
	// ^FOx,y field origin.
	fieldOriginCmd = regexp.MustCompile(`\^FO(\d+),(\d+)`)
	// ^A0o,h,w scalable font.
	fontCmd = regexp.MustCompile(`\^A0([NRIB]),(\d+),(\d+)`)
	// ^BCo,h,f Code 128 with its bar height and interpretation-line flag.
	code128Cmd = regexp.MustCompile(`\^BC([NRIB]),(\d+),([YN])`)
	// ^BXo,m Data Matrix module size.
	dataMatrixCmd = regexp.MustCompile(`\^BX([NRIB]),(\d+)`)
	// ^FD field data, up to the end of the field's commands.
	fieldDataCmd = regexp.MustCompile(`\^FD(.*)$`)
	// One field: its origin, then its commands up to ^FS. Field data cannot hold
	// a caret (Render refuses it), so a field never matches across another.
	fieldCmd = regexp.MustCompile(`\^FO(\d+),(\d+)([^\n]*?)\^FS`)
)

// SizeLabel renders a ZPL label for one device: it scales the layout from
// LayoutDpi to dpi, replaces any template ^PW (width) and ^LL (length) with the
// media's, and checks that every field lies inside the label. A label authored
// for larger media is refused with PRN-0007 instead of printing clipped. The fit
// check covers each field's origin and its height (font, Code 128 bars and the
// interpretation line), and the width of each Code 128 bar code with its quiet
// zone on both sides. Text width and Data Matrix size are not estimated, so for
// those fields only the origin is checked across the label.
func SizeLabel(zpl, media string, dpi int) (string, error) {
	w, h, ok := LabelDots(media, dpi)
	if !ok {
		return "", &Error{CodeMediaMismatch, fmt.Sprintf("media %s at %d dpi is not a label size", media, dpi)}
	}
	const start = "^XA\n"
	if !strings.HasPrefix(zpl, start) {
		return "", &Error{CodeInvalid, "label is not a ZPL document"}
	}
	body := scaleLayout(geometryCmd.ReplaceAllString(zpl[len(start):], ""), dpi)
	for _, m := range fieldCmd.FindAllStringSubmatch(body, -1) {
		x, _ := strconv.Atoi(m[1])
		y, _ := strconv.Atoi(m[2])
		if x >= w || y+fieldHeight(m[3], dpi) > h {
			return "", &Error{CodeMediaMismatch, fmt.Sprintf("field at %d,%d does not fit the %s label at %d dpi", x, y, media, dpi)}
		}
		if bars, ok := code128Width(m[3], dpi); ok {
			quiet := scaleDots(quietZoneModules*defaultModuleWidth, dpi)
			if x < quiet || x+bars+quiet > w {
				return "", &Error{CodeMediaMismatch, fmt.Sprintf("bar code at %d,%d is %d dots wide and does not fit the %s label at %d dpi", x, y, bars, media, dpi)}
			}
		}
	}
	head := fmt.Sprintf("^PW%d\n^LL%d\n", w, h)
	if dpi != LayoutDpi {
		head += fmt.Sprintf("^BY%d\n", scaleDots(defaultModuleWidth, dpi))
	}
	return start + head + body, nil
}

// fieldHeight is the vertical extent, in device dots, of one field's commands.
func fieldHeight(cmds string, dpi int) int {
	if m := fontCmd.FindStringSubmatch(cmds); m != nil {
		n, _ := strconv.Atoi(m[2])
		return n
	}
	if m := code128Cmd.FindStringSubmatch(cmds); m != nil {
		n, _ := strconv.Atoi(m[2])
		if m[3] == "Y" {
			n += scaleDots(interpretationLine, dpi)
		}
		return n
	}
	return 0
}

// code128Width is the width in device dots of a field's Code 128 bars, without
// quiet zones. ok is false when the field is not a Code 128 bar code.
func code128Width(cmds string, dpi int) (int, bool) {
	if !code128Cmd.MatchString(cmds) {
		return 0, false
	}
	data := ""
	if m := fieldDataCmd.FindStringSubmatch(cmds); m != nil {
		data = m[1]
	}
	return code128Modules(data) * scaleDots(defaultModuleWidth, dpi), true
}

// code128Modules counts the modules of a Code 128 symbol from ZPL ^BC field data
// (mode N): every symbol character is 11 modules (the start code, each data
// character, the check character) and the stop pattern is 13. The data starts in
// subset B unless it opens with a start code (>9 A, >: B, >; C). In subset C two
// digits make one character. Every other invocation code (>5, >6, >7 code set
// switches, >8 FNC1) is one character.
func code128Modules(data string) int {
	chars := 2 // start and check characters
	subsetC := false
	switch {
	case strings.HasPrefix(data, ">;"):
		subsetC, data = true, data[2:]
	case strings.HasPrefix(data, ">:"), strings.HasPrefix(data, ">9"):
		data = data[2:]
	}
	for i := 0; i < len(data); chars++ {
		if data[i] == '>' && i+1 < len(data) {
			switch data[i+1] {
			case '5':
				subsetC = true
			case '6', '7':
				subsetC = false
			}
			i += 2
			continue
		}
		if subsetC && i+1 < len(data) && isDigit(data[i]) && isDigit(data[i+1]) {
			i += 2
			continue
		}
		i++
	}
	return chars*11 + 13
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// scaleLayout converts the dot values of a LayoutDpi layout to dpi.
func scaleLayout(body string, dpi int) string {
	if dpi == LayoutDpi {
		return body
	}
	body = fieldOriginCmd.ReplaceAllStringFunc(body, func(s string) string {
		m := fieldOriginCmd.FindStringSubmatch(s)
		return fmt.Sprintf("^FO%d,%d", scaleText(m[1], dpi), scaleText(m[2], dpi))
	})
	body = fontCmd.ReplaceAllStringFunc(body, func(s string) string {
		m := fontCmd.FindStringSubmatch(s)
		return fmt.Sprintf("^A0%s,%d,%d", m[1], scaleText(m[2], dpi), scaleText(m[3], dpi))
	})
	body = code128Cmd.ReplaceAllStringFunc(body, func(s string) string {
		m := code128Cmd.FindStringSubmatch(s)
		return fmt.Sprintf("^BC%s,%d,%s", m[1], scaleText(m[2], dpi), m[3])
	})
	return dataMatrixCmd.ReplaceAllStringFunc(body, func(s string) string {
		m := dataMatrixCmd.FindStringSubmatch(s)
		return fmt.Sprintf("^BX%s,%d", m[1], scaleText(m[2], dpi))
	})
}

func scaleText(s string, dpi int) int {
	n, _ := strconv.Atoi(s)
	return scaleDots(n, dpi)
}

// scaleDots converts a LayoutDpi dot count to dpi, rounded to the nearest dot
// (integer arithmetic, so the result is exact and repeatable).
func scaleDots(n, dpi int) int {
	return (n*dpi + LayoutDpi/2) / LayoutDpi
}

// SizeForDevice sizes a ZPL payload for one printer. PDF payloads and devices
// that are not ZPL pass through unchanged.
func SizeForDevice(format string, d Printer, payload string) (string, error) {
	if format != FormatZPL || d.DeviceType != DeviceZPL {
		return payload, nil
	}
	return SizeLabel(payload, d.MediaSize, d.Dpi)
}
