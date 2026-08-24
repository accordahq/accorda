package compose

import (
	"sort"
	"strings"
)

const composeDisableEnvFile = "COMPOSE_DISABLE_ENV_FILE"

// composeOperationalEnvironment is the small host-environment allowlist that
// Docker needs for client connectivity, credentials, proxies, and certificate
// discovery. Arbitrary application variables are deliberately excluded so
// Compose interpolation cannot override Git-authored defaults.
var composeOperationalEnvironment = map[string]struct{}{
	"DOCKER_API_VERSION": {}, "DOCKER_CERT_PATH": {}, "DOCKER_CONFIG": {},
	"DOCKER_CONTEXT": {}, "DOCKER_HOST": {}, "DOCKER_TLS": {},
	"DOCKER_TLS_VERIFY": {}, "HOME": {}, "HTTP_PROXY": {},
	"HTTPS_PROXY": {}, "NO_PROXY": {}, "PATH": {}, "SSH_AUTH_SOCK": {},
	"SSL_CERT_DIR": {}, "SSL_CERT_FILE": {}, "TEMP": {}, "TMP": {},
	"TMPDIR": {}, "USERPROFILE": {}, "XDG_CONFIG_HOME": {},
	"XDG_RUNTIME_DIR": {}, "http_proxy": {}, "https_proxy": {},
	"no_proxy": {},
}

// controlledComposeEnvironment returns the exact environment used both for
// desired-state interpolation and the docker compose child process. Disabling
// Compose's implicit .env loading keeps those two paths equivalent.
func controlledComposeEnvironment(environ []string) map[string]string {
	out := map[string]string{composeDisableEnvFile: "1"}
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, allowed := composeOperationalEnvironment[name]; allowed {
			out[name] = value
		}
	}
	return out
}

func composeCommandEnvironment(environ []string) []string {
	controlled := controlledComposeEnvironment(environ)
	keys := make([]string, 0, len(controlled))
	for key := range controlled {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+controlled[key])
	}
	return out
}
