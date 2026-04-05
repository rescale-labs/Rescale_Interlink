package analysis

import (
	"context"

	"github.com/rescale/rescale-int/internal/api"
)

// ResolveVersion resolves a version name (like "CPU") to its versionCode (like "0").
// The Rescale API accepts versionCode in the "version" field for job creation.
// If the version is already a valid versionCode or if resolution fails, returns the original value.
func ResolveVersion(ctx context.Context, client *api.Client, analysisCode, versionInput string) string {
	if versionInput == "" {
		return versionInput
	}

	analyses, err := client.GetAnalyses(ctx)
	if err != nil {
		return versionInput
	}

	for _, analysis := range analyses {
		if analysis.Code == analysisCode {
			for _, v := range analysis.Versions {
				if v.Version == versionInput {
					if v.VersionCode != "" {
						return v.VersionCode
					}
					return versionInput
				}
				if v.VersionCode == versionInput {
					return versionInput
				}
			}
			break
		}
	}

	return versionInput
}
