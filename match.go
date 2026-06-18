package codeowners

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

type pattern struct {
	pattern             string
	regex               *regexp.Regexp
	regexPrefix         string
	leftAnchoredLiteral bool
}

// newPattern creates a new pattern struct from a gitignore-style pattern string
func newPattern(patternStr string) (pattern, error) {
	pat := pattern{pattern: patternStr}

	if !strings.ContainsAny(patternStr, "*?\\") && patternStr[0] == '/' {
		pat.leftAnchoredLiteral = true
	} else {
		patternRegex, err := buildPatternRegex(patternStr)
		if err != nil {
			return pattern{}, err
		}
		pat.regex = patternRegex
		// Any match must begin with this literal prefix, so we can cheaply
		// reject non-matching paths with a string comparison before paying for
		// a full (backtracking) regex evaluation.
		pat.regexPrefix = literalPrefix(patternStr)
	}

	return pat, nil
}

// literalPrefix returns the leading literal path text that any matching path
// must start with, or "" if no such prefix can be guaranteed. It's used as a
// cheap pre-filter to avoid running the regex against paths that can't match.
//
// We compute it from the pattern string rather than via regexp.LiteralPrefix
// because our regexes are anchored with \A, which that method treats as a
// non-literal instruction and bails out on, yielding an empty prefix.
func literalPrefix(patternStr string) string {
	// Only root-anchored patterns (those with a leading slash) constrain the
	// start of the path. Patterns without one match relative to any directory.
	if patternStr[0] != '/' {
		return ""
	}
	// Everything up to the first wildcard or escape is literal path text that a
	// matching path must contain as a prefix. Stopping at '\' keeps us safe
	// around escaped wildcards without having to interpret the escape.
	s := patternStr[1:]
	if i := strings.IndexAny(s, "*?\\"); i >= 0 {
		s = s[:i]
	}
	return s
}

// match tests if the path provided matches the pattern
func (p pattern) match(testPath string) (bool, error) {
	// Normalize Windows-style path separators to forward slashes
	testPath = filepath.ToSlash(testPath)

	if p.leftAnchoredLiteral {
		prefix := p.pattern

		// Strip the leading slash as we're anchored to the root already
		if prefix[0] == '/' {
			prefix = prefix[1:]
		}

		// If the pattern ends with a slash we can do a simple prefix match
		if prefix[len(prefix)-1] == '/' {
			return strings.HasPrefix(testPath, prefix), nil
		}

		// If the strings are the same length, check for an exact match
		if len(testPath) == len(prefix) {
			return testPath == prefix, nil
		}

		// Otherwise check if the test path is a subdirectory of the pattern
		if len(testPath) > len(prefix) && testPath[len(prefix)] == '/' {
			return testPath[:len(prefix)] == prefix, nil
		}

		// Otherwise the test path must be shorter than the pattern, so it can't match
		return false, nil
	}

	// Cheap rejection: if the regex requires a literal prefix the path doesn't
	// have, it cannot match, so skip the expensive regex evaluation entirely.
	if p.regexPrefix != "" && !strings.HasPrefix(testPath, p.regexPrefix) {
		return false, nil
	}

	return p.regex.MatchString(testPath), nil
}

// buildPatternRegex compiles a new regexp object from a gitignore-style pattern string
func buildPatternRegex(pattern string) (*regexp.Regexp, error) {
	// Handle specific edge cases first
	switch {
	case strings.Contains(pattern, "***"):
		return nil, fmt.Errorf("pattern cannot contain three consecutive asterisks")
	case pattern == "":
		return nil, fmt.Errorf("empty pattern")
	case pattern == "/":
		// "/" doesn't match anything
		return regexp.Compile(`\A\z`)
	}

	segs := strings.Split(pattern, "/")

	if segs[0] == "" {
		// Leading slash: match is relative to root
		segs = segs[1:]
	} else {
		// No leading slash - check for a single segment pattern, which matches
		// relative to any descendent path (equivalent to a leading **/)
		if len(segs) == 1 || (len(segs) == 2 && segs[1] == "") {
			if segs[0] != "**" {
				segs = append([]string{"**"}, segs...)
			}
		}
	}

	if len(segs) > 1 && segs[len(segs)-1] == "" {
		// Trailing slash is equivalent to "/**"
		segs[len(segs)-1] = "**"
	}

	sep := "/"

	lastSegIndex := len(segs) - 1
	needSlash := false
	var re strings.Builder
	re.WriteString(`\A`)
	for i, seg := range segs {
		switch seg {
		case "**":
			switch {
			case i == 0 && i == lastSegIndex:
				// If the pattern is just "**" we match everything
				re.WriteString(`.+`)
			case i == 0:
				// If the pattern starts with "**" we match any leading path segment
				re.WriteString(`(?:.+` + sep + `)?`)
				needSlash = false
			case i == lastSegIndex:
				// If the pattern ends with "**" we match any trailing path segment
				re.WriteString(sep + `.*`)
			default:
				// If the pattern contains "**" we match zero or more path segments
				re.WriteString(`(?:` + sep + `.+)?`)
				needSlash = true
			}

		case "*":
			if needSlash {
				re.WriteString(sep)
			}

			// Regular wildcard - match any characters except the separator
			re.WriteString(`[^` + sep + `]+`)
			needSlash = true

		default:
			if needSlash {
				re.WriteString(sep)
			}

			escape := false
			for _, ch := range seg {
				if escape {
					escape = false
					re.WriteString(regexp.QuoteMeta(string(ch)))
					continue
				}

				// Other pathspec implementations handle character classes here (e.g.
				// [AaBb]), but CODEOWNERS doesn't support that so we don't need to
				switch ch {
				case '\\':
					escape = true
				case '*':
					// Multi-character wildcard
					re.WriteString(`[^` + sep + `]*`)
				case '?':
					// Single-character wildcard
					re.WriteString(`[^` + sep + `]`)
				default:
					// Regular character
					re.WriteString(regexp.QuoteMeta(string(ch)))
				}
			}

			if i == lastSegIndex {
				// As there's no trailing slash (that'd hit the '**' case), we
				// need to match descendent paths
				re.WriteString(`(?:` + sep + `.*)?`)
			}

			needSlash = true
		}
	}
	re.WriteString(`\z`)
	return regexp.Compile(re.String())
}
