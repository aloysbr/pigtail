package testrun

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspacePatterns(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		body    string
		want    []string
		wantErr bool
	}{
		{
			// a large monorepo's, verbatim.
			name: "plain array",
			file: "vitest.workspace.ts",
			body: "export default ['**/*/vite.config.ts', '**/*/vitest.config.ts'];\n",
			want: []string{"**/*/vite.config.ts", "**/*/vitest.config.ts"},
		},
		{
			name: "defineWorkspace wrapper",
			file: "vitest.workspace.ts",
			body: "import { defineWorkspace } from 'vitest/config';\nexport default defineWorkspace([\n  'packages/*',\n  \"apps/*/vite.config.ts\",\n]);\n",
			want: []string{"packages/*", "apps/*/vite.config.ts"},
		},
		{
			name: "json workspace",
			file: "vitest.workspace.json",
			body: "[\"packages/*\", \"apps/web\"]\n",
			want: []string{"packages/*", "apps/web"},
		},
		{
			name: "template literals and trailing comma",
			file: "vitest.workspace.mjs",
			body: "export default [\n  `packages/*`,\n  'libs/*/vitest.config.ts',\n];\n",
			want: []string{"packages/*", "libs/*/vitest.config.ts"},
		},
		{
			// A comment must not become a pattern — it is quoted text that
			// never reaches the array.
			name: "comments are not patterns",
			file: "vitest.workspace.ts",
			body: "// see 'docs/testing.md' for why\nexport default ['packages/*'];\n",
			want: []string{"packages/*"},
		},
		{
			// Inline project objects are configuration this parser cannot
			// honestly read: each carries its own test options, and wrapping
			// it by path is not possible. Better a named skip than a wrapper
			// built on a guess.
			name:    "inline project objects",
			file:    "vitest.workspace.ts",
			body:    "export default [{ test: { name: 'a', include: ['a/**'] } }];\n",
			wantErr: true,
		},
		{
			name:    "computed array",
			file:    "vitest.workspace.ts",
			body:    "export default readdirSync('packages').map((p) => `packages/${p}`);\n",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := tree(t, map[string]string{tc.file: tc.body})
			got, err := WorkspacePatterns(filepath.Join(root, tc.file))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %v, want an error the caller can turn into a named skip", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("WorkspacePatterns: %v", err)
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// A `${...}` inside a template literal is a computed path: the pattern we
// would read is not the pattern vitest would use.
func TestWorkspacePatternsRejectsInterpolation(t *testing.T) {
	root := tree(t, map[string]string{
		"vitest.workspace.ts": "const d = 'packages';\nexport default [`${d}/*`];\n",
	})

	if got, err := WorkspacePatterns(filepath.Join(root, "vitest.workspace.ts")); err == nil {
		t.Errorf("got %v, want an error: an interpolated pattern cannot be read statically", got)
	}
}
