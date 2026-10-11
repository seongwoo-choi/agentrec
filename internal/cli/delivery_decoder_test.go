package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeliveryRejectsInconsistentPersistedCheckState(t *testing.T) {
	for _, tc := range []struct {
		status string
		count  int
	}{{"success", 100}, {"missing", 1}} {
		t.Run(fmt.Sprintf("%s-%d", tc.status, tc.count), func(t *testing.T) {
			root := home(t)
			const runID = "delivery-invalid-count"
			writeRun(t, root, runID, "claude", early, "completed")
			sha := strings.Repeat("a", 40)
			raw := []byte(fmt.Sprintf(`{"schemaVersion":1,"associations":[{"pr":"https://github.com/o/r/pull/1","observedAt":"2026-10-11T00:00:00Z","observation":{"state":"OPEN","headSha":%q,"observedAt":"2026-10-11T00:00:00Z","headCI":{"targetSha":%q,"status":%q,"checkCount":%d},"mergeCI":{"status":"unobserved"}}}]}`, sha, sha, tc.status, tc.count))
			if _, err := decodeDelivery(raw); err == nil {
				t.Error("decoder accepted inconsistent state/count")
			}
			if err := os.WriteFile(filepath.Join(root, runID, deliveryFile), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			var out, errout bytes.Buffer
			if code := Run([]string{"delivery", "show", runID, "--json"}, &out, &errout); code == 0 {
				t.Error("CLI rendered corrupt state as valid delivery")
			}
			store := newViewSnapshotStore(root)
			t.Cleanup(func() { _ = store.Close() })
			if _, err := store.create(runID); err == nil {
				t.Error("snapshot rendered corrupt state as valid delivery")
			}
		})
	}
}
