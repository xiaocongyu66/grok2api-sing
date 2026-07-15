package account

import "testing"

func TestSamplePreselectIDs(t *testing.T) {
	ids := []uint64{10, 20, 30, 40, 50, 60, 70}
	got := samplePreselectIDs(ids, 5)
	if len(got) != 5 || got[0] != 10 || got[4] != 50 {
		t.Fatalf("sample 5 = %#v", got)
	}
	small := []uint64{1, 2, 3}
	got = samplePreselectIDs(small, 5)
	if len(got) != 3 {
		t.Fatalf("sample remaining = %#v, want all 3", got)
	}
	if samplePreselectIDs(nil, 5) != nil {
		t.Fatal("empty pool should yield nil")
	}
	got = samplePreselectIDs(ids, 0)
	if len(got) != DefaultPreselectValidateCount {
		t.Fatalf("default limit = %#v", got)
	}
}
