package document

import "strings"

// symbolChars maps the characters of the symbol fonts Word uses for bullets
// and w:sym to Unicode stand-ins, keyed by lowercased font name and the
// character's code in the font's private-use range (U+F020-U+F0FF). It is for
// when the font itself is not available: each stand-in is one the
// replacement font has. A character with no stand-in is left as it is.
var symbolChars = map[string]map[rune]string{
	"symbol":    symbolFontChars(),
	"wingdings": wingdingsChars,
}

// wingdingsChars are the Wingdings pictograms Word documents use most - list
// bullets and check boxes. A checked box has no stand-in in the replacement
// font and is drawn filled.
var wingdingsChars = map[rune]string{
	0xF06C: "●",
	0xF06E: "■",
	0xF06F: "□",
	0xF071: "□", // ❑
	0xF076: "♦", // ❖
	0xF0A7: "▪",
	0xF0A8: "□", // ◻, the usual empty check box
	0xF0D8: "►", // ➢
	0xF0E8: "→", // ➔
	0xF0FB: "×", // ✗
	0xF0FC: "√", // ✓
	0xF0FD: "■", // ☒
	0xF0FE: "■", // ☑
}

// symbolFontChars builds the Symbol font's map: Greek letters on the Latin
// keys, ASCII digits and punctuation in their places, and the math signs and
// arrows above them.
func symbolFontChars() map[rune]string {
	m := map[rune]string{
		0xF02A: "*", // ∗
		0xF02D: "−",
		0xF0A3: "≤",
		0xF0A5: "∞",
		0xF0A7: "♣",
		0xF0A8: "♦",
		0xF0A9: "♥",
		0xF0AA: "♠",
		0xF0AB: "↔",
		0xF0AC: "←",
		0xF0AD: "↑",
		0xF0AE: "→",
		0xF0AF: "↓",
		0xF0B0: "°",
		0xF0B1: "±",
		0xF0B3: "≥",
		0xF0B4: "×",
		0xF0B6: "∂",
		0xF0B7: "•",
		0xF0B8: "÷",
		0xF0B9: "≠",
		0xF0BB: "≈",
		0xF0D6: "√",
		0xF0D7: "·",
		0xF0DB: "⇔",
		0xF0E5: "∑",
		0xF0F2: "∫",
	}
	// ∀, ∃ and ∋ sit on '"', '$' and '\'' and have no stand-in.
	for c := rune(0x20); c <= 0x3F; c++ {
		if _, ok := m[0xF000+c]; !ok && c != '"' && c != '$' && c != '\'' {
			m[0xF000+c] = string(c)
		}
	}
	for i, g := range []rune("ΑΒΧΔΕΦΓΗΙϑΚΛΜΝΟΠΘΡΣΤΥςΩΞΨΖ") {
		m[0xF041+rune(i)] = string(g)
	}
	for i, g := range []rune("αβχδεφγηιϕκλμνοπθρστυϖωξψζ") {
		m[0xF061+rune(i)] = string(g)
	}
	for _, c := range "[]_{|}" {
		m[0xF000+c] = string(c)
	}
	return m
}

// symbolText rewrites s, set in family, into the Unicode stand-ins of a
// symbol font's characters, for drawing in a replacement font. A character is
// looked up by its private-use code, or - as Word reads a single-byte
// character in a symbol font - by that byte. s is returned unchanged for any
// other family.
func symbolText(family, s string) string {
	chars := symbolChars[strings.ToLower(strings.TrimSpace(family))]
	if chars == nil {
		return s
	}
	var b strings.Builder
	for _, c := range s {
		code := c
		if c < 0x100 {
			code += 0xF000
		}
		if stand, ok := chars[code]; ok {
			b.WriteString(stand)
		} else {
			b.WriteRune(c)
		}
	}
	return b.String()
}
