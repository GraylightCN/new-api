package volcadapter

// Hardcoded per-model Seedance billing expressions (Option X').
//
// These are the authoritative prices, expressed in the shared billingexpr
// language and evaluated at settlement with the actual token count (c) reported
// by Volc plus a synthesized request body for the param() lookups. They are
// intentionally NOT admin-editable — changing a price is a code change + redeploy
// (consistent with the fork's deploy cadence), which removes the entire
// tiered_expr admin-settings + frontend-editor surface that made the old
// implementation un-mergeable.
//
// Prices are RMB per 1M output tokens; the zero-multiplier ("base") is the
// cheapest tier and each rule multiplies a dimension on top:
//
//	seedance-2-0:          std+text=46, std+video=28, 1080p+text=51, 1080p+video≈31
//	seedance-2-0-fast:     text=37, video≈22
//	seedance-1-5-pro:      silent=8, with-audio=16
//	seedance-1-0-pro:      online=15, flex=7.5
//	seedance-1-0-pro-fast: online=4.2, flex=2.1
//	seedance-1-0-lite:     online=10, flex=5
//
// The expression strings are kept byte-identical to the regression tests in
// seedance_presets_test.go — change one side, change the other.
const (
	seedance20Expr     = `(tier("base", c * 46)) * (param("resolution") == "1080p" ? 1.108696 : 1) * (param("content.#(type==\"video_url\")") != nil ? 0.608696 : 1)`
	seedance20FastExpr = `(tier("base", c * 37)) * (param("content.#(type==\"video_url\")") != nil ? 0.594595 : 1)`
	seedance15ProExpr  = `(tier("base", c * 8)) * (param("generate_audio") == true ? 2 : 1)`
	seedance10ProExpr  = `(tier("base", c * 15)) * (param("service_tier") == "flex" ? 0.5 : 1)`
	seedance10ProFast  = `(tier("base", c * 4.2)) * (param("service_tier") == "flex" ? 0.5 : 1)`
	seedance10LiteExpr = `(tier("base", c * 10)) * (param("service_tier") == "flex" ? 0.5 : 1)`
)

// seedanceExprByModel maps every supported Seedance model ID — full doubao-prefixed
// form and the bare alias — to its billing expression. The alias set mirrors the
// keys in seedanceDefaultResolution/seedanceMaxDuration (seedance_estimator.go).
var seedanceExprByModel = map[string]string{
	// seedance 2.0
	"doubao-seedance-2-0-260128": seedance20Expr,
	"seedance-2-0-260128":        seedance20Expr,
	// seedance 2.0 fast
	"doubao-seedance-2-0-fast-260128": seedance20FastExpr,
	"seedance-2-0-fast-260128":        seedance20FastExpr,
	// seedance 1.5 pro
	"doubao-seedance-1-5-pro-251215": seedance15ProExpr,
	"seedance-1-5-pro-251215":        seedance15ProExpr,
	// seedance 1.0 pro
	"doubao-seedance-1-0-pro-250528": seedance10ProExpr,
	"seedance-1-0-pro-250528":        seedance10ProExpr,
	// seedance 1.0 pro fast
	"doubao-seedance-1-0-pro-fast-251015": seedance10ProFast,
	"seedance-1-0-pro-fast-251015":        seedance10ProFast,
	// seedance 1.0 lite (i2v + t2v share the lite price)
	"doubao-seedance-1-0-lite-i2v-250428": seedance10LiteExpr,
	"seedance-1-0-lite-i2v-250428":        seedance10LiteExpr,
	"doubao-seedance-1-0-lite-t2v-250428": seedance10LiteExpr,
	"seedance-1-0-lite-t2v-250428":        seedance10LiteExpr,
}

// getSeedanceExpr returns the billing expression for a model and whether one exists.
func getSeedanceExpr(modelName string) (string, bool) {
	expr, ok := seedanceExprByModel[modelName]
	return expr, ok
}

// HasSeedanceExpr reports whether the model has a hardcoded Seedance billing
// expression (i.e. it settles via AdjustBillingOnComplete). Exported for the
// submit-time billing wiring in controller/relay.go.
func HasSeedanceExpr(modelName string) bool {
	_, ok := seedanceExprByModel[modelName]
	return ok
}
