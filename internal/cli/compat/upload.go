package compat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/cloud/upload"
	"github.com/rescale/rescale-int/internal/constants"
	inthttp "github.com/rescale/rescale-int/internal/http"
	"github.com/rescale/rescale-int/internal/progress"
	"github.com/rescale/rescale-int/internal/resources"
	"github.com/rescale/rescale-int/internal/transfer"
	"github.com/rescale/rescale-int/internal/util/glob"
)

// compatUploadItem implements transfer.WorkItem for compat upload.
type compatUploadItem struct {
	idx  int
	path string
	size int64
}

func (u compatUploadItem) FileSize() int64 { return u.size }

func newUploadCmd() *cobra.Command {
	var files []string
	var directoryID string
	var extendedOutput bool
	var report string
	var targets []string
	var copyToCFS bool

	cmd := &cobra.Command{
		Use:   "upload",
		Short: "Upload files",
		RunE: func(cmd *cobra.Command, args []string) error {
			if extendedOutput {
				return fmt.Errorf("'-e' (extended output) is not yet implemented in compat mode (planned for Plan 3)")
			}
			if report != "" {
				return fmt.Errorf("'-r' (report) is not yet implemented in compat mode")
			}
			if len(targets) > 0 {
				return fmt.Errorf("'-T' (target) is not yet implemented in compat mode")
			}
			if copyToCFS {
				return fmt.Errorf("'--copy-to-cfs' is not yet implemented in compat mode")
			}

			if len(files) == 0 {
				return fmt.Errorf("-f (--files) is required")
			}

			cc := GetCompatContext(cmd)
			client, err := cc.GetAPIClient(cmd.Context())
			if err != nil {
				return err
			}

			return compatUploadFiles(cmd.Context(), files, directoryID, client, cc)
		},
	}

	cmd.Flags().StringSliceVarP(&files, "files", "f", nil, "Files to upload (supports globs)")
	cmd.Flags().StringVarP(&directoryID, "directory-id", "d", "", "Destination directory ID")

	// Deferred flags
	cmd.Flags().BoolVarP(&extendedOutput, "extended-output", "e", false, "Extended JSON output")
	cmd.Flags().MarkHidden("extended-output")
	cmd.Flags().StringVarP(&report, "report", "r", "", "Report file")
	cmd.Flags().MarkHidden("report")
	cmd.Flags().StringSliceVarP(&targets, "Target", "T", nil, "Target storage")
	cmd.Flags().MarkHidden("Target")
	cmd.Flags().BoolVar(&copyToCFS, "copy-to-cfs", false, "Copy to CFS after upload")
	cmd.Flags().MarkHidden("copy-to-cfs")

	return cmd
}

// compatUploadFiles handles the upload logic for compat mode.
// Uses the same infrastructure as cli.UploadFilesWithIDs but avoids importing cli.
func compatUploadFiles(ctx context.Context, filePatterns []string, folderID string, apiClient *api.Client, cc *CompatContext) error {
	inthttp.WarmupProxyIfNeeded(ctx, apiClient.GetConfig())
	credentials.GetManager(apiClient).WarmAll(ctx)

	filePaths, err := glob.ExpandPatterns(filePatterns)
	if err != nil {
		return err
	}

	// Validate all files exist
	for _, filePath := range filePaths {
		info, err := os.Stat(filePath)
		if os.IsNotExist(err) {
			return fmt.Errorf("file not found: %s", filePath)
		}
		if err != nil {
			return fmt.Errorf("failed to stat %s: %w", filePath, err)
		}
		if info.IsDir() {
			return fmt.Errorf("'%s' is a directory, not a file", filePath)
		}
	}

	cc.Printf("Uploading %d file(s)\n", len(filePaths))

	resourceMgr := resources.NewManager(resources.Config{AutoScale: true})
	transferMgr := transfer.NewManager(resourceMgr)

	items := make([]compatUploadItem, len(filePaths))
	for i, fPath := range filePaths {
		var size int64
		if info, err := os.Stat(fPath); err == nil {
			size = info.Size()
		}
		items[i] = compatUploadItem{idx: i, path: fPath, size: size}
	}

	cfg := transfer.BatchConfig{
		MaxWorkers:  constants.DefaultMaxConcurrent,
		ResourceMgr: resourceMgr,
		Label:       "COMPAT-UPLOAD",
	}
	numWorkers := transfer.ComputedWorkers(items, cfg)

	// Create progress UI (skip in quiet mode)
	var uploadUI *progress.UploadUI
	if !cc.Quiet {
		uploadUI = progress.NewUploadUI(len(filePaths))
		defer uploadUI.Wait()
	}

	uploadedFileIDs := make([]string, len(filePaths))

	batchResult := transfer.RunBatch(ctx, items, cfg, func(ctx context.Context, item compatUploadItem) error {
		fPath := item.path
		fileInfo, _ := os.Stat(fPath)

		transferHandle := transferMgr.AllocateTransfer(item.size, numWorkers)

		var fileBar *progress.FileBar
		var barOnce sync.Once

		var progressCB func(float64)
		if uploadUI != nil {
			progressCB = func(fraction float64) {
				barOnce.Do(func() {
					fileBar = uploadUI.AddFileBar(fPath, folderID, fileInfo.Size())
				})
				if fileBar != nil {
					fileBar.UpdateProgress(fraction)
				}
			}
		}

		cloudFile, err := upload.UploadFile(ctx, upload.UploadParams{
			LocalPath:        fPath,
			FolderID:         folderID,
			APIClient:        apiClient,
			ProgressCallback: progressCB,
			TransferHandle:   transferHandle,
			PreEncrypt:       false,
		})

		if err != nil {
			if uploadUI != nil {
				if fileBar == nil {
					fileBar = uploadUI.AddFileBar(fPath, folderID, fileInfo.Size())
				}
				fileBar.Complete("", err)
			}
			return fmt.Errorf("failed to upload %s: %w", filepath.Base(fPath), err)
		}

		if uploadUI != nil {
			if fileBar == nil {
				fileBar = uploadUI.AddFileBar(fPath, folderID, fileInfo.Size())
			}
			fileBar.Complete(cloudFile.ID, nil)
		}

		uploadedFileIDs[item.idx] = cloudFile.ID
		return nil
	})

	if len(batchResult.Errors) > 0 {
		if len(batchResult.Errors) == 1 {
			return batchResult.Errors[0]
		}
		return fmt.Errorf("upload failed: %d file(s) failed (first error: %v)", len(batchResult.Errors), batchResult.Errors[0])
	}

	cc.Printf("Successfully uploaded %d file(s)\n", len(uploadedFileIDs))
	// Print file IDs as data output (not suppressed by quiet)
	for _, fileID := range uploadedFileIDs {
		if fileID != "" {
			fmt.Fprintln(os.Stdout, fileID)
		}
	}

	return nil
}

// compatUploadFilesReturnIDs uploads pre-validated file paths and returns file IDs.
// Used by submit to upload input files without printing individual IDs.
func compatUploadFilesReturnIDs(ctx context.Context, filePaths []string, folderID string, apiClient *api.Client, cc *CompatContext) ([]string, error) {
	inthttp.WarmupProxyIfNeeded(ctx, apiClient.GetConfig())
	credentials.GetManager(apiClient).WarmAll(ctx)

	resourceMgr := resources.NewManager(resources.Config{AutoScale: true})
	transferMgr := transfer.NewManager(resourceMgr)

	items := make([]compatUploadItem, len(filePaths))
	for i, fPath := range filePaths {
		var size int64
		if info, err := os.Stat(fPath); err == nil {
			size = info.Size()
		}
		items[i] = compatUploadItem{idx: i, path: fPath, size: size}
	}

	cfg := transfer.BatchConfig{
		MaxWorkers:  constants.DefaultMaxConcurrent,
		ResourceMgr: resourceMgr,
		Label:       "COMPAT-UPLOAD",
	}
	numWorkers := transfer.ComputedWorkers(items, cfg)

	var uploadUI *progress.UploadUI
	if !cc.Quiet {
		uploadUI = progress.NewUploadUI(len(filePaths))
		defer uploadUI.Wait()
	}

	uploadedFileIDs := make([]string, len(filePaths))

	batchResult := transfer.RunBatch(ctx, items, cfg, func(ctx context.Context, item compatUploadItem) error {
		fPath := item.path
		fileInfo, _ := os.Stat(fPath)

		transferHandle := transferMgr.AllocateTransfer(item.size, numWorkers)

		var fileBar *progress.FileBar
		var barOnce sync.Once

		var progressCB func(float64)
		if uploadUI != nil {
			progressCB = func(fraction float64) {
				barOnce.Do(func() {
					fileBar = uploadUI.AddFileBar(fPath, folderID, fileInfo.Size())
				})
				if fileBar != nil {
					fileBar.UpdateProgress(fraction)
				}
			}
		}

		cloudFile, err := upload.UploadFile(ctx, upload.UploadParams{
			LocalPath:        fPath,
			FolderID:         folderID,
			APIClient:        apiClient,
			ProgressCallback: progressCB,
			TransferHandle:   transferHandle,
			PreEncrypt:       false,
		})

		if err != nil {
			if uploadUI != nil {
				if fileBar == nil {
					fileBar = uploadUI.AddFileBar(fPath, folderID, fileInfo.Size())
				}
				fileBar.Complete("", err)
			}
			return fmt.Errorf("failed to upload %s: %w", filepath.Base(fPath), err)
		}

		if uploadUI != nil {
			if fileBar == nil {
				fileBar = uploadUI.AddFileBar(fPath, folderID, fileInfo.Size())
			}
			fileBar.Complete(cloudFile.ID, nil)
		}

		uploadedFileIDs[item.idx] = cloudFile.ID
		return nil
	})

	if len(batchResult.Errors) > 0 {
		return nil, batchResult.Errors[0]
	}

	return uploadedFileIDs, nil
}
