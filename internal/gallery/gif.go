package gallery

import (
	"encoding/binary"
	"errors"
	"image/gif"
	"io"
)

var errInvalidGIF = errors.New("invalid GIF or animation limits exceeded")

// validateGIF checks every frame's allocation bounds before DecodeAll can
// allocate frame buffers. The structural pass uses fixed-size buffers only.
func validateGIF(r io.ReadSeeker, maxPixels int64) error {
	if maxPixels <= 0 {
		return errInvalidGIF
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return errInvalidGIF
	}
	var header [13]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return errInvalidGIF
	}
	if string(header[:6]) != "GIF87a" && string(header[:6]) != "GIF89a" {
		return errInvalidGIF
	}
	width := int64(binary.LittleEndian.Uint16(header[6:8]))
	height := int64(binary.LittleEndian.Uint16(header[8:10]))
	if width == 0 || height == 0 || width > maxPixels/height {
		return errInvalidGIF
	}
	if header[10]&0x80 != 0 {
		if err := gifSkipBytes(r, int64(3*(1<<((header[10]&7)+1)))); err != nil {
			return err
		}
	}
	var frameCount int
	var totalPixels int64
	for {
		var marker [1]byte
		if _, err := io.ReadFull(r, marker[:]); err != nil {
			return errInvalidGIF
		}
		switch marker[0] {
		case 0x21: // Extension introducer.
			if err := gifSkipExtension(r); err != nil {
				return err
			}
		case 0x2c: // Image descriptor.
			frameCount++
			if frameCount > 1000 {
				return errInvalidGIF
			}
			var descriptor [9]byte
			if _, err := io.ReadFull(r, descriptor[:]); err != nil {
				return errInvalidGIF
			}
			left := int64(binary.LittleEndian.Uint16(descriptor[0:2]))
			top := int64(binary.LittleEndian.Uint16(descriptor[2:4]))
			frameWidth := int64(binary.LittleEndian.Uint16(descriptor[4:6]))
			frameHeight := int64(binary.LittleEndian.Uint16(descriptor[6:8]))
			if frameWidth == 0 || frameHeight == 0 || left > width || top > height || frameWidth > width-left || frameHeight > height-top || descriptor[8]&0x18 != 0 {
				return errInvalidGIF
			}
			framePixels := frameWidth * frameHeight // GIF dimensions are uint16.
			if framePixels > maxPixels-totalPixels {
				return errInvalidGIF
			}
			totalPixels += framePixels
			if descriptor[8]&0x80 != 0 {
				if err := gifSkipBytes(r, int64(3*(1<<((descriptor[8]&7)+1)))); err != nil {
					return err
				}
			}
			var codeSize [1]byte
			if _, err := io.ReadFull(r, codeSize[:]); err != nil || codeSize[0] < 2 || codeSize[0] > 8 {
				return errInvalidGIF
			}
			if err := gifSkipSubblocks(r); err != nil {
				return err
			}
		case 0x3b: // Trailer; require a complete file, with no trailing payload.
			if frameCount == 0 {
				return errInvalidGIF
			}
			var extra [1]byte
			if _, err := io.ReadFull(r, extra[:]); err != io.EOF {
				return errInvalidGIF
			}
			if _, err := r.Seek(0, io.SeekStart); err != nil {
				return errInvalidGIF
			}
			if _, err := gif.DecodeAll(r); err != nil {
				return errInvalidGIF
			}
			return nil
		default:
			return errInvalidGIF
		}
	}
}

func gifSkipExtension(r io.Reader) error {
	var label [1]byte
	if _, err := io.ReadFull(r, label[:]); err != nil {
		return errInvalidGIF
	}
	switch label[0] {
	case 0xf9: // Graphic control has one fixed block and a terminator.
		var control [6]byte
		if _, err := io.ReadFull(r, control[:]); err != nil || control[0] != 4 || control[5] != 0 {
			return errInvalidGIF
		}
		return nil
	case 0xff, 0x01: // Application and plain-text fixed headers.
		var size [1]byte
		if _, err := io.ReadFull(r, size[:]); err != nil {
			return errInvalidGIF
		}
		expected := byte(11)
		if label[0] == 0x01 {
			expected = 12
		}
		if size[0] != expected {
			return errInvalidGIF
		}
		if err := gifSkipBytes(r, int64(size[0])); err != nil {
			return err
		}
	case 0xfe: // Comments consist only of subblocks.
	default:
		return errInvalidGIF
	}
	return gifSkipSubblocks(r)
}

func gifSkipSubblocks(r io.Reader) error {
	var size [1]byte
	for {
		if _, err := io.ReadFull(r, size[:]); err != nil {
			return errInvalidGIF
		}
		if size[0] == 0 {
			return nil
		}
		if err := gifSkipBytes(r, int64(size[0])); err != nil {
			return err
		}
	}
}

func gifSkipBytes(r io.Reader, count int64) error {
	if _, err := io.CopyN(io.Discard, r, count); err != nil {
		return errInvalidGIF
	}
	return nil
}
