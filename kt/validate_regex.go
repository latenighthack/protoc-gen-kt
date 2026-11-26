package kt

import (
	"fmt"
	"regexp/syntax"
	"strings"
	"unicode"
)

// compileRE2ToKotlin validates an RE2 pattern with Go's own parser (the engine protovalidate
// uses) and re-emits it from the syntax tree in a dialect whose meaning is identical in
// java.util.regex, JavaScript (with the "u" flag) and Kotlin/Native.
//
// This is an allowlist: only constructs listed below are accepted, everything else is an error.
//
//   - literals, concatenation, alternation, groups (emitted non-capturing; there are no backreferences in RE2)
//   - greedy and lazy repetition (*, +, ?, {n}, {n,}, {n,m}); a repeat containing a variable-width
//     repeat is rejected because backtracking engines can take exponential time where RE2 cannot
//   - character classes made of explicit ranges (including POSIX [[:alpha:]] and \d \w, which are
//     ASCII-only everywhere), emitted as explicit code points. Classes reaching beyond U+FFFF
//     are accepted only when the complement is a BMP-only set (emitted negated)
//   - ^ and $ without (?m): ^ is start of input, $ is end of input (RE2 $ never matches before a
//     trailing newline, unlike Java), emitted as a lookahead so all engines agree
//   - . is emitted as [^\n]; (?s) dot as [\s\S]
//   - case folding (?i) is expanded to explicit classes using unicode.SimpleFold
//
// Rejected: \b \B (Unicode-dependent word boundary), \s \S (differs between engines),
// \pN / \p{..} / \PN (Unicode tables differ between engines), \C, (?m) line anchors.
func compileRE2ToKotlin(pattern string) (string, error) {
	if err := scanEscapes(pattern); err != nil {
		return "", err
	}
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return "", fmt.Errorf("not valid RE2: %v", err)
	}
	if _, err := syntax.Compile(re.Simplify()); err != nil {
		return "", fmt.Errorf("not valid RE2: %v", err)
	}
	var sb strings.Builder
	if err := emitRegex(&sb, re); err != nil {
		return "", err
	}
	return sb.String(), nil
}

func scanEscapes(p string) error {
	inClass := false
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '\\' && i+1 < len(p):
			n := p[i+1]
			switch n {
			case 'p', 'P':
				return fmt.Errorf("Unicode classes (\\%c) differ between engines", n)
			case 'C':
				return fmt.Errorf("\\C is not portable")
			case 'b', 'B':
				if !inClass {
					return fmt.Errorf("\\%c word boundaries differ between engines", n)
				}
			case 's', 'S':
				return fmt.Errorf("\\%c differs between engines", n)
			}
			i++
		case c == '[':
			inClass = true
		case c == ']':
			inClass = false
		}
	}
	return nil
}

func variableRepeat(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpStar, syntax.OpPlus, syntax.OpQuest:
		return true
	case syntax.OpRepeat:
		return re.Min != re.Max
	}
	for _, s := range re.Sub {
		if variableRepeat(s) {
			return true
		}
	}
	return false
}

func emitRegex(sb *strings.Builder, re *syntax.Regexp) error {
	switch re.Op {
	case syntax.OpNoMatch:
		sb.WriteString(`[^\s\S]`)
	case syntax.OpEmptyMatch:
		sb.WriteString(`(?:)`)
	case syntax.OpLiteral:
		for _, r := range re.Rune {
			if re.Flags&syntax.FoldCase != 0 {
				writeClass(sb, foldRanges(r), false)
			} else {
				writeLiteral(sb, r)
			}
		}
	case syntax.OpCharClass:
		return emitClass(sb, re.Rune)
	case syntax.OpAnyCharNotNL:
		sb.WriteString(`[^\n]`)
	case syntax.OpAnyChar:
		sb.WriteString(`[\s\S]`)
	case syntax.OpBeginText:
		sb.WriteString(`^`)
	case syntax.OpEndText:
		sb.WriteString(`(?![\s\S])`)
	case syntax.OpBeginLine, syntax.OpEndLine:
		return fmt.Errorf("multi-line anchors (?m) are not portable")
	case syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return fmt.Errorf("word boundaries differ between engines")
	case syntax.OpCapture:
		sb.WriteString(`(?:`)
		if err := emitRegex(sb, re.Sub[0]); err != nil {
			return err
		}
		sb.WriteString(`)`)
	case syntax.OpStar, syntax.OpPlus, syntax.OpQuest, syntax.OpRepeat:
		if variableRepeat(re.Sub[0]) {
			return fmt.Errorf("nested variable repetition can backtrack exponentially")
		}
		sb.WriteString(`(?:`)
		if err := emitRegex(sb, re.Sub[0]); err != nil {
			return err
		}
		sb.WriteString(`)`)
		var q string
		switch re.Op {
		case syntax.OpStar:
			q = "*"
		case syntax.OpPlus:
			q = "+"
		case syntax.OpQuest:
			q = "?"
		default:
			switch {
			case re.Max == -1:
				q = fmt.Sprintf("{%d,}", re.Min)
			case re.Min == re.Max:
				q = fmt.Sprintf("{%d}", re.Min)
			default:
				q = fmt.Sprintf("{%d,%d}", re.Min, re.Max)
			}
		}
		sb.WriteString(q)
		if re.Flags&syntax.NonGreedy != 0 {
			sb.WriteString("?")
		}
	case syntax.OpConcat:
		for _, s := range re.Sub {
			if s.Op == syntax.OpAlternate {
				sb.WriteString(`(?:`)
				if err := emitRegex(sb, s); err != nil {
					return err
				}
				sb.WriteString(`)`)
			} else if err := emitRegex(sb, s); err != nil {
				return err
			}
		}
	case syntax.OpAlternate:
		for i, s := range re.Sub {
			if i > 0 {
				sb.WriteString("|")
			}
			if err := emitRegex(sb, s); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported construct %v", re.Op)
	}
	return nil
}

func writeLiteral(sb *strings.Builder, r rune) {
	switch {
	case strings.ContainsRune(`\.+*?()|[]{}^$/`, r):
		sb.WriteByte('\\')
		sb.WriteRune(r)
	case r == '\n':
		sb.WriteString(`\n`)
	case r == '\r':
		sb.WriteString(`\r`)
	case r == '\t':
		sb.WriteString(`\t`)
	case r < 0x20 || r == 0x7f:
		fmt.Fprintf(sb, `\u%04x`, r)
	default:
		sb.WriteRune(r)
	}
}

func classChar(sb *strings.Builder, r rune) {
	if strings.ContainsRune(`\]^-[`, r) {
		sb.WriteByte('\\')
		sb.WriteRune(r)
		return
	}
	switch r {
	case '\n':
		sb.WriteString(`\n`)
	case '\r':
		sb.WriteString(`\r`)
	case '\t':
		sb.WriteString(`\t`)
	default:
		if r < 0x20 || r == 0x7f {
			fmt.Fprintf(sb, `\u%04x`, r)
		} else {
			sb.WriteRune(r)
		}
	}
}

func foldRanges(r rune) []rune {
	out := []rune{r, r}
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		out = append(out, f, f)
	}
	return out
}

func writeClass(sb *strings.Builder, ranges []rune, negate bool) {
	sb.WriteByte('[')
	if negate {
		sb.WriteByte('^')
	}
	for i := 0; i < len(ranges); i += 2 {
		lo, hi := ranges[i], ranges[i+1]
		classChar(sb, lo)
		if hi != lo {
			sb.WriteByte('-')
			classChar(sb, hi)
		}
	}
	sb.WriteByte(']')
}

func emitClass(sb *strings.Builder, ranges []rune) error {
	if len(ranges) == 0 {
		sb.WriteString(`[^\s\S]`)
		return nil
	}
	if ranges[len(ranges)-1] <= 0xffff {
		writeClass(sb, ranges, false)
		return nil
	}
	// Complement within [0, 0x10FFFF] must be BMP-only.
	var comp []rune
	next := rune(0)
	for i := 0; i < len(ranges); i += 2 {
		if ranges[i] > next {
			comp = append(comp, next, ranges[i]-1)
		}
		next = ranges[i+1] + 1
	}
	if next <= unicode.MaxRune {
		comp = append(comp, next, unicode.MaxRune)
	}
	if len(comp) == 0 || comp[len(comp)-1] > 0xffff {
		return fmt.Errorf("character classes spanning supplementary code points are not portable")
	}
	writeClass(sb, comp, true)
	return nil
}
