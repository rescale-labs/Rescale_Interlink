package compat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/cloud/download"
	"github.com/rescale/rescale-int/internal/constants"
	inthttp "github.com/rescale/rescale-int/internal/http"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/progress"
	"github.com/rescale/rescale-int/internal/resources"
	"github.com/rescale/rescale-int/internal/transfer"
	"github.com/rescale/rescale-int/internal/validation"
)

// compatDownloadItem implements transfer.WorkItem for compat download.
type compatDownloadItem struct {
	idx       int
	fileID    string
	name      string
	size      int64
	localPath string
}

func (d compatDownloadItem) FileSize() int64 { return d.size }

func newDownloadFileCmd() *cobra.Command {
	var jobID string
	var fileID string
	var fileName string
	var outputPath string
	var extendedOutput bool
	var runID string

	cmd := &cobra.Command{
		Use:   "download-file",
		Short: "Download job output files",
		RunE: func(cmd *cobra.Command, args []string) error {
			if runID != "" {
				return fmt.Errorf("'-r' (run-id) is not yet implemented in compat mode (planned for Plan 4)")
			}

			if jobID == "" && fileID == "" {
				return fmt.Errorf("one of -j (--job-id) or --file-id is required")
			}
			if jobID != "" && fileID != "" {
				return fmt.Errorf("-j (--job-id) and --file-id are mutually exclusive")
			}

			cc := GetCompatContext(cmd)
			client, err := cc.GetAPIClient(cmd.Context())
			if err != nil {
				return err
			}

			if extendedOutput {
				if jobID != "" {
					return fmt.Errorf("download-file -e -j is not supported (broken in rescale-cli due to Java NPE)")
				}
				return compatDownloadExtended(cmd.Context(), fileID, client)
			}

			if fileID != "" {
				return compatDownloadByFileID(cmd.Context(), fileID, outputPath, client, cc)
			}

			if fileName == "" {
				// Rescale-cli prints this as SLF4J INFO (not error) and exits 0.
				if !cc.Quiet {
					fmt.Fprintf(os.Stdout, "%s - Please provide a file name to download\n", FormatSLF4JTimestamp(time.Now()))
				}
				return nil
			}
			return compatDownloadByJobID(cmd.Context(), jobID, compatDownloadOpts{FileName: fileName, OutputDir: outputPath}, client, cc)
		},
	}

	cmd.Flags().StringVarP(&jobID, "job-id", "j", "", "Job ID")
	// --file-id has no single-char shorthand; -fid is normalized to --file-id by NormalizeCompatArgs
	cmd.Flags().StringVar(&fileID, "file-id", "", "File ID to download")
	cmd.Flags().StringVarP(&fileName, "file-name", "f", "", "Filter by filename (exact match)")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output path or directory")

	cmd.Flags().BoolVarP(&extendedOutput, "extended-output", "e", false, "Extended JSON output")
	cmd.Flags().StringVarP(&runID, "run-id", "r", "", "Run ID")
	cmd.Flags().MarkHidden("run-id")

	return cmd
}

// compatDownloadExtended handles download-file -e -fid: metadata query, no download.
// Uses typed GetFileInfo + toCompatFileEntry to produce exactly the 9-field set
// matching rescale-cli's output (not raw API passthrough which has 17+ fields).
func compatDownloadExtended(ctx context.Context, fileID string, apiClient *api.Client) error {
	startTime := time.Now()

	fileInfo, err := apiClient.GetFileInfo(ctx, fileID)
	if err != nil {
		endTime := time.Now()
		writeTransferEnvelope(os.Stdout, false, startTime, endTime, []compatFileEntry{})
		return fmt.Errorf("failed to get file info: %w", err)
	}

	endTime := time.Now()
	entry := toCompatFileEntry(fileInfo)
	return writeTransferEnvelope(os.Stdout, true, startTime, endTime, []compatFileEntry{entry})
}

// compatDownloadByFileID downloads a single file by its file ID.
func compatDownloadByFileID(ctx context.Context, fileID, outputPath string, apiClient *api.Client, cc *CompatContext) error {
	inthttp.WarmupProxyIfNeeded(ctx, apiClient.GetConfig())
	credentials.GetManager(apiClient).WarmAll(ctx)

	fileInfo, err := apiClient.GetFileInfo(ctx, fileID)
	if err != nil {
		return fmt.Errorf("failed to get file info: %w", err)
	}

	if err := validation.ValidateFilename(fileInfo.Name); err != nil {
		return fmt.Errorf("invalid filename from API for file %s: %w", fileID, err)
	}

	if outputPath == "" {
		outputPath = filepath.Join(".", fileInfo.Name)
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	cc.Printf("Downloading %s (%.2f MB)\n", fileInfo.Name, float64(fileInfo.DecryptedSize)/(1024*1024))

	resourceMgr := resources.NewManager(resources.Config{AutoScale: true})
	transferMgr := transfer.NewManager(resourceMgr)
	transferHandle := transferMgr.AllocateTransfer(fileInfo.DecryptedSize, 1)

	var downloadUI *progress.DownloadUI
	if !cc.Quiet {
		downloadUI = progress.NewDownloadUI(1)
		defer downloadUI.Wait()
	}

	var fileBar *progress.DownloadFileBar
	var barOnce sync.Once

	var progressCB func(float64)
	if downloadUI != nil {
		progressCB = func(fraction float64) {
			barOnce.Do(func() {
				fileBar = downloadUI.AddFileBar(1, fileID, fileInfo.Name, outputPath, fileInfo.DecryptedSize)
			})
			if fileBar != nil {
				fileBar.UpdateProgress(fraction)
			}
		}
	}

	err = download.DownloadFile(ctx, download.DownloadParams{
		FileID:           fileID,
		LocalPath:        outputPath,
		APIClient:        apiClient,
		ProgressCallback: progressCB,
		TransferHandle:   transferHandle,
	})

	if downloadUI != nil {
		if fileBar == nil {
			fileBar = downloadUI.AddFileBar(1, fileID, fileInfo.Name, outputPath, fileInfo.DecryptedSize)
		}
		if err != nil {
			fileBar.Complete(err)
		} else {
			fileBar.Complete(nil)
		}
	}

	if err != nil {
		return fmt.Errorf("failed to download file: %w", err)
	}

	cc.Printf("Downloaded: %s\n", outputPath)
	return nil
}

// compatDownloadOpts configures file filtering for job downloads.
type compatDownloadOpts struct {
	FileName     string   // exact match (for download-file -f)
	OutputDir    string
	FileMatchers []string // glob include patterns (for sync -f)
	ExcludeTerm  string   // exclude pattern (for sync --exclude)
	SearchTerm   string   // search substring (for sync -s)
}

// compatDownloadByJobID downloads output files for a job, optionally filtered by filename or glob patterns.
func compatDownloadByJobID(ctx context.Context, jobID string, opts compatDownloadOpts, apiClient *api.Client, cc *CompatContext) error {
	inthttp.WarmupProxyIfNeeded(ctx, apiClient.GetConfig())
	credentials.GetManager(apiClient).WarmAll(ctx)

	cc.Printf("Fetching output files for job %s...\n", jobID)

	allFiles, err := apiClient.ListJobFiles(ctx, jobID)
	if err != nil {
		return fmt.Errorf("failed to list job files: %w", err)
	}

	if len(allFiles) == 0 {
		cc.Printf("No output files found for job %s\n", jobID)
		return nil
	}

	// Filter by exact filename or glob patterns
	files := allFiles
	if opts.FileName != "" {
		var matched []models.JobFile
		for _, f := range allFiles {
			if f.Name == opts.FileName {
				matched = append(matched, f)
			}
		}
		files = matched
		if len(files) == 0 {
			return fmt.Errorf("no files matching '%s' found in job %s", opts.FileName, jobID)
		}
	} else if len(opts.FileMatchers) > 0 || opts.ExcludeTerm != "" || opts.SearchTerm != "" {
		var filtered []models.JobFile
		for _, f := range allFiles {
			if matchesE2EFilters(f.Name, opts.FileMatchers, opts.ExcludeTerm, opts.SearchTerm) {
				filtered = append(filtered, f)
			}
		}
		files = filtered
	}

	outputDir := opts.OutputDir
	if outputDir == "" {
		outputDir = "."
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	cc.Printf("Downloading %d file(s) from job %s\n", len(files), jobID)

	resourceMgr := resources.NewManager(resources.Config{AutoScale: true})
	transferMgr := transfer.NewManager(resourceMgr)

	items := make([]compatDownloadItem, len(files))
	for i, f := range files {
		localPath := filepath.Join(outputDir, f.Name)
		if f.RelativePath != "" {
			candidate := filepath.Join(outputDir, f.RelativePath)
			if validation.ValidatePathInDirectory(candidate, outputDir) == nil {
				localPath = candidate
			}
		}
		items[i] = compatDownloadItem{
			idx:       i,
			fileID:    f.ID,
			name:      f.Name,
			size:      f.DecryptedSize,
			localPath: localPath,
		}
	}

	cfg := transfer.BatchConfig{
		MaxWorkers:  constants.DefaultMaxConcurrent,
		ResourceMgr: resourceMgr,
		Label:       "COMPAT-DOWNLOAD",
	}
	numWorkers := transfer.ComputedWorkers(items, cfg)

	var downloadUI *progress.DownloadUI
	if !cc.Quiet {
		downloadUI = progress.NewDownloadUI(len(files))
		defer downloadUI.Wait()
	}

	batchResult := transfer.RunBatch(ctx, items, cfg, func(ctx context.Context, item compatDownloadItem) error {
		outputPath := item.localPath

		// Ensure directory exists for relative paths
		if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
			return fmt.Errorf("failed to create directory for %s: %w", item.name, err)
		}

		// Skip existing files (compat default behavior)
		if info, err := os.Stat(outputPath); err == nil && !info.IsDir() {
			cc.Printf("Skipping existing: %s\n", item.name)
			return nil
		}

		transferHandle := transferMgr.AllocateTransfer(item.size, numWorkers)

		var fileBar *progress.DownloadFileBar
		var barOnce sync.Once

		var progressCB func(float64)
		if downloadUI != nil {
			progressCB = func(fraction float64) {
				barOnce.Do(func() {
					fileBar = downloadUI.AddFileBar(item.idx+1, item.fileID, item.name, outputPath, item.size)
				})
				if fileBar != nil {
					fileBar.UpdateProgress(fraction)
				}
			}
		}

		// Use job file metadata directly to avoid per-file GetFileInfo calls
		cloudFile := files[item.idx].ToCloudFile()

		dlErr := download.DownloadFile(ctx, download.DownloadParams{
			FileInfo:         cloudFile,
			LocalPath:        outputPath,
			APIClient:        apiClient,
			ProgressCallback: progressCB,
			TransferHandle:   transferHandle,
		})

		if downloadUI != nil {
			if fileBar == nil {
				fileBar = downloadUI.AddFileBar(item.idx+1, item.fileID, item.name, outputPath, item.size)
			}
			if dlErr != nil {
				fileBar.Complete(dlErr)
			} else {
				fileBar.Complete(nil)
			}
		}

		if dlErr != nil {
			return fmt.Errorf("failed to download %s: %w", item.name, dlErr)
		}
		return nil
	})

	if len(batchResult.Errors) > 0 {
		cc.Printf("Downloaded %d file(s), %d failed\n", batchResult.Completed, batchResult.Failed)
		return batchResult.Errors[0]
	}

	cc.Printf("Successfully downloaded %d file(s)\n", batchResult.Completed)
	return nil
}
