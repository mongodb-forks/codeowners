package codeowners

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type patternTest struct {
	Name    string          `json:"name"`
	Pattern string          `json:"pattern"`
	Paths   map[string]bool `json:"paths"`
	Focus   bool            `json:"focus"`
}

func TestMatch(t *testing.T) {
	data, err := os.ReadFile("testdata/patterns.json")
	require.NoError(t, err)

	var tests []patternTest
	err = json.Unmarshal(data, &tests)
	require.NoError(t, err)

	focus := false
	for _, test := range tests {
		if test.Focus {
			focus = true
		}
	}

	for _, test := range tests {
		if test.Focus != focus {
			continue
		}

		t.Run(test.Name, func(t *testing.T) {
			for path, shouldMatch := range test.Paths {
				pattern, err := newPattern(test.Pattern)
				require.NoError(t, err)

				// Debugging tips:
				// - Print the generated regex: `fmt.Println(pattern.regex.String())`
				// - Only run a single case by adding `"focus" : true` to the test in the JSON file

				actual, err := pattern.match(path)
				require.NoError(t, err)

				if shouldMatch {
					assert.True(t, actual, "expected pattern %s to match path %s", test.Pattern, path)
				} else {
					assert.False(t, actual, "expected pattern %s to not match path %s", test.Pattern, path)
				}
			}
		})
	}
}

func TestLiteralPrefix(t *testing.T) {
	tests := []struct {
		pattern string
		want    string
	}{
		// Root-anchored patterns: the literal text up to the first wildcard.
		{"/foo/bar/*.go", "foo/bar/"},
		{"/foo/bar/**", "foo/bar/"},
		{"/foo/bar/baz.go", "foo/bar/baz.go"},
		{"/.github/workflows/**/*", ".github/workflows/"},
		{"/foo/b*r", "foo/b"},
		{"/foo?bar", "foo"},
		// Wildcard in the first segment leaves no usable prefix.
		{"/*.go", ""},
		{"/*", ""},
		// Unanchored patterns match relative to any directory, so no path prefix
		// is guaranteed.
		{"foo", ""},
		{"*.go", ""},
		{"foo/bar/*.go", ""},
		// Escapes are treated conservatively: we stop at the backslash rather
		// than interpret it, which still yields a valid (shorter) prefix.
		{"/foo\\*bar", "foo"},
	}

	for _, test := range tests {
		t.Run(test.pattern, func(t *testing.T) {
			assert.Equal(t, test.want, literalPrefix(test.pattern))
		})
	}
}

// TestLiteralPrefixIsNecessaryCondition guards the core safety property of the
// prefix pre-filter: whenever a path actually matches a pattern, it must also
// start with that pattern's literal prefix. If this ever failed, the filter
// would discard real matches.
func TestLiteralPrefixIsNecessaryCondition(t *testing.T) {
	data, err := os.ReadFile("testdata/patterns.json")
	require.NoError(t, err)

	var tests []patternTest
	require.NoError(t, json.Unmarshal(data, &tests))

	for _, test := range tests {
		prefix := literalPrefix(test.Pattern)
		if prefix == "" {
			continue
		}
		for path, shouldMatch := range test.Paths {
			if shouldMatch {
				assert.Truef(t, strings.HasPrefix(filepath.ToSlash(path), prefix),
					"pattern %q matches path %q but path lacks required prefix %q",
					test.Pattern, path, prefix)
			}
		}
	}
}
