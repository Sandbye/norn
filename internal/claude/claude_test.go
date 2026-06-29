package claude

import "testing"

func TestParseEnvelope(t *testing.T) {
	sample := `{"type":"result","subtype":"success","result":"  Did X and Y.\n","session_id":"abc123","total_cost_usd":0.0123,"is_error":false}`
	r, err := parseEnvelope([]byte(sample))
	if err != nil {
		t.Fatalf("parseEnvelope: %v", err)
	}
	if r.Text != "Did X and Y." {
		t.Errorf("Text = %q, want trimmed %q", r.Text, "Did X and Y.")
	}
	if r.SessionID != "abc123" {
		t.Errorf("SessionID = %q", r.SessionID)
	}
	if r.CostUSD != 0.0123 {
		t.Errorf("CostUSD = %v", r.CostUSD)
	}
	if r.IsError {
		t.Error("IsError = true, want false")
	}
}

func TestParseEnvelopeBadJSON(t *testing.T) {
	if _, err := parseEnvelope([]byte("not json")); err == nil {
		t.Error("expected error on bad json")
	}
}
