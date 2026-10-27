package connector

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/png"
	"slices"
	"sort"
)

// A Matrix sticker holds one image, so a message with several eicons is sent as one image
// that shows them side by side. If any of them is animated, the combined image is an animated
// GIF in which every eicon keeps its own timing.

const (
	// eiconCell is the width and height of one eicon in a combined image.
	eiconCell = 100
	// maxEIconsPerRow is how many eicons are put next to each other before starting a new row.
	maxEIconsPerRow = 8
	// maxCombinedEIcons is the most eicons that are combined; longer messages stay text.
	maxCombinedEIcons = 32
	// maxLoopCentis is the longest loop (in 1/100 s) used to keep all animations in sync.
	maxLoopCentis = 1000
	// maxOutputFrames limits the size of combined animations.
	maxOutputFrames = 150
	// minFrameDelay is the delay browsers use for GIF frames with a delay below 2.
	minFrameDelay = 10
)

// animFrame is one fully drawn frame of an image, scaled to the cell size.
type animFrame struct {
	img   *image.RGBA
	delay int // 1/100 s
}

// decodeFrames decodes an image into full frames, scaled to fit into a cell.
func decodeFrames(data []byte, cell int) ([]animFrame, error) {
	if anim, err := gif.DecodeAll(bytes.NewReader(data)); err == nil && len(anim.Image) > 0 {
		return gifFrames(anim, cell), nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return []animFrame{{img: fitToCell(img, cell)}}, nil
}

// gifFrames draws the frames of a GIF, applying the disposal methods.
func gifFrames(anim *gif.GIF, cell int) []animFrame {
	bounds := image.Rect(0, 0, anim.Config.Width, anim.Config.Height)
	for _, frame := range anim.Image {
		bounds = bounds.Union(frame.Bounds())
	}
	canvas := image.NewRGBA(bounds)
	frames := make([]animFrame, 0, len(anim.Image))
	for i, frame := range anim.Image {
		disposal := byte(gif.DisposalNone)
		if i < len(anim.Disposal) {
			disposal = anim.Disposal[i]
		}
		var previous *image.RGBA
		if disposal == gif.DisposalPrevious {
			previous = image.NewRGBA(bounds)
			draw.Draw(previous, bounds, canvas, bounds.Min, draw.Src)
		}
		draw.Draw(canvas, frame.Bounds(), frame, frame.Bounds().Min, draw.Over)
		delay := minFrameDelay
		if i < len(anim.Delay) && anim.Delay[i] >= 2 {
			delay = anim.Delay[i]
		}
		frames = append(frames, animFrame{img: fitToCell(canvas, cell), delay: delay})
		switch disposal {
		case gif.DisposalBackground:
			draw.Draw(canvas, frame.Bounds(), image.Transparent, image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			canvas = previous
		}
	}
	if len(frames) == 1 {
		frames[0].delay = 0
	}
	return frames
}

// fitToCell copies an image into a cell, centered and scaled down (nearest neighbor) if it's
// too large.
func fitToCell(src image.Image, cell int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > cell || h > cell {
		if w >= h {
			w, h = cell, max(1, h*cell/w)
		} else {
			w, h = max(1, w*cell/h), cell
		}
	}
	out := image.NewRGBA(image.Rect(0, 0, cell, cell))
	offX, offY := (cell-w)/2, (cell-h)/2
	if w == b.Dx() && h == b.Dy() {
		draw.Draw(out, image.Rect(offX, offY, offX+w, offY+h), src, b.Min, draw.Src)
		return out
	}
	for y := 0; y < h; y++ {
		sy := b.Min.Y + y*b.Dy()/h
		for x := 0; x < w; x++ {
			out.Set(offX+x, offY+y, src.At(b.Min.X+x*b.Dx()/w, sy))
		}
	}
	return out
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// frameAt returns the frame of an animation that is shown at time t (looping).
func frameAt(frames []animFrame, total, t int) *image.RGBA {
	if len(frames) == 1 || total <= 0 {
		return frames[0].img
	}
	t %= total
	for _, frame := range frames {
		if t < frame.delay {
			return frame.img
		}
		t -= frame.delay
	}
	return frames[len(frames)-1].img
}

// combineEIcons puts images (as returned by decodeFrames) into a grid. It returns a PNG if
// none of them is animated, otherwise an animated GIF.
func combineEIcons(sources [][]animFrame, cell int) (data []byte, mimeType string, width, height int, animated bool, err error) {
	if len(sources) == 0 {
		return nil, "", 0, 0, false, errors.New("no images")
	}
	cols := min(len(sources), maxEIconsPerRow)
	rows := (len(sources) + cols - 1) / cols
	width, height = cols*cell, rows*cell
	totals := make([]int, len(sources))
	loop := 0
	for i, frames := range sources {
		if len(frames) < 2 {
			continue
		}
		for _, frame := range frames {
			totals[i] += frame.delay
		}
		switch {
		case loop == 0:
			loop = totals[i]
		case loop <= maxLoopCentis:
			loop = loop / gcd(loop, totals[i]) * totals[i]
		}
	}
	if loop > maxLoopCentis {
		// The animations can't loop in sync within a reasonable time; use the longest one.
		loop = slices.Max(totals)
	}
	draw1 := func(canvas *image.RGBA, t int) {
		for i, frames := range sources {
			x, y := (i%cols)*cell, (i/cols)*cell
			draw.Draw(canvas, image.Rect(x, y, x+cell, y+cell), frameAt(frames, totals[i], t), image.Point{}, draw.Src)
		}
	}
	if loop == 0 {
		canvas := image.NewRGBA(image.Rect(0, 0, width, height))
		draw1(canvas, 0)
		var buf bytes.Buffer
		if err = png.Encode(&buf, canvas); err != nil {
			return nil, "", 0, 0, false, err
		}
		return buf.Bytes(), "image/png", width, height, false, nil
	}

	// Start a new output frame whenever any of the animations changes its frame.
	changes := map[int]bool{0: true}
	for i, frames := range sources {
		if totals[i] == 0 {
			continue
		}
		for start := 0; start < loop; {
			for _, frame := range frames {
				if start >= loop {
					break
				}
				changes[start] = true
				start += frame.delay
			}
		}
	}
	times := make([]int, 0, len(changes))
	for t := range changes {
		times = append(times, t)
	}
	sort.Ints(times)
	if len(times) > maxOutputFrames {
		times = times[:0]
		for i := range maxOutputFrames {
			times = append(times, i*loop/maxOutputFrames)
		}
		times = slices.Compact(times)
	}
	out := &gif.GIF{LoopCount: 0, Config: image.Config{Width: width, Height: height}}
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	for i, t := range times {
		next := loop
		if i+1 < len(times) {
			next = times[i+1]
		}
		draw1(canvas, t)
		out.Image = append(out.Image, quantize(canvas))
		out.Delay = append(out.Delay, max(next-t, 2))
		out.Disposal = append(out.Disposal, gif.DisposalBackground)
	}
	out.Config.ColorModel = out.Image[0].Palette
	var buf bytes.Buffer
	if err = gif.EncodeAll(&buf, out); err != nil {
		return nil, "", 0, 0, false, err
	}
	return buf.Bytes(), "image/gif", width, height, true, nil
}

// colorKey packs an opaque color into an int.
func colorKey(c color.RGBA) uint32 {
	return uint32(c.R)<<16 | uint32(c.G)<<8 | uint32(c.B)
}

// quantize converts an image into a paletted image with at most 255 colors and a transparent
// color at index 0. Pixels that are less than half opaque become transparent.
func quantize(img *image.RGBA) *image.Paletted {
	counts := make(map[uint32]int)
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := img.RGBAAt(x, y)
			if c.A >= 128 {
				counts[colorKey(unpremultiply(c))]++
			}
		}
	}
	colors := make([]uint32, 0, len(counts))
	for key := range counts {
		colors = append(colors, key)
	}
	slices.Sort(colors)
	if len(colors) > 255 {
		colors = medianCut(colors, counts, 255)
	}
	pal := make(color.Palette, 0, len(colors)+1)
	pal = append(pal, color.RGBA{})
	exact := make(map[uint32]uint8, len(colors))
	for i, key := range colors {
		pal = append(pal, color.RGBA{R: uint8(key >> 16), G: uint8(key >> 8), B: uint8(key), A: 255})
		exact[key] = uint8(i + 1)
	}
	out := image.NewPaletted(b, pal)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := img.RGBAAt(x, y)
			if c.A < 128 {
				continue // index 0 = transparent
			}
			key := colorKey(unpremultiply(c))
			idx, ok := exact[key]
			if !ok {
				idx = uint8(nearest(pal, key))
				exact[key] = idx
			}
			out.Pix[out.PixOffset(x, y)] = idx
		}
	}
	return out
}

// unpremultiply returns the color as if it was fully opaque.
func unpremultiply(c color.RGBA) color.RGBA {
	if c.A == 255 || c.A == 0 {
		return color.RGBA{R: c.R, G: c.G, B: c.B, A: 255}
	}
	return color.RGBA{
		R: uint8(min(255, int(c.R)*255/int(c.A))),
		G: uint8(min(255, int(c.G)*255/int(c.A))),
		B: uint8(min(255, int(c.B)*255/int(c.A))),
		A: 255,
	}
}

// nearest returns the index of the closest opaque palette color (index 0 is transparent).
func nearest(pal color.Palette, key uint32) int {
	r, g, b := int(key>>16&0xff), int(key>>8&0xff), int(key&0xff)
	best, bestDist := 1, -1
	for i := 1; i < len(pal); i++ {
		c := pal[i].(color.RGBA)
		dr, dg, db := int(c.R)-r, int(c.G)-g, int(c.B)-b
		if dist := dr*dr + dg*dg + db*db; bestDist < 0 || dist < bestDist {
			best, bestDist = i, dist
		}
	}
	return best
}

// medianCut reduces colors to at most n representative colors.
func medianCut(colors []uint32, counts map[uint32]int, n int) []uint32 {
	channel := func(key uint32, ch int) int { return int(key >> (16 - 8*ch) & 0xff) }
	boxes := [][]uint32{colors}
	for len(boxes) < n {
		// Split the box with the largest color range.
		bestBox, bestCh, bestRange := -1, 0, 0
		for i, box := range boxes {
			if len(box) < 2 {
				continue
			}
			for ch := range 3 {
				lo, hi := 255, 0
				for _, key := range box {
					v := channel(key, ch)
					lo, hi = min(lo, v), max(hi, v)
				}
				if hi-lo > bestRange {
					bestBox, bestCh, bestRange = i, ch, hi-lo
				}
			}
		}
		if bestBox < 0 {
			break
		}
		box := boxes[bestBox]
		slices.SortFunc(box, func(a, b uint32) int { return channel(a, bestCh) - channel(b, bestCh) })
		total := 0
		for _, key := range box {
			total += counts[key]
		}
		split, seen := 1, 0
		for i, key := range box[:len(box)-1] {
			seen += counts[key]
			if seen*2 >= total {
				split = i + 1
				break
			}
		}
		boxes[bestBox] = box[:split]
		boxes = append(boxes, box[split:])
	}
	result := make([]uint32, 0, len(boxes))
	for _, box := range boxes {
		var r, g, b, total int
		for _, key := range box {
			w := counts[key]
			r += channel(key, 0) * w
			g += channel(key, 1) * w
			b += channel(key, 2) * w
			total += w
		}
		result = append(result, uint32(r/total)<<16|uint32(g/total)<<8|uint32(b/total))
	}
	return slices.Compact(slices.Sorted(slices.Values(result)))
}
