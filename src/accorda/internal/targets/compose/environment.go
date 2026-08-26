package compose

import (
	"strings"

	shareddocker "accorda/internal/docker"
)

const composeDisableEnvFile = "COMPOSE_DISABLE_ENV_FILE"

// composeCommandEnvironment returns the filtered environment for the
// `docker compose` child process. It delegates to the shared Docker
// operational allowlist (docs/DECISIONS.md #21) and adds
// COMPOSE_DISABLE_ENV_FILE=1 so Compose's implicit .env loading does not
// override Git-authored defaults.
func composeCommandEnvironment(environ []string) []string {
	return shareddocker.ControlledEnvironment(environ, composeDisableEnvFile+"=1")
}

// controlledComposeEnvironment returns the exact environment used for
// desired-state interpolation. It is the map form of the child-process
// environment, including COMPOSE_DISABLE_ENV_FILE=1 so the two paths stay
// equivalent.
func controlledComposeEnvironment(environ []string) map[string]string {
	out := map[string]string{composeDisableEnvFile: "1"}
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, allowed := shareddocker.IsOperationalEnv(name); allowed {
			out[name] = value
		}
	}
	return out
}
