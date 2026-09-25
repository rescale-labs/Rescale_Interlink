package cli

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/cloud/upload"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/reporting"
)

// Each file's error is kept and weighed on its own, so a genuine failure is
// reported whether another transfer's lock refusal or a cancel came before or
// after it, and refusals alone are not. A cancel that lands during a file stops
// the batch before the next one starts, which leaves the upload unfinished.
func TestUploadFilesWeighsEachFailure(t *testing.T) {
	locked := fmt.Errorf("S3Storage upload failed: failed to acquire upload lock: %w", state.ErrUploadLocked)
	broken := errors.New("upload finished without a file record")
	for name, tc := range map[string]struct {
		results     []error // what each file's upload returns, in turn (nil: uploaded)
		cancelAfter int     // the upload during which the user cancels
		report      bool
	}{
		"refused twice":           {results: []error{locked, locked}},
		"refused, then failed":    {results: []error{locked, broken}, report: true},
		"failed, then refused":    {results: []error{broken, locked}, report: true},
		"failed, then canceled":   {results: []error{broken, nil}, cancelAfter: 1, report: true},
		"uploaded, then canceled": {results: []error{nil, nil}, cancelAfter: 1},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			defer func(orig func(context.Context, upload.UploadParams) (*models.CloudFile, error)) { uploadFileFn = orig }(uploadFileFn)
			calls := 0
			uploadFileFn = func(context.Context, upload.UploadParams) (*models.CloudFile, error) {
				if calls++; calls == tc.cancelAfter {
					cancel()
				}
				if err := tc.results[calls-1]; err != nil {
					return nil, err
				}
				return &models.CloudFile{ID: "uploaded"}, nil
			}
			// Warming credentials first is best effort: an API nobody answers is fine.
			apiClient := api.NewClientForTest(&config.Config{APIBaseURL: "http://127.0.0.1:1", APIKey: "test"})
			var err error
			captureStdout(t, func() {
				_, err = UploadFilesWithIDs(ctx, []string{writeUploadFixture(t, "a.bin", 16), writeUploadFixture(t, "b.bin", 16)},
					"", 1, false, nil, apiClient, GetLogger(), true)
			})

			kept := append([]error(nil), tc.results[:calls]...)
			if tc.cancelAfter > 0 {
				kept = append(kept, context.Canceled)
			}
			for _, want := range kept {
				if want != nil && !errors.Is(err, want) {
					t.Errorf("returned %v, which lost %v", err, want)
				}
			}
			reportable := reporting.IsReportable(err, reporting.CategoryTransfer)
			if class := reporting.Classify(err, reporting.CategoryTransfer, "", "").ErrorClass; reportable != tc.report ||
				tc.report && class != reporting.ClassInternal {
				t.Errorf("returned %v: reportable %v, as %s; want reportable %v", err, reportable, class, tc.report)
			}
		})
	}
}
