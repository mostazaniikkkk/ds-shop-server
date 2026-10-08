package main

import (
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"io"
	"strings"
	"unicode/utf16"
)

// Banner languages, in NDS format order.
const (
	LangJapanese = iota
	LangEnglish
	LangFrench
	LangGerman
	LangItalian
	LangSpanish
	numLangs
)

// IconSize is the size of the icon sent to the DS: 4x4 tiles of 8x8 at 4bpp + 16-color palette.
const IconSize = 512 + 32

// Banner is the information extracted from a .nds ROM header.
type Banner struct {
	GameCode string
	Titles   [numLangs]string
	Icon     []byte // IconSize bytes, nil if the ROM has no banner
}

var errNotNDS = errors.New("no parece una ROM de Nintendo DS")

// ReadBanner reads a ROM's header and banner.
func ReadBanner(r io.ReaderAt, size int64) (*Banner, error) {
	hdr := make([]byte, 0x200)
	if _, err := r.ReadAt(hdr, 0); err != nil {
		return nil, errNotNDS
	}
	// Basic check: the Nintendo logo has a fixed CRC at 0x15C.
	if binary.LittleEndian.Uint16(hdr[0x15C:]) != 0xCF56 {
		return nil, errNotNDS
	}
	b := &Banner{GameCode: strings.TrimRight(string(hdr[0x0C:0x10]), "\x00")}
	off := int64(binary.LittleEndian.Uint32(hdr[0x68:]))
	if off == 0 || off+0x840 > size {
		return b, nil
	}
	ban := make([]byte, 0x840)
	if _, err := r.ReadAt(ban, off); err != nil {
		return b, nil
	}
	b.Icon = append([]byte(nil), ban[0x20:0x20+IconSize]...)
	for i := 0; i < numLangs; i++ {
		b.Titles[i] = decodeUTF16(ban[0x240+i*0x100 : 0x340+i*0x100])
	}
	return b, nil
}

func decodeUTF16(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		c := binary.LittleEndian.Uint16(b[i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return strings.TrimSpace(string(utf16.Decode(u)))
}

// IconImage converts the banner icon into an image (for the web panel).
func IconImage(icon []byte) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	if len(icon) < IconSize {
		return img
	}
	var pal [16]color.NRGBA
	for i := range pal {
		v := binary.LittleEndian.Uint16(icon[512+i*2:])
		r, g, b := uint8(v&31), uint8((v>>5)&31), uint8((v>>10)&31)
		pal[i] = color.NRGBA{r<<3 | r>>2, g<<3 | g>>2, b<<3 | b>>2, 255}
	}
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			t := (y/8)*4 + x/8
			v := icon[t*32+(y%8)*4+(x%8)/2]
			if x&1 == 1 {
				v >>= 4
			}
			v &= 15
			if v != 0 {
				img.SetNRGBA(x, y, pal[v])
			}
		}
	}
	return img
}
