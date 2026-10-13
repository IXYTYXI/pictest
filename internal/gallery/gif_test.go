package gallery

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"
	"testing"
)

func animatedGIF(t *testing.T, frameCount int) []byte {
	t.Helper()
	animation := &gif.GIF{}
	for range frameCount {
		frame := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
		frame.SetColorIndex(0, 0, 1)
		animation.Image = append(animation.Image, frame)
		animation.Delay = append(animation.Delay, 1)
	}
	var output bytes.Buffer
	if err := gif.EncodeAll(&output, animation); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func lastGIFFrame(t *testing.T, data []byte) int {
	t.Helper()
	prefix := []byte{0x2c, 0, 0, 0, 0, 2, 0, 2, 0}
	index := bytes.LastIndex(data, prefix)
	if index < 0 {
		t.Fatal("frame descriptor missing from fixture")
	}
	return index
}

func TestValidateGIFAcceptsAllFramesAtPixelLimit(t *testing.T) {
	if err := validateGIF(bytes.NewReader(animatedGIF(t, 2)), 8); err != nil {
		t.Fatal(err)
	}
}

func TestValidateGIFRejectsMalformedLaterFrame(t *testing.T) {
	data := animatedGIF(t, 2)
	frame := lastGIFFrame(t, data)
	position := frame + 10
	packed := data[frame+9]
	if packed&0x80 != 0 {
		position += 3 * (1 << ((packed & 7) + 1))
	}
	position += 2         // LZW minimum code size and the first subblock length.
	data[position] = 0xff // Undefined first LZW code in only the second frame.
	if _, err := gif.Decode(bytes.NewReader(data)); err != nil {
		t.Fatalf("first frame should remain valid: %v", err)
	}
	if err := validateGIF(bytes.NewReader(data), 8); err == nil {
		t.Fatal("malformed later frame accepted")
	}
}

func TestValidateGIFRejectsLaterFrameOutsideCanvas(t *testing.T) {
	data := animatedGIF(t, 2)
	frame := lastGIFFrame(t, data)
	binary.LittleEndian.PutUint16(data[frame+5:frame+7], 65535)
	binary.LittleEndian.PutUint16(data[frame+7:frame+9], 65535)
	if _, err := gif.Decode(bytes.NewReader(data)); err != nil {
		t.Fatalf("first frame should remain valid: %v", err)
	}
	if err := validateGIF(bytes.NewReader(data), 8); err == nil {
		t.Fatal("later frame outside canvas accepted")
	}
}

func TestValidateGIFRejectsCumulativeFramePixels(t *testing.T) {
	if err := validateGIF(bytes.NewReader(animatedGIF(t, 2)), 7); err == nil {
		t.Fatal("cumulative frame limit ignored")
	}
}

func TestValidateGIFRejectsFrameCountLimit(t *testing.T) {
	if err := validateGIF(bytes.NewReader(animatedGIF(t, 1001)), 100000); err == nil {
		t.Fatal("frame count limit ignored")
	}
}

func TestValidateGIFRejectsTruncatedStructure(t *testing.T) {
	data := animatedGIF(t, 2)
	for _, length := range []int{0, 5, 12, len(data) - 1, len(data) - 5} {
		if err := validateGIF(bytes.NewReader(data[:length]), 8); err == nil {
			t.Fatalf("truncated GIF with %d bytes accepted", length)
		}
	}
}

func TestValidateGIFRejectsInvalidCanvasAndLimit(t *testing.T) {
	data := animatedGIF(t, 2)
	if err := validateGIF(bytes.NewReader(data), 0); err == nil {
		t.Fatal("nonpositive pixel limit accepted")
	}
	if err := validateGIF(bytes.NewReader(data), 3); err == nil {
		t.Fatal("canvas over pixel limit accepted")
	}
	binary.LittleEndian.PutUint16(data[6:8], 0)
	if err := validateGIF(bytes.NewReader(data), 8); err == nil {
		t.Fatal("zero canvas width accepted")
	}
}
