package channel

import (
	"log/slog"
	"path/filepath"
	"strings"
)

// ValidateImagePath checks that the path is safe to send back to a user:
// resolves symlinks, verifies the file lives within one of the configured
// extension directories, and only allows .png and .jpg files. It returns the
// cleaned, resolved path on success or "" if the path is rejected. Rejections
// are logged via the supplied logger (which may be nil).
//
// This guards against an extension returning an arbitrary host path (e.g.
// "/etc/passwd") that torii would otherwise blindly upload to Telegram.
func ValidateImagePath(imagePath string, extensionDirs []string, logger *slog.Logger) string {
	if imagePath == "" {
		return ""
	}

	warn := func(msg string, args ...any) {
		if logger != nil {
			logger.Warn(msg, args...)
		}
	}

	// Only allow .png and .jpg
	ext := strings.ToLower(filepath.Ext(imagePath))
	if ext != ".png" && ext != ".jpg" {
		warn("image path rejected: invalid extension", "path", imagePath, "ext", ext)
		return ""
	}

	// Resolve symlinks and normalize
	resolved, err := filepath.EvalSymlinks(imagePath)
	if err != nil {
		warn("image path rejected: cannot resolve", "path", imagePath, "error", err)
		return ""
	}
	resolved = filepath.Clean(resolved)

	// Check that resolved path is within one of the configured extension dirs
	for _, dir := range extensionDirs {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		absDir, err = filepath.EvalSymlinks(absDir)
		if err != nil {
			continue
		}
		// Ensure prefix check uses trailing separator to avoid partial matches
		if strings.HasPrefix(resolved, absDir+string(filepath.Separator)) {
			return resolved
		}
	}

	warn("image path rejected: outside allowed dirs", "path", imagePath, "resolved", resolved)
	return ""
}
