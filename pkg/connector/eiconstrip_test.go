package connector

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"testing"
)

func solidGIF(t *testing.T, delays []int, colors ...color.RGBA) []byte {
	t.Helper()
	anim := &gif.GIF{Config: image.Config{Width: 50, Height: 50}}
	for i, c := range colors {
		frame := image.NewPaletted(image.Rect(0, 0, 50, 50), color.Palette{color.RGBA{}, c})
		for p := range frame.Pix {
			frame.Pix[p] = 1
		}
		anim.Image = append(anim.Image, frame)
		anim.Delay = append(anim.Delay, delays[i])
		anim.Disposal = append(anim.Disposal, gif.DisposalNone)
	}
	anim.Config.ColorModel = anim.Image[0].Palette
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, anim); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func solidPNG(t *testing.T, size int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for p := 0; p < len(img.Pix); p += 4 {
		img.Pix[p], img.Pix[p+1], img.Pix[p+2], img.Pix[p+3] = c.R, c.G, c.B, c.A
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var (
	red   = color.RGBA{R: 255, A: 255}
	green = color.RGBA{G: 255, A: 255}
	blue  = color.RGBA{B: 255, A: 255}
)

func decodeAll(t *testing.T, cell int, images ...[]byte) [][]animFrame {
	t.Helper()
	sources := make([][]animFrame, len(images))
	for i, data := range images {
		frames, err := decodeFrames(data, cell)
		if err != nil {
			t.Fatal(err)
		}
		sources[i] = frames
	}
	return sources
}

func TestCombineStaticEIcons(t *testing.T) {
	// The second image is too large and gets scaled down into its cell.
	sources := decodeAll(t, 100, solidPNG(t, 100, red), solidPNG(t, 300, blue), solidPNG(t, 100, red))
	data, mimeType, w, h, animated, err := combineEIcons(sources, 100)
	if err != nil || mimeType != "image/png" || w != 300 || h != 100 || animated {
		t.Fatalf("unexpected result %s %dx%d animated=%v err=%v", mimeType, w, h, animated, err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got := color.RGBAModel.Convert(img.At(150, 50)).(color.RGBA); got != blue {
		t.Errorf("middle cell is %v, expected blue", got)
	}
	if got := color.RGBAModel.Convert(img.At(250, 50)).(color.RGBA); got != red {
		t.Errorf("last cell is %v, expected red", got)
	}
}

func TestCombineAnimatedEIcons(t *testing.T) {
	// Loops of 40 and 60 hundredths of a second are combined into one loop of 120.
	sources := decodeAll(t, 100,
		solidGIF(t, []int{20, 20}, red, green),
		solidPNG(t, 100, blue),
		solidGIF(t, []int{30, 30}, green, red),
	)
	data, mimeType, w, h, animated, err := combineEIcons(sources, 100)
	if err != nil || mimeType != "image/gif" || w != 300 || h != 100 || !animated {
		t.Fatalf("unexpected result %s %dx%d animated=%v err=%v", mimeType, w, h, animated, err)
	}
	anim, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, d := range anim.Delay {
		total += d
	}
	// Frame changes at 0, 20, 30, 40, 60, 80, 90, 100.
	if total != 120 || len(anim.Image) != 8 {
		t.Errorf("expected 8 frames in 120cs, got %d frames in %dcs (%v)", len(anim.Image), total, anim.Delay)
	}
	at := func(frame, x int) color.RGBA {
		return color.RGBAModel.Convert(anim.Image[frame].At(x, 50)).(color.RGBA)
	}
	// At 30cs: first is green (20-40), second blue, third red (30-60).
	if at(2, 50) != green || at(2, 150) != blue || at(2, 250) != red {
		t.Errorf("unexpected colors in frame 2: %v %v %v", at(2, 50), at(2, 150), at(2, 250))
	}
}

func TestCombineManyEIconsWraps(t *testing.T) {
	images := make([][]byte, 10)
	for i := range images {
		images[i] = solidPNG(t, 100, red)
	}
	_, _, w, h, _, err := combineEIcons(decodeAll(t, 100, images...), 100)
	if err != nil || w != maxEIconsPerRow*100 || h != 200 {
		t.Errorf("expected %dx200, got %dx%d (%v)", maxEIconsPerRow*100, w, h, err)
	}
}

func TestQuantizeManyColors(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := range 64 {
		for x := range 64 {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 4), B: 128, A: 255})
		}
	}
	img.SetRGBA(0, 0, color.RGBA{})
	out := quantize(img)
	if len(out.Palette) > 256 {
		t.Fatalf("palette has %d colors", len(out.Palette))
	}
	if out.ColorIndexAt(0, 0) != 0 {
		t.Error("transparent pixel didn't get the transparent index")
	}
	got := out.Palette[out.ColorIndexAt(40, 20)].(color.RGBA)
	if diff := int(got.R) - 160; diff < -24 || diff > 24 {
		t.Errorf("color too far off: %v", got)
	}
}
