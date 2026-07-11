package volcadapter

import (
	"encoding/json"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

// EstimateBilling computes a CONSERVATIVE pre-charge lock for a Seedance task.
//
// The exact charge is settled later in AdjustBillingOnComplete using the real
// token count Volc reports, so this only needs to lock a safe upper-ish bound
// ("多锁不少锁"): it runs the model's hardcoded expression with an over-estimated
// token count (EstimateSeedanceTokens) against the real request body.
//
// The pre-charge mechanism only lets an adaptor return OtherRatios that multiply
// the base model quota. To express the token-based estimate through that
// mechanism WITHOUT any shared-code edit, we return a single ratio r such that
// baseQuota × r == estimatedQuota. Because both estimatedQuota and baseQuota
// carry the same group ratio, r is group-ratio-invariant; the final pre-charge
// equals estimatedQuota regardless of what nonzero base price the admin set.
//
// Requires a nonzero base quota (info.PriceData.Quota). ModelPriceHelperPerCall
// already guarantees this for any configured model (it errors out otherwise) or
// yields 0 for a genuinely free model, in which case we lock nothing (settlement
// also charges 0 — the group ratio makes the whole task free).
func (a *TaskAdaptor) EstimateBilling(c *gin.Context, info *relaycommon.RelayInfo) map[string]float64 {
	exprStr, ok := getSeedanceExpr(info.OriginModelName)
	if !ok {
		return nil
	}
	body, err := readBodyBytes(c)
	if err != nil {
		return nil
	}
	tokens := EstimateSeedanceTokens(info.OriginModelName, body)
	if tokens <= 0 {
		return nil
	}

	cost, _, err := billingexpr.RunExprWithRequest(
		exprStr,
		billingexpr.TokenParams{C: float64(tokens), Len: float64(tokens)},
		billingexpr.RequestInput{Body: body},
	)
	if err != nil || cost <= 0 {
		return nil
	}

	groupRatio := info.PriceData.GroupRatioInfo.GroupRatio
	estimatedQuota := cost / 1_000_000 * common.QuotaPerUnit * groupRatio
	baseQuota := float64(info.PriceData.Quota)
	if baseQuota <= 0 || estimatedQuota <= 0 {
		// Free model (groupRatio/price == 0) or no base to scale: lock nothing.
		return nil
	}
	return map[string]float64{"seedance_estimate": estimatedQuota / baseQuota}
}

// AdjustBillingOnComplete returns the exact quota for a settled Seedance task.
//
// Upstream service/task_polling.go calls this after a task reaches a terminal
// state and, when the return is > 0, delta-settles it against the pre-charge.
// The quota is computed from the actual token count Volc reports plus a
// synthesized request body for the expression's param() lookups.
//
// Quota formula (identical to the retired tiered-snapshot path):
//
//	quota = QuotaRound( cost/1e6 * QuotaPerUnit * groupRatio )
//
// where cost = expr(tokens, params). QuotaPerUnit is the global
// common.QuotaPerUnit and groupRatio is the value captured on the task's
// BillingContext at submit time (relayInfo.PriceData.GroupRatioInfo.GroupRatio),
// so the settled amount matches what the submit-time pricing would have charged.
func (a *TaskAdaptor) AdjustBillingOnComplete(task *model.Task, taskResult *relaycommon.TaskInfo) int {
	if task == nil || taskResult == nil {
		return 0
	}
	bc := task.PrivateData.BillingContext
	if bc == nil {
		return 0
	}

	modelName := bc.OriginModelName
	if modelName == "" {
		modelName = task.Properties.OriginModelName
	}
	exprStr, ok := getSeedanceExpr(modelName)
	if !ok {
		return 0
	}

	// Prefer TotalTokens; Volc video callbacks sometimes report only completion.
	tokens := taskResult.TotalTokens
	if tokens <= 0 {
		tokens = taskResult.CompletionTokens
	}
	if tokens <= 0 {
		return 0
	}

	synthBody := buildSynthesizedBody(task, bc.VolcBillingFlags)

	cost, _, err := billingexpr.RunExprWithRequest(
		exprStr,
		billingexpr.TokenParams{C: float64(tokens), Len: float64(tokens)},
		billingexpr.RequestInput{Body: synthBody},
	)
	if err != nil || cost <= 0 {
		return 0
	}

	quotaBeforeGroup := cost / 1_000_000 * common.QuotaPerUnit
	return billingexpr.QuotaRound(quotaBeforeGroup * bc.GroupRatio)
}

// volcFetchParams is the subset of the Volc GET task response used as a fallback
// param source at settlement (resolution/service_tier/duration ARE echoed there;
// generate_audio and the input content[] are NOT — those come from captured flags).
type volcFetchParams struct {
	Resolution  string `json:"resolution"`
	ServiceTier string `json:"service_tier"`
	Duration    int    `json:"duration"`
}

// buildSynthesizedBody constructs a minimal Volc-native body for the expression's
// param() lookups. Captured submit flags are authoritative; task.Data (the Volc
// fetch response) fills any gaps for the fields Volc does echo.
func buildSynthesizedBody(task *model.Task, flags *model.VolcBillingFlags) []byte {
	var fetch volcFetchParams
	if len(task.Data) > 0 {
		_ = common.Unmarshal(task.Data, &fetch) // best-effort
	}

	body := map[string]interface{}{}

	if flags != nil && flags.Resolution != "" {
		body["resolution"] = flags.Resolution
	} else if fetch.Resolution != "" {
		body["resolution"] = fetch.Resolution
	}
	if flags != nil && flags.ServiceTier != "" {
		body["service_tier"] = flags.ServiceTier
	} else if fetch.ServiceTier != "" {
		body["service_tier"] = fetch.ServiceTier
	}
	if flags != nil && flags.Duration > 0 {
		body["duration"] = flags.Duration
	} else if fetch.Duration > 0 {
		body["duration"] = fetch.Duration
	}
	if flags != nil {
		if flags.GenerateAudio != nil {
			body["generate_audio"] = *flags.GenerateAudio
		}
		// Synthesize the input content[] with a video_url item so
		// param("content.#(type==\"video_url\")") resolves at settle time.
		if flags.HasVideoInput {
			body["content"] = []map[string]string{{"type": "video_url"}}
		}
	}

	out, err := common.Marshal(body)
	if err != nil {
		return []byte("{}")
	}
	return out
}

// ExtractVolcBillingFlags parses the Volc-native submit body into the settlement
// flags. Called from controller/relay.go at task-insert time for VolcAdapter
// seedance tasks (the Volc GET response does not echo generate_audio or the input
// content[], so they must be snapshotted here). Returns nil on empty input.
func ExtractVolcBillingFlags(body []byte) *model.VolcBillingFlags {
	if len(body) == 0 {
		return nil
	}
	var parsed map[string]json.RawMessage
	if err := common.Unmarshal(body, &parsed); err != nil {
		return nil
	}
	flags := &model.VolcBillingFlags{}
	if raw, ok := parsed["resolution"]; ok {
		_ = common.Unmarshal(raw, &flags.Resolution)
	}
	if raw, ok := parsed["service_tier"]; ok {
		_ = common.Unmarshal(raw, &flags.ServiceTier)
	}
	if raw, ok := parsed["duration"]; ok {
		var d int
		if err := common.Unmarshal(raw, &d); err == nil && d > 0 {
			flags.Duration = d
		}
	}
	if raw, ok := parsed["generate_audio"]; ok {
		var b bool
		if err := common.Unmarshal(raw, &b); err == nil {
			flags.GenerateAudio = &b
		}
	}
	if raw, ok := parsed["content"]; ok {
		flags.HasVideoInput = hasVideoInVolcContent(raw)
	}
	return flags
}
