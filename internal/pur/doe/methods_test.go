package doe

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/models"
)

// MethodInfo is descriptive metadata about behaviour implemented elsewhere, so
// every test here checks it against the code it describes rather than against a
// second copy of the same expectations.

func TestMethods_CoverEveryMethodInOrder(t *testing.T) {
	infos := Methods()
	all := AllMethods()

	if len(infos) != len(all) {
		t.Fatalf("Methods() describes %d methods but AllMethods() lists %d", len(infos), len(all))
	}

	for i := range all {
		if infos[i].Method != all[i] {
			t.Errorf("Methods()[%d] = %q, want %q", i, infos[i].Method, all[i])
		}
		if infos[i].Label == "" {
			t.Errorf("method %q has no label", infos[i].Method)
		}
		if infos[i].Description == "" {
			t.Errorf("method %q has no description", infos[i].Method)
		}
		if infos[i].MinParameters < 1 {
			t.Errorf("method %q has MinParameters %d, want at least 1", infos[i].Method, infos[i].MinParameters)
		}
	}
}

func TestMethodInfoFor(t *testing.T) {
	info, ok := MethodInfoFor(MethodSobol)
	if !ok {
		t.Fatal("MethodInfoFor(sobol) reported not found")
	}
	if info.MaxParameters != sobolMaxDimensions {
		t.Errorf("sobol MaxParameters = %d, want %d", info.MaxParameters, sobolMaxDimensions)
	}

	if _, ok := MethodInfoFor(Method("nonsense")); ok {
		t.Error("MethodInfoFor reported an unknown method as found")
	}
}

// Every MethodInfo field is a claim about what validation does, checked against
// validation itself rather than against a second copy of the expectations.
func TestMethods_MetadataAgreesWithValidation(t *testing.T) {
	for _, info := range Methods() {
		// UsesLevels drives whether a UI offers a level count, so it has to match
		// the predicate validation actually uses.
		if info.UsesLevels != usesLevels(info.Method) {
			t.Errorf("method %q: UsesLevels = %v, but usesLevels() = %v",
				info.Method, info.UsesLevels, usesLevels(info.Method))
		}

		// UsesSamples means "Samples must be set", which is what validation enforces.
		opts := probeOptions(info, info.MinParameters)
		opts.Samples = 0

		rejected := hasCode(validateMethodRequirements(opts), CodeBadSamples)
		if rejected != info.UsesSamples {
			t.Errorf("method %q: UsesSamples = %v, but Samples=0 rejected = %v",
				info.Method, info.UsesSamples, rejected)
		}

		if info.UsesSamples {
			opts.Samples = 1
			if hasCode(validateMethodRequirements(opts), CodeBadSamples) {
				t.Errorf("method %q rejected Samples=1", info.Method)
			}
		}

		// UsesCases means "Cases must be supplied".
		opts = probeOptions(info, info.MinParameters)
		opts.Cases = nil

		rejected = hasCode(validateMethodRequirements(opts), CodeNoCases)
		if rejected != info.UsesCases {
			t.Errorf("method %q: UsesCases = %v, but omitting Cases rejected = %v",
				info.Method, info.UsesCases, rejected)
		}

		// MinParameters and MaxParameters are the bounds a UI offers, so one step
		// outside each must be rejected and each bound itself accepted.
		if info.MinParameters > 1 {
			opts = probeOptions(info, info.MinParameters-1)
			if !hasCode(validateMethodRequirements(opts), CodeTooFewParams) {
				t.Errorf("method %q accepted %d parameters despite MinParameters %d",
					info.Method, info.MinParameters-1, info.MinParameters)
			}
		}

		opts = probeOptions(info, info.MinParameters)
		if hasCode(validateMethodRequirements(opts), CodeTooFewParams) {
			t.Errorf("method %q rejected its own minimum of %d parameters",
				info.Method, info.MinParameters)
		}

		if info.MaxParameters > 0 {
			opts = probeOptions(info, info.MaxParameters)
			if hasCode(validateMethodRequirements(opts), CodeBadMethod) {
				t.Errorf("method %q rejected its own maximum of %d parameters",
					info.Method, info.MaxParameters)
			}

			opts = probeOptions(info, info.MaxParameters+1)
			if !hasCode(validateMethodRequirements(opts), CodeBadMethod) {
				t.Errorf("method %q accepted %d parameters despite MaxParameters %d",
					info.Method, info.MaxParameters+1, info.MaxParameters)
			}
		}
	}
}

// A method described as valid for MinParameters must actually generate a sweep
// at that size, which is the claim a UI relies on when it offers the method.
func TestMethods_GenerateAtMinimumParameters(t *testing.T) {
	for _, info := range Methods() {
		t.Run(string(info.Method), func(t *testing.T) {
			opts := probeOptions(info, info.MinParameters)

			result := Generate(opts)
			if !result.OK() {
				t.Fatalf("Generate failed for %q: %v", info.Method, result.Errors)
			}
			if len(result.Cases) == 0 {
				t.Errorf("method %q generated no cases", info.Method)
			}
		})
	}
}

// probeOptions builds a valid sweep of n parameters for one method, with every
// field that method reads populated.
func probeOptions(info MethodInfo, n int) Options {
	params := make([]Parameter, n)
	tokens := make([]string, n)
	values := make(map[string]string, n)

	for i := range params {
		name := fmt.Sprintf("p%d", i+1)
		params[i] = Parameter{Name: name, Min: 1, Max: 2, Levels: 2}
		tokens[i] = fmt.Sprintf("-%s {{%s}}", name, name)
		values[name] = "1"
	}

	template := models.JobSpec{
		JobName: "probe",
		Command: "solve " + strings.Join(tokens, " "),
	}

	opts := Options{
		Template:     template,
		Parameters:   params,
		Method:       info.Method,
		Samples:      4,
		CenterPoints: 1,
	}
	if info.UsesCases {
		opts.Cases = []map[string]string{values}
	}
	return withDefaults(opts)
}
