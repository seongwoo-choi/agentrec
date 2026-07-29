package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

// comparisonHeader opens the comparison, so what follows is never read as one
// run's report.
const comparisonHeader = "SHADOW COMPARISON"

// notRun is what a runner is shown as when its leg never started: an interrupted
// or abandoned comparison says which side is missing rather than leaving the
// reader to notice.
const notRun = "(not run)"

// renderComparison writes the recorded runs side by side, in the fixed runner
// order and with each leg's fields in a fixed order of their own, so that two
// operators who recorded the same two runs read the same comparison whichever
// order they asked for them in.
//
// Every field is read back out of the persisted bundle rather than taken from
// what this process still holds: what a comparison is between is the evidence
// two runs left, which is what an operator can go back to afterwards.
//
// It compares and does not judge. Which run to prefer is a decision made from
// this evidence together with everything the evidence does not hold, and a
// comparison that announced a winner would be announcing one without it.
func renderComparison(w io.Writer, runsRoot string, legs []leg) error {
	if _, err := fmt.Fprintf(w, "%s\n", comparisonHeader); err != nil {
		return err
	}
	for _, name := range shadowRunners {
		if _, err := fmt.Fprintf(w, "\n%s\n", name); err != nil {
			return err
		}
		l, ok := legRun(legs, name)
		if !ok {
			if _, err := fmt.Fprintf(w, "  %s\n", notRun); err != nil {
				return err
			}
			continue
		}
		fields, err := comparisonFields(runsRoot, l)
		if err != nil {
			return err
		}
		for _, f := range fields {
			if _, err := fmt.Fprintln(w, comparisonField(f.name, f.value)); err != nil {
				return err
			}
		}
	}
	return renderSequenceNote(w, legs)
}

// sequenceNote says what the Order field means, so the two results are not read
// as though they were produced under identical conditions. The legs are
// serialized and nothing between them is reset, which is a property of the
// comparison and not a caveat about one of the runs — so it is stated once, at
// the end, rather than repeated inside each block.
const sequenceNote = "The legs ran in the Order shown, one after another. Provider authentication,\n" +
	"caches, rate limits and any network service both agents use are not reset\n" +
	"between them, so a later leg may observe what an earlier one left."

// renderSequenceNote states the ordering caveat, and only when more than one leg
// actually ran: a comparison with a single recorded run has no sequence for it
// to be about.
func renderSequenceNote(w io.Writer, legs []leg) error {
	if len(legs) < 2 {
		return nil
	}
	_, err := fmt.Fprintf(w, "\n%s\n", sequenceNote)
	return err
}

// legRun reports one runner's leg, if that leg ran at all.
func legRun(legs []leg, name string) (leg, bool) {
	for _, l := range legs {
		if l.runner == name {
			return l, true
		}
	}
	return leg{}, false
}

// field is one line of a leg's summary. The slice of them is built in one fixed
// order and never from a map, so the same evidence always renders the same way.
type field struct {
	name  string
	value string
}

// comparisonFields summarizes one recorded run out of its own bundle, in the
// order a reader compares two legs in: which run this is, what judged it, how
// its process ended, what it left in its checkout, and how much it did. A
// document the run never wrote is reported as the absence it is rather than as
// a measured zero.
func comparisonFields(runsRoot string, l leg) ([]field, error) {
	runID := l.runID
	dir, err := runDir(runsRoot, runID)
	if err != nil {
		return nil, err
	}
	manifest, err := readManifest(dir)
	if err != nil {
		return nil, err
	}
	if err := validateUnparsedStream(dir, manifest.UnparsedLines); err != nil {
		return nil, err
	}
	result, err := readProcessResult(dir)
	if err != nil {
		return nil, err
	}
	git, err := readGitResult(dir)
	if err != nil {
		return nil, err
	}
	verification, err := readVerification(dir)
	if err != nil {
		return nil, err
	}
	actions, err := readActions(dir)
	if err != nil {
		return nil, err
	}

	// Second, right under the run it identifies: what conditions a leg ran under
	// is the first thing a reader needs before comparing anything else about it.
	fields := []field{{"Run ID", runID}, {"Order", strconv.Itoa(l.order)}}
	if verification == nil {
		fields = append(fields, field{"Verification", none})
	} else {
		fields = append(fields,
			field{"Verification", verdict(verification.Status)},
			field{"Config SHA-256", verification.ConfigSHA256},
		)
	}
	fields = append(fields, field{"Exit Reason", exitReason(manifest, result)})
	if result != nil && result.ExitCode != nil {
		fields = append(fields, field{"Exit Code", strconv.Itoa(*result.ExitCode)})
	}
	if result != nil && result.Signal != "" {
		fields = append(fields, field{"Signal", result.Signal})
	}
	return append(fields,
		field{"Duration", runDuration(manifest, result)},
		field{"Repository", repositorySummary(git)},
		field{"Actions", strconv.Itoa(len(actions))},
		field{"Warnings", strconv.Itoa(manifest.WarningCount)},
		field{"Unparsed", strconv.Itoa(manifest.UnparsedLines)},
	), nil
}

// none states a document the run never wrote, so an absence is never read as a
// measurement.
const none = "(none)"

// repositorySummary reduces what a run left in its checkout to one line: the
// status it was recorded under, and the counts only when the measurement that
// produced them finished. A capture that did not finish holds zeros it never
// measured, and a zero read as a measurement is a run reported to have changed
// nothing.
func repositorySummary(res *gitResult) string {
	if res == nil {
		return none
	}
	status := strings.ToUpper(res.Status)
	if res.Reason != "" {
		status += "  " + res.Reason
	}
	if res.Status != gitAvailable {
		return status
	}
	return fmt.Sprintf("%s  %d files (%d tracked, %d untracked)  +%d/-%d, %d binary",
		status, res.TrackedFiles+res.UntrackedFiles, res.TrackedFiles, res.UntrackedFiles,
		res.Added, res.Deleted, res.BinaryTracked)
}

// comparisonField renders one name/value pair in the same aligned column a
// rendered run uses, with the recorded value made safe to print: it was read
// back off disk, where anything could have replaced it, and a value holding
// control characters would otherwise forge lines or drive the terminal.
func comparisonField(name, value string) string {
	return strings.TrimRight(fmt.Sprintf("  %-12s %s", name, oneLine(value)), " ")
}
