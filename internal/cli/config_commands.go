// Package cli provides configuration management commands.
package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
)

// newConfigCmd creates the 'config' command group.
func newConfigCmd() *cobra.Command {
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Manage rescale-int configuration",
		Long: `Configuration management commands for rescale-int.

Commands:
  init  - Interactive configuration setup
  show  - Display current configuration
  test  - Test API connection
  path  - Show configuration file path`,
	}

	// Add config subcommands
	configCmd.AddCommand(newConfigInitCmd())
	configCmd.AddCommand(newConfigShowCmd())
	configCmd.AddCommand(newConfigTestCmd())
	configCmd.AddCommand(newConfigPathCmd())

	return configCmd
}

// newConfigInitCmd creates the 'config init' command.
func newConfigInitCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize configuration interactively",
		Long: `Interactive configuration setup for rescale-int.

The configuration will be saved to ~/.config/rescale-int/config.csv

Use --force to overwrite existing configuration.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			// Get default config path
			configPath := config.GetDefaultConfigPath()

			// Check if config already exists
			if !force {
				if _, err := os.Stat(configPath); err == nil {
					fmt.Printf("Configuration already exists at: %s\n", configPath)
					fmt.Println("Use --force to overwrite or run 'config show' to view current config.")
					return nil
				}
			}

			fmt.Println("Rescale Configuration Setup")
			fmt.Println("===========================")
			fmt.Println()

			reader := bufio.NewReader(os.Stdin)

			// API Key (required)
			var apiKeyInput string
			for apiKeyInput == "" {
				fmt.Print("API Key (required): ")
				input, _ := reader.ReadString('\n')
				apiKeyInput = strings.TrimSpace(input)
				if apiKeyInput == "" {
					fmt.Println("  Error: API key is required")
				}
			}

			// API Base URL
			fmt.Print("API Base URL [https://platform.rescale.com]: ")
			apiURLInput, _ := reader.ReadString('\n')
			apiURLInput = strings.TrimSpace(apiURLInput)
			if apiURLInput == "" {
				apiURLInput = "https://platform.rescale.com"
			}

			// Worker settings
			fmt.Println()
			fmt.Println("Worker Settings (press Enter for defaults)")
			fmt.Println("-------------------------------------------")

			fmt.Print("Tar workers [4]: ")
			tarWorkersInput, _ := reader.ReadString('\n')
			tarWorkersInput = strings.TrimSpace(tarWorkersInput)
			tarWorkers := 4
			if tarWorkersInput != "" {
				if v, err := strconv.Atoi(tarWorkersInput); err == nil && v > 0 {
					tarWorkers = v
				}
			}

			fmt.Print("Upload workers [4]: ")
			uploadWorkersInput, _ := reader.ReadString('\n')
			uploadWorkersInput = strings.TrimSpace(uploadWorkersInput)
			uploadWorkers := 4
			if uploadWorkersInput != "" {
				if v, err := strconv.Atoi(uploadWorkersInput); err == nil && v > 0 {
					uploadWorkers = v
				}
			}

			fmt.Print("Job workers [4]: ")
			jobWorkersInput, _ := reader.ReadString('\n')
			jobWorkersInput = strings.TrimSpace(jobWorkersInput)
			jobWorkers := 4
			if jobWorkersInput != "" {
				if v, err := strconv.Atoi(jobWorkersInput); err == nil && v > 0 {
					jobWorkers = v
				}
			}

			// Proxy settings
			fmt.Println()
			fmt.Print("Configure proxy? [y/N]: ")
			proxyInput, _ := reader.ReadString('\n')
			proxyInput = strings.TrimSpace(strings.ToLower(proxyInput))

			var proxyMode, proxyHost string
			var proxyPort int

			if proxyInput == "y" || proxyInput == "yes" {
				fmt.Println()
				fmt.Println("Proxy Configuration")
				fmt.Println("-------------------")
				fmt.Println("Proxy modes: no-proxy, system, basic, ntlm")
				fmt.Print("Proxy mode [system]: ")
				proxyModeInput, _ := reader.ReadString('\n')
				proxyMode = strings.TrimSpace(proxyModeInput)
				if proxyMode == "" {
					proxyMode = "system"
				}

				if proxyMode != "no-proxy" {
					fmt.Print("Proxy host: ")
					proxyHostInput, _ := reader.ReadString('\n')
					proxyHost = strings.TrimSpace(proxyHostInput)

					fmt.Print("Proxy port [8080]: ")
					proxyPortInput, _ := reader.ReadString('\n')
					proxyPortInput = strings.TrimSpace(proxyPortInput)
					proxyPort = 8080
					if proxyPortInput != "" {
						if v, err := strconv.Atoi(proxyPortInput); err == nil && v > 0 {
							proxyPort = v
						}
					}
				}
			} else {
				proxyMode = "no-proxy"
			}

			// Create config
			cfg := &config.Config{
				APIKey:         apiKeyInput,
				APIBaseURL:     apiURLInput,
				TenantURL:      apiURLInput,
				TarWorkers:     tarWorkers,
				UploadWorkers:  uploadWorkers,
				JobWorkers:     jobWorkers,
				ProxyMode:      proxyMode,
				ProxyHost:      proxyHost,
				ProxyPort:      proxyPort,
				TarCompression: "none",
				MaxRetries:     1,
			}

			// Ensure config directory exists
			configDir := filepath.Dir(configPath)
			if err := os.MkdirAll(configDir, 0755); err != nil {
				return fmt.Errorf("failed to create config directory: %w", err)
			}

			// Save API key to a separate token file (for security, not in config CSV)
			tokenFilePath := filepath.Join(configDir, "rescale_token")
			if err := os.WriteFile(tokenFilePath, []byte(apiKeyInput), 0600); err != nil {
				return fmt.Errorf("failed to save API token file: %w", err)
			}
			logger.Info().Str("path", tokenFilePath).Msg("API token saved")

			// Save config (without API key - it's in the token file)
			if err := config.SaveConfigCSV(cfg, configPath); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}

			logger.Info().Str("path", configPath).Msg("Configuration saved")

			fmt.Println()
			fmt.Printf("✓ Configuration saved to: %s\n", configPath)
			fmt.Printf("✓ API token saved to: %s\n", tokenFilePath)
			fmt.Println()
			fmt.Println("IMPORTANT: Your API key is stored in a separate token file for security.")
			fmt.Println("To use rescale-int commands, you have two options:")
			fmt.Println()
			fmt.Printf("  Option 1: Use the token file (recommended):\n")
			fmt.Printf("    rescale-int --token-file %s <command>\n", tokenFilePath)
			fmt.Println()
			fmt.Printf("  Option 2: Set environment variable:\n")
			fmt.Printf("    export RESCALE_API_KEY=$(cat %s)\n", tokenFilePath)
			fmt.Println()
			fmt.Println("Test your configuration with: rescale-int config test")

			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing configuration")

	return cmd
}

// newConfigShowCmd creates the 'config show' command.
func newConfigShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Display current configuration",
		Long: `Display the current configuration settings.

This command shows the merged configuration from:
  1. Configuration file (~/.config/rescale-int/config.csv)
  2. Environment variables (RESCALE_API_KEY, RESCALE_API_URL)
  3. Command-line flags (--api-key, --api-url)

Priority: flags > environment > config file > defaults`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Get config path
			configPath := cfgFile
			if configPath == "" {
				configPath = config.GetDefaultConfigPath()
			}

			// Load config
			cfg, err := config.LoadConfigCSV(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Merge with environment, token file, and flags
			cfg.MergeWithFlagsAndTokenFile(apiKey, tokenFile, apiBaseURL, "", "", 0)

			// Display config
			fmt.Println("Current Configuration")
			fmt.Println("=====================")
			fmt.Println()

			fmt.Println("API Settings:")
			fmt.Printf("  API Base URL: %s\n", cfg.APIBaseURL)
			if cfg.APIKey != "" {
				// Security: Never display any portion of the API key (FedRAMP compliance)
				fmt.Printf("  API Key:      <set (%d chars)>\n", len(cfg.APIKey))
			} else {
				fmt.Println("  API Key:      <not set>")
			}
			fmt.Println()

			fmt.Println("Worker Settings:")
			fmt.Printf("  Tar Workers:    %d\n", cfg.TarWorkers)
			fmt.Printf("  Upload Workers: %d\n", cfg.UploadWorkers)
			fmt.Printf("  Job Workers:    %d\n", cfg.JobWorkers)
			fmt.Println()

			fmt.Println("Proxy Settings:")
			fmt.Printf("  Proxy Mode: %s\n", cfg.ProxyMode)
			if cfg.ProxyHost != "" {
				fmt.Printf("  Proxy Host: %s\n", cfg.ProxyHost)
				fmt.Printf("  Proxy Port: %d\n", cfg.ProxyPort)
			}
			fmt.Println()

			fmt.Println("Advanced Settings:")
			fmt.Printf("  Tar Compression: %s\n", cfg.TarCompression)
			fmt.Printf("  Max Retries:     %d\n", cfg.MaxRetries)
			if cfg.RunSubpath != "" {
				fmt.Printf("  Run Subpath:     %s\n", cfg.RunSubpath)
			}
			if cfg.ValidationPattern != "" {
				fmt.Printf("  Validation Pattern: %s\n", cfg.ValidationPattern)
			}
			if len(cfg.ExcludePatterns) > 0 {
				fmt.Printf("  Exclude Patterns: %s\n", strings.Join(cfg.ExcludePatterns, ", "))
			}
			if len(cfg.IncludePatterns) > 0 {
				fmt.Printf("  Include Patterns: %s\n", strings.Join(cfg.IncludePatterns, ", "))
			}
			fmt.Println()

			fmt.Printf("Configuration file: %s\n", configPath)
			if _, err := os.Stat(configPath); os.IsNotExist(err) {
				fmt.Println("  (file does not exist - using defaults)")
			}

			return nil
		},
	}

	return cmd
}

// newConfigTestCmd creates the 'config test' command.
func newConfigTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Test API connection",
		Long: `Test the API connection with current configuration.

Use this to verify your API key and network connectivity.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			fmt.Println("Testing API Connection")
			fmt.Println("======================")
			fmt.Println()

			// Load config
			configPath := cfgFile
			if configPath == "" {
				configPath = config.GetDefaultConfigPath()
			}

			cfg, err := config.LoadConfigCSV(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Merge with environment, token file, and flags
			cfg.MergeWithFlagsAndTokenFile(apiKey, tokenFile, apiBaseURL, "", "", 0)

			// Validate config
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("invalid configuration: %w", err)
			}

			fmt.Printf("API URL: %s\n", cfg.APIBaseURL)
			fmt.Println("Testing connection...")
			fmt.Println()

			// Create API client
			apiClient, err := api.NewClient(cfg)
			if err != nil {
				return fmt.Errorf("failed to create API client: %w", err)
			}

			// Test connection with timeout
			ctx, cancel := context.WithTimeout(GetContext(), 10*time.Second)
			defer cancel()

			// Fetch user info as a test
			user, err := apiClient.GetUserProfile(ctx)
			if err != nil {
				logger.Error().Err(err).Msg("Connection test failed")
				fmt.Println("✗ Connection FAILED")
				fmt.Printf("  Error: %v\n", err)
				return fmt.Errorf("connection test failed")
			}

			logger.Info().Msg("Connection test successful")

			fmt.Println("✓ Connection SUCCESSFUL")
			fmt.Println()
			fmt.Println("User Information:")
			fmt.Printf("  Email: %s\n", user.Email)
			fmt.Println()
			fmt.Println("Your API key is valid and the connection is working!")

			return nil
		},
	}

	return cmd
}

// newConfigPathCmd creates the 'config path' command.
func newConfigPathCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "path",
		Short: "Show configuration file path",
		Long:  `Display the path to the configuration file.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := cfgFile
			if configPath == "" {
				configPath = config.GetDefaultConfigPath()
				fmt.Println("Default configuration path:")
			} else {
				fmt.Println("Configuration path (from --config flag):")
			}

			fmt.Printf("  %s\n", configPath)
			fmt.Println()

			// Check if file exists
			if _, err := os.Stat(configPath); err == nil {
				fmt.Println("Status: ✓ File exists")

				// Show file info
				if fileInfo, err := os.Stat(configPath); err == nil {
					fmt.Printf("Size:   %d bytes\n", fileInfo.Size())
					fmt.Printf("Modified: %s\n", fileInfo.ModTime().Format("2006-01-02 15:04:05"))
				}
			} else {
				fmt.Println("Status: File does not exist")
				fmt.Println()
				fmt.Println("Create a configuration file with: rescale-int config init")
			}

			return nil
		},
	}

	return cmd
}
