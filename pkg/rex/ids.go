package rex

import (
	"strings"
	"unicode"
)

// Full Rex IDs. lazyrex always passes full IDs, never labels or suffixes.
type (
	SessionID string
	WindowID  string
	BlockID   string
	ClientID  string
)

func validID(prefix, s string) bool {
	if len(s) <= len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (id SessionID) Valid() bool { return validID("session:", string(id)) }
func (id WindowID) Valid() bool  { return validID("window:", string(id)) }
func (id BlockID) Valid() bool   { return validID("block:", string(id)) }
func (id ClientID) Valid() bool  { return validID("client:", string(id)) }
