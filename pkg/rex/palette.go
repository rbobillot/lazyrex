package rex

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// SplitLine splits a palette line into words. Single quotes group anywhere
// and are literal inside; double quotes group only at the start of a word,
// where \" and \\ are their escapes. Elsewhere a backslash only escapes
// whitespace and a single quote, so bare JSON keeps its own escapes. Nothing
// is expanded. trailing reports whitespace after the last word.
func SplitLine(line string) (words []string, trailing bool, err error) {
	var cur strings.Builder
	inWord := false
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case unicode.IsSpace(r):
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
			continue
		case r == '\'':
			end := indexRune(rs, i+1, '\'')
			if end < 0 {
				return nil, false, errors.New("unterminated single quote")
			}
			cur.WriteString(string(rs[i+1 : end]))
			i = end
		case r == '"' && !inWord:
			j := i + 1
			for ; j < len(rs) && rs[j] != '"'; j++ {
				if rs[j] == '\\' && j+1 < len(rs) && (rs[j+1] == '"' || rs[j+1] == '\\') {
					j++
				}
				cur.WriteRune(rs[j])
			}
			if j >= len(rs) {
				return nil, false, errors.New("unterminated double quote")
			}
			i = j
		case r == '\\':
			if i+1 >= len(rs) {
				return nil, false, errors.New("trailing backslash")
			}
			if next := rs[i+1]; unicode.IsSpace(next) || next == '\'' {
				cur.WriteRune(next)
				i++
			} else {
				cur.WriteRune(r)
			}
		default:
			cur.WriteRune(r)
		}
		inWord = true
	}
	if inWord {
		words = append(words, cur.String())
	}
	trailing = len(rs) > 0 && unicode.IsSpace(rs[len(rs)-1])
	return words, trailing, nil
}

func indexRune(rs []rune, from int, r rune) int {
	for i := from; i < len(rs); i++ {
		if rs[i] == r {
			return i
		}
	}
	return -1
}

// ParsePalette reads `[rex] api call [-s SESSION] METHOD [JSON]` or
// `[rex] do ACTION [KEY=VALUE... | --args JSON]` into a command. Global
// flags, client targets, files and stdin are refused.
func ParsePalette(line string) (Cmd, error) {
	words, _, err := SplitLine(line)
	if err != nil {
		return Cmd{}, err
	}
	if len(words) > 0 && words[0] == "rex" {
		words = words[1:]
	}
	if len(words) == 0 {
		return Cmd{}, errors.New("expected api call or do")
	}
	var cmd Cmd
	switch words[0] {
	case "api":
		cmd, err = parseAPICall(words[1:])
	case "do":
		cmd, err = parseDo(words[1:])
	default:
		return Cmd{}, fmt.Errorf("%q: only api call and do run from the palette", words[0])
	}
	if err != nil {
		return Cmd{}, err
	}
	if cmd.Err != nil {
		return Cmd{}, cmd.Err
	}
	return cmd, nil
}

func parseAPICall(words []string) (Cmd, error) {
	if len(words) == 0 || words[0] != "call" {
		return Cmd{}, errors.New("only api call runs from the palette")
	}
	words = words[1:]
	var session SessionID
	if len(words) > 0 {
		switch w := words[0]; {
		case w == "-s" || w == "--session":
			if len(words) < 2 {
				return Cmd{}, fmt.Errorf("%s needs a session ID", w)
			}
			session, words = SessionID(words[1]), words[2:]
		case strings.HasPrefix(w, "--session="):
			session, words = SessionID(strings.TrimPrefix(w, "--session=")), words[1:]
		}
		if session != "" && !session.Valid() {
			return Cmd{}, fmt.Errorf("invalid session ID %q", session)
		}
	}
	if len(words) == 0 {
		return Cmd{}, errors.New("api call needs a method")
	}
	if strings.HasPrefix(words[0], "-") {
		return Cmd{}, fmt.Errorf("flag %q is not allowed in the palette", words[0])
	}
	var payload string
	switch len(words) {
	case 1:
	case 2:
		payload = words[1]
		if err := checkArgsObject(payload); err != nil {
			return Cmd{}, errors.New("the api call payload must be a JSON object")
		}
	default:
		return Cmd{}, errors.New("api call takes a method and at most one JSON object")
	}
	return Commands{}.APICall(session, words[0], payload), nil
}

func parseDo(words []string) (Cmd, error) {
	if len(words) == 0 {
		return Cmd{}, errors.New("do needs an action name")
	}
	name, rest := words[0], words[1:]
	if strings.HasPrefix(name, "-") {
		return Cmd{}, fmt.Errorf("flag %q is not allowed in the palette", name)
	}
	if len(rest) > 0 && rest[0] == "--args" {
		if len(rest) != 2 {
			return Cmd{}, errors.New("--args takes one JSON object and nothing after it")
		}
		return Commands{}.DoJSON(name, rest[1]), nil
	}
	for _, kv := range rest {
		if strings.HasPrefix(kv, "-") || strings.HasPrefix(kv, "@") || !strings.Contains(kv, "=") {
			return Cmd{}, fmt.Errorf("%q is not a KEY=VALUE argument", kv)
		}
	}
	return Commands{}.Do(name, rest...), nil
}

// CompletionSlot is what the word under the cursor of a palette line is.
type CompletionSlot int

const (
	SlotNone CompletionSlot = iota
	SlotVerb
	SlotCall
	SlotMethod
	SlotAction
	SlotTarget
)

// PaletteSlot names what the last word of a split palette line completes, the
// session a valid -s selects, and the prefix typed so far. A KEY=VALUE word
// completes its value.
func PaletteSlot(words []string, trailing bool) (CompletionSlot, SessionID, string) {
	if len(words) > 0 && words[0] == "rex" && (len(words) > 1 || trailing) {
		words = words[1:]
	}
	prefix, done := "", words
	if !trailing && len(words) > 0 {
		prefix, done = words[len(words)-1], words[:len(words)-1]
	}
	if len(done) == 0 {
		return SlotVerb, "", prefix
	}
	switch done[0] {
	case "api":
		if len(done) == 1 {
			return SlotCall, "", prefix
		}
		if done[1] != "call" {
			return SlotNone, "", prefix
		}
		var session SessionID
		i := 2
		if i < len(done) && (done[i] == "-s" || done[i] == "--session") {
			if i+1 == len(done) {
				return SlotTarget, "", prefix
			}
			if s := SessionID(done[i+1]); s.Valid() {
				session = s
			}
			i += 2
		}
		if i >= len(done) {
			return SlotMethod, session, prefix
		}
		return SlotTarget, session, prefix
	case "do":
		if len(done) == 1 {
			return SlotAction, "", prefix
		}
		if _, v, ok := strings.Cut(prefix, "="); ok {
			prefix = v
		}
		return SlotTarget, "", prefix
	}
	return SlotNone, "", prefix
}
