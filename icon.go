package main

import (
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"sort"
)

// MakeIcon converts any image into a DS banner icon:
// 32x32, 4x4 tiles of 8x8 at 4bpp and a 16-color BGR555 palette in which
// color 0 is transparent.
func MakeIcon(r io.Reader) ([]byte, error) {
	src, _, err := image.Decode(r)
	if err != nil {
		return nil, errors.New("no se reconoce la imagen (usa PNG, GIF o JPG)")
	}
	px := scaleTo32(src)

	// opaque colors in 5-bit RGB
	var opaque []rgb5
	for _, p := range px {
		if p.a {
			opaque = append(opaque, p.c)
		}
	}
	pal := medianCut(opaque, 15)

	icon := make([]byte, IconSize)
	for i, c := range pal {
		v := uint16(c.r) | uint16(c.g)<<5 | uint16(c.b)<<10
		binary.LittleEndian.PutUint16(icon[512+(i+1)*2:], v)
	}
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			p := px[y*32+x]
			idx := 0
			if p.a && len(pal) > 0 {
				idx = nearest(pal, p.c) + 1
			}
			o := ((y/8)*4+x/8)*32 + (y%8)*4 + (x%8)/2
			if x&1 == 1 {
				icon[o] |= byte(idx << 4)
			} else {
				icon[o] |= byte(idx)
			}
		}
	}
	return icon, nil
}

type rgb5 struct{ r, g, b uint8 }

type pixel struct {
	c rgb5
	a bool
}

// scaleTo32 fits the image into 32x32 (keeping the aspect ratio) by averaging
// the source pixels of each cell.
func scaleTo32(src image.Image) []pixel {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	side := w
	if h > side {
		side = h
	}
	ox, oy := (side-w)/2, (side-h)/2
	out := make([]pixel, 32*32)
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			x0, x1 := x*side/32, (x+1)*side/32
			y0, y1 := y*side/32, (y+1)*side/32
			if x1 == x0 {
				x1++
			}
			if y1 == y0 {
				y1++
			}
			var sr, sg, sb, sa, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					ix, iy := sx-ox, sy-oy
					n++
					if ix < 0 || iy < 0 || ix >= w || iy >= h {
						continue
					}
					c := color.NRGBAModel.Convert(src.At(b.Min.X+ix, b.Min.Y+iy)).(color.NRGBA)
					sr += uint64(c.R) * uint64(c.A)
					sg += uint64(c.G) * uint64(c.A)
					sb += uint64(c.B) * uint64(c.A)
					sa += uint64(c.A)
				}
			}
			p := &out[y*32+x]
			if sa == 0 || sa/n < 128 {
				continue
			}
			p.a = true
			p.c = rgb5{uint8(sr / sa >> 3), uint8(sg / sa >> 3), uint8(sb / sa >> 3)}
		}
	}
	return out
}

// medianCut reduces the colors to at most n (median cut algorithm).
func medianCut(cs []rgb5, n int) []rgb5 {
	if len(cs) == 0 {
		return nil
	}
	boxes := [][]rgb5{append([]rgb5(nil), cs...)}
	for len(boxes) < n {
		// the splittable box with the largest color range
		bi, br, bch := -1, 0, 0
		for i, bx := range boxes {
			if len(bx) < 2 {
				continue
			}
			for ch := 0; ch < 3; ch++ {
				lo, hi := 255, 0
				for _, c := range bx {
					v := chan5(c, ch)
					if v < lo {
						lo = v
					}
					if v > hi {
						hi = v
					}
				}
				if hi-lo > br {
					bi, br, bch = i, hi-lo, ch
				}
			}
		}
		if bi < 0 {
			break
		}
		bx := boxes[bi]
		sort.Slice(bx, func(i, j int) bool { return chan5(bx[i], bch) < chan5(bx[j], bch) })
		mid := len(bx) / 2
		boxes[bi] = bx[:mid]
		boxes = append(boxes, bx[mid:])
	}
	pal := make([]rgb5, 0, len(boxes))
	for _, bx := range boxes {
		var r, g, b int
		for _, c := range bx {
			r += int(c.r)
			g += int(c.g)
			b += int(c.b)
		}
		k := len(bx)
		pal = append(pal, rgb5{uint8((r + k/2) / k), uint8((g + k/2) / k), uint8((b + k/2) / k)})
	}
	return pal
}

func chan5(c rgb5, ch int) int {
	switch ch {
	case 0:
		return int(c.r)
	case 1:
		return int(c.g)
	}
	return int(c.b)
}

func nearest(pal []rgb5, c rgb5) int {
	best, bd := 0, 1<<30
	for i, p := range pal {
		dr, dg, db := int(p.r)-int(c.r), int(p.g)-int(c.g), int(p.b)-int(c.b)
		if d := dr*dr*3 + dg*dg*4 + db*db*2; d < bd {
			best, bd = i, d
		}
	}
	return best
}
