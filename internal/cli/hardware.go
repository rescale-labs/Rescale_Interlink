// Package cli provides hardware discovery commands.
package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/models"
)

// newHardwareCmd creates the 'hardware' command group.
func newHardwareCmd() *cobra.Command {
	hardwareCmd := &cobra.Command{
		Use:   "hardware",
		Short: "Hardware discovery and information",
		Long:  `Commands for discovering available hardware types (core types) on the Rescale platform.`,
	}

	// Add hardware subcommands
	hardwareCmd.AddCommand(newHardwareListCmd())

	return hardwareCmd
}

// newHardwareListCmd creates the 'hardware list' command.
func newHardwareListCmd() *cobra.Command {
	var (
		search     string
		outputJSON bool
		showAll    bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available hardware types (core types)",
		Long: `List available hardware types (core types) on Rescale.

By default, only active hardware types are shown. Use --all to include
inactive/deprecated types.

Examples:
  # List active hardware types (default)
  rescale-int hardware list

  # Include inactive/deprecated hardware types
  rescale-int hardware list --all

  # Search for specific hardware
  rescale-int hardware list --search emerald

  # Get JSON output
  rescale-int hardware list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := GetContext()

			// Load config
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Create API client
			apiClient, err := api.NewClient(cfg)
			if err != nil {
				return fmt.Errorf("failed to create API client: %w", err)
			}

			// Fetch core types (by default active only, --all includes inactive)
			coreTypes, err := apiClient.GetCoreTypes(ctx, showAll)
			if err != nil {
				return fmt.Errorf("failed to get core types: %w", err)
			}

			// Filter by search term if provided
			if search != "" {
				filtered := make([]models.CoreType, 0)
				searchLower := strings.ToLower(search)
				for _, ct := range coreTypes {
					if strings.Contains(strings.ToLower(ct.Code), searchLower) ||
						strings.Contains(strings.ToLower(ct.Name), searchLower) {
						filtered = append(filtered, ct)
					}
				}
				coreTypes = filtered
			}

			// Sort by display order
			sort.Slice(coreTypes, func(i, j int) bool {
				return coreTypes[i].DisplayOrder < coreTypes[j].DisplayOrder
			})

			// Output
			if outputJSON {
				// JSON output
				data, err := json.MarshalIndent(coreTypes, "", "  ")
				if err != nil {
					return fmt.Errorf("failed to marshal JSON: %w", err)
				}
				fmt.Println(string(data))
			} else {
				// Human-readable output
				if len(coreTypes) == 0 {
					fmt.Println("No hardware types found")
					return nil
				}

				fmt.Printf("Found %d hardware type(s):\n\n", len(coreTypes))

				// Find max width for alignment
				maxCodeWidth := 0
				for _, ct := range coreTypes {
					if len(ct.Code) > maxCodeWidth {
						maxCodeWidth = len(ct.Code)
					}
				}

				// Print table
				for _, ct := range coreTypes {
					fmt.Printf("  %-*s  %s\n", maxCodeWidth, ct.Code, ct.Name)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&search, "search", "s", "", "Search for hardware by code or name")
	cmd.Flags().BoolVarP(&outputJSON, "json", "J", false, "Output as JSON")
	cmd.Flags().BoolVarP(&showAll, "all", "a", false, "Include inactive/deprecated hardware types")

	return cmd
}
