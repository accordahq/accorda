package compose

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseEnvFile_KeyValuePairs(t *testing.T) {
	path := writeEnvFile(t, `# comment
API_KEY=secret123
EMPTY=
WITH_SPACES = spaced value
"QUOTED=double quoted"
'SINGLE=single quoted'
export EXPORTED=yes
no_equals_line
`)
	entries, err := parseEnvFile(path)
	if err != nil {
		t.Fatalf("parseEnvFile: %v", err)
	}
	want := []envEntry{
		{key: "API_KEY", value: "secret123"},
		{key: "EMPTY", value: ""},
		{key: "WITH_SPACES", value: "spaced value"},
		{key: `"QUOTED`, value: `double quoted"`},
		{key: `'SINGLE`, value: `single quoted'`},
		{key: "EXPORTED", value: "yes"},
	}
	if len(entries) != len(want) {
		t.Fatalf("entries = %d, want %d: %+v", len(entries), len(want), entries)
	}
	for i, w := range want {
		if entries[i].key != w.key || entries[i].value != w.value {
			t.Errorf("entries[%d] = {%q: %q}, want {%q: %q}", i, entries[i].key, entries[i].value, w.key, w.value)
		}
	}
}

func TestParseEnvFile_QuotedValues(t *testing.T) {
	path := writeEnvFile(t, `DOUBLE="hello world"
SINGLE='hello world'
MIXED="it's a test"
`)
	entries, err := parseEnvFile(path)
	if err != nil {
		t.Fatalf("parseEnvFile: %v", err)
	}
	want := []envEntry{
		{key: "DOUBLE", value: "hello world"},
		{key: "SINGLE", value: "hello world"},
		{key: "MIXED", value: "it's a test"},
	}
	if len(entries) != len(want) {
		t.Fatalf("entries = %d, want %d", len(entries), len(want))
	}
	for i, w := range want {
		if entries[i].key != w.key || entries[i].value != w.value {
			t.Errorf("entries[%d] = {%q: %q}, want {%q: %q}", i, entries[i].key, entries[i].value, w.key, w.value)
		}
	}
}

func TestParseEnvFile_MissingFile(t *testing.T) {
	_, err := parseEnvFile(filepath.Join(t.TempDir(), "nonexistent.env"))
	if err == nil {
		t.Fatal("parseEnvFile on missing file expected error")
	}
}

func TestParseEnvFile_BlankAndComments(t *testing.T) {
	path := writeEnvFile(t, `

# this is a comment

KEY=val

`)
	entries, err := parseEnvFile(path)
	if err != nil {
		t.Fatalf("parseEnvFile: %v", err)
	}
	if len(entries) != 1 || entries[0].key != "KEY" || entries[0].value != "val" {
		t.Fatalf("entries = %+v, want one entry KEY=val", entries)
	}
}

func writeEnvFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	return path
}
