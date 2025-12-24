// Package cli provides software discovery commands.
package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/models"
)

// newSoftwareCmd creates the 'software' command group.
func newSoftwareCmd() *cobra.Command {
	softwareCmd := &cobra.Command{
		Use:   "software",
		Short: "Software discovery and information",
		Long: `Commands for discovering available software applications (analyses) on the Rescale platform.

Use these commands to:
  - List all available software applications
  - Search for specific applications
  - Get details about versions and capabilities`,
	}

	// Add software subcommands
	softwareCmd.AddCommand(newSoftwareListCmd())

	return softwareCmd
}

// newSoftwareListCmd creates the 'software list' command.
func newSoftwareListCmd() *cobra.Command {
	var (
		search       string
		outputJSON   bool
		showVersions bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available software applications (analyses)",
		Long: `List all available software applications (analyses) on Rescale.

Examples:
  # List all software
  rescale-int software list

  # Search for specific software
  rescale-int software list --search openfoam

  # Show available versions
  rescale-int software list --versions

  # Get JSON output
  rescale-int software list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := GetContext()

			// Get API client
			apiClient, err := getAPIClient()
			if err != nil {
				return err
			}

			// Fetch analyses
			analyses, err := apiClient.GetAnalyses(ctx)
			if err != nil {
				return fmt.Errorf("failed to get analyses: %w", err)
			}

			// Filter by search term if provided
			if search != "" {
				filtered := make([]models.Analysis, 0)
				searchLower := strings.ToLower(search)
				for _, a := range analyses {
					if strings.Contains(strings.ToLower(a.Code), searchLower) ||
						strings.Contains(strings.ToLower(a.Name), searchLower) ||
						strings.Contains(strings.ToLower(a.Description), searchLower) {
						filtered = append(filtered, a)
					}
				}
				analyses = filtered
			}

			// Sort by display order
			sort.Slice(analyses, func(i, j int) bool {
				return analyses[i].DisplayOrder < analyses[j].DisplayOrder
			})

			// Output
			if outputJSON {
				// JSON output
				data, err := json.MarshalIndent(analyses, "", "  ")
				if err != nil {
					return fmt.Errorf("failed to marshal JSON: %w", err)
				}
				fmt.Println(string(data))
			} else {
				// Human-readable output
				if len(analyses) == 0 {
					fmt.Println("No software found")
					return nil
				}

				fmt.Printf("Found %d software application(s):\n\n", len(analyses))

				// Find max width for alignment
				maxCodeWidth := 0
				for _, a := range analyses {
					if len(a.Code) > maxCodeWidth {
						maxCodeWidth = len(a.Code)
					}
				}

				// Print table
				for _, a := range analyses {
					fmt.Printf("  %-*s  %s\n", maxCodeWidth, a.Code, a.Name)

					// Show versions if requested
					if showVersions && len(a.Versions) > 0 {
						versionStrs := make([]string, 0, len(a.Versions))
						for _, v := range a.Versions {
							if v.Version != "" {
								versionStrs = append(versionStrs, v.Version)
							} else if v.VersionCode != "" {
								versionStrs = append(versionStrs, v.VersionCode)
							}
						}
						if len(versionStrs) > 0 {
							// Show first 5 versions to avoid overwhelming output
							displayVersions := versionStrs
							if len(displayVersions) > 5 {
								displayVersions = versionStrs[:5]
								fmt.Printf("  %s  Versions: %s ... (%d more)\n",
									strings.Repeat(" ", maxCodeWidth),
									strings.Join(displayVersions, ", "),
									len(versionStrs)-5)
							} else {
								fmt.Printf("  %s  Versions: %s\n",
									strings.Repeat(" ", maxCodeWidth),
									strings.Join(displayVersions, ", "))
							}
						}
					}
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&search, "search", "s", "", "Search for software by code, name, or description")
	cmd.Flags().BoolVarP(&outputJSON, "json", "J", false, "Output as JSON")
	cmd.Flags().BoolVarP(&showVersions, "versions", "V", false, "Show available versions for each software")

	return cmd
}
