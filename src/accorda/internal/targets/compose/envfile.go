package compose

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// parseEnvFile reads a .env file and returns its key/value entries in
// declaration order. It implements the subset of the dotenv format that
// Docker Compose uses for env_file: KEY=VALUE lines, with optional surrounding
// quotes and optional `export ` prefix. Comments (#) and blank lines are
// skipped. A line without `=` is skipped (Compose silently ignores malformed
// lines). Values are not interpolated; Compose does not expand variables in
// env_file entries.
//
// This is a deploy-time helper (docs/DECISIONS.md #45): the parsed values
// are merged into the deploy Compose file's environment: and never enter
// desired state, hashing, or receipts.
func parseEnvFile(path string) ([]envEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("compose: read env file %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var entries []envEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		value = strings.TrimSpace(value)
		value = trimEnvQuotes(value)
		entries = append(entries, envEntry{key: key, value: value})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("compose: scan env file %q: %w", path, err)
	}
	return entries, nil
}

// envEntry is one key/value pair parsed from a .env file.
type envEntry struct {
	key   string
	value string
}

// trimEnvQuotes removes matching surrounding quotes from a dotenv value, as
// Docker Compose does: `"value"` → value, `'value'` → value.
func trimEnvQuotes(value string) string {
	if len(value) < 2 {
		return value
	}
	if (value[0] == '"' && value[len(value)-1] == '"') ||
		(value[0] == '\'' && value[len(value)-1] == '\'') {
		return value[1 : len(value)-1]
	}
	return value
}
