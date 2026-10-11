package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeliveryObservationRejectsMalformedFields(t *testing.T) {
	old := deliveryGH
	t.Cleanup(func() { deliveryGH = old })
	url := "https://github.com/o/r/pull/1"
	sha := strings.Repeat("a", 40)
	for _, check := range []string{`{"__typename":"CheckRun","status":null,"conclusion":""}`, `{"__typename":"CheckRun","status":"IN_PROGRESS","conclusion":"bogus"}`, `{"__typename":"StatusContext","state":null}`} {
		deliveryGH = func(context.Context, ...string) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"url":%q,"number":1,"state":"OPEN","headRefOid":%q,"mergeCommit":null,"statusCheckRollup":[%s]}`, url, sha, check)), nil
		}
		if _, err := observeDeliveryPR(url); err == nil {
			t.Errorf("accepted malformed check %s", check)
		}
	}
	for _, wrong := range []string{"https://github.com/other/r/pull/1", "https://github.com/o/other/pull/1", "https://github.com/o/r/pull/2"} {
		deliveryGH = func(context.Context, ...string) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"url":%q,"number":1,"state":"OPEN","headRefOid":%q,"statusCheckRollup":[]}`, wrong, sha)), nil
		}
		if _, err := observeDeliveryPR(url); err == nil {
			t.Error("accepted wrong identity")
		}
	}
	deliveryGH = func(context.Context, ...string) ([]byte, error) {
		checks := strings.TrimSuffix(strings.Repeat(`{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"},`, 100), ",")
		return []byte(fmt.Sprintf(`{"url":%q,"number":1,"state":"OPEN","headRefOid":%q,"statusCheckRollup":[%s]}`, url, sha, checks)), nil
	}
	o, err := observeDeliveryPR(url)
	if err != nil {
		t.Fatal(err)
	}
	if o.HeadCI.Status != "incomplete" {
		t.Fatalf("possibly truncated rollup says %s", o.HeadCI.Status)
	}
}

func TestDeliverySnapshotCoherenceAndImmutableBundle(t *testing.T) {
	root := home(t)
	writeRun(t, root, "delivery-run", "claude", early, "completed")
	originals := map[string][]byte{}
	err := filepath.WalkDir(filepath.Join(root, "delivery-run"), func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			b, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			originals[p] = b
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if Run([]string{"delivery", "add", "delivery-run", "--commit", strings.Repeat("a", 40)}, &b, &b) != 0 {
		t.Fatal(b.String())
	}
	store := newViewSnapshotStore(root)
	t.Cleanup(func() { _ = store.Close() })
	first, err := store.create("delivery-run")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(first)
	store.afterCopy = func() {
		var out bytes.Buffer
		if Run([]string{"delivery", "add", "delivery-run", "--commit", strings.Repeat("b", 40)}, &out, &out) != 0 {
			t.Fatal(out.String())
		}
	}
	if _, err = store.create("delivery-run"); err == nil {
		t.Fatal("accepted changing sidecar snapshot")
	}
	store.afterCopy = nil
	after, _ := json.Marshal(first)
	if !bytes.Equal(before, after) {
		t.Fatal("captured response mutated")
	}
	second, err := store.create("delivery-run")
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Delivery.Associations) != 2 || len(first.Delivery.Associations) != 1 {
		t.Fatal("snapshot association counts")
	}
	for p, want := range originals {
		got, e := os.ReadFile(p)
		if e != nil || !bytes.Equal(want, got) {
			t.Fatalf("original changed %s", p)
		}
	}
	old := deliveryGH
	t.Cleanup(func() { deliveryGH = old })
	deliveryGH = func(context.Context, ...string) ([]byte, error) { t.Fatal("list did network work"); return nil, nil }
	if Run([]string{"list", "--json"}, &b, &b) != 0 {
		t.Fatal(b.String())
	}
}
