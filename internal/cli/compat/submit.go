package compat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/pur/parser"
	"github.com/rescale/rescale-int/internal/util/analysis"
	"github.com/rescale/rescale-int/internal/util/glob"
)

func newSubmitCmd() *cobra.Command {
	var inputFile string
	var jobType string
	var endToEnd bool
	var fileMatchers []string
	var excludeTerm string
	var searchTerm string
	var extendedOutput bool
	var pCluster string
	var waiveSLA bool

	cmd := &cobra.Command{
		Use:   "submit",
		Short: "Submit a job from an SGE script",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if extendedOutput {
				return fmt.Errorf("'-e' (extended output) is not yet implemented in compat mode (planned for Plan 3)")
			}
			if pCluster != "" {
				return fmt.Errorf("'--p-cluster' is not yet implemented in compat mode")
			}
			if waiveSLA {
				return fmt.Errorf("'--waive-sla' is not yet implemented in compat mode")
			}

			// Accept and ignore -t/--type (rescale-cli uses this for job type, always "script")
			_ = jobType

			// Determine script file and input files from positional args
			scriptFile := inputFile
			inputFiles := args
			if scriptFile == "" && len(args) > 0 {
				scriptFile = args[0]
				inputFiles = args[1:]
			}
			if scriptFile == "" {
				return fmt.Errorf("script file required: use -i FILE or pass as first argument")
			}

			// Validate script exists
			if _, err := os.Stat(scriptFile); os.IsNotExist(err) {
				return fmt.Errorf("script file not found: %s", scriptFile)
			}

			cc := GetCompatContext(cmd)
			client, err := cc.GetAPIClient(cmd.Context())
			if err != nil {
				return err
			}

			ctx := cmd.Context()

			// Parse SGE script
			sgeParser := parser.NewSGEParser()
			metadata, err := sgeParser.Parse(scriptFile)
			if err != nil {
				return fmt.Errorf("failed to parse script: %w", err)
			}

			jobReq := metadata.ToJobRequest()

			// Resolve analysis versions
			for i := range jobReq.JobAnalyses {
				if jobReq.JobAnalyses[i].Analysis.Version != "" {
					jobReq.JobAnalyses[i].Analysis.Version = analysis.ResolveVersion(
						ctx, client,
						jobReq.JobAnalyses[i].Analysis.Code,
						jobReq.JobAnalyses[i].Analysis.Version,
					)
				}
			}

			// Upload input files if specified (positional args after script)
			if len(inputFiles) > 0 {
				fileIDs, err := compatUploadInputFiles(ctx, inputFiles, client, cc)
				if err != nil {
					return fmt.Errorf("failed to upload input files: %w", err)
				}
				if len(jobReq.JobAnalyses) > 0 {
					for _, fid := range fileIDs {
						jobReq.JobAnalyses[0].InputFiles = append(
							jobReq.JobAnalyses[0].InputFiles,
							models.InputFileRequest{ID: fid},
						)
					}
				}
			}

			// Create job
			jobResp, err := client.CreateJob(ctx, *jobReq)
			if err != nil {
				return fmt.Errorf("failed to create job: %w", err)
			}

			// Submit job
			if err := client.SubmitJob(ctx, jobResp.ID); err != nil {
				return fmt.Errorf("failed to submit job: %w", err)
			}

			// Data output (not suppressed by -q)
			fmt.Fprintf(os.Stdout, "The job has been created with the id: %s\n", jobResp.ID)
			fmt.Fprintln(os.Stdout, "The job has been submitted")

			// E2E mode: monitor and optionally download
			if endToEnd {
				cc.Printf("Monitoring job %s...\n", jobResp.ID)
				if err := compatMonitorJob(ctx, jobResp.ID, client, cc); err != nil {
					return err
				}

				// Download output files with optional filters
				return compatE2EDownload(ctx, jobResp.ID, fileMatchers, excludeTerm, searchTerm, client, cc)
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&inputFile, "input-file", "i", "", "SGE script file path")
	cmd.Flags().StringVarP(&jobType, "type", "t", "script", "Job type (accepted and ignored)")
	cmd.Flags().MarkHidden("type")
	cmd.Flags().BoolVarP(&endToEnd, "end-to-end", "E", false, "Full workflow: submit, monitor, download")
	cmd.Flags().StringSliceVarP(&fileMatchers, "file-matcher", "f", nil, "Download filter glob patterns (E2E mode)")
	cmd.Flags().StringVar(&excludeTerm, "exclude", "", "Exclude files matching term (E2E mode)")
	cmd.Flags().StringVarP(&searchTerm, "search", "s", "", "Search term for file filtering (E2E mode)")

	// Deferred flags
	cmd.Flags().BoolVarP(&extendedOutput, "extended-output", "e", false, "Extended JSON output")
	cmd.Flags().MarkHidden("extended-output")
	cmd.Flags().StringVar(&pCluster, "p-cluster", "", "Persistent cluster ID")
	cmd.Flags().MarkHidden("p-cluster")
	cmd.Flags().BoolVar(&waiveSLA, "waive-sla", false, "Waive SLA")
	cmd.Flags().MarkHidden("waive-sla")

	return cmd
}

// compatUploadInputFiles uploads input files and returns their file IDs.
func compatUploadInputFiles(ctx context.Context, filePatterns []string, apiClient *api.Client, cc *CompatContext) ([]string, error) {
	filePaths, err := glob.ExpandPatterns(filePatterns)
	if err != nil {
		return nil, err
	}

	// Validate files exist
	for _, fp := range filePaths {
		info, err := os.Stat(fp)
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("input file not found: %s", fp)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to stat %s: %w", fp, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("'%s' is a directory, not a file", fp)
		}
	}

	cc.Printf("Uploading %d input file(s)...\n", len(filePaths))

	// Reuse the compat upload infrastructure
	return compatUploadFilesReturnIDs(ctx, filePaths, "", apiClient, cc)
}

// compatE2EDownload downloads job output files with optional filtering.
func compatE2EDownload(ctx context.Context, jobID string, fileMatchers []string, excludeTerm, searchTerm string, apiClient *api.Client, cc *CompatContext) error {
	allFiles, err := apiClient.ListJobFiles(ctx, jobID)
	if err != nil {
		return fmt.Errorf("failed to list job files: %w", err)
	}

	if len(allFiles) == 0 {
		cc.Printf("No output files found\n")
		return nil
	}

	files := allFiles

	// Apply filters
	if len(fileMatchers) > 0 || excludeTerm != "" || searchTerm != "" {
		var filtered []models.JobFile
		for _, f := range files {
			if !matchesE2EFilters(f.Name, fileMatchers, excludeTerm, searchTerm) {
				continue
			}
			filtered = append(filtered, f)
		}
		files = filtered
	}

	if len(files) == 0 {
		cc.Printf("No files match the specified filters\n")
		return nil
	}

	cc.Printf("Downloading %d output file(s)...\n", len(files))
	return compatDownloadByJobID(ctx, jobID, "", ".", apiClient, cc)
}

// matchesE2EFilters checks if a filename passes the E2E download filters.
func matchesE2EFilters(name string, fileMatchers []string, excludeTerm, searchTerm string) bool {
	// Include filter (glob patterns)
	if len(fileMatchers) > 0 {
		matched := false
		for _, pattern := range fileMatchers {
			if ok, _ := matchGlob(pattern, name); ok {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// Exclude filter
	if excludeTerm != "" {
		if ok, _ := matchGlob(excludeTerm, name); ok {
			return false
		}
	}

	// Search filter (substring match)
	if searchTerm != "" {
		if !containsInsensitive(name, searchTerm) {
			return false
		}
	}

	return true
}

// matchGlob does filepath.Match-style glob matching.
func matchGlob(pattern, name string) (bool, error) {
	return filepath.Match(pattern, name)
}

// containsInsensitive does case-insensitive substring search.
func containsInsensitive(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
