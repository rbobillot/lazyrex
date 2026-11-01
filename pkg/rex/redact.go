package rex

import (
	"fmt"
	"strings"
)

func redactedText(s string) string { return fmt.Sprintf("<redacted %d bytes>", len(s)) }

// Redacted returns a copy of args with the redacted entries replaced by
// "<redacted N bytes>", N being the byte length of the hidden part.
func Redacted(args []string, r []Redaction) []string {
	out := append([]string{}, args...)
	for _, red := range r {
		if red.Index < 0 || red.Index >= len(out) {
			continue
		}
		a := out[red.Index]
		if red.ValueOnly {
			if k, v, ok := strings.Cut(a, "="); ok {
				out[red.Index] = k + "=" + redactedText(v)
				continue
			}
		}
		out[red.Index] = redactedText(a)
	}
	return out
}
