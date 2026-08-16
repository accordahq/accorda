// Package secrets resolves encrypted secrets for deployment without
// implementing its own cryptography.
//
// Accorda manages the flow from encrypted input through controlled
// decryption, rendering, deployment, and plaintext cleanup. Cryptographic
// operations are delegated to tools such as SOPS (initially SOPS + age).
// Plaintext secrets should never be written to persistent storage; when
// temporary files are unavoidable they use restrictive permissions (0600) on
// memory-backed or tmpfs locations and are deleted immediately after use.
// Logs must never include secret values, tokens, or credentials.
//
// See docs/ACCORDA.md §17 (Secrets and SOPS) and §18 (Secret Handling
// Security) for the authoritative description.
package secrets
