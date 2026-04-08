package compat

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// installArgparseHelp replaces Cobra's default help and usage output with
// a format resembling rescale-cli's argparse4j style. This makes --help
// feel consistent when running in compat mode.
func installArgparseHelp(rootCmd *cobra.Command) {
	rootCmd.SetHelpFunc(argparseHelpFunc)
	rootCmd.SetUsageFunc(argparseUsageFunc)
}

func argparseHelpFunc(cmd *cobra.Command, _ []string) {
	fmt.Fprint(os.Stdout, argparseUsage(cmd))
}

func argparseUsageFunc(cmd *cobra.Command) error {
	fmt.Fprint(os.Stderr, argparseUsage(cmd))
	return nil
}

func argparseUsage(cmd *cobra.Command) string {
	var b strings.Builder

	isRoot := !cmd.HasParent()

	// --- usage: line ---
	if isRoot {
		fmt.Fprintf(&b, "usage: Rescale Client App [-h]")
		writeFlagSynopsis(&b, cmd.Flags(), true)
		b.WriteString("\n")
		names := subcommandNames(cmd)
		if len(names) > 0 {
			fmt.Fprintf(&b, "                          {%s}\n", strings.Join(names, ","))
			b.WriteString("                          ...\n")
		}
	} else {
		fmt.Fprintf(&b, "usage: Rescale Client App %s [-h]", cmd.Name())
		writeFlagSynopsis(&b, cmd.LocalFlags(), false)
		// Include -p in synopsis if inherited
		if f := cmd.InheritedFlags().Lookup("api-token"); f != nil {
			b.WriteString(" [-p API-TOKEN]")
		}
		b.WriteString("\n")
	}

	// --- description (root only) ---
	if isRoot {
		b.WriteString("\nClient app for running jobs on Rescale\n")
	}

	// --- positional arguments (root only) ---
	if isRoot {
		names := subcommandNames(cmd)
		if len(names) > 0 {
			b.WriteString("\npositional arguments:\n")
			fmt.Fprintf(&b, "  {%s}\n", strings.Join(names, ","))
		}
	}

	// --- named arguments ---
	var namedFlags []*pflag.Flag
	localFlags := cmd.LocalFlags()
	localFlags.VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		// -p is shown in "creds:" on subcommands, omitted on root
		if f.Name == "api-token" {
			return
		}
		namedFlags = append(namedFlags, f)
	})

	if isRoot {
		// On root, also include persistent flags not already in local
		cmd.PersistentFlags().VisitAll(func(f *pflag.Flag) {
			if f.Hidden {
				return
			}
			if f.Name == "api-token" {
				return
			}
			if localFlags.Lookup(f.Name) != nil {
				return // already included
			}
			namedFlags = append(namedFlags, f)
		})
	}

	if len(namedFlags) > 0 {
		b.WriteString("\nnamed arguments:\n")
		for _, f := range namedFlags {
			b.WriteString(formatArgparseFlagLine(f))
		}
	}

	// --- creds: section (subcommands only) ---
	if !isRoot {
		if f := cmd.InheritedFlags().Lookup("api-token"); f != nil {
			b.WriteString("\ncreds:\n")
			b.WriteString(formatArgparseFlagLine(f))
		}
	}

	return b.String()
}

// writeFlagSynopsis appends [-flag METAVAR] entries to the builder.
// It skips -h (already in the line) and hidden flags.
func writeFlagSynopsis(b *strings.Builder, flags *pflag.FlagSet, skipAPIToken bool) {
	flags.VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" {
			return
		}
		if skipAPIToken && f.Name == "api-token" {
			return
		}

		if f.Shorthand != "" {
			if f.Value.Type() == "bool" {
				fmt.Fprintf(b, " [-%s]", f.Shorthand)
			} else {
				fmt.Fprintf(b, " [-%s %s]", f.Shorthand, flagMetavar(f))
			}
		} else {
			if f.Value.Type() == "bool" {
				fmt.Fprintf(b, " [--%s]", f.Name)
			} else {
				fmt.Fprintf(b, " [--%s %s]", f.Name, flagMetavar(f))
			}
		}
	})
}

// formatArgparseFlagLine formats a single flag in argparse4j style.
// Example: "  -f FILES, --files FILES  The path of the input files to upload\n"
func formatArgparseFlagLine(f *pflag.Flag) string {
	var left string
	metavar := flagMetavar(f)

	if f.Value.Type() == "bool" {
		if f.Shorthand != "" {
			left = fmt.Sprintf("  -%s, --%s", f.Shorthand, f.Name)
		} else {
			left = fmt.Sprintf("  --%s", f.Name)
		}
	} else {
		if f.Shorthand != "" {
			left = fmt.Sprintf("  -%s %s, --%s %s", f.Shorthand, metavar, f.Name, metavar)
		} else {
			left = fmt.Sprintf("  --%s %s", f.Name, metavar)
		}
	}

	usage := f.Usage
	// Override Cobra's default help description to match argparse4j
	if f.Name == "help" {
		usage = "show this help message and exit"
	}

	// Pad to 25 chars for alignment, or wrap to next line
	const padTo = 25
	if len(left) < padTo {
		return fmt.Sprintf("%-25s %s\n", left, usage)
	}
	return fmt.Sprintf("%s\n%-25s %s\n", left, "", usage)
}

// flagMetavar returns an uppercase metavar derived from the flag name.
// Uses pflag's UnquoteUsage if a backtick-quoted name is present in the
// usage string, otherwise derives from the flag name.
func flagMetavar(f *pflag.Flag) string {
	name, _ := pflag.UnquoteUsage(f)
	// pflag returns the Go type name (e.g. "string", "int") when there's
	// no backtick-quoted override. Derive from the flag name instead.
	switch name {
	case "string", "int", "float64", "duration", "strings", "":
		return strings.ToUpper(strings.ReplaceAll(f.Name, "-", "-"))
	}
	return strings.ToUpper(name)
}

// subcommandNames returns the visible subcommand names, excluding
// Cobra's auto-generated "help" and "completion" commands.
func subcommandNames(cmd *cobra.Command) []string {
	var names []string
	for _, sub := range cmd.Commands() {
		if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
			continue
		}
		names = append(names, sub.Name())
	}
	return names
}
