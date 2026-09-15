package machine

import (
	"fmt"
	"strings"
)

// unifiedDiff renders a unified diff (---/+++ headers, @@ hunks, 3 context
// lines) between the current and desired config texts. Deliberately
// dependency-free: the desired-vs-applied config diff (ADR 0008, #199) is the
// only consumer, and a standard LCS keeps it small and deterministic.
func unifiedDiff(desired, current string) string {
	a := splitLines(current) // current file: '-' lines
	b := splitLines(desired) // desired file: '+' lines
	if equalStrings(a, b) {
		return ""
	}

	m, n := len(a), len(b)
	lcs := make([][]int, m+1)
	for i := range lcs {
		lcs[i] = make([]int, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				lcs[i][j] = lcs[i+1][j+1] + 1
			case lcs[i+1][j] >= lcs[i][j+1]:
				lcs[i][j] = lcs[i+1][j]
			default:
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	type op struct {
		kind byte // ' ', '-', '+'
		line string
	}
	ops := make([]op, 0, m+n)
	i, j := 0, 0
	for i < m && j < n {
		switch {
		case a[i] == b[j]:
			ops = append(ops, op{' ', a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, op{'-', a[i]})
			i++
		default:
			ops = append(ops, op{'+', b[j]})
			j++
		}
	}
	for ; i < m; i++ {
		ops = append(ops, op{'-', a[i]})
	}
	for ; j < n; j++ {
		ops = append(ops, op{'+', b[j]})
	}

	const ctx = 3

	// change[k] reports whether ops[k] is a change; line numbers per file are
	// derived by counting kinds up to an index.
	change := make([]bool, len(ops))
	any := false
	for k, o := range ops {
		change[k] = o.kind != ' '
		any = any || change[k]
	}
	if !any {
		return ""
	}

	fileLine := func(upto int, which byte) int {
		n := 1
		for k := 0; k < upto; k++ {
			if ops[k].kind == ' ' || ops[k].kind == which {
				n++
			}
		}
		return n
	}

	var out strings.Builder
	out.WriteString("--- current\n+++ desired\n")
	k := 0
	for k < len(ops) {
		if !change[k] {
			k++
			continue
		}
		// grow the hunk: from the first change, extend forward while another
		// change sits within 2*ctx context lines.
		from, last := k, k
		for idx := k; idx < len(ops); idx++ {
			if change[idx] {
				if idx-last > 2*ctx {
					break
				}
				last = idx
			}
		}
		from = max(from-ctx, 0)
		to := min(last+1+ctx, len(ops))

		aStart, bStart := fileLine(from, '-'), fileLine(from, '+')
		aCount, bCount := 0, 0
		for idx := from; idx < to; idx++ {
			switch ops[idx].kind {
			case ' ':
				aCount++
				bCount++
			case '-':
				aCount++
			case '+':
				bCount++
			}
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", aStart, aCount, bStart, bCount)
		for idx := from; idx < to; idx++ {
			fmt.Fprintf(&out, "%c%s\n", ops[idx].kind, ops[idx].line)
		}
		k = to
	}
	return out.String()
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
