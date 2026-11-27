package protocol

import (
	"encoding/json"
	"testing"
)

func TestPatchScanProtocolCompatibility(t *testing.T) {
	var request PatchScanMsg
	if err := json.Unmarshal([]byte(`{}`), &request); err != nil || request.RequestID != "" || request.RefreshMetadata {
		t.Fatalf("empty request: %+v %v", request, err)
	}
	var report PatchReportMsg
	if err := json.Unmarshal([]byte(`{"os":"linux","patches":[]}`), &report); err != nil || report.RequestID != "" || report.ScanMode != "" {
		t.Fatalf("legacy report: %+v %v", report, err)
	}
	request = PatchScanMsg{RequestID: "request", RefreshMetadata: true}
	raw, _ := json.Marshal(request)
	var copy PatchScanMsg
	if err := json.Unmarshal(raw, &copy); err != nil || copy != request {
		t.Fatalf("round trip: %+v %v", copy, err)
	}
	if SafePatchScanError("https://user:password@repo.invalid") != "scan_failed" {
		t.Fatal("unsafe error exposed")
	}
}
