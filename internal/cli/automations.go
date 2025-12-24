// Package cli provides automation discovery commands.
// Added in v3.6.1.
package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// newAutomationsCmd creates the 'automations' command group.
func newAutomationsCmd() *cobra.Command {
	automationsCmd := &cobra.Command{
		Use:   "automations",
		Short: "Automation discovery and information",
		Long: `Commands for discovering available automations on the Rescale platform.

Automations are pre-configured scripts that run before (pre) or after (post) job execution.
Use these commands to:
  - List all available automations
  - Get details about specific automations`,
	}

	automationsCmd.AddCommand(newAutomationsListCmd())
	automationsCmd.AddCommand(newAutomationsGetCmd())

	return automationsCmd
}

// newAutomationsListCmd creates the 'automations list' command.
func newAutomationsListCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available automations",
		Long: `List all available automations on Rescale.

Automations can be attached to jobs to run custom scripts before or after job execution.

Examples:
  # List all automations
  rescale-int automations list

  # Get JSON output
  rescale-int automations list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := GetContext()

			apiClient, err := getAPIClient()
			if err != nil {
				return err
			}

			automations, err := apiClient.ListAutomations(ctx)
			if err != nil {
				return fmt.Errorf("failed to list automations: %w", err)
			}

			if outputJSON {
				data, err := json.MarshalIndent(automations, "", "  ")
				if err != nil {
					return fmt.Errorf("failed to marshal JSON: %w", err)
				}
				fmt.Println(string(data))
				return nil
			}

			if len(automations) == 0 {
				fmt.Println("No automations found")
				return nil
			}

			fmt.Printf("Found %d automation(s):\n\n", len(automations))
			fmt.Printf("%-10s %-50s %-8s %s\n", "ID", "NAME", "PHASE", "SCRIPT")
			fmt.Println(strings.Repeat("-", 90))

			for _, a := range automations {
				name := a.Name
				if len(name) > 48 {
					name = name[:45] + "..."
				}
				fmt.Printf("%-10s %-50s %-8s %s\n", a.ID, name, a.ExecuteOn, a.ScriptName)
			}

			return nil
		},
	}

	cmd.Flags().BoolVarP(&outputJSON, "json", "J", false, "Output as JSON")

	return cmd
}

// newAutomationsGetCmd creates the 'automations get' command.
func newAutomationsGetCmd() *cobra.Command {
	var automationID string
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get details about an automation",
		Long: `Get detailed information about a specific automation.

Examples:
  # Get automation details
  rescale-int automations get --id YYnVk

  # Get JSON output
  rescale-int automations get --id YYnVk --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if automationID == "" {
				return fmt.Errorf("--id is required")
			}

			ctx := GetContext()

			apiClient, err := getAPIClient()
			if err != nil {
				return err
			}

			automation, err := apiClient.GetAutomation(ctx, automationID)
			if err != nil {
				return fmt.Errorf("failed to get automation: %w", err)
			}

			if outputJSON {
				data, err := json.MarshalIndent(automation, "", "  ")
				if err != nil {
					return fmt.Errorf("failed to marshal JSON: %w", err)
				}
				fmt.Println(string(data))
				return nil
			}

			fmt.Printf("Automation Details:\n")
			fmt.Printf("  ID:          %s\n", automation.ID)
			fmt.Printf("  Name:        %s\n", automation.Name)
			fmt.Printf("  Phase:       %s\n", automation.ExecuteOn)
			fmt.Printf("  Script:      %s\n", automation.ScriptName)
			if automation.Description != "" {
				fmt.Printf("  Description: %s\n", automation.Description)
			}
			if automation.OSFamily != "" {
				fmt.Printf("  OS Family:   %s\n", automation.OSFamily)
			}
			if automation.Command != "" {
				fmt.Printf("  Command:     %s\n", automation.Command)
			}
			if len(automation.EnvironmentVariables) > 0 {
				fmt.Printf("  Env Vars:    %s\n", strings.Join(automation.EnvironmentVariables, ", "))
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&automationID, "id", "", "Automation ID (required)")
	cmd.Flags().BoolVarP(&outputJSON, "json", "J", false, "Output as JSON")

	return cmd
}
