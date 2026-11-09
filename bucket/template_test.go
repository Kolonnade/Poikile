package bucket

import "testing"

func TestBaselineValidate(t *testing.T) {
	b := Baseline("https://app.example")
	if b.NoncurrentDays != 30 || b.IncomingDays != 1 {
		t.Fatalf("Baseline = %+v", b)
	}
	if err := b.Validate(); err != nil {
		t.Fatalf("Validate = %v", err)
	}
	for _, o := range []string{"", "https://app.example/path", "https://app.example?q=1", "https://u@app.example", "ftp://app.example"} {
		if Baseline(o).Validate() == nil {
			t.Errorf("origin %q accepted", o)
		}
	}
	bad := b
	bad.NoncurrentDays = 0
	if bad.Validate() == nil {
		t.Error("NoncurrentDays 0 accepted")
	}
	bad = b
	bad.LogBucket = "Bad_Bucket"
	if bad.Validate() == nil {
		t.Error("bad LogBucket accepted")
	}
	good := b
	good.LogBucket = "acme-logs"
	if err := good.Validate(); err != nil {
		t.Errorf("good LogBucket: %v", err)
	}
}
