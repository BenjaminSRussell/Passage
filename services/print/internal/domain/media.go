package domain

// Media sizes (FR-32.13 "media sizes"). Label media is used by ZPL II devices;
// sheet media by PDF over IPP and PCL 6 devices. A device accepts only the
// kind of media its type prints on.
const (
	Media2x1In  = "2X1_IN"
	Media3x1In  = "3X1_IN"
	Media4x2In  = "4X2_IN"
	Media4x6In  = "4X6_IN"
	MediaA4     = "A4"
	MediaLetter = "LETTER"
)

// DefaultMedia returns the media size a device type uses when none is given:
// the 4 x 6 in shipping label for ZPL II, A4 sheets otherwise.
func DefaultMedia(deviceType string) string {
	if deviceType == DeviceZPL {
		return Media4x6In
	}
	return MediaA4
}

// MediaForDevice reports whether media is a valid size for the device type.
// An empty media string is valid: the registry applies DefaultMedia.
func MediaForDevice(deviceType, media string) bool {
	if media == "" {
		return true
	}
	switch deviceType {
	case DeviceZPL:
		return media == Media2x1In || media == Media3x1In || media == Media4x2In || media == Media4x6In
	case DevicePDFIPP, DevicePCL6:
		return media == MediaA4 || media == MediaLetter
	default:
		return false
	}
}
