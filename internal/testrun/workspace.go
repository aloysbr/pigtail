package testrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrWorkspaceNotStatic reports a workspace file whose project list cannot be
// read without executing it: inline project objects, a computed array, an
// interpolated path.
//
// It is an error rather than an empty result on purpose. Returning "no
// patterns" would look exactly like a workspace file that matches nothing, and
// the run would go unarbitrated with a misleading reason — the silence this
// package exists to end. The caller turns this into a named skip that says
// which file it could not read.
var ErrWorkspaceNotStatic = errors.New("workspace file is not a static list of patterns")

// WorkspacePatterns reads the glob patterns out of a vitest workspace file.
//
// Why read it as text rather than evaluate it: evaluating means running node
// on the repo's TypeScript before pigtail has decided anything, in a process
// whose failure modes (a missing dependency, a plugin with side effects) would
// become pigtail's. Every workspace file in the wild that this needs to handle
// — including a large monorepo's — is a literal array; anything else is refused by name
// rather than guessed at. See ErrWorkspaceNotStatic.
func WorkspacePatterns(path string) ([]string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // a config path resolved from the worktree
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	src := string(data)

	if strings.EqualFold(filepath.Ext(path), ".json") {
		var patterns []string
		if jerr := json.Unmarshal(data, &patterns); jerr != nil {
			return nil, fmt.Errorf("%w: %s", ErrWorkspaceNotStatic, filepath.Base(path))
		}
		return patterns, nil
	}

	body, err := defaultExportArray(src)
	if err != nil {
		return nil, err
	}
	return arrayStrings(body)
}

// defaultExportArray returns the text between the brackets of the array the
// module default-exports, unwrapping a defineWorkspace()/defineConfig() call.
func defaultExportArray(src string) (string, error) {
	code := stripComments(src)

	i := strings.Index(code, "export default")
	if i < 0 {
		return "", fmt.Errorf("%w: no default export", ErrWorkspaceNotStatic)
	}
	rest := code[i+len("export default"):]

	open := strings.IndexByte(rest, '[')
	if open < 0 {
		return "", fmt.Errorf("%w: the default export is not an array literal", ErrWorkspaceNotStatic)
	}
	// Anything other than whitespace and a wrapping call before the bracket
	// means the array is computed: `readdirSync(...).map(...)` reaches its
	// own brackets eventually, and reading those would be reading arguments,
	// not projects.
	head := strings.TrimSpace(rest[:open])
	head = strings.TrimSuffix(head, "(")
	head = strings.TrimSpace(head)
	if head != "" && !isIdentifier(head) {
		return "", fmt.Errorf("%w: the default export is computed", ErrWorkspaceNotStatic)
	}

	depth := 0
	for j := open; j < len(rest); j++ {
		switch rest[j] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return rest[open+1 : j], nil
			}
		}
	}
	return "", fmt.Errorf("%w: unterminated array", ErrWorkspaceNotStatic)
}

// arrayStrings reads the string literals of an array body, refusing anything
// that is not one.
func arrayStrings(body string) ([]string, error) {
	var out []string
	for i := 0; i < len(body); i++ {
		switch c := body[i]; c {
		case ' ', '\t', '\n', '\r', ',':
			continue
		case '\'', '"', '`':
			lit, end, err := readLiteral(body, i)
			if err != nil {
				return nil, err
			}
			out = append(out, lit)
			i = end
		default:
			// An object, a spread, an identifier: configuration this reader
			// cannot turn into a path.
			return nil, fmt.Errorf("%w: entry at offset %d is not a string literal", ErrWorkspaceNotStatic, i)
		}
	}
	return out, nil
}

// readLiteral reads one quoted string starting at start, returning its value
// and the index of its closing quote.
func readLiteral(body string, start int) (string, int, error) {
	quote := body[start]
	var sb strings.Builder
	for i := start + 1; i < len(body); i++ {
		switch body[i] {
		case '\\':
			if i+1 < len(body) {
				sb.WriteByte(body[i+1])
				i++
			}
		case '$':
			// `${dir}/*` — the pattern in the file is not the pattern vitest
			// would use, so there is nothing honest to return.
			if quote == '`' && i+1 < len(body) && body[i+1] == '{' {
				return "", 0, fmt.Errorf("%w: an interpolated pattern", ErrWorkspaceNotStatic)
			}
			sb.WriteByte(body[i])
		case quote:
			return sb.String(), i, nil
		default:
			sb.WriteByte(body[i])
		}
	}
	return "", 0, fmt.Errorf("%w: unterminated string", ErrWorkspaceNotStatic)
}

// stripComments removes // and /* */ comments, leaving string literals alone —
// a commented-out pattern, or a path mentioned in prose, must not be read as a
// project.
func stripComments(src string) string {
	var sb strings.Builder
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			j := i + 1
			for ; j < len(src); j++ {
				if src[j] == '\\' {
					j++
					continue
				}
				if src[j] == c {
					break
				}
			}
			if j >= len(src) {
				sb.WriteString(src[i:])
				return sb.String()
			}
			sb.WriteString(src[i : j+1])
			i = j
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			sb.WriteByte('\n')
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return sb.String()
			}
			i += 2 + end + 1
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

// isIdentifier reports whether s is a bare function name such as
// "defineWorkspace" — the only thing allowed to wrap the array.
func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		word := r == '_' || r == '$' || r == '.' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !word {
			return false
		}
	}
	return true
}
