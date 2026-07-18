// Генератор .ico из PNG (формат ICO с PNG-вложениями, Vista+).
// Запуск: go run ./build/gen-ico -in build/icon.png -o build/windows/icon.ico
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

var sizes = []int{16, 24, 32, 48, 64, 128, 256}

func main() {
	in := flag.String("in", "icon.png", "исходный PNG")
	out := flag.String("o", "icon.ico", "выходной ICO")
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

	// рендерим каждый размер в PNG
	var images [][]byte
	for _, s := range sizes {
		var buf bytes.Buffer
		if err := png.Encode(&buf, scaleBox(src, s)); err != nil {
			log.Fatal(err)
		}
		images = append(images, buf.Bytes())
	}

	// ICO: заголовок + каталог + данные
	var w bytes.Buffer
	binary.Write(&w, binary.LittleEndian, uint16(0)) // reserved
	binary.Write(&w, binary.LittleEndian, uint16(1)) // type: icon
	binary.Write(&w, binary.LittleEndian, uint16(len(sizes)))
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		b := byte(s)
		if s >= 256 {
			b = 0 // 0 означает 256
		}
		w.WriteByte(b)                                    // width
		w.WriteByte(b)                                    // height
		w.WriteByte(0)                                    // palette
		w.WriteByte(0)                                    // reserved
		binary.Write(&w, binary.LittleEndian, uint16(1))  // planes
		binary.Write(&w, binary.LittleEndian, uint16(32)) // bpp
		binary.Write(&w, binary.LittleEndian, uint32(len(images[i])))
		binary.Write(&w, binary.LittleEndian, uint32(offset))
		offset += len(images[i])
	}
	for _, img := range images {
		w.Write(img)
	}
	if err := os.WriteFile(*out, w.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("ico written: %s (%d sizes)", *out, len(sizes))
}

// scaleBox — уменьшение усреднением по площади (box filter):
// для даунскейла квадратной иконки даёт чистый результат без библиотек.
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
