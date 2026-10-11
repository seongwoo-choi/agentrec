package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const deliveryFile = "delivery.json"
const deliveryMaxBytes = 256 << 10
const deliveryMaxRecords = 32

type deliveryDocument struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Associations  []deliveryAssociation `json:"associations"`
}
type deliveryAssociation struct {
	Commit      string               `json:"commit,omitempty"`
	PR          string               `json:"pr,omitempty"`
	ObservedAt  time.Time            `json:"observedAt"`
	Observation *deliveryObservation `json:"observation,omitempty"`
}
type deliveryObservation struct {
	State      string     `json:"state"`
	HeadSha    string     `json:"headSha"`
	MergeSha   string     `json:"mergeSha,omitempty"`
	ObservedAt time.Time  `json:"observedAt"`
	HeadCI     deliveryCI `json:"headCI"`
	MergeCI    deliveryCI `json:"mergeCI"`
}
type deliveryCI struct {
	TargetSha  string `json:"targetSha,omitempty"`
	Status     string `json:"status"`
	CheckCount int    `json:"checkCount,omitempty"`
}

var deliverySHA = regexp.MustCompile(`^(?:[a-fA-F0-9]{40}|[a-fA-F0-9]{64})$`)
var deliveryPR = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9][A-Za-z0-9-]{0,38})/([A-Za-z0-9_.-]{1,100})/pull/([1-9][0-9]*)$`)

func validateDeliveryPR(value string) (int, error) {
	m := deliveryPR.FindStringSubmatch(value)
	if m == nil || m[2] == "." || m[2] == ".." {
		return 0, errors.New("cli: invalid GitHub PR URL")
	}
	n, err := strconv.Atoi(m[3])
	if err != nil {
		return 0, errors.New("cli: invalid PR number")
	}
	return n, nil
}
func validDeliveryState(s string) bool { return s == "OPEN" || s == "CLOSED" || s == "MERGED" }
func decodeDelivery(raw []byte) (*deliveryDocument, error) {
	if raw == nil {
		return nil, nil
	}
	if len(raw) > deliveryMaxBytes {
		return nil, errors.New("cli: delivery exceeds byte limit")
	}
	if err := rejectDuplicateJSONMembers(raw); err != nil {
		return nil, err
	}
	var doc deliveryDocument
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	if doc.SchemaVersion != 1 || len(doc.Associations) == 0 || len(doc.Associations) > deliveryMaxRecords {
		return nil, errors.New("cli: invalid delivery schema or history length")
	}
	for _, a := range doc.Associations {
		if a.ObservedAt.IsZero() || (a.Commit == "" && a.PR == "") || (a.Commit != "" && !deliverySHA.MatchString(a.Commit)) {
			return nil, errors.New("cli: invalid delivery association")
		}
		if a.PR == "" {
			if a.Observation != nil {
				return nil, errors.New("cli: observation without PR")
			}
			continue
		}
		if _, err := validateDeliveryPR(a.PR); err != nil {
			return nil, err
		}
		o := a.Observation
		if o == nil || !validDeliveryState(o.State) || !deliverySHA.MatchString(o.HeadSha) || (o.MergeSha != "" && !deliverySHA.MatchString(o.MergeSha)) || o.ObservedAt.IsZero() || o.HeadCI.TargetSha != o.HeadSha || o.HeadCI.CheckCount < 0 || o.HeadCI.CheckCount > 100 || o.MergeCI.Status != "unobserved" || o.MergeCI.TargetSha != "" || o.MergeCI.CheckCount != 0 {
			return nil, errors.New("cli: invalid delivery observation")
		}
		switch o.HeadCI.Status {
		case "missing", "incomplete", "pending", "success", "failure":
		default:
			return nil, errors.New("cli: invalid CI status")
		}
		if (o.HeadCI.CheckCount == 0) != (o.HeadCI.Status == "missing") || (o.HeadCI.CheckCount == 100 && o.HeadCI.Status == "success") {
			return nil, errors.New("cli: CI state is inconsistent with observed check count")
		}
	}
	return &doc, nil
}
func readDeliveryDocument(root *os.Root) (*deliveryDocument, error) {
	f, err := openRegularFromRoot(root, deliveryFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, deliveryMaxBytes+1))
	if err != nil {
		return nil, err
	}
	return decodeDelivery(raw)
}

// The executor is read-only argv, deadline-bound, and never exposes stderr.
var deliveryGH = executeDeliveryGH

type deliveryBoundedBuffer struct{ bytes.Buffer }

func (b *deliveryBoundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > deliveryMaxBytes {
		return 0, errors.New("output limit")
	}
	return b.Buffer.Write(p)
}
func executeDeliveryGH(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	var out deliveryBoundedBuffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return nil, errors.New("cli: GitHub observation failed")
	}
	return out.Bytes(), nil
}
func observeDeliveryPR(url string) (*deliveryObservation, error) {
	number, err := validateDeliveryPR(url)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	raw, err := deliveryGH(ctx, "pr", "view", url, "--json", "url,number,state,headRefOid,mergeCommit,statusCheckRollup")
	if err != nil {
		return nil, errors.New("cli: GitHub observation failed")
	}
	if len(raw) > deliveryMaxBytes {
		return nil, errors.New("cli: GitHub observation exceeds limit")
	}
	if err = rejectDuplicateJSONMembers(raw); err != nil {
		return nil, errors.New("cli: malformed GitHub observation")
	}
	var pr struct {
		URL    string `json:"url"`
		Number int    `json:"number"`
		State  string `json:"state"`
		Head   string `json:"headRefOid"`
		Merge  *struct {
			OID string `json:"oid"`
		} `json:"mergeCommit"`
		Checks []struct {
			Type       string `json:"__typename"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			State      string `json:"state"`
		} `json:"statusCheckRollup"`
	}
	if err = json.Unmarshal(raw, &pr); err != nil {
		return nil, errors.New("cli: malformed GitHub observation")
	}
	var checkFields struct {
		Checks []map[string]json.RawMessage `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(raw, &checkFields); err != nil {
		return nil, errors.New("cli: invalid check fields")
	}
	for _, fields := range checkFields.Checks {
		for _, key := range []string{"__typename", "status", "conclusion", "state"} {
			if value, ok := fields[key]; ok {
				var text string
				if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &text) != nil {
					return nil, errors.New("cli: invalid check field type")
				}
			}
		}
	}
	// Exact canonical URL also pins the returned owner/repository, not only number.
	if pr.URL != url || pr.Number != number || !validDeliveryState(pr.State) || !deliverySHA.MatchString(pr.Head) || len(pr.Checks) > 100 {
		return nil, errors.New("cli: GitHub identity/state/check bounds mismatch")
	}
	o := &deliveryObservation{State: pr.State, HeadSha: pr.Head, ObservedAt: time.Now().UTC(), HeadCI: deliveryCI{TargetSha: pr.Head, Status: "missing", CheckCount: len(pr.Checks)}, MergeCI: deliveryCI{Status: "unobserved"}}
	if pr.Merge != nil {
		if !deliverySHA.MatchString(pr.Merge.OID) {
			return nil, errors.New("cli: invalid merge SHA")
		}
		o.MergeSha = pr.Merge.OID
	}
	if len(pr.Checks) == 0 {
		return o, nil
	}
	status := "success"
	for _, c := range pr.Checks {
		result := "incomplete"
		switch c.Type {
		case "CheckRun":
			switch c.Conclusion {
			case "", "SUCCESS", "NEUTRAL", "SKIPPED", "FAILURE", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE":
			default:
				return nil, errors.New("cli: invalid check conclusion")
			}
			switch c.Status {
			case "QUEUED", "IN_PROGRESS", "WAITING", "PENDING", "REQUESTED":
				result = "pending"
			case "COMPLETED":
				switch c.Conclusion {
				case "SUCCESS":
					result = "success"
				case "NEUTRAL", "SKIPPED":
					result = "incomplete"
				case "FAILURE", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE":
					result = "failure"
				case "":
				default:
					return nil, errors.New("cli: invalid check conclusion")
				}
			case "":
			default:
				return nil, errors.New("cli: invalid check status")
			}
		case "StatusContext":
			switch c.State {
			case "SUCCESS":
				result = "success"
			case "FAILURE", "ERROR":
				result = "failure"
			case "PENDING", "EXPECTED":
				result = "pending"
			case "":
			default:
				return nil, errors.New("cli: invalid status context")
			}
		default:
			return nil, errors.New("cli: invalid check type")
		}
		rank := map[string]int{"success": 0, "incomplete": 1, "pending": 2, "failure": 3}
		if rank[result] > rank[status] {
			status = result
		}
	}
	// gh's bounded rollup can be truncated at 100 contexts; do not assert all checks passed.
	if len(pr.Checks) == 100 && status == "success" {
		status = "incomplete"
	}
	o.HeadCI.Status = status
	return o, nil
}
func addDelivery(root, runID, commit, pr string) (*deliveryDocument, error) {
	if commit == "" && pr == "" {
		return nil, errors.New("cli: at least one commit or PR is required")
	}
	if commit != "" && !deliverySHA.MatchString(commit) {
		return nil, errors.New("cli: commit must be an exact full 40 or 64 hexadecimal SHA")
	}
	if pr != "" {
		if _, err := validateDeliveryPR(pr); err != nil {
			return nil, err
		}
	}
	if err := checkRunID(runID); err != nil {
		return nil, err
	}
	rr, err := openRunRoot(root, runID)
	if err != nil {
		return nil, err
	}
	m, err := readManifestFromRoot(rr)
	if err != nil {
		rr.Close()
		return nil, err
	}
	if err = runOpenFromRoot(root, rr, m); err != nil {
		rr.Close()
		return nil, err
	}
	lock, err := acquirePosthocVerificationLock(rr)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err = requireInstalledRun(root, runID, rr); err != nil {
		return nil, err
	}
	doc, err := readDeliveryDocument(rr)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		doc = &deliveryDocument{SchemaVersion: 1}
	}
	if len(doc.Associations) >= deliveryMaxRecords {
		return nil, errors.New("cli: delivery history is full")
	}
	a := deliveryAssociation{Commit: commit, PR: pr, ObservedAt: time.Now().UTC()}
	if pr != "" {
		a.Observation, err = observeDeliveryPR(pr)
		if err != nil {
			return nil, err
		}
		a.ObservedAt = a.Observation.ObservedAt
	}
	doc.Associations = append(doc.Associations, a)
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	if _, err = decodeDelivery(raw); err != nil {
		return nil, err
	}
	tmp := ".delivery-" + newViewToken()
	if err = writePosthocFile(rr, tmp, raw); err != nil {
		return nil, err
	}
	defer rr.Remove(tmp)
	// Observation can block on GitHub; retain the selected run identity across it.
	if err = requireInstalledRun(root, runID, rr); err != nil {
		return nil, err
	}
	if err = rr.Rename(tmp, deliveryFile); err != nil {
		return nil, err
	}
	if err = syncPosthocDirectory(rr, "."); err != nil {
		return nil, err
	}
	return doc, nil
}
func runDelivery(args []string, stdout, stderr io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(stderr, err); return exitFailure }
	if len(args) < 2 {
		return fail(errors.New("usage: agentrec delivery add RUN [--commit FULL_SHA] [--pr URL] | delivery show RUN [--json]"))
	}
	root, err := runsRoot()
	if err != nil {
		return fail(err)
	}
	var doc *deliveryDocument
	switch args[0] {
	case "add":
		var commit, pr string
		seen := map[string]bool{}
		for i := 2; i < len(args); i += 2 {
			if i+1 >= len(args) || seen[args[i]] {
				return fail(errors.New("cli: invalid delivery arguments"))
			}
			seen[args[i]] = true
			switch args[i] {
			case "--commit":
				commit = args[i+1]
			case "--pr":
				pr = args[i+1]
			default:
				return fail(errors.New("cli: invalid delivery option"))
			}
			if args[i+1] == "" {
				return fail(errors.New("cli: empty delivery value"))
			}
		}
		doc, err = addDelivery(root, args[1], commit, pr)
	case "show":
		if len(args) > 3 || (len(args) == 3 && args[2] != "--json") {
			return fail(errors.New("cli: invalid delivery show arguments"))
		}
		if err = checkRunID(args[1]); err != nil {
			return fail(err)
		}
		rr, e := openRunRoot(root, args[1])
		if e != nil {
			return fail(e)
		}
		defer rr.Close()
		doc, err = readDeliveryDocument(rr)
	default:
		return fail(errors.New("cli: unknown delivery operation"))
	}
	if err != nil {
		return fail(err)
	}
	if args[0] == "show" && len(args) == 3 {
		if err = json.NewEncoder(stdout).Encode(doc); err != nil {
			return fail(err)
		}
	} else {
		fmt.Fprintln(stdout, "Explicit delivery association (not causality); later observation:")
		if doc == nil {
			fmt.Fprintln(stdout, "unobserved")
		} else {
			for _, a := range doc.Associations {
				fmt.Fprintf(stdout, "%s %s\n", a.ObservedAt.Format(time.RFC3339), strings.TrimSpace(a.Commit+" "+a.PR))
				if a.Observation != nil {
					fmt.Fprintf(stdout, "  PR %s; head CI %s at %s; merge CI unobserved\n", a.Observation.State, a.Observation.HeadCI.Status, a.Observation.HeadSha)
				}
			}
		}
	}
	return 0
}
