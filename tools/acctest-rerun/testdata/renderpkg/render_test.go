// Package renderpkg is run by TestRender_MatchesVerboseResultLines with both
// -v and -json. Its tests nest, run in parallel, skip and fail, so every kind
// of result line appears at more than one depth.
package renderpkg

import "testing"

func TestNested(t *testing.T) {
	t.Run("a", func(t *testing.T) {
		t.Log("in a")
		t.Run("deep", func(t *testing.T) { t.Skip("skipped deep") })
		t.Run("deeper", func(t *testing.T) { t.Error("failed deeper") })
	})
	t.Run("b", func(t *testing.T) {
		t.Parallel()
		t.Log("in b")
	})
}

func TestParallelTop(t *testing.T) {
	t.Parallel()
	t.Log("top")
}

func TestSkippedTop(t *testing.T) { t.Skip("skipped top") }
