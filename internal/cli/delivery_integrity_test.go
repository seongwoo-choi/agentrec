package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeliveryNonExecutedChecksAreNotSuccess(t *testing.T) {
	old := deliveryGH
	t.Cleanup(func() { deliveryGH = old })
	for _, conclusion := range []string{"SKIPPED", "NEUTRAL"} {
		t.Run(conclusion, func(t *testing.T) {
			deliveryGH = func(context.Context, ...string) ([]byte, error) {
				return []byte(fmt.Sprintf(`{"url":"https://github.com/o/r/pull/1","number":1,"state":"OPEN","headRefOid":%q,"statusCheckRollup":[{"__typename":"CheckRun","status":"COMPLETED","conclusion":%q}]}`, strings.Repeat("a", 40), conclusion)), nil
			}
			o, err := observeDeliveryPR("https://github.com/o/r/pull/1")
			if err != nil {
				t.Fatal(err)
			}
			if o.HeadCI.Status != "incomplete" {
				t.Fatalf("non-success check %s classified as %s", conclusion, o.HeadCI.Status)
			}
		})
	}
}

func TestDeliveryRejectsRunReplacementDuringObservation(t *testing.T) {
	root := home(t)
	writeRun(t, root, "delivery-replaced", "claude", early, "completed")
	dir := filepath.Join(root, "delivery-replaced")
	moved := filepath.Join(root, "held-original")
	old := deliveryGH
	t.Cleanup(func() { deliveryGH = old })
	deliveryGH = func(context.Context, ...string) ([]byte, error) {
		if err := os.Rename(dir, moved); err != nil {
			t.Fatal(err)
		}
		writeRun(t, root, "delivery-replaced", "claude", early, "completed")
		return []byte(fmt.Sprintf(`{"url":"https://github.com/o/r/pull/1","number":1,"state":"OPEN","headRefOid":%q,"statusCheckRollup":[]}`, strings.Repeat("a", 40))), nil
	}
	if _, err := addDelivery(root, "delivery-replaced", "", "https://github.com/o/r/pull/1"); err == nil {
		t.Fatal("reported success after run identity changed during remote observation")
	}
	for _, p := range []string{dir, moved} {
		if _, err := os.Stat(filepath.Join(p, deliveryFile)); !os.IsNotExist(err) {
			t.Fatalf("published delivery after replacement: %s (%v)", p, err)
		}
	}
}
