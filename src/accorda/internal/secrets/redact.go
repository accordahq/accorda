package secrets

// RedactedValue is the placeholder used when a sensitive value exists but
// must not be exposed in user-facing output.
const RedactedValue = "<redacted>"

// UnsetValue is the placeholder used when one side of a sensitive-value
// comparison is absent.
const UnsetValue = "<unset>"

// DisplayValue reports only whether a sensitive value exists. It deliberately
// ignores the plaintext value so callers cannot accidentally include it in
// terminal output, logs, or errors.
func DisplayValue(_ string, present bool) string {
	if !present {
		return UnsetValue
	}
	return RedactedValue
}
