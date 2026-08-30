package doe

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
)

// maxCasesCSVBytes bounds the input a cases file may carry. A sweep of the
// largest allowed size is a few hundred kilobytes of CSV, so anything past this
// is a file that was pointed at the flag by mistake rather than a design.
const maxCasesCSVBytes = 4 << 20

// ParseCasesCSV reads explicit cases from a CSV whose header names the
// parameters. Returns the parameter names in column order and one map per row.
//
// This is the single parser behind both the CLI's --cases-csv and the GUI's
// pasted-cases box, so the same text means the same sweep on either surface.
// Quoting, embedded commas and the field-count check are encoding/csv's, which
// reports the offending line number.
func ParseCasesCSV(r io.Reader) ([]string, []map[string]string, error) {
	limited := &io.LimitedReader{R: r, N: maxCasesCSVBytes + 1}
	reader := csv.NewReader(limited)

	tooBig := func() error {
		return fmt.Errorf("cases CSV is larger than %d bytes", maxCasesCSVBytes)
	}

	header, err := reader.Read()
	if err != nil {
		if limited.N <= 0 {
			return nil, nil, tooBig()
		}
		if errors.Is(err, io.EOF) {
			return nil, nil, fmt.Errorf("cases CSV is empty; it needs a header row and at least one case")
		}
		return nil, nil, fmt.Errorf("failed to read cases CSV: %w", err)
	}

	names := make([]string, 0, len(header))
	seen := make(map[string]bool, len(header))
	for _, column := range header {
		name := strings.TrimSpace(column)
		if name == "" {
			return nil, nil, fmt.Errorf("cases CSV has an unnamed column")
		}
		if seen[name] {
			return nil, nil, fmt.Errorf("cases CSV names column %q more than once", name)
		}
		seen[name] = true
		names = append(names, name)
	}

	var cases []map[string]string
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if limited.N <= 0 {
				return nil, nil, tooBig()
			}
			return nil, nil, fmt.Errorf("failed to read cases CSV: %w", err)
		}

		// Stopping at the ceiling rather than parsing on: the sweep would be
		// rejected anyway, and the rows past here cost memory to reach that answer.
		if len(cases) >= absoluteMaxCases {
			return nil, nil, fmt.Errorf("cases CSV has more than %d cases, which is beyond what could "+
				"be submitted as one sweep", absoluteMaxCases)
		}

		values := make(map[string]string, len(names))
		for j, name := range names {
			values[name] = strings.TrimSpace(record[j])
		}
		cases = append(cases, values)
	}

	if limited.N <= 0 {
		return nil, nil, tooBig()
	}
	if len(cases) == 0 {
		return nil, nil, fmt.Errorf("cases CSV has a header but no cases")
	}

	return names, cases, nil
}
