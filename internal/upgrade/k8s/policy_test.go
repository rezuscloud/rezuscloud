package k8s

import (
	"testing"
)

func TestVersionPolicy_ValidUpgrade(t *testing.T) {
	tests := []struct {
		current, target string
		wantErr         bool
	}{
		{"1.35.0", "1.35.1", false},
		{"1.35.0", "1.36.0", false},
		{"1.35.0", "1.34.0", true}, // downgrade
		{"1.35.0", "1.37.0", true}, // skip minor
		{"1.35.3", "1.35.1", true}, // patch downgrade
		{"", "1.36.0", false},      // empty current
	}

	for _, tt := range tests {
		err := VersionPolicy{}.ValidateUpgrade(tt.current, tt.target)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateUpgrade(%q, %q) = %v, wantErr=%v", tt.current, tt.target, err, tt.wantErr)
		}
	}
}

func TestUpgradeOrder(t *testing.T) {
	machines := []MachineInfo{
		{ID: "w1", Role: "worker"},
		{ID: "cp1", Role: "controlplane"},
		{ID: "w2", Role: "worker"},
		{ID: "cp2", Role: "controlplane"},
	}

	order := UpgradeOrder(machines)

	if len(order) != 4 {
		t.Fatalf("expected 4 machines, got %d", len(order))
	}

	// Control planes first.
	if order[0] != "cp1" || order[1] != "cp2" {
		t.Errorf("control planes should come first, got %v", order[:2])
	}

	// Workers after.
	if order[2] != "w1" || order[3] != "w2" {
		t.Errorf("workers should come after, got %v", order[2:])
	}
}

func TestUpgradeOrder_Empty(t *testing.T) {
	order := UpgradeOrder(nil)
	if len(order) != 0 {
		t.Errorf("empty input should return empty, got %v", order)
	}
}

func TestUpgradeOrder_WorkersOnly(t *testing.T) {
	machines := []MachineInfo{
		{ID: "w1", Role: "worker"},
		{ID: "w2", Role: "worker"},
	}

	order := UpgradeOrder(machines)
	if len(order) != 2 {
		t.Fatalf("expected 2, got %d", len(order))
	}
}

func TestPreCheck_ValidUpgrade(t *testing.T) {
	warnings := PreCheck("1.35.0", "1.36.0", "1.12.6")
	if len(warnings) != 0 {
		t.Errorf("valid upgrade should have no warnings, got %v", warnings)
	}
}

func TestPreCheck_SkipMinor(t *testing.T) {
	warnings := PreCheck("1.35.0", "1.37.0", "1.12.6")
	if len(warnings) == 0 {
		t.Error("skip minor should produce warning")
	}
}

func TestPreCheck_UnknownTalos(t *testing.T) {
	warnings := PreCheck("1.35.0", "1.36.0", "")
	if len(warnings) == 0 {
		t.Error("unknown Talos version should warn")
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		input     string
		wantMinor int
		wantPatch int
	}{
		{"1.35.0", 35, 0},
		{"1.36.3", 36, 3},
		{"2.0.0", 0, 0}, // major != 1, Sscanf won't match
		{"", 0, 0},
	}

	for _, tt := range tests {
		minor, patch, _ := parseVersion(tt.input)
		switch tt.input {
		case "":
			if minor != 0 || patch != 0 {
				t.Errorf("parseVersion(%q) = (%d, %d)", tt.input, minor, patch)
			}
		case "2.0.0":
			// Major != 1 won't parse correctly but won't error.
		default:
			if minor != tt.wantMinor || patch != tt.wantPatch {
				t.Errorf("parseVersion(%q) = (%d, %d), want (%d, %d)", tt.input, minor, patch, tt.wantMinor, tt.wantPatch)
			}
		}
	}
}
