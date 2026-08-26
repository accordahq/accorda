package docker

import (
	"sort"
	"strings"
)

// operationalEnvironment is the small host-environment allowlist that the
// Docker CLI needs for client connectivity, credentials, proxies, and
// certificate discovery. Arbitrary application variables are deliberately
// excluded so Docker Compose interpolation cannot override Git-authored
// defaults, and so a `docker run` child process does not inherit host secrets
// (docs/ACCORDA.md §18, §56, docs/DECISIONS.md #41).
var operationalEnvironment = map[string]struct{}{
	"DOCKER_API_VERSION": {}, "DOCKER_CERT_PATH": {}, "DOCKER_CONFIG": {},
	"DOCKER_CONTEXT": {}, "DOCKER_HOST": {}, "DOCKER_TLS": {},
	"DOCKER_TLS_VERIFY": {}, "HOME": {}, "HTTP_PROXY": {},
	"HTTPS_PROXY": {}, "NO_PROXY": {}, "PATH": {}, "SSH_AUTH_SOCK": {},
	"SSL_CERT_DIR": {}, "SSL_CERT_FILE": {}, "TEMP": {}, "TMP": {},
	"TMPDIR": {}, "USERPROFILE": {}, "XDG_CONFIG_HOME": {},
	"XDG_RUNTIME_DIR": {}, "http_proxy": {}, "https_proxy": {},
	"no_proxy": {},
}

// IsOperationalEnv reports whether name is in the operational environment
// allowlist. It is exported so callers that need the map form (for example
// Compose interpolation) can check membership without re-implementing the
// allowlist.
func IsOperationalEnv(name string) (struct{}, bool) {
	v, ok := operationalEnvironment[name]
	return v, ok
}

// ControlledEnvironment returns the exact environment a Docker child process
// should receive, filtered to the operational allowlist. extras are
// additional key=value entries the caller wants to inject (for example
// Compose's COMPOSE_DISABLE_ENV_FILE=1); they are added verbatim.
func ControlledEnvironment(environ []string, extras ...string) []string {
	out := make(map[string]string, len(operationalEnvironment)+len(extras))
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, allowed := operationalEnvironment[name]; allowed {
			out[name] = value
		}
	}
	for _, e := range extras {
		name, value, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		out[name] = value
	}
	keys := make([]string, 0, len(out))
	for key := range out {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+out[key])
	}
	return result
}
