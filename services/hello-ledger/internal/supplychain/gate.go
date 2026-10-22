// Package supplychain is the wave-1 admission rule for container images.
// CI calls the same rule after Syft and cosign have reported their results.
package supplychain

import "errors"

var (
	ErrMissingSBOM = errors.New("sbom missing")
	ErrUnsigned    = errors.New("unsigned image blocked")
)

// Image is the evidence the gate needs. HasSBOM is true when an SBOM was
// produced for the image. Signed is true when signature verification succeeded.
type Image struct {
	HasSBOM bool
	Signed  bool
}

// Evaluate blocks images that have no SBOM or no verified signature.
func Evaluate(img Image) error {
	if !img.HasSBOM {
		return ErrMissingSBOM
	}
	if !img.Signed {
		return ErrUnsigned
	}
	return nil
}
