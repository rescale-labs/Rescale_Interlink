package api

import (
	"errors"
	"fmt"
	"testing"

	"github.com/rescale/rescale-int/internal/reporting"
)

// The platform's refusal of a new folder whose name its parent already holds,
// even by a folder in Trash, is a 400 whose answer opens with its refusal of a
// name already in use. Only that answer decides. It is recognised however it
// is wrapped, even by a folder named like a status, and explained in plain
// words after what the error says before it, never as an error report. Nothing
// else is taken for it: not a refusal that quotes a name holding the
// refusal's words, bare or wrapped by such a name, nor one that only repeats
// "duplicate", a file's refusal, which states no status, or the same words in
// a server error.
func TestFolderNameTaken(t *testing.T) {
	refusal := errors.New(`API request failed with status 400: "duplicate key value violates unique constraint \"folder_name_parent\""`)
	const plain = "a folder of that name already exists in this location, possibly in Trash " +
		"(restore it or delete it permanently from Trash in the Rescale web UI, or use another name)"
	for before, err := range map[string]error{
		"":                                 refusal,
		`failed to create folder "runs": `: fmt.Errorf("failed to create folder %q: %w", "runs", refusal),
		"failed to create folder HTTP 500, rerun: ": fmt.Errorf("failed to create folder %s: %w", "HTTP 500, rerun", refusal),
		"failed to create folder 404 Not Found: ":   fmt.Errorf("failed to create folder %s: %w", "404 Not Found", refusal),
	} {
		explained := ExplainFolderNameTaken(err)
		if !IsFolderNameTakenError(err) || explained.Error() != before+plain || reporting.IsReportable(explained, reporting.CategoryTransfer) {
			t.Errorf("%v was explained as %q (reportable: %v), want %q, not reportable",
				err, explained, reporting.IsReportable(explained, reporting.CategoryTransfer), before+plain)
		}
	}

	quoting := errors.New(`API request failed with status 400: {"name":["not a valid name: duplicate key value violates unique constraint"]}`)
	for _, other := range []error{
		nil,
		quoting,
		fmt.Errorf("failed to create folder %s: %w", "duplicate key value violates unique constraint", quoting),
		errors.New(`API request failed with status 400: {"name":["not a valid name: duplicate?"]}`),
		fmt.Errorf("%w: %s", ErrFileAlreadyExists, `"duplicate key value violates unique constraint \"file_name_folder\""`),
		errors.New(`API request failed with status 500: "duplicate key value violates unique constraint \"other\""`),
	} {
		if IsFolderNameTakenError(other) || ExplainFolderNameTaken(other) != other {
			t.Errorf("%v was taken for a folder name already in use", other)
		}
	}
}
