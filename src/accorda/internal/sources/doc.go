// Package sources adapts Git providers and generic Git servers into a common
// source abstraction used by Accorda core. Sources fetch revisions and expose
// releasable filesystem views; target adapters own artifact parsing.
//
// Generic Git is the foundation and must work with any Git server over SSH or
// HTTPS, including on-premises servers, with no SaaS dependency. Provider
// integrations (under internal/providers) add optional capabilities on top of
// generic Git; this package defines the shared Source contract so core never
// depends on a specific Git host.
//
// See docs/ACCORDA.md §13 (Git Abstraction) for the authoritative description.
package sources
