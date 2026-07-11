package volcadapter

// Regression tests for the hardcoded doubao-seedance-* billing preset expressions
// (Option X'). These pin the exact prices each expression produces so a typo in a
// multiplier can never silently mischarge.
//
// Pricing reference (RMB / 1M output tokens):
//
//	seedance-2-0:          std+text=46, std+video=28, 1080p+text=51, 1080p+video≈31
//	seedance-2-0-fast:     text=37, video≈22
//	seedance-1-5-pro:      silent=8, with-audio=16
//	seedance-1-0-pro:      online=15, flex=7.5
//	seedance-1-0-pro-fast: online=4.2, flex=2.1
//	seedance-1-0-lite:     online=10, flex=5

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/stretchr/testify/require"
)

// runSD runs exprStr with c=1 token (so the result equals price in RMB/M) and
// checks it against want within tol.
func runSD(t *testing.T, exprStr, body string, want, tol float64) {
	t.Helper()
	got, _, err := billingexpr.RunExprWithRequest(
		exprStr,
		billingexpr.TokenParams{C: 1, Len: 1},
		billingexpr.RequestInput{Body: []byte(body)},
	)
	require.NoError(t, err)
	require.InDelta(t, want, got, tol, "expr=%s body=%s", exprStr, body)
}

func TestSeedancePresetPricing(t *testing.T) {
	cases := []struct {
		name string
		expr string
		body string
		want float64
		tol  float64
	}{
		// seedance 2.0 — resolution × content-type
		{"2.0 std+text", seedance20Expr, `{"content":[{"type":"text","text":"hi"}]}`, 46, 0.01},
		{"2.0 std+video", seedance20Expr, `{"content":[{"type":"video_url","video_url":{"url":"x"}},{"type":"text","text":"hi"}]}`, 28, 0.01},
		{"2.0 1080p+text", seedance20Expr, `{"content":[{"type":"text","text":"hi"}],"resolution":"1080p"}`, 51, 0.01},
		{"2.0 1080p+video", seedance20Expr, `{"content":[{"type":"video_url","video_url":{"url":"x"}}],"resolution":"1080p"}`, 31, 0.05},
		// seedance 2.0 fast — content-type
		{"2.0-fast text", seedance20FastExpr, `{"content":[{"type":"text","text":"hi"}]}`, 37, 0.01},
		{"2.0-fast video", seedance20FastExpr, `{"content":[{"type":"video_url","video_url":{"url":"x"}}]}`, 22, 0.01},
		// seedance 1.5 pro — generate_audio
		{"1.5-pro silent (absent)", seedance15ProExpr, `{"content":[{"type":"text","text":"hi"}]}`, 8, 0.01},
		{"1.5-pro silent (false)", seedance15ProExpr, `{"generate_audio":false}`, 8, 0.01},
		{"1.5-pro with-audio", seedance15ProExpr, `{"generate_audio":true}`, 16, 0.01},
		// seedance 1.0 pro — service_tier
		{"1.0-pro online", seedance10ProExpr, `{"content":[{"type":"text","text":"hi"}]}`, 15, 0.01},
		{"1.0-pro flex", seedance10ProExpr, `{"service_tier":"flex"}`, 7.5, 0.01},
		// seedance 1.0 pro fast
		{"1.0-pro-fast online", seedance10ProFast, `{"content":[{"type":"text"}]}`, 4.2, 0.01},
		{"1.0-pro-fast flex", seedance10ProFast, `{"service_tier":"flex"}`, 2.1, 0.01},
		// seedance 1.0 lite
		{"1.0-lite online", seedance10LiteExpr, `{"content":[{"type":"text"}]}`, 10, 0.01},
		{"1.0-lite flex", seedance10LiteExpr, `{"service_tier":"flex"}`, 5, 0.01},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			runSD(t, tt.expr, tt.body, tt.want, tt.tol)
		})
	}
}

// TestSeedanceExprMapCoversModelList ensures every seedance model in ModelList
// resolves to a billing expression (bare aliases are additional keys).
func TestSeedanceExprMapCoversModelList(t *testing.T) {
	for _, m := range ModelList {
		if !strings.Contains(strings.ToLower(m), "seedance") {
			continue
		}
		_, ok := getSeedanceExpr(m)
		require.Truef(t, ok, "seedance model %q has no billing expression", m)
	}
}

// TestAllPresetExpressionsCompile guards against a syntax typo in any expression.
func TestAllPresetExpressionsCompile(t *testing.T) {
	seen := map[string]bool{}
	for _, exprStr := range seedanceExprByModel {
		if seen[exprStr] {
			continue
		}
		seen[exprStr] = true
		_, err := billingexpr.CompileFromCache(exprStr)
		require.NoErrorf(t, err, "expression failed to compile: %s", exprStr)
	}
}
