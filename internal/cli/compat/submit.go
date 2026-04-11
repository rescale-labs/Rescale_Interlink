package compat

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

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
			metadata, err := sgeParser.ParseWithOptions(scriptFile, parser.ParseOptions{CompatDefaults: true})
			if err != nil {
				return fmt.Errorf("failed to parse script: %w", err)
			}

			jobReq := metadata.ToJobRequest()

			// Apply compat flags
			jobReq.IsLowPriority = waiveSLA
			if pCluster != "" {
				jobReq.ClusterID = pCluster
			}

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

			// Expand glob patterns for input files
			var expandedInputFiles []string
			if len(inputFiles) > 0 {
				expandedInputFiles, err = glob.ExpandPatterns(inputFiles)
				if err != nil {
					return fmt.Errorf("failed to expand input file patterns: %w", err)
				}
				for _, fp := range expandedInputFiles {
					info, statErr := os.Stat(fp)
					if os.IsNotExist(statErr) {
						return fmt.Errorf("input file not found: %s", fp)
					}
					if statErr != nil {
						return fmt.Errorf("failed to stat %s: %w", fp, statErr)
					}
					if info.IsDir() {
						return fmt.Errorf("'%s' is a directory, not a file", fp)
					}
				}
			}

			// Stage files in temp dir matching rescale-cli behavior:
			// run.sh = script content, input.zip = ZIP of other input files
			cc.Printf("%s - Zipping Files\n", FormatSLF4JTimestamp(compatNow()))
			tmpDir, cleanup, err := compatStageSubmitFiles(scriptFile, expandedInputFiles)
			if err != nil {
				return fmt.Errorf("failed to stage submit files: %w", err)
			}
			defer cleanup()

			// Upload run.sh and input.zip
			cc.Printf("%s - Uploading Files\n", FormatSLF4JTimestamp(compatNow()))
			uploadPaths := []string{
				filepath.Join(tmpDir, "run.sh"),
				filepath.Join(tmpDir, "input.zip"),
			}
			fileIDs, err := compatUploadFilesReturnIDs(ctx, uploadPaths, "", client, cc)
			if err != nil {
				return fmt.Errorf("failed to upload files: %w", err)
			}

			// Override command and input files to match CLI behavior
			if len(jobReq.JobAnalyses) > 0 {
				jobReq.JobAnalyses[0].Command = "./run.sh"
				jobReq.JobAnalyses[0].InputFiles = []models.InputFileRequest{
					{ID: fileIDs[0], Decompress: true}, // run.sh
					{ID: fileIDs[1], Decompress: true}, // input.zip
				}
			}

			// Create job
			cc.Printf("%s - Job: Saving Job\n", FormatSLF4JTimestamp(compatNow()))
			jobResp, err := client.CreateJob(ctx, *jobReq)
			if err != nil {
				return fmt.Errorf("failed to create job: %w", err)
			}

			cc.Printf("%s - Job %s: Saved\n", FormatSLF4JTimestamp(compatNow()), jobResp.ID)

			// Submit job
			cc.Printf("%s - Job %s: Submitting\n", FormatSLF4JTimestamp(compatNow()), jobResp.ID)
			if err := client.SubmitJob(ctx, jobResp.ID); err != nil {
				return fmt.Errorf("failed to submit job: %w", err)
			}

			// Extended JSON output: raw job JSON after submission, transformed to CLI format
			if extendedOutput {
				rawJob, err := client.GetJobRaw(ctx, jobResp.ID)
				if err != nil {
					return fmt.Errorf("failed to get job details: %w", err)
				}
				transformed, err := transformSubmitJSON(rawJob)
				if err != nil {
					return fmt.Errorf("failed to transform job JSON: %w", err)
				}
				return writeJSON(os.Stdout, transformed)
			}

			if !endToEnd {
				cc.Printf("%s - Job %s: --end-to-end flag not set, polling should be done manually.\n",
					FormatSLF4JTimestamp(compatNow()), jobResp.ID)
			}

			// E2E mode: monitor and optionally download
			if endToEnd {
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

	cmd.Flags().BoolVarP(&extendedOutput, "extended-output", "e", false, "Extended JSON output")

	cmd.Flags().StringVar(&pCluster, "p-cluster", "", "Persistent cluster ID")
	cmd.Flags().BoolVar(&waiveSLA, "waive-sla", false, "Waive SLA (low priority)")


	// Accepted-but-ignored flags (rescale-cli has these, scripts may pass them)
	var verify string
	var maxConcurrent int
	cmd.Flags().StringVar(&verify, "verify", "true", "Verify file integrity (accepted, ignored)")
	cmd.Flags().MarkHidden("verify")
	cmd.Flags().IntVar(&maxConcurrent, "max-concurrent", 5, "Max concurrent downloads (accepted, ignored)")
	cmd.Flags().MarkHidden("max-concurrent")

	return cmd
}

// compatStageSubmitFiles creates a temp directory with run.sh (copy of script)
// and input.zip (ZIP of additional input files). This matches rescale-cli behavior:
// the script is renamed to run.sh, other files are zipped into input.zip,
// and the platform extracts both via decompress=true.
func compatStageSubmitFiles(scriptFile string, inputFiles []string) (tmpDir string, cleanup func(), err error) {
	tmpDir, err = os.MkdirTemp("", "rescale-compat-submit-*")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp directory: %w", err)
	}
	cleanup = func() { os.RemoveAll(tmpDir) }

	// Copy script to run.sh
	runShPath := filepath.Join(tmpDir, "run.sh")
	scriptData, err := os.ReadFile(scriptFile)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("failed to read script file: %w", err)
	}
	if err := os.WriteFile(runShPath, scriptData, 0644); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("failed to write run.sh: %w", err)
	}

	// Create input.zip containing the additional input files (flat, no directory structure)
	zipPath := filepath.Join(tmpDir, "input.zip")
	if err := compatCreateInputZip(zipPath, inputFiles); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("failed to create input.zip: %w", err)
	}

	return tmpDir, cleanup, nil
}

// compatCreateInputZip creates a ZIP archive of the given files with flat structure
// (basenames only, no directory paths). If inputFiles is empty, creates an empty ZIP.
func compatCreateInputZip(zipPath string, inputFiles []string) error {
	f, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer f.Close()

	w := zip.NewWriter(f)
	for _, filePath := range inputFiles {
		if err := compatAddFileToZip(w, filePath); err != nil {
			w.Close()
			return fmt.Errorf("failed to add %s to zip: %w", filepath.Base(filePath), err)
		}
	}
	return w.Close()
}

func compatAddFileToZip(w *zip.Writer, filePath string) error {
	src, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return err
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = filepath.Base(filePath) // flat structure
	header.Method = zip.Deflate

	dst, err := w.CreateHeader(header)
	if err != nil {
		return err
	}

	_, err = io.Copy(dst, src)
	return err
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
	return compatDownloadByJobID(ctx, jobID, compatDownloadOpts{OutputDir: "."}, apiClient, cc)
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

	// Exclude filter (substring match, not glob — matches rescale-cli behavior)
	if excludeTerm != "" {
		if containsInsensitive(name, excludeTerm) {
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

// compatNow is a function variable for testability.
var compatNow = func() time.Time { return time.Now() }

// v3-only top-level keys that CLI does not include
var submitV3OnlyTopKeys = []string{
	"attachedJobs", "autoAttachCloudfilesystem", "autosuspend", "clonedFrom",
	"currentUserHasFullAccess", "customFields", "delayedStart", "description",
	"elapsedWalltimeSeconds", "folderId", "isPinned", "isPublisherSandbox",
	"isTemplate", "jobAutomations", "jobvariables", "launchConfig", "osCostTier",
	"overrideImageName", "overrideImageRegion", "owner", "region", "remoteVizConfig",
	"rsjJob", "sharedWith", "study", "supportsReboot", "suspendAvailable",
	"suspensionStatus", "templatedFrom", "userTags",
}

// CLI-only top-level keys with their default values
var submitCLIOnlyTopKeys = map[string]interface{}{
	"analysisNames":        nil,
	"apiKey":               nil,
	"autoTerminateCluster": true,
	"htcSettings":          nil,
	"isInteractive":        false,
	"isLargeDoe":           false,
	"jobUsername":           nil,
	"ownerCompanyCode":     nil,
	"ownerId":              nil,
}

// v3-only jobanalyses keys that CLI does not include
var submitV3OnlyJAKeys = []string{
	"analysis", "flags", "onDemandLicenseSeller", "userDefinedLicenseSettings",
}

// v3-only inputFile keys that CLI does not include
var submitV3OnlyInputFileKeys = []string{
	"dateUploaded", "downloadUrl", "isDeleted", "owner", "path",
	"relativePath", "userTags", "viewInBrowser",
}

// transformSubmitJSON reshapes a v3 API job response to match rescale-cli's
// client-side submit -e JSON structure.
func transformSubmitJSON(raw json.RawMessage) (json.RawMessage, error) {
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}

	// Remove v3-only top-level keys
	for _, k := range submitV3OnlyTopKeys {
		delete(m, k)
	}

	// Add CLI-only top-level keys
	for k, v := range submitCLIOnlyTopKeys {
		m[k] = v
	}

	// Flatten each jobanalyses entry
	if jaRaw, ok := m["jobanalyses"]; ok {
		if jaSlice, ok := jaRaw.([]interface{}); ok {
			for i, entry := range jaSlice {
				if jaMap, ok := entry.(map[string]interface{}); ok {
					jaSlice[i] = flattenJobAnalysis(jaMap)
				}
			}
		}
	}

	return json.Marshal(m)
}

// flattenJobAnalysis transforms a v3 jobanalyses entry to CLI format:
// - Extracts analysis.code/type/version into flat fields
// - Flattens hardware.coreType from object to string
// - Removes v3-only keys, adds CLI-only defaults
// - Transforms inputFiles entries
func flattenJobAnalysis(ja map[string]interface{}) map[string]interface{} {
	// Extract fields from nested analysis object
	var analysisCode interface{}
	var analysisType interface{} = "compute"
	var analysisVersionID interface{}
	if analysis, ok := ja["analysis"].(map[string]interface{}); ok {
		analysisCode = analysis["code"]
		if at, ok := analysis["type"]; ok && at != nil {
			analysisType = at
		}
		analysisVersionID = analysis["id"]
	}

	// Remove v3-only JA keys
	for _, k := range submitV3OnlyJAKeys {
		delete(ja, k)
	}

	// Add flattened analysis fields
	ja["analysisCode"] = analysisCode
	ja["analysisType"] = analysisType
	ja["analysisVersionId"] = analysisVersionID

	// Add CLI-only JA defaults
	ja["isCustomDoe"] = false
	ja["order"] = 0
	ja["setupCommand"] = ""
	ja["shouldRunForever"] = false
	ja["stopCommand"] = ""
	ja["stopCommandTimeout"] = nil
	ja["useSharedStorage"] = false
	if _, ok := ja["licenseSettings"]; !ok {
		ja["licenseSettings"] = []interface{}{}
	}

	// Flatten hardware.coreType from {code: "emerald", ...} to "emerald"
	if hw, ok := ja["hardware"].(map[string]interface{}); ok {
		if ct, ok := hw["coreType"].(map[string]interface{}); ok {
			hw["coreType"] = ct["code"]
		}
	}

	// Transform inputFiles entries
	if ifRaw, ok := ja["inputFiles"]; ok {
		if ifSlice, ok := ifRaw.([]interface{}); ok {
			for i, entry := range ifSlice {
				if ifMap, ok := entry.(map[string]interface{}); ok {
					ifSlice[i] = flattenInputFile(ifMap)
				}
			}
		}
	}

	return ja
}

// flattenInputFile removes v3-only fields and adds CLI-only fields to an inputFile entry.
func flattenInputFile(f map[string]interface{}) map[string]interface{} {
	for _, k := range submitV3OnlyInputFileKeys {
		delete(f, k)
	}
	if _, ok := f["inputFileType"]; !ok {
		f["inputFileType"] = "REMOTE"
	}
	return f
}
