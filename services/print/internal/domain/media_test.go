package domain

import "testing"

func TestMediaForDevice(t *testing.T) {
	cases := []struct {
		device, media string
		want          bool
	}{
		{DeviceZPL, "", true},
		{DeviceZPL, Media4x6In, true},
		{DeviceZPL, Media2x1In, true},
		{DeviceZPL, Media3x1In, true},
		{DeviceZPL, Media4x2In, true},
		{DeviceZPL, MediaA4, false},
		{DeviceZPL, "4X8_IN", false},
		{DevicePDFIPP, MediaA4, true},
		{DevicePDFIPP, MediaLetter, true},
		{DevicePDFIPP, Media4x6In, false},
		{DevicePCL6, MediaLetter, true},
		{DevicePCL6, Media2x1In, false},
		{"LASER", MediaA4, false},
		{"LASER", "", true},
	}
	for _, c := range cases {
		if got := MediaForDevice(c.device, c.media); got != c.want {
			t.Errorf("MediaForDevice(%s, %q) = %v, want %v", c.device, c.media, got, c.want)
		}
	}
}

func TestDefaultMedia(t *testing.T) {
	cases := map[string]string{
		DeviceZPL:    Media4x6In,
		DevicePDFIPP: MediaA4,
		DevicePCL6:   MediaA4,
	}
	for device, want := range cases {
		if got := DefaultMedia(device); got != want {
			t.Errorf("DefaultMedia(%s) = %s, want %s", device, got, want)
		}
		if !MediaForDevice(device, DefaultMedia(device)) {
			t.Errorf("default for %s is not valid for it", device)
		}
	}
}

func TestPrinterValidate_MediaSize(t *testing.T) {
	base := Printer{Code: "DOCK-1", Name: "Dock", DeviceType: DeviceZPL, Dpi: 203, Host: "10.0.0.5", Port: 9100, QueueCode: "DOCK", FailoverRank: 1}
	if f := base.Validate(); len(f) != 0 {
		t.Fatalf("unset media should validate, got %v", f)
	}
	bad := base
	bad.MediaSize = MediaA4
	if _, ok := bad.Validate()["mediaSize"]; !ok {
		t.Fatal("A4 on a ZPL label printer should fail mediaSize")
	}
	sheet := Printer{Code: "OFFICE-1", Name: "Office", DeviceType: DevicePDFIPP, Dpi: 300, MediaSize: MediaLetter, Host: "10.0.0.6", Port: 631, QueueCode: "OFFICE", FailoverRank: 1}
	if f := sheet.Validate(); len(f) != 0 {
		t.Fatalf("letter sheet printer should validate, got %v", f)
	}
}

func TestDeviceTypeFor_SendsPDFOnlyToIPPDevices(t *testing.T) {
	queue := []Printer{{Code: "Z", DeviceType: DeviceZPL}, {Code: "P", DeviceType: DevicePDFIPP}, {Code: "C", DeviceType: DevicePCL6}}
	cases := []struct {
		format string
		want   []string
	}{
		{FormatZPL, []string{"Z"}},
		{"", []string{"Z"}},
		{FormatPDF, []string{"P"}},
	}
	for _, c := range cases {
		got := Accepting(queue, DeviceTypeFor(c.format))
		codes := make([]string, 0, len(got))
		for _, p := range got {
			codes = append(codes, p.Code)
		}
		if len(codes) != len(c.want) || (len(codes) > 0 && codes[0] != c.want[0]) {
			t.Errorf("format %q: devices %v, want %v", c.format, codes, c.want)
		}
	}
}
