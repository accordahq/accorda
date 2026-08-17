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
package git
