package folder

import (
	"context"
	"fmt"
	"strings"

	"github.com/rescale/rescale-int/internal/api"
)

// SplitFolderPath splits a remote folder path into its segments, accepting both
// slash styles and dropping empty and "." segments.
// Returns an error for ".." segments, which have no meaning in a remote path.
func SplitFolderPath(path string) ([]string, error) {
	raw := strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	})

	var segments []string
	for _, segment := range raw {
		segment = strings.TrimSpace(segment)
		if segment == "" || segment == "." {
			continue
		}
		if segment == ".." {
			return nil, fmt.Errorf("invalid folder path %q: %q segments are not supported", path, "..")
		}
		segments = append(segments, segment)
	}

	return segments, nil
}

// ResolveOrCreatePath resolves a slash-separated remote folder path beneath
// parentID, creating any segment that does not already exist. Existing folders
// are reused rather than duplicated.
//
// An empty parentID resolves to My Library. An empty path returns the resolved
// parentID unchanged, so callers can pass user input through unconditionally.
//
// Returns the ID of the deepest folder in the path.
func ResolveOrCreatePath(ctx context.Context, apiClient *api.Client, parentID, path string) (string, error) {
	if apiClient == nil {
		return "", fmt.Errorf("api client is required")
	}

	if parentID == "" {
		roots, err := apiClient.GetRootFolders(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get root folders: %w", err)
		}
		parentID = roots.MyLibrary
	}

	segments, err := SplitFolderPath(path)
	if err != nil {
		return "", err
	}
	if len(segments) == 0 {
		return parentID, nil
	}

	cache := NewFolderCache()
	currentID := parentID

	for _, name := range segments {
		existingID, exists, err := CheckFolderExists(ctx, apiClient, cache, currentID, name)
		if err != nil {
			return "", fmt.Errorf("failed to check folder %q: %w", name, err)
		}
		if exists {
			currentID = existingID
			continue
		}

		newID, err := apiClient.CreateFolder(ctx, name, currentID)
		if err != nil {
			return "", fmt.Errorf("failed to create folder %q: %w", name, err)
		}

		// A folder we just created is empty; drop any cached contents for it so a
		// later lookup reads through instead of trusting a placeholder entry.
		cache.Invalidate(newID)
		currentID = newID
	}

	return currentID, nil
}
