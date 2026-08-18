// Package git implements the generic Git source adapter
// (docs/ACCORDA.md §13, §15).
//
// It is the foundation source for Accorda: it works against any Git server
// over SSH or HTTPS, including on-premises servers, with zero SaaS
// dependency and no GitHub-specific calls. Provider integrations
// (internal/providers) add optional capabilities on top of generic Git; this
// package depends on go-git (github.com/go-git/go-git/v6) and the shared
// sources.Source contract.
//
// The implementation uses the go-git library rather than shelling out to the
// system `git` CLI, so `git` is not required at runtime. Authentication is
// handled via go-git transport methods, matching §15:
//
//   - auth.type=ssh reads the private key from the configured path and uses
//     go-git's SSH transport (ssh.PublicKeys). The key material is held in
//     memory and never logged by Accorda.
//   - auth.type=https uses the token as HTTP basic auth (http.BasicAuth) for
//     go-git's HTTPS transport. The token never appears in Source.URL or in
//     error output (§18, §56).
//   - An empty auth.type means "use the ambient environment": go-git uses
//     SSH agent for ssh:// URLs and unauthenticated HTTPS for https:// URLs.
//
// Fetch scope: Fetch updates only refs/remotes/origin/<configured branch>.
// Reading desired state at a commit on a different branch requires that ref
// to have been fetched separately; the adapter does not fetch all remotes on
// every sync by design, to keep fetches cheap and scoped to the configured
// branch.
package git
