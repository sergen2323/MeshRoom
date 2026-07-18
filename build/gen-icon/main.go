// Генератор иконки MeshRoom: стеклянная плитка с ромбом (◈) в духе UI.
// Запуск: go run ./build/gen-icon -o build/icon.png -size 1024
package main

import (
	"flag"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
)

func main() {
	out := flag.String("o", "icon.png", "выходной PNG")
	size := flag.Int("size", 1024, "размер стороны")
	flag.Parse()

	s := *size
	img := image.NewRGBA(image.Rect(0, 0, s, s))
	f := float64(s)

	// параметры формы
	margin := f * 0.08
	radius := f * 0.22
	cx, cy := f/2, f/2

	for y := 0; y < s; y++ {
		for x := 0; x < s; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5

			// скруглённый квадрат (плитка)
			d := roundedRectSDF(fx, fy, margin, margin, f-margin, f-margin, radius)
			if d > 0.5 {
				continue // прозрачный фон
			}
			// градиент фона: сверху-слева светлее (мягкий «свет» стекломорфизма)
			t := (fx/f + fy/f) / 2
			r := lerp(52, 30, t)
			g := lerp(60, 36, t)
			b := lerp(84, 56, t)

			// ромб в центре
			dd := math.Abs(fx-cx)/(f*0.26) + math.Abs(fy-cy)/(f*0.26)
			inner := math.Abs(fx-cx)/(f*0.13) + math.Abs(fy-cy)/(f*0.13)
			if dd <= 1 && inner >= 1 {
				// грань ромба — приглушённый серо-голубой акцент
				r, g, b = 143, 157, 196
			} else if inner < 1 {
				// сердцевина чуть светлее фона
				r, g, b = lerp(80, 60, t), lerp(88, 66, t), lerp(116, 92, t)
			}

			// светлая кромка (inset-блик), плавно затухающая книзу
			edge := roundedRectSDF(fx, fy, margin, margin, f-margin, f-margin, radius)
			if edge > -f*0.012 {
				k := (cy*1.6 - fy) / (cy * 1.6)
				if k < 0 {
					k = 0
				}
				r, g, b = r+44*k, g+44*k, b+48*k
			}

			// сглаживание края плитки
			alpha := 255.0
			if d > -1.5 {
				alpha = 255 * (0.5 - d) / 2
				if alpha < 0 {
					alpha = 0
				}
				if alpha > 255 {
					alpha = 255
				}
			}
			img.Set(x, y, color.RGBA{clamp(r), clamp(g), clamp(b), uint8(alpha)})
		}
	}

	fp, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	defer fp.Close()
	if err := png.Encode(fp, img); err != nil {
		log.Fatal(err)
	}
	log.Printf("icon written: %s (%dx%d)", *out, s, s)
}

// roundedRectSDF — расстояние со знаком до границы скруглённого прямоугольника.
func roundedRectSDF(px, py, x0, y0, x1, y1, r float64) float64 {
	cx := math.Max(x0+r, math.Min(px, x1-r))
	cy := math.Max(y0+r, math.Min(py, y1-r))
	dx, dy := px-cx, py-cy
	return math.Hypot(dx, dy) - r
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

func clamp(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}
