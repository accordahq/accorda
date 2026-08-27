package sources

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"accorda/internal/config"
)

// ComposePath resolves the Compose file within a Git repository worktree.
// sourcePath may name either a Compose file or a directory; targetPath
// supplies the configured target filename for the directory form. Empty
// values default to compose.yaml. The returned path always uses
// repository-style separators.
//
// It is shared by the git source (which resolves the path against its managed
// checkout) and the local source (which resolves it against a user-owned
// worktree), so both adapters agree on how a configured source path maps to a
// services file.
func ComposePath(sourcePath, targetPath string) (string, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	targetPath = strings.TrimSpace(targetPath)
	if sourcePath == "" {
		if targetPath == "" {
			targetPath = config.DefaultComposeFile
		}
		return CleanRepositoryPath(targetPath)
	}
	if IsComposeFile(sourcePath) {
		return CleanRepositoryPath(sourcePath)
	}
	if targetPath == "" {
		targetPath = config.DefaultComposeFile
	}
	return CleanRepositoryPath(path.Join(filepath.ToSlash(sourcePath), filepath.ToSlash(targetPath)))
}

// IsComposeFile reports whether path looks like a Compose file name.
func IsComposeFile(filePath string) bool {
	base := strings.ToLower(filepath.Base(filePath))
	switch base {
	case config.DefaultComposeFile, "compose.yml", "docker-compose.yaml", "docker-compose.yml":
		return true
	default:
		ext := strings.ToLower(filepath.Ext(base))
		return ext == ".yaml" || ext == ".yml"
	}
}

// CleanRepositoryPath normalizes a repository-relative path and rejects
// absolute paths and traversal so a configured artifact cannot escape its
// worktree (docs/DECISIONS.md #49).
func CleanRepositoryPath(repositoryPath string) (string, error) {
	normalized := filepath.ToSlash(strings.TrimSpace(repositoryPath))
	clean := path.Clean(normalized)
	if clean == "." || path.IsAbs(clean) || filepath.IsAbs(repositoryPath) ||
		clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("source: repository path %q must stay within the worktree", repositoryPath)
	}
	return clean, nil
}
