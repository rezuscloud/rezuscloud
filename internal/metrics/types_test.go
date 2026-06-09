package metrics

import (
	"testing"
)

func TestPercent(t *testing.T) {
	tests := []struct {
		usage, capacity, want int
	}{
		{50, 100, 50},
		{0, 100, 0},
		{100, 100, 100},
		{150, 100, 100}, // capped
		{50, 0, 0},      // zero capacity
		{50, -1, 0},     // negative capacity
	}
	for _, tt := range tests {
		got := Percent(int64(tt.usage), int64(tt.capacity))
		if got != tt.want {
			t.Errorf("Percent(%d, %d) = %d, want %d", tt.usage, tt.capacity, got, tt.want)
		}
	}
}

func TestPressureLevel(t *testing.T) {
	tests := []struct {
		pct  int
		want string
	}{
		{0, "ok"},
		{50, "ok"},
		{69, "ok"},
		{70, "warning"},
		{80, "warning"},
		{89, "warning"},
		{90, "critical"},
		{100, "critical"},
	}
	for _, tt := range tests {
		got := PressureLevel(tt.pct)
		if got != tt.want {
			t.Errorf("PressureLevel(%d) = %q, want %q", tt.pct, got, tt.want)
		}
	}
}

func TestParseK8sQuantity(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		// CPU
		{"500m", 500},
		{"100m", 100},
		{"2", 2}, // plain number < 1000 without dot → not treated as cores
		// Memory
		{"1Gi", 1073741824},
		{"512Mi", 536870912},
		{"32Gi", 34359738368},
		{"100Ki", 102400},
		// Pods
		{"110", 110},
		{"220", 220},
		// Empty
		{"", 0},
	}
	for _, tt := range tests {
		got := parseK8sQuantity(tt.input)
		if got != tt.want {
			t.Errorf("parseK8sQuantity(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestResolveNodeName(t *testing.T) {
	tests := []struct {
		metric map[string]string
		want   string
	}{
		{map[string]string{"node": "worker-01"}, "worker-01"},
		{map[string]string{"instance": "10.0.0.1:9100"}, "10.0.0.1"},
		{map[string]string{}, ""},
	}
	for _, tt := range tests {
		got := resolveNodeName(tt.metric)
		if got != tt.want {
			t.Errorf("resolveNodeName(%v) = %q, want %q", tt.metric, got, tt.want)
		}
	}
}
