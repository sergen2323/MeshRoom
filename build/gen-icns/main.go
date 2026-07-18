// Генератор .icns из PNG (вложения PNG, macOS 10.7+) — без sips/iconutil.
// Запуск: go run ./build/gen-icns -in build/icon.png -o dist/MeshRoom.icns
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
)

// типы ICNS-вложений: размер → четырёхбуквенный код
var entries = []struct {
	size int
	code string
}{
	{16, "icp4"}, {32, "icp5"}, {64, "icp6"}, {128, "ic07"},
	{256, "ic08"}, {512, "ic09"}, {1024, "ic10"},
	{32, "ic11"}, {64, "ic12"}, {256, "ic13"}, {512, "ic14"}, // @2x-варианты
}

func main() {
	in := flag.String("in", "icon.png", "исходный PNG (1024x1024)")
	out := flag.String("o", "icon.icns", "выходной ICNS")
	flag.Parse()

	f, err := os.Open(*in)
	if err != nil {
		log.Fatal(err)
	}
	src, err := png.Decode(f)
	f.Close()
	if err != nil {
		log.Fatal(err)
	}

	var body bytes.Buffer
	for _, e := range entries {
		var buf bytes.Buffer
		if err := png.Encode(&buf, scaleBox(src, e.size)); err != nil {
			log.Fatal(err)
		}
		body.WriteString(e.code)
		binary.Write(&body, binary.BigEndian, uint32(8+buf.Len()))
		body.Write(buf.Bytes())
	}

	var file bytes.Buffer
	file.WriteString("icns")
	binary.Write(&file, binary.BigEndian, uint32(8+body.Len()))
	file.Write(body.Bytes())

	if err := os.WriteFile(*out, file.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("icns written: %s (%d entries)", *out, len(entries))
}

// scaleBox — уменьшение усреднением по площади (тот же фильтр, что в gen-ico).
func scaleBox(src image.Image, size int) *image.RGBA {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	for dy := 0; dy < size; dy++ {
		for dx := 0; dx < size; dx++ {
			x0 := sb.Min.X + dx*sw/size
			x1 := sb.Min.X + (dx+1)*sw/size
			y0 := sb.Min.Y + dy*sh/size
			y1 := sb.Min.Y + (dy+1)*sh/size
			if x1 <= x0 {
				x1 = x0 + 1
			}
			if y1 <= y0 {
				y1 = y0 + 1
			}
			var r, g, b, a, n uint64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					pr, pg, pb, pa := src.At(x, y).RGBA()
					r += uint64(pr)
					g += uint64(pg)
					b += uint64(pb)
					a += uint64(pa)
					n++
				}
			}
			dst.Set(dx, dy, color.RGBA64{
				R: uint16(r / n), G: uint16(g / n),
				B: uint16(b / n), A: uint16(a / n),
			})
		}
	}
	return dst
}
