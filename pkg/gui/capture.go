package gui

import (
	"strings"

	"github.com/rbobillot/lazyrex/pkg/render"
)

// AltNote heads the capture of a block showing a full-screen program.
const AltNote = "full-screen program: no scrollback, this is the current screen"

// RenderCapture sanitizes a capture, keeps its last maxLines lines and puts
// AltNote first when the block is on the alternate screen.
func RenderCapture(raw []byte, maxLines int, alt bool) string {
	out := render.Capture(raw, maxLines)
	if alt {
		return AltNote + "\n" + out
	}
	return out
}

// FindMatches returns the indices of the lines containing q, ignoring case
// and SGR sequences.
func FindMatches(content, q string) []int {
	if q == "" {
		return nil
	}
	q = strings.ToLower(q)
	var out []int
	for i, line := range strings.Split(strings.TrimSuffix(content, "\n"), "\n") {
		if strings.Contains(strings.ToLower(render.StripSGR(line)), q) {
			out = append(out, i)
		}
	}
	return out
}
