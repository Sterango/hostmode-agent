package hoststats

import "testing"

func TestSampleReportsMemoryAndCPU(t *testing.T) {
	var s Sampler
	first := s.Sample()
	if first == nil {
		t.Skip("/proc not readable here")
	}
	if first.MemTotalMB <= 0 || first.MemUsedMB <= 0 || first.MemUsedMB > first.MemTotalMB {
		t.Fatalf("memory out of range: %+v", first)
	}
	if first.CPUPercent != 0 {
		t.Fatalf("first sample has no baseline, want 0 CPU, got %v", first.CPUPercent)
	}
	for i := 0; i < 2000000; i++ {
		_ = i * i
	}
	second := s.Sample()
	if second.CPUPercent < 0 || second.CPUPercent > 100 {
		t.Fatalf("cpu out of range: %v", second.CPUPercent)
	}
}
