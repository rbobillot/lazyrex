package render

import "strings"

// TailLines returns the last max lines of s. A trailing newline ends the last
// line rather than starting an empty one.
func TailLines(s string, max int) string {
	if max <= 0 {
		return ""
	}
	end := len(s)
	if strings.HasSuffix(s, "\n") {
		end--
	}
	n := 0
	for i := end - 1; i >= 0; i-- {
		if s[i] == '\n' {
			n++
			if n == max {
				return s[i+1:]
			}
		}
	}
	return s
}

// Capture sanitizes a whole capture, so sequences that straddle the cut are
// parsed with their state, then keeps its last maxLines lines.
func Capture(raw []byte, maxLines int) string {
	return TailLines(SanitizeVT(raw), maxLines)
}
