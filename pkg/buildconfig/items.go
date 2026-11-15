package buildconfig

import "strings"

// Item is one logical item of prjconf text: a Macros: block, or one line
// after the parser's own continuation handling (backslash-continued %define
// lines and multi-line %{?x: ... } conditions are joined). Importers use it
// to tokenize a prjconf exactly like Parse does, without evaluating it.
type Item struct {
	// Raw is the original text of the item, physical lines joined by "\n".
	Raw string
	// Text is the logical line as the parser sees it. Empty for Macros items.
	Text string
	// Body is the verbatim body of a Macros: block, each line terminated by
	// "\n". Empty for other items.
	Body string
	// Macros reports whether the item is a Macros: ... :Macros block.
	Macros bool
	// Line is the 1-based number of the first physical line.
	Line int
}

// Items splits prjconf text into logical items.
func Items(text string) []Item {
	lines := splitLines(text)
	p := &specParser{}
	var out []Item
	var pending []string
	first := 0
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if len(pending) == 0 && macrosOpenRe.MatchString(l) {
			it, next := macrosItem(lines, i)
			out = append(out, it)
			i = next
			continue
		}
		if len(pending) == 0 {
			first = i + 1
		}
		pending = append(pending, l)
		joined, ok := p.joinContinuations(l)
		if !ok {
			continue
		}
		out = append(out, Item{Raw: strings.Join(pending, "\n"), Text: joined, Line: first})
		pending = nil
	}
	if len(pending) > 0 {
		raw := strings.Join(pending, "\n")
		out = append(out, Item{Raw: raw, Text: raw, Line: first})
	}
	return out
}

// macrosItem collects the Macros: block starting at lines[start] and returns
// it with the index of its last physical line.
func macrosItem(lines []string, start int) (Item, int) {
	raw := []string{lines[start]}
	var body strings.Builder
	i := start + 1
	for ; i < len(lines); i++ {
		raw = append(raw, lines[i])
		if macrosCloseRe.MatchString(lines[i]) {
			break
		}
		body.WriteString(lines[i] + "\n")
	}
	return Item{Raw: strings.Join(raw, "\n"), Body: body.String(), Macros: true, Line: start + 1}, i
}
