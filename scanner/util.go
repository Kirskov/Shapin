package scanner

import (
	"fmt"
	"path/filepath"
	"strings"
)

// assertWithinRoot returns an error if path is not contained within root,
// preventing directory traversal attacks.
func assertWithinRoot(path, root string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolving root: %w", err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}
	if !strings.HasPrefix(absPath, absRoot+string(filepath.Separator)) {
		return fmt.Errorf("path %q escapes root %q", path, root)
	}
	return nil
}
