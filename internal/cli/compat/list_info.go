package compat

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newListInfoCmd() *cobra.Command {
	var coreTypes bool
	var analyses bool
	var desktops bool

	cmd := &cobra.Command{
		Use:   "list-info",
		Short: "List available hardware or software",
		RunE: func(cmd *cobra.Command, args []string) error {
			if desktops {
				return fmt.Errorf("'-d' (desktops) is not yet implemented in compat mode")
			}

			if !coreTypes && !analyses {
				return fmt.Errorf("one of -c (--core-types) or -a (--analyses) is required")
			}
			if coreTypes && analyses {
				return fmt.Errorf("-c (--core-types) and -a (--analyses) are mutually exclusive")
			}

			cc := GetCompatContext(cmd)
			client, err := cc.GetAPIClient(cmd.Context())
			if err != nil {
				return err
			}

			ctx := cmd.Context()

			if coreTypes {
				items, err := client.GetCoreTypesRaw(ctx, false)
				if err != nil {
					return fmt.Errorf("failed to list core types: %w", err)
				}
				return printPrettyJSON(items)
			}

			// analyses
			items, err := client.GetAnalysesRaw(ctx)
			if err != nil {
				return fmt.Errorf("failed to list analyses: %w", err)
			}
			return printPrettyJSON(items)
		},
	}

	cmd.Flags().BoolVarP(&coreTypes, "core-types", "c", false, "List available hardware core types")
	cmd.Flags().BoolVarP(&analyses, "analyses", "a", false, "List available software analyses")

	// Deferred flag
	cmd.Flags().BoolVarP(&desktops, "desktops", "d", false, "List desktops")
	cmd.Flags().MarkHidden("desktops")

	return cmd
}

func printPrettyJSON(items []json.RawMessage) error {
	for _, raw := range items {
		var obj interface{}
		if err := json.Unmarshal(raw, &obj); err != nil {
			return err
		}
		pretty, err := json.MarshalIndent(obj, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, string(pretty))
	}
	return nil
}
