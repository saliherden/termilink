package agent

import (
	"regexp"
	"strings"
)

// ctrlRe matches a Telegram message that encodes a single control character in
// terminal notation, e.g. `^p` for Ctrl+P (the opencode command palette).
var ctrlRe = regexp.MustCompile(`^\^([a-zA-Z^])$`)

var keyMappings = map[string]string{
	"/up":        "\x1b[A",
	"/down":      "\x1b[B",
	"/right":     "\x1b[C",
	"/left":      "\x1b[D",
	"/esc":       "\x1b",
	"/tab":       "\t",
	"/backspace": "\x7f",
	"/enter":     "\r",
	"/home":      "\x1b[H",
	"/end":       "\x1b[F",
	"/pageup":    "\x1b[5~",
	"/pagedown":  "\x1b[6~",
	"/delete":    "\x1b[3~",
	"/insert":    "\x1b[2~",
	"\u2191":     "\x1b[A", // ↑
	"\u2193":     "\x1b[B", // ↓
	"\u2192":     "\x1b[C", // →
	"\u2190":     "\x1b[D", // ←
	"up":         "\x1b[A",
	"down":       "\x1b[B",
	"left":       "\x1b[C",
	"right":      "\x1b[D",
	"esc":        "\x1b",
	"tab":        "\t",
	"enter":      "\r",
	"home":       "\x1b[H",
	"end":        "\x1b[F",
	"pageup":     "\x1b[5~",
	"pagedown":   "\x1b[6~",
	"delete":     "\x1b[3~",
	"insert":     "\x1b[2~",
}

// MapInput converts a Telegram message into raw bytes for the TUI and a
// human-readable label for auditing. Plain text becomes "text\r" (typed into
// the agent's input box, then Enter). Control and special keys map to their
// terminal encodings, and multi-key chords like `^x l` (Ctrl+X then l) are
// sent as the joined raw bytes without Enter. isSpecial is true for
// control/special-key inputs.
func MapInput(text string) ([]byte, string, bool) {
	t := strings.TrimSpace(text)
	if m := ctrlRe.FindStringSubmatch(t); m != nil {
		return []byte{m[1][0] & 0x1f}, "^" + m[1], true
	}
	if raw, ok := keyMappings[t]; ok {
		return []byte(raw), t, true
	}
	if tokens := strings.Fields(t); len(tokens) > 1 && isChord(tokens) {
		var joined []byte
		for _, tok := range tokens {
			joined = append(joined, keyBytes(tok)...)
		}
		return joined, strings.Join(tokens, " "), true
	}
	return []byte(t + "\r"), t, false
}

// isChord reports whether tokens look like a deliberate key chord (e.g.
// "^p enter" or "^x l") rather than normal text: the first token is a
// control/special key and every later token is either a single character or
// another key.
func isChord(tokens []string) bool {
	if !isKeyToken(tokens[0]) {
		return false
	}
	for _, tok := range tokens[1:] {
		if len(tok) == 1 {
			continue
		}
		if !isKeyToken(tok) {
			return false
		}
	}
	return true
}

func isKeyToken(tok string) bool {
	if ctrlRe.MatchString(tok) {
		return true
	}
	_, ok := keyMappings[tok]
	return ok
}

func keyBytes(tok string) []byte {
	if m := ctrlRe.FindStringSubmatch(tok); m != nil {
		return []byte{m[1][0] & 0x1f}
	}
	if raw, ok := keyMappings[tok]; ok {
		return []byte(raw)
	}
	return []byte(tok)
}
