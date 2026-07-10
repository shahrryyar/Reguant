package server

import "testing"

func TestCombinedStatusState(t *testing.T) {
	if got := combinedStatusState([]byte(`{"state":"success","total_count":3}`)); got != "success" {
		t.Errorf("want success, got %q", got)
	}
	if got := combinedStatusState([]byte(`{"state":"pending"}`)); got != "pending" {
		t.Errorf("want pending, got %q", got)
	}
	if got := combinedStatusState([]byte(`not json`)); got != "" {
		t.Errorf("want empty on bad json, got %q", got)
	}
}
