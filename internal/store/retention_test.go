package store

import (
	"testing"

	"github.com/ekko-studio/natpilot/internal/model"
)

// TestTrimScansKeepsNewest ensures the scan retention cap drops old rows and
// their ports, which is what "只保留最近 N 条" means for the scans table.
func TestTrimScansKeepsNewest(t *testing.T) {
	st := newTestStore(t)
	ids := make([]int64, 0, 3)
	for i := 0; i < 3; i++ {
		id, err := st.CreateScan(&model.ScanRun{TargetKind: "local", TargetRef: "127.0.0.1", PortsSpec: "80", Status: "done"})
		if err != nil {
			t.Fatalf("create scan: %v", err)
		}
		ids = append(ids, id)
	}
	if _, err := st.TrimScans(1); err != nil {
		t.Fatalf("trim scans: %v", err)
	}
	scans, _, err := st.ListScans(100, 0)
	if err != nil {
		t.Fatalf("list scans: %v", err)
	}
	if len(scans) != 1 {
		t.Fatalf("scans = %d, want 1 after trimming to 1", len(scans))
	}
	// 0 disables the cap.
	if n, err := st.TrimScans(0); err != nil || n != 0 {
		t.Fatalf("TrimScans(0) = %d, %v; want 0, nil", n, err)
	}
}
