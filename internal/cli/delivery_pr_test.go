package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeliveryPRObservation(t *testing.T) {
	root := home(t)
	writeRun(t, root, "delivery-run", "claude", early, "completed")
	old := deliveryGH
	t.Cleanup(func() { deliveryGH = old })
	url := "https://github.com/owner/repo/pull/12"
	sha := strings.Repeat("b", 40)
	calls := 0
	deliveryGH = func(ctx context.Context, args ...string) ([]byte, error) {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing deadline")
		}
		if strings.Join(args, " ") != "pr view "+url+" --json url,number,state,headRefOid,mergeCommit,statusCheckRollup" {
			t.Fatalf("unexpected argv %q", args)
		}
		return []byte(fmt.Sprintf(`{"url":%q,"number":12,"state":"MERGED","headRefOid":%q,"mergeCommit":{"oid":%q},"statusCheckRollup":[]}`, url, sha, strings.Repeat("c", 40))), nil
	}
	var out, stderr bytes.Buffer
	if code := Run([]string{"delivery", "add", "delivery-run", "--pr", url}, &out, &stderr); code != 0 {
		t.Fatalf("PR add %d %s", code, &stderr)
	}
	raw, err := os.ReadFile(filepath.Join(root, "delivery-run", deliveryFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"status":"missing"`)) || !bytes.Contains(raw, []byte(`"targetSha":"`+sha+`"`)) || !bytes.Contains(raw, []byte(`"mergeCI":{"status":"unobserved"}`)) {
		t.Fatalf("wrong CI attribution %s", raw)
	}
	deliveryGH = func(context.Context, ...string) ([]byte, error) { return nil, errors.New("TOKEN-secret") }
	stderr.Reset()
	if Run([]string{"delivery", "add", "delivery-run", "--pr", url}, &out, &stderr) == 0 {
		t.Fatal("failed gh accepted")
	}
	if strings.Contains(stderr.String(), "TOKEN-secret") {
		t.Fatal("raw error leaked")
	}
	after, _ := os.ReadFile(filepath.Join(root, "delivery-run", deliveryFile))
	if !bytes.Equal(raw, after) {
		t.Fatal("failed fetch changed history")
	}
	if calls != 1 {
		t.Fatalf("calls %d", calls)
	}
}

func TestDeliveryPRValidationAndChecks(t *testing.T) {
	root := home(t)
	writeRun(t, root, "delivery-run", "claude", early, "completed")
	old := deliveryGH
	t.Cleanup(func() { deliveryGH = old })
	calls := 0
	deliveryGH = func(context.Context, ...string) ([]byte, error) { calls++; return nil, errors.New("unexpected") }
	for _, url := range []string{"http://github.com/o/r/pull/1", "https://github.com/o/r/pull/0", "https://github.com/o/r/pull/1?x=1", "https://github.com/o/r/pull/1#x", "https://user@github.com/o/r/pull/1", "https://github.com/o/r/pull/1/extra", "https://evil.test/o/r/pull/1", "file:///tmp/x"} {
		var b bytes.Buffer
		if Run([]string{"delivery", "add", "delivery-run", "--pr", url}, &b, &b) == 0 {
			t.Errorf("accepted %s", url)
		}
	}
	if calls != 0 {
		t.Fatal("invalid URL fetched")
	}
	url := "https://github.com/o/r/pull/1"
	sha := strings.Repeat("d", 40)
	for _, tc := range []struct {
		name, fields, want string
		bad                bool
	}{
		{"pending", `"statusCheckRollup":[{"__typename":"CheckRun","status":"IN_PROGRESS","conclusion":""}]`, "pending", false},
		{"missing", `"statusCheckRollup":null`, "missing", false},
		{"success", `"statusCheckRollup":[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"}]`, "success", false},
		{"incomplete", `"statusCheckRollup":[{"__typename":"CheckRun","status":"COMPLETED","conclusion":""}]`, "incomplete", false},
		{"wrong identity", `"statusCheckRollup":[],"number":2`, "", true},
		{"bad type", `"statusCheckRollup":[{"__typename":"CheckRun","status":true}]`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deliveryGH = func(context.Context, ...string) ([]byte, error) {
				return []byte(fmt.Sprintf(`{"url":%q,"number":1,"state":"OPEN","headRefOid":%q,"mergeCommit":null,%s}`, url, sha, tc.fields)), nil
			}
			var b bytes.Buffer
			code := Run([]string{"delivery", "add", "delivery-run", "--pr", url}, &b, &b)
			if tc.bad {
				if code == 0 {
					t.Fatal("bad observation accepted")
				}
				return
			}
			if code != 0 {
				t.Fatalf("add %s", &b)
			}
			raw, _ := os.ReadFile(filepath.Join(root, "delivery-run", deliveryFile))
			if !bytes.Contains(raw, []byte(`"status":"`+tc.want+`"`)) {
				t.Fatalf("missing %s in %s", tc.want, raw)
			}
		})
	}
}

func TestDeliveryHistoryBoundsAndCorruption(t *testing.T) {
	root := home(t)
	writeRun(t, root, "delivery-run", "claude", early, "completed")
	sha := strings.Repeat("a", 40)
	for _, bad := range []string{sha + " ", " " + sha, sha[:39], strings.Repeat("g", 40)} {
		var b bytes.Buffer
		if Run([]string{"delivery", "add", "delivery-run", "--commit", bad}, &b, &b) == 0 {
			t.Fatalf("accepted %q", bad)
		}
	}
	for i := 0; i < 32; i++ {
		var b bytes.Buffer
		if Run([]string{"delivery", "add", "delivery-run", "--commit", sha}, &b, &b) != 0 {
			t.Fatal(b.String())
		}
	}
	path := filepath.Join(root, "delivery-run", deliveryFile)
	before, _ := os.ReadFile(path)
	var b bytes.Buffer
	if Run([]string{"delivery", "add", "delivery-run", "--commit", sha}, &b, &b) == 0 {
		t.Fatal("accepted overflow")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("overflow changed history")
	}
	for _, raw := range []string{`{}`, `{"schemaVersion":1,"schemaVersion":1}`, `{"schemaVersion":1,"unknown":true}`, strings.Repeat("x", 256<<10+1)} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if Run([]string{"delivery", "show", "delivery-run", "--json"}, &b, &b) == 0 {
			t.Fatal("accepted corrupt sidecar")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "delivery-run", "manifest.json"), path); err != nil {
		t.Fatal(err)
	}
	if Run([]string{"delivery", "show", "delivery-run", "--json"}, &b, &b) == 0 {
		t.Fatal("accepted symlink")
	}
}
