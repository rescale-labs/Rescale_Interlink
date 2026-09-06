package filescan

import (
	"fmt"

	"github.com/rescale/rescale-int/internal/models"
)

// BuildJobs turns one template and a scan's file sets into the jobs to submit.
//
// The whole of file-scan mode's job assembly: the templates are validated once,
// each file set is rendered into its own command and name, the names are checked
// for collisions, and the template is copied per job with the file set attached.
// The CLI and the GUI both go through it, so a scan started from either produces
// the same jobs, the same skips and the same refusals.
//
// skipped names the files that could not be rendered, one line each, and is not
// an error: a filename that cannot be substituted safely costs that file, not
// the other 199. err is reserved for what condemns the batch — a template no
// file can render, or two files that would submit under one name.
func BuildJobs(template models.JobSpec, found []JobFiles) (jobs []models.JobSpec, skipped []string, warnings []string, err error) {
	// Checked once, before any job is built: a command whose tokens are wrong is
	// wrong for every file, and a typo must not become a batch of jobs each
	// carrying a literal "{{bse}}" on its command line.
	warnings, err = ValidateCommandTemplate(template.Command)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := ValidateJobNameTemplate(template.JobName); err != nil {
		return nil, nil, nil, err
	}

	// Two jobs answering to one name misroute each other's progress and state
	// updates — see ValidateJobNameTemplate. A collision fails the batch rather
	// than dropping the second file, as it fails DOE generation: handing back a
	// batch quietly smaller than the one scanned for is the worse answer.
	seenNames := make(map[string]string, len(found))

	for i, jf := range found {
		// Under {{base}} the colliding files share a base name, so naming them
		// that way reads as one file colliding with itself; the parent folder is
		// what tells the two apart.
		display := displayPath(jf.PrimaryDir, jf.PrimaryFile)

		command, jobName, renderErr := Render(template.Command, template.JobName, jf, i+1)
		if renderErr != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %v", display, renderErr))
			continue
		}
		if first, dup := seenNames[jobName]; dup {
			return nil, nil, nil, fmt.Errorf("%s and %s both render to job name %q; "+
				"add {{index}} or {{dir}} to the job name template to keep names unique",
				first, display, jobName)
		}
		seenNames[jobName] = display

		job := template
		job.Command = command
		job.JobName = jobName
		job.Directory = jf.PrimaryDir
		// There is no directory walk in this mode for a subpath inherited from a
		// loaded template to apply to; left set it would fail every job at the
		// tar stage, and the GUI has no field in this mode to clear it.
		job.TarSubpath = ""
		// The job's archive is exactly its own files, wherever they live: a
		// secondary pattern can resolve outside PrimaryDir. InputFiles means IDs
		// of files already on Rescale, which these are not.
		job.LocalInputFiles = jf.InputFiles
		job.InputFiles = nil

		jobs = append(jobs, job)
	}

	return jobs, skipped, warnings, nil
}
