package api

import (
	"image"
	"math"
	"os"
	"strings"
)

const blurhashAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz#$%*+,-.:;=?@[]^_{|}~"

func blurhashForStoredImage(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	img, _, err := image.Decode(file)
	if err != nil {
		return ""
	}
	hash, ok := blurhashEncode(img, 4, 4)
	if !ok {
		return ""
	}
	return hash
}

func blurhashEncode(img image.Image, componentsX int, componentsY int) (string, bool) {
	if componentsX < 1 || componentsX > 9 || componentsY < 1 || componentsY > 9 {
		return "", false
	}
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 {
		return "", false
	}
	factors := make([][3]float64, 0, componentsX*componentsY)
	for y := 0; y < componentsY; y++ {
		for x := 0; x < componentsX; x++ {
			factors = append(factors, multiplyBlurhashBasis(img, bounds, x, y))
		}
	}
	sizeFlag := (componentsX - 1) + (componentsY-1)*9
	out := blurhashEncodeBase83(sizeFlag, 1)
	maxAC := 0.0
	for _, factor := range factors[1:] {
		maxAC = math.Max(maxAC, math.Abs(factor[0]))
		maxAC = math.Max(maxAC, math.Abs(factor[1]))
		maxAC = math.Max(maxAC, math.Abs(factor[2]))
	}
	quantMaxAC := 0
	if maxAC > 0 {
		quantMaxAC = int(math.Max(0, math.Min(82, math.Floor(maxAC*166-0.5))))
	}
	maximumValue := float64(quantMaxAC+1) / 166
	out += blurhashEncodeBase83(quantMaxAC, 1)
	out += blurhashEncodeBase83(blurhashEncodeDC(factors[0]), 4)
	for _, factor := range factors[1:] {
		out += blurhashEncodeBase83(blurhashEncodeAC(factor, maximumValue), 2)
	}
	return out, true
}

func multiplyBlurhashBasis(img image.Image, bounds image.Rectangle, componentX int, componentY int) [3]float64 {
	width := bounds.Dx()
	height := bounds.Dy()
	normalisation := 2.0
	if componentX == 0 && componentY == 0 {
		normalisation = 1
	}
	var r, g, b float64
	for y := 0; y < height; y++ {
		basisY := math.Cos(math.Pi * float64(componentY) * float64(y) / float64(height))
		for x := 0; x < width; x++ {
			basis := basisY * math.Cos(math.Pi*float64(componentX)*float64(x)/float64(width))
			cr, cg, cb, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			r += basis * blurhashSRGBToLinear(float64(cr>>8))
			g += basis * blurhashSRGBToLinear(float64(cg>>8))
			b += basis * blurhashSRGBToLinear(float64(cb>>8))
		}
	}
	scale := normalisation / float64(width*height)
	return [3]float64{r * scale, g * scale, b * scale}
}

func blurhashEncodeDC(value [3]float64) int {
	r := blurhashLinearToSRGB(value[0])
	g := blurhashLinearToSRGB(value[1])
	b := blurhashLinearToSRGB(value[2])
	return (r << 16) + (g << 8) + b
}

func blurhashEncodeAC(value [3]float64, maximumValue float64) int {
	r := blurhashQuantizeAC(value[0], maximumValue)
	g := blurhashQuantizeAC(value[1], maximumValue)
	b := blurhashQuantizeAC(value[2], maximumValue)
	return r*19*19 + g*19 + b
}

func blurhashQuantizeAC(value float64, maximumValue float64) int {
	if maximumValue <= 0 {
		return 9
	}
	quantized := math.Floor(blurhashSignPow(value/maximumValue, 0.5)*9 + 9.5)
	return int(math.Max(0, math.Min(18, quantized)))
}

func blurhashSignPow(value float64, exp float64) float64 {
	if value < 0 {
		return -math.Pow(-value, exp)
	}
	return math.Pow(value, exp)
}

func blurhashSRGBToLinear(value float64) float64 {
	value = value / 255
	if value <= 0.04045 {
		return value / 12.92
	}
	return math.Pow((value+0.055)/1.055, 2.4)
}

func blurhashLinearToSRGB(value float64) int {
	value = math.Max(0, math.Min(1, value))
	if value <= 0.0031308 {
		return int(value*12.92*255 + 0.5)
	}
	return int((1.055*math.Pow(value, 1.0/2.4)-0.055)*255.0 + 0.5)
}

func blurhashEncodeBase83(value int, length int) string {
	var out strings.Builder
	for i := 1; i <= length; i++ {
		digit := (value / int(math.Pow(83, float64(length-i)))) % 83
		out.WriteByte(blurhashAlphabet[digit])
	}
	return out.String()
}
