// Package git implements the generic Git source adapter
// (docs/ACCORDA.md §13, §15).
//
// It is the foundation source for Accorda: it works against any Git server
// over SSH or HTTPS, including on-premises servers, with zero SaaS
// dependency and no GitHub-specific calls. Provider integrations
// (internal/providers) add optional capabilities on top of generic Git; this
// package only depends on the Git CLI and the shared sources.Source
// contract.
//
// The implementation shells out to the system `git` command rather than
// embedding a Git library, so it inherits the user's Git transport, SSH
// agent, and credential configuration. Authentication is supported via SSH
// keys (GIT_SSH_COMMAND) and HTTPS credentials/tokens, matching §15.
//
// Explicit auth is configured via config.Auth on the source:
//
//   - auth.type=ssh sets GIT_SSH_COMMAND to "ssh -i <key> -o
//     IdentitiesOnly=yes" so a specific key is used without relying on the
//     SSH agent. The key material is never read or logged by Accorda.
//   - auth.type=https embeds the token in the remote URL so git's HTTPS
//     transport uses it directly. The token never appears on the command
//     line or in error output (§18, §56).
//   - An empty auth.type inherits the ambient Git environment (SSH agent,
//     credential helpers), which is the default for local development.
//
// Fetch scope: Fetch updates only refs/remotes/origin/<configured branch>.
// Reading desired state at a commit on a different branch requires that ref
// to have been fetched separately; the adapter does not fetch all remotes on
// every sync by design, to keep fetches cheap and scoped to the configured
// branch.
package git
