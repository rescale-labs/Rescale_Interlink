// Package cli provides the command-line interface for rescale-int.
package cli

import (
	"context"
	"crypto/fips140"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/resources"
)

var (
	// Global flags
	cfgFile    string
	apiKey     string
	tokenFile  string // Path to file containing API key
	apiBaseURL string
	verbose    bool
	debug      bool

	// Thread control flags
	maxThreads  int
	noAutoScale bool

	// Global logger
	logger *logging.Logger

	// Global context for signal handling
	rootContext context.Context
	cancelFunc  context.CancelFunc
)

// Version information - set by main package at startup
// The actual version is defined in:
// 1. Makefile (source of truth for releases, injected via LDFLAGS)
// 2. cmd/rescale-int/main.go (fallback for non-Makefile builds)
var (
	Version   = "v4.0.0-dev"
	BuildTime = "2025-12-27"
)

// FIPSStatus returns FIPS 140-3 compliance status string
func FIPSStatus() string {
	if fips140.Enabled() {
		return "[FIPS 140-3]"
	}
	return "[FIPS: disabled]"
}

// NewRootCmd creates the root command for CLI mode.
func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "rescale-int",
		Short: "Rescale Interlink - CLI and GUI for Rescale platform",
		Long: `Rescale Interlink ` + Version + ` - Built: ` + BuildTime + ` ` + FIPSStatus() + `
Tool for interacting with the Rescale platform via CLI or GUI.

CLI Mode (default):
  Command-line interface for job submission, file management,
  and pipeline execution.

GUI Mode (--gui flag):
  Graphical interface with job monitoring.

Security:
  FIPS 140-3 compliant cryptography for FedRAMP Moderate.`,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			// Initialize logger
			logger = logging.NewDefaultCLILogger()
			if verbose || debug {
				logging.SetGlobalLevel(-1) // Debug level (zerolog.DebugLevel)
			}
		},
	}

	// Global flags
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "Configuration file path")
	rootCmd.PersistentFlags().StringVar(&apiKey, "api-key", "", "Rescale API key (overrides all other sources)")
	rootCmd.PersistentFlags().StringVar(&tokenFile, "token-file", "", "Path to file containing API key")
	rootCmd.PersistentFlags().StringVar(&apiBaseURL, "api-url", "", "Rescale API base URL (overrides config)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Verbose output (shows debug messages)")
	rootCmd.PersistentFlags().BoolVar(&debug, "debug", false, "Enable debug output (same as --verbose)")

	// Thread control flags for multi-threaded transfers
	rootCmd.PersistentFlags().IntVar(&maxThreads, "max-threads", 0, "Maximum threads for transfers (0 = auto-detect, range: 1-32)")
	rootCmd.PersistentFlags().BoolVar(&noAutoScale, "no-auto-scale", false, "Disable automatic thread scaling")

	// Version command (includes FIPS status)
	rootCmd.Version = Version + " (" + BuildTime + ") " + FIPSStatus()

	// Customize completion command description
	completionCmd := &cobra.Command{
		Use:   "completion [bash|zsh|fish|powershell]",
		Short: "Enable tab-completion for rescale-int commands",
		Long: `Generate shell completion scripts to enable tab-completion for rescale-int.

Tab-completion lets you press Tab to:
  • Auto-complete command names (e.g., "rescale-int j<Tab>" → "jobs")
  • Auto-complete flag names (e.g., "rescale-int jobs download --<Tab>" → shows all flags)
  • See available subcommands

QUICK START:

  macOS with zsh (default on modern Macs):
    mkdir -p ~/.zsh/completions
    rescale-int completion zsh > ~/.zsh/completions/_rescale-int
    # Then add to ~/.zshrc: fpath=(~/.zsh/completions $fpath)
    # Restart terminal

  macOS with bash:
    rescale-int completion bash > $(brew --prefix)/etc/bash_completion.d/rescale-int
    # Restart terminal

  Linux with bash:
    rescale-int completion bash | sudo tee /etc/bash_completion.d/rescale-int
    # Restart terminal

For detailed instructions, use: rescale-int completion [shell] --help`,
	}
	rootCmd.AddCommand(completionCmd)

	// Add subcommands for each shell
	completionCmd.AddCommand(&cobra.Command{
		Use:   "bash",
		Short: "Generate bash completion script",
		Long: `Generate the autocompletion script for bash.

SETUP INSTRUCTIONS:

macOS:
  1. Install bash-completion (if not already installed):
       brew install bash-completion@2

  2. Generate completion script:
       rescale-int completion bash > $(brew --prefix)/etc/bash_completion.d/rescale-int

  3. Add to ~/.bash_profile (if not already there):
       [[ -r "$(brew --prefix)/etc/profile.d/bash_completion.sh" ]] && . "$(brew --prefix)/etc/profile.d/bash_completion.sh"

  4. Restart your terminal

Linux:
  1. Install bash-completion (if not already installed):
       # Ubuntu/Debian:
       sudo apt-get install bash-completion
       # RHEL/CentOS:
       sudo yum install bash-completion

  2. Generate completion script:
       rescale-int completion bash | sudo tee /etc/bash_completion.d/rescale-int

  3. Restart your terminal

QUICK TEST (temporary, current session only):
  source <(rescale-int completion bash)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return rootCmd.Root().GenBashCompletion(cmd.OutOrStdout())
		},
	})

	completionCmd.AddCommand(&cobra.Command{
		Use:   "zsh",
		Short: "Generate zsh completion script",
		Long: `Generate the autocompletion script for zsh.

SETUP INSTRUCTIONS:

macOS (modern Macs use zsh by default):
  1. Create completions directory:
       mkdir -p ~/.zsh/completions

  2. Generate completion script:
       rescale-int completion zsh > ~/.zsh/completions/_rescale-int

  3. Add to ~/.zshrc (if not already there):
       fpath=(~/.zsh/completions $fpath)
       autoload -Uz compinit && compinit

  4. Restart your terminal (or run: source ~/.zshrc)

Linux:
  1. Generate completion script:
       rescale-int completion zsh > "${fpath[1]}/_rescale-int"

  2. Restart your terminal

QUICK TEST (temporary, current session only):
  source <(rescale-int completion zsh)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return rootCmd.Root().GenZshCompletion(cmd.OutOrStdout())
		},
	})

	completionCmd.AddCommand(&cobra.Command{
		Use:   "fish",
		Short: "Generate fish completion script",
		Long: `Generate the autocompletion script for fish.

SETUP INSTRUCTIONS:

  1. Generate completion script:
       rescale-int completion fish > ~/.config/fish/completions/rescale-int.fish

  2. Restart your terminal

QUICK TEST (temporary, current session only):
  rescale-int completion fish | source`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return rootCmd.Root().GenFishCompletion(cmd.OutOrStdout(), true)
		},
	})

	completionCmd.AddCommand(&cobra.Command{
		Use:   "powershell",
		Short: "Generate PowerShell completion script",
		Long: `Generate the autocompletion script for PowerShell.

SETUP INSTRUCTIONS (Windows):

  1. Find your PowerShell profile location:
       $PROFILE

  2. Generate completion script:
       rescale-int completion powershell >> $PROFILE

  3. Restart PowerShell

QUICK TEST (temporary, current session only):
  rescale-int completion powershell | Out-String | Invoke-Expression`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return rootCmd.Root().GenPowerShellCompletion(cmd.OutOrStdout())
		},
	})

	// Disable default completion command (we're adding our own above)
	rootCmd.CompletionOptions.DisableDefaultCmd = true

	return rootCmd
}

// Execute runs the CLI.
func Execute() error {
	// Create a context that can be cancelled by signals
	rootContext, cancelFunc = context.WithCancel(context.Background())

	// Set up signal handling for graceful cancellation
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Start goroutine to handle signals
	// v4.0.0: Loop to handle multiple signals (e.g., user pressing Ctrl+C multiple times)
	go func() {
		for sig := range sigChan {
			// Only print cancellation message if we received an actual signal
			// (when channel is closed, sig will be nil and the loop exits)
			if sig != nil {
				fmt.Fprintf(os.Stderr, "\n\n🛑 Received signal %v, cancelling operations...\n", sig)
				fmt.Fprintf(os.Stderr, "   Please wait for cleanup to complete.\n\n")
				cancelFunc()
			}
		}
	}()

	rootCmd := NewRootCmd()
	AddCommands(rootCmd)
	err := rootCmd.Execute()

	// Clean up signal handler
	signal.Stop(sigChan)
	close(sigChan)

	return err
}

// AddCommands adds all subcommands to the root command.
func AddCommands(rootCmd *cobra.Command) {
	// Add command groups
	rootCmd.AddCommand(newPURCmd())
	rootCmd.AddCommand(newFilesCmd())
	rootCmd.AddCommand(newFoldersCmd())
	rootCmd.AddCommand(newJobsCmd())
	rootCmd.AddCommand(newHardwareCmd())
	rootCmd.AddCommand(newSoftwareCmd())
	rootCmd.AddCommand(newAutomationsCmd()) // v3.6.1: Automation discovery
	rootCmd.AddCommand(newConfigCmd())
	rootCmd.AddCommand(newDaemonCmd())  // v3.4.0: Background service for auto-downloading completed jobs
	rootCmd.AddCommand(newServiceCmd()) // v4.0.0: Windows service management

	// Add shortcuts for convenience
	AddShortcuts(rootCmd)
}

// GetLogger returns the global CLI logger.
func GetLogger() *logging.Logger {
	if logger == nil {
		logger = logging.NewDefaultCLILogger()
	}
	return logger
}

// GetContext returns the global CLI context with signal handling.
// This context will be cancelled when the user presses Ctrl+C.
func GetContext() context.Context {
	if rootContext == nil {
		// Fallback to background context if called before Execute()
		return context.Background()
	}
	return rootContext
}

// CreateResourceManager creates a resource manager from global flags
func CreateResourceManager() *resources.Manager {
	// Validate maxThreads
	if maxThreads < 0 || maxThreads > 32 {
		fmt.Fprintf(os.Stderr, "Warning: --max-threads must be between 0 and 32, using auto-detect\n")
		maxThreads = 0
	}

	// Create resource manager config
	config := resources.Config{
		MaxThreads: maxThreads,
		AutoScale:  !noAutoScale,
	}

	return resources.NewManager(config)
}
