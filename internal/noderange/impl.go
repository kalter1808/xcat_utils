package noderange

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const recursionLimit = 4096

type noderangeCtx struct {
	prefix     string
	suffix     string
	knownNodes map[string]bool // optional: nodes considered valid (for regex + verification)
	depth      int
}

func expandOpts(rangeExpr string, known map[string]bool) (nodes []string, missed []string) {
	prefix := os.Getenv("XCAT_NODE_PREFIX")
	if prefix == "" {
		prefix = "node"
	}
	suffix := os.Getenv("XCAT_NODE_SUFFIX")
	ctx := &noderangeCtx{prefix: prefix, suffix: suffix, knownNodes: known}
	result, missedAtoms, err := ctx.noderange(stripQuotes(rangeExpr))
	if err != nil {
		return nil, []string{rangeExpr}
	}
	return result, missedAtoms
}

func stripQuotes(s string) string {
	s = strings.ReplaceAll(s, "'", "")
	return strings.ReplaceAll(s, "\"", "")
}

// noderange is the top-level parser (NodeRange.pm noderange(), lines 624-758).
func (c *noderangeCtx) noderange(expr string) ([]string, []string, error) {
	c.depth++
	defer func() { c.depth-- }()
	if c.depth > recursionLimit {
		return nil, nil, fmt.Errorf("noderange recursion limit exceeded")
	}

	result := map[string]bool{}
	var missedAtoms []string
	deferredExcludes := []string{}
	intersectPending := false

	var elements []string
	var err error
	// Split on commas, respecting [] and () nesting (NodeRange.pm:667).
	elements, err = splitTopLevel(expr)
	if err != nil {
		return nil, nil, err
	}
	// If there is a single element containing '@', split on it so that
	// "group1@group2" works without commas (NodeRange.pm:668-670).
	if len(elements) == 1 && strings.Contains(elements[0], "@") && !strings.HasPrefix(elements[0], "@") {
		parts := strings.Split(elements[0], "@")
		elements = []string{}
		for i, p := range parts {
			if i > 0 {
				elements = append(elements, "@"+p)
			} else {
				elements = append(elements, p)
			}
		}
	}

	for _, element := range elements {
		element = strings.TrimSpace(element)
		if element == "" {
			continue
		}
		switch {
		case strings.HasPrefix(element, "^"): // file inclusion (^/path, NodeRange.pm:686-705)
			fileNodes, fileMissed, ferr := c.expandFile(strings.TrimPrefix(element, "^"))
			if ferr == nil {
				if intersectPending {
					result = intersectMaps(result, fileNodes)
					intersectPending = false
				} else {
					for n := range fileNodes {
						result[n] = true
					}
				}
				missedAtoms = append(missedAtoms, fileMissed...)
			}
		case strings.HasPrefix(element, "-"): // exclusion (delayed, NodeRange.pm:677-679)
			sub := strings.TrimSpace(strings.TrimPrefix(element, "-"))
			if strings.HasPrefix(sub, "(") { // -(...) subtracts a grouped range
				inner, im, ierr := c.parenRange(sub)
				if ierr == nil {
					deferredExcludes = append(deferredExcludes, inner...)
					missedAtoms = append(missedAtoms, im...)
				}
				continue
			}
			exNodes, exMissed, exErr := c.expandAtomList(sub)
			if exErr == nil {
				for n := range exNodes {
					deferredExcludes = append(deferredExcludes, n)
				}
				missedAtoms = append(missedAtoms, exMissed...)
			}
		case strings.HasPrefix(element, "@"): // intersection (NodeRange.pm:669,680-682)
			sub := strings.TrimSpace(strings.TrimPrefix(element, "@"))
			if strings.HasPrefix(sub, "(") {
				inner, im, ierr := c.parenRange(sub)
				if ierr == nil {
					result = intersectMaps(result, sliceToSet(inner))
					missedAtoms = append(missedAtoms, im...)
				}
				continue
			}
			inNodes, inMissed, inErr := c.expandAtomList(sub)
			if inErr == nil {
				result = intersectMaps(result, inNodes)
				missedAtoms = append(missedAtoms, inMissed...)
			}
			intersectPending = false
		default:
			if strings.HasPrefix(element, "(") { // (x) unions a grouped range
				inner, im, ierr := c.parenRange(element)
				if ierr != nil {
					return nil, nil, ierr
				}
				if intersectPending {
					result = intersectMaps(result, sliceToSet(inner))
					intersectPending = false
				} else {
					for _, n := range inner {
						result[n] = true
					}
				}
				missedAtoms = append(missedAtoms, im...)
				continue
			}
			atoms := strings.Split(element, "@")
			atomNodes, aMissed, aErr := c.expandAtomList(atoms[0])
			if aErr != nil {
				return nil, nil, aErr
			}
			if intersectPending {
				result = intersectMaps(result, atomNodes)
				intersectPending = false
			} else {
				for n := range atomNodes {
					result[n] = true
				}
			}
			// a@b@... within one comma element: successive intersections
			for _, extra := range atoms[1:] {
				extraNodes, eMissed, eErr := c.expandAtomList(strings.TrimSpace(extra))
				if eErr == nil {
					result = intersectMaps(result, extraNodes)
					missedAtoms = append(missedAtoms, eMissed...)
				}
			}
			missedAtoms = append(missedAtoms, aMissed...)
			if len(atoms) > 1 {
				intersectPending = true
			}
		}
	}

	// Deferred exclusion: always wins over inclusion (NodeRange.pm:747-752).
	for _, ex := range deferredExcludes {
		delete(result, ex)
	}

	out := make([]string, 0, len(result))
	for n := range result {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, missedAtoms, nil
}

func sliceToSet(s []string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		m[v] = true
	}
	return m
}

// parenRange expands "(...)" content as a full noderange
// (NodeRange.pm:646-664).
func (c *noderangeCtx) parenRange(element string) ([]string, []string, error) {
	if !strings.HasSuffix(element, ")") {
		return nil, nil, fmt.Errorf("unbalanced parentheses in noderange")
	}
	inner := strings.TrimSpace(element[1 : len(element)-1])
	return c.noderange(inner)
}

// expandAtomList expands one comma-free atom expression, handling a single
// parenthesized group or plain atoms.
func (c *noderangeCtx) expandAtomList(atom string) (map[string]bool, []string, error) {
	result := map[string]bool{}
	var missedAtoms []string

	if strings.HasPrefix(atom, "(") {
		inner, im, err := c.parenRange(atom)
		if err != nil {
			return nil, nil, err
		}
		for _, n := range inner {
			result[n] = true
		}
		return result, im, nil
	}

	nodes, missed, err := c.expandAtom(atom)
	if err != nil {
		return nil, nil, err
	}
	for _, n := range nodes {
		result[n] = true
	}
	missedAtoms = append(missedAtoms, missed...)
	return result, missedAtoms, nil
}

// expandAtom is the atom-level expander (NodeRange.pm expandatom(), 211-498).
func (c *noderangeCtx) expandAtom(atom string) ([]string, []string, error) {
	atom = strings.TrimSpace(atom)
	if atom == "" {
		return nil, nil, nil
	}
	var missed []string

	// Parenthesized sub-noderange (NodeRange.pm:234-238).
	if strings.HasPrefix(atom, "(") {
		return c.parenRange(atom)
	}

	// Atom containing '@': recurse into noderange for intersection
	// (NodeRange.pm:239-242).
	if strings.Contains(atom, "@") {
		return c.noderange(atom)
	}

	// Regex atom: /pattern (NodeRange.pm:342-355). Without a known-node set
	// the regex cannot select anything; return it unchanged (genericrange
	// semantics, NodeRange.pm:343-345).
	if strings.HasPrefix(atom, "/") {
		pattern := strings.TrimPrefix(atom, "/")
		if c.knownNodes != nil && len(c.knownNodes) > 0 {
			re, err := regexp.Compile("^" + pattern + "$")
			if err != nil {
				return nil, []string{atom}, nil
			}
			var matched []string
			for n := range c.knownNodes {
				if re.MatchString(n) {
					matched = append(matched, n)
				}
			}
			sort.Strings(matched)
			return matched, nil, nil
		}
		return []string{atom}, nil, nil
	}

	// Bracket syntax: prefix[...]suffix (NodeRange.pm:357-386).
	if loc := bracketRe.FindStringSubmatchIndex(atom); loc != nil {
		prefix := atom[loc[2]:loc[3]]
		inside := atom[loc[4]:loc[5]]
		suffix := atom[loc[6]:loc[7]]
		moreBrackets := bracketRe.MatchString(suffix)
		var expanded []string
		// Each comma item inside brackets produces "prefix<lo>-prefix<hi>"
		// (lowered to hyphen-range form); the suffix is appended only when
		// there are no further bracket groups, mirroring NodeRange.pm:375.
		for _, item := range splitCommasRespectingNesting(inside) {
			item = strings.TrimSpace(item)
			lo, hi := item, item
			if idx := strings.IndexAny(item, "-:"); idx >= 0 {
				lo, hi = strings.TrimSpace(item[:idx]), strings.TrimSpace(item[idx+1:])
			}
			joined := prefix + lo + "-" + prefix + hi
			if !moreBrackets {
				joined += suffix
			}
			sub, _, err := c.expandAtom(joined)
			if err != nil {
				return nil, nil, err
			}
			if moreBrackets {
				for _, n := range sub {
					rec, _, err := c.expandAtom(n+suffix)
					if err != nil {
						return nil, nil, err
					}
					expanded = append(expanded, rec...)
				}
			} else {
				expanded = append(expanded, sub...)
			}
		}
		return expanded, nil, nil
	}

	// Plus/increment: node10+3 => node10..node13 (NodeRange.pm:388-407).
	if m := plusRe.FindStringSubmatch(atom); m != nil {
		p, numStr, sfx, incStr := m[1], m[2], m[3], m[4]
		inc, err1 := strconv.Atoi(incStr)
		start, err2 := strconv.Atoi(numStr)
		if err1 == nil && err2 == nil && inc > 0 {
			if p == "" && sfx == "" {
				p = c.prefix
				sfx = c.suffix
			}
			var out []string
			pad := len(numStr)
			for i := start; i <= start+inc; i++ {
				out = append(out, fmt.Sprintf("%s%0*d%s", p, pad, i, sfx))
			}
			return out, nil, nil
		}
	}

	// Pure-number shorthand: "10-15" => "node10-node15" (NodeRange.pm:333-336).
	// Note: only an entirely-numeric atom gets the prefix; a numeric range
	// like "10-12" is not all digits, so the prefix is injected into each
	// side inside the hyphen/colon range expander instead.
	if isAllDigits(atom) {
		return c.expandAtom(c.prefix + atom + c.suffix)
	}

	// Hyphen or colon range: node1-node200 / node1:node200
	// (NodeRange.pm:409-490).
	if nodes, ok, err := c.expandHyphenColonRange(atom); err != nil {
		return nil, nil, err
	} else if ok {
		return nodes, nil, nil
	}

	// Plain nodename.
	return []string{atom}, missed, nil
}

var (
	bracketRe = regexp.MustCompile(`^(.+?)\[(.+?)\](.*)$`)
	plusRe    = regexp.MustCompile(`^(.*?)(\d+)(\..+)?\+(\d+)$`)
)

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// expandHyphenColonRange implements NodeRange.pm:409-490:
// exactly one numeric component may differ between the two sides; all other
// components must be identical. Zero padding is preserved via numeric
// formatting to the width of the operands.
func (c *noderangeCtx) expandHyphenColonRange(atom string) ([]string, bool, error) {
	var left, right string
	if strings.Contains(atom, ":") {
		parts := strings.Split(atom, ":")
		if len(parts) != 2 {
			return nil, false, nil
		}
		left, right = parts[0], parts[1]
	} else if strings.Contains(atom, "-") {
		// Odd number of '-' is required so names containing hyphens work
		// (NodeRange.pm:415-423).
		if strings.Count(atom, "-")%2 == 0 {
			return nil, false, nil
		}
		// Split in the middle of the hyphens: half the hyphens belong to
		// the left name, half to the right (mirrors the constructed regex
		// in NodeRange.pm:424).
		n := strings.Count(atom, "-")
		hyphens := (n - 1) / 2
		idx := -1
		seen := 0
		for i := 0; i < len(atom); i++ {
			if atom[i] == '-' {
				seen++
				if seen == hyphens+1 {
					idx = i
					break
				}
			}
		}
		left = atom[:idx]
		right = atom[idx+1:]
	} else {
		return nil, false, nil
	}

	// Numeric shorthand inside a range: "10-12" means "node10-node12"
	// (implicit XCAT_NODE_PREFIX/SUFFIX on both sides).
	if isAllDigits(left) && isAllDigits(right) {
		left = c.prefix + left + c.suffix
		right = c.prefix + right + c.suffix
	}

	// node1-node1 degenerates to plain expansion (NodeRange.pm:429-431).
	if left == right {
		nodes, _, err := c.expandAtom(left)
		return nodes, true, err
	}

	leftParts := splitDigits(left)
	rightParts := splitDigits(right)
	if len(leftParts) != len(rightParts) {
		return nil, false, nil
	}

	// Find the differing numeric component (exactly one allowed).
	diffIdx := -1
	for i := range leftParts {
		if leftParts[i] != rightParts[i] {
			if diffIdx >= 0 {
				return nil, false, nil // more than one difference
			}
			if !isNumeric(leftParts[i]) || !isNumeric(rightParts[i]) {
				return nil, false, nil // differing part must be numeric
			}
			diffIdx = i
		}
	}
	if diffIdx < 0 {
		nodes, _, err := c.expandAtom(left) // identical modulo padding
		return nodes, true, err
	}

	lo, err1 := strconv.Atoi(leftParts[diffIdx])
	hi, err2 := strconv.Atoi(rightParts[diffIdx])
	if err1 != nil || err2 != nil {
		return nil, false, nil
	}
	if lo > hi {
		return nil, false, nil
	}

	pad := len(leftParts[diffIdx])
	if len(rightParts[diffIdx]) > pad {
		pad = len(rightParts[diffIdx])
	}
	var out []string
	for i := lo; i <= hi; i++ {
		// Perl magic string increment: the width follows the operand
		// ("01".."05" stays 2 digits; "1".."10" widens). Match by
		// formatting to the operand width and widening only if needed.
		s := fmt.Sprintf("%0*d", len(leftParts[diffIdx]), i)
		if len(s) < pad {
			s = fmt.Sprintf("%0*d", pad, i)
		}
		prefix := strings.Join(leftParts[:diffIdx], "")
		suffix := strings.Join(leftParts[diffIdx+1:], "")
		out = append(out, prefix+s+suffix)
	}
	return out, true, nil
}

// splitDigits splits a string into digit/non-digit components
// (Perl split(/(\d+)/) semantics, NodeRange.pm:432-433).
func splitDigits(s string) []string {
	if s == "" {
		return []string{""}
	}
	var parts []string
	cur := strings.Builder{}
	curDigit := -1 // -1 unknown, 0 non-digit run, 1 digit run
	for _, r := range s {
		isD := r >= '0' && r <= '9'
		if curDigit == -1 {
			curDigit = boolToInt(isD)
			cur.WriteRune(r)
			continue
		}
		if boolToInt(isD) != curDigit {
			parts = append(parts, cur.String())
			cur.Reset()
			curDigit = boolToInt(isD)
		}
		cur.WriteRune(r)
	}
	parts = append(parts, cur.String())
	return parts
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isNumeric(s string) bool {
	return isAllDigits(s)
}

// splitTopLevel splits on commas that are not inside [] or ()
// (NodeRange.pm:667).
func splitTopLevel(expr string) ([]string, error) {
	var elements []string
	depthSq, depthPar := 0, 0
	balancedPar := 0
	cur := strings.Builder{}
	for _, r := range expr {
		switch r {
		case '[':
			depthSq++
		case ']':
			depthSq--
		case '(':
			depthPar++
			balancedPar++
		case ')':
			depthPar--
			balancedPar--
		case ',':
			if depthSq <= 0 && depthPar <= 0 {
				elements = append(elements, cur.String())
				cur.Reset()
				continue
			}
		}
		cur.WriteRune(r)
	}
	if balancedPar != 0 {
		return nil, fmt.Errorf("Unbalanced parentheses in noderange")
	}
	elements = append(elements, cur.String())
	return elements, nil
}

func splitCommasRespectingNesting(s string) []string {
	var out []string
	cur := strings.Builder{}
	for _, r := range s {
		if r == ',' {
			out = append(out, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteRune(r)
	}
	if cur.Len() > 0 || len(out) == 0 {
		out = append(out, cur.String())
	}
	return out
}

// expandFile reads a noderange file: each line is a noderange; '#'/'^'
// lines ignored; only the first whitespace/colon-delimited token is used
// (NodeRange.pm:686-705).
func (c *noderangeCtx) expandFile(path string) (map[string]bool, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err // nonexistent file is silently skipped by caller check
	}
	defer f.Close()

	result := map[string]bool{}
	var missed []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		// First token up to whitespace or colon.
		end := len(line)
		for i := 0; i < len(line); i++ {
			if line[i] == ' ' || line[i] == '\t' || line[i] == ':' {
				end = i
				break
			}
		}
		token := line[:end]
		if token == "" {
			continue
		}
		nodes, m, err := c.noderange(token)
		if err != nil {
			continue
		}
		for _, n := range nodes {
			result[n] = true
		}
		missed = append(missed, m...)
	}
	return result, missed, nil
}

func intersectMaps(a, b map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		if b[k] {
			out[k] = true
		}
	}
	return out
}
