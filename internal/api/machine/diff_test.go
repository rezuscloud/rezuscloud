package machine

import "testing"

func TestUnifiedDiff(t *testing.T) {
	if got := unifiedDiff("a\nb\n", "a\nb\n"); got != "" {
		t.Errorf("identical configs must produce an empty diff, got:\n%s", got)
	}
	got := unifiedDiff("a\nX\nc\n", "a\nb\nc\n") // desired first, current second
	for _, want := range []string{"--- current", "+++ desired", "@@ -1,3 +1,3 @@", "-b", "+X", " a"} {
		if !contains(got, want) {
			t.Errorf("diff missing %q:\n%s", want, got)
		}
	}
	added := unifiedDiff("a\nb\n", "")
	if !contains(added, "+a") || !contains(added, "+b") {
		t.Errorf("empty current must diff as all-added:\n%s", added)
	}
}

func contains(s, want string) bool {
	return len(s) >= len(want) && (func() bool {
		for i := 0; i+len(want) <= len(s); i++ {
			if s[i:i+len(want)] == want {
				return true
			}
		}
		return false
	})()
}
