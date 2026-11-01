// Package render turns untrusted terminal output, labels and stderr into text
// that is safe to write into a gocui view.
package render

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

type mode int

const (
	modeVT    mode = iota // keep validated SGR, \n and \t
	modeLog               // keep \n and \t
	modeLabel             // single line
)

// SanitizeVT keeps SGR sequences gocui can parse, \n and \t, and drops every
// other escape sequence and control character. \r\n becomes \n.
func SanitizeVT(b []byte) string { return sanitize(b, modeVT) }

// LogText keeps \n and \t and drops every escape sequence, \r and control.
func LogText(s string) string { return sanitize([]byte(s), modeLog) }

// Label returns s on one line without escape sequences or controls.
func Label(s string) string { return sanitize([]byte(s), modeLabel) }

// StripSGR removes SGR sequences from text already sanitized by SanitizeVT.
func StripSGR(s string) string {
	if !strings.Contains(s, "\x1b[") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			if end := strings.IndexByte(s[i:], 'm'); end >= 0 {
				i += end
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

type state int

const (
	stGround state = iota
	stEsc
	stEscIntermediate
	stCSI
	stString // OSC, DCS, APC, PM, SOS: dropped up to BEL (OSC only) or ST
)

const maxCSI = 64

func sanitize(in []byte, m mode) string {
	var out strings.Builder
	out.Grow(len(in))
	st := stGround
	var csi []byte
	csiBad := false
	oscBEL := false

	enterString := func(osc bool) {
		st = stString
		oscBEL = osc
	}
	enterCSI := func() {
		st = stCSI
		csi = csi[:0]
		csiBad = false
	}

	for i := 0; i < len(in); {
		r, size := utf8.DecodeRune(in[i:])
		if r == utf8.RuneError && size <= 1 {
			i++
			continue
		}
		i += size

		switch st {
		case stString:
			switch {
			case r == 0x07 && oscBEL, r == 0x9c:
				st = stGround
			case r == 0x1b:
				// Only ESC \ ends the string; any other ESC is payload.
				if i < len(in) && in[i] == '\\' {
					i++
					st = stGround
				}
			case r == 0x18 || r == 0x1a:
				st = stGround
			}
			continue

		case stEsc:
			switch {
			case r == '[':
				enterCSI()
			case r == ']':
				enterString(true)
			case r == 'P' || r == '_' || r == '^' || r == 'X':
				enterString(false)
			case r >= 0x20 && r <= 0x2f:
				st = stEscIntermediate
			case r == 0x1b:
				st = stEsc
			default:
				st = stGround
			}
			continue

		case stEscIntermediate:
			switch {
			case r >= 0x20 && r <= 0x2f:
			case r == 0x1b:
				st = stEsc
			default:
				st = stGround
			}
			continue

		case stCSI:
			switch {
			case r >= 0x40 && r <= 0x7e:
				if r == 'm' && m == modeVT && !csiBad {
					if sgr, ok := normalizeSGR(csi); ok {
						out.WriteString(sgr)
					}
				}
				st = stGround
			case r >= 0x20 && r <= 0x3f:
				if len(csi) < maxCSI {
					csi = append(csi, byte(r))
				} else {
					csiBad = true
				}
			case r == 0x1b:
				st = stEsc
			case r == 0x18 || r == 0x1a:
				st = stGround
			case r < 0x20:
			default:
				st = stGround
			}
			continue
		}

		switch {
		case r == 0x1b:
			st = stEsc
		case r == 0x9b:
			enterCSI()
		case r == 0x9d:
			enterString(true)
		case r == 0x90 || r == 0x9f || r == 0x9e || r == 0x98:
			enterString(false)
		case r == '\n':
			if m == modeLabel {
				out.WriteByte(' ')
			} else {
				out.WriteByte('\n')
			}
		case r == '\t':
			if m == modeLabel {
				out.WriteByte(' ')
			} else {
				out.WriteByte('\t')
			}
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// normalizeSGR rebuilds an SGR from its parameter bytes in the subset gocui's
// escape parser accepts; anything else is dropped rather than risk gocui
// printing a raw ESC.
func normalizeSGR(params []byte) (string, bool) {
	for _, c := range params {
		if (c < '0' || c > '9') && c != ';' {
			return "", false
		}
	}
	fields := strings.Split(string(params), ";")
	nums := make([]int, 0, len(fields))
	for _, f := range fields {
		if f == "" {
			nums = append(nums, 0)
			continue
		}
		if len(f) > 5 {
			return "", false
		}
		n, err := strconv.Atoi(f)
		if err != nil {
			return "", false
		}
		nums = append(nums, n)
	}
	var kept []string
	for i := 0; i < len(nums); i++ {
		n := nums[i]
		switch n {
		case 38, 48, 58:
			if i+1 >= len(nums) {
				return "", false
			}
			var width int
			switch nums[i+1] {
			case 5:
				width = 3
			case 2:
				width = 5
			default:
				return "", false
			}
			if i+width > len(nums) {
				return "", false
			}
			if n != 58 {
				for _, v := range nums[i : i+width] {
					kept = append(kept, strconv.Itoa(v))
				}
			}
			i += width - 1
		default:
			kept = append(kept, strconv.Itoa(n))
		}
	}
	if len(kept) == 0 || len(kept) > 16 {
		return "", false
	}
	return "\x1b[" + strings.Join(kept, ";") + "m", true
}
