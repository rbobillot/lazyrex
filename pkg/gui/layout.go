package gui

// Rect is an inclusive cell rectangle, frame included.
type Rect struct{ X0, Y0, X1, Y1 int }

// Arrange lays out the side panels in the left column (the focused one taller),
// the main view over the command log on the right, and a one-row footer.
func Arrange(w, h int, focus Panel) map[string]Rect {
	avail := h - 1
	leftW := min(max(30, w/3), w/2)

	var heights [5]int
	for i := range heights {
		heights[i] = 3
	}
	focused := -1
	if focus.isSide() {
		focused = int(focus)
		heights[focused] = 5
	}
	extra := avail
	for _, hh := range heights {
		extra -= hh
	}
	if extra >= 0 {
		var weights [5]int
		total := 0
		for i := range weights {
			if i == 0 && focused != 0 {
				continue
			}
			weights[i] = 1
			if i == focused {
				weights[i] = 2
			}
			total += weights[i]
		}
		given := 0
		for i := range heights {
			add := extra * weights[i] / total
			heights[i] += add
			given += add
		}
		last := 4
		if focused >= 0 {
			last = focused
		}
		heights[last] += extra - given
	} else {
		for need := -extra; need > 0; {
			shrunk := false
			for i := 4; i >= 0 && need > 0; i-- {
				if i != focused && heights[i] > 2 {
					heights[i]--
					need--
					shrunk = true
				}
			}
			if !shrunk {
				if focused >= 0 && heights[focused] > 2 {
					heights[focused]--
					need--
					continue
				}
				break
			}
		}
	}

	rects := map[string]Rect{}
	y := 0
	for i, p := range sidePanels {
		rects[viewName(p)] = Rect{0, y, leftW - 1, y + heights[i] - 1}
		y += heights[i]
	}
	logH := max(min(8, avail/3), 3)
	rects["main"] = Rect{leftW, 0, w - 1, avail - logH - 1}
	rects["log"] = Rect{leftW, avail - logH, w - 1, avail - 1}
	rects["footer"] = Rect{0, h - 1, w - 1, h - 1}
	return rects
}
