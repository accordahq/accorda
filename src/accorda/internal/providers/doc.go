// Package providers hosts Git provider integrations that add optional
// capabilities on top of generic Git.
//
// Provider integrations are enhancements, never fundamental dependencies.
// They contribute provider-specific behavior such as GitHub App installation
// tokens, deployment status reporting, Checks, and webhooks, while generic
// Git operations remain in internal/sources. A user of generic Git should not
// require any provider integration.
//
// See docs/ACCORDA.md §14 (Git Provider Integrations) and §15
// (Authentication) for the authoritative description.
package providers
