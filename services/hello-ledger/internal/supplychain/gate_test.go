package supplychain

import (
	"errors"
	"testing"
)

func TestUnsignedImageIsBlocked(t *testing.T) {
	err := Evaluate(Image{HasSBOM: true, Signed: false})
	if !errors.Is(err, ErrUnsigned) {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingSBOMIsBlocked(t *testing.T) {
	err := Evaluate(Image{HasSBOM: false, Signed: true})
	if !errors.Is(err, ErrMissingSBOM) {
		t.Fatalf("err = %v", err)
	}
}

func TestSignedImageWithSBOMPasses(t *testing.T) {
	if err := Evaluate(Image{HasSBOM: true, Signed: true}); err != nil {
		t.Fatal(err)
	}
}
