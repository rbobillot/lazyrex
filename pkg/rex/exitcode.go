package rex

import (
	"context"
	"errors"
	"strings"

	"github.com/rbobillot/lazyrex/pkg/render"
)

// ExitKind is what an exit code means to the UI.
type ExitKind int

const (
	ExitOK ExitKind = iota
	ExitFailed
	ExitUsage
	ExitNotFound
	ExitUnreachable
	ExitInterrupted
	ExitSSH
	ExitStartError
	ExitTimeout
)

func (k ExitKind) String() string {
	return [...]string{"ok", "failed", "usage", "not found", "unreachable", "interrupted", "ssh", "start error", "timeout"}[k]
}

// MapExit maps a rex exit code. It does not apply to `rex wait` or
// `rex run --wait`, which exit with the program's status.
func MapExit(code int, t Transport, ctxErr error) ExitKind {
	switch {
	case errors.Is(ctxErr, context.Canceled):
		return ExitInterrupted
	case errors.Is(ctxErr, context.DeadlineExceeded):
		return ExitTimeout
	}
	switch code {
	case 0:
		return ExitOK
	case 2:
		return ExitUsage
	case 3:
		return ExitNotFound
	case 4:
		return ExitUnreachable
	case 130:
		return ExitInterrupted
	case 255:
		if t != nil && t.SSH() {
			return ExitSSH
		}
		return ExitFailed
	case -1:
		return ExitStartError
	}
	return ExitFailed
}

const maxToast = 200

// ToastLine returns the first non-empty line of stderr, sanitized and capped.
func ToastLine(stderr []byte) string {
	for _, line := range strings.Split(render.LogText(string(stderr)), "\n") {
		line = strings.TrimSpace(strings.ReplaceAll(line, "\t", " "))
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > maxToast {
			line = string(r[:maxToast-1]) + "…"
		}
		return line
	}
	return ""
}
