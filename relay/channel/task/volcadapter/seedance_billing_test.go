package volcadapter

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

func boolPtr(b bool) *bool { return &b }

// TestAdjustBillingOnComplete pins the exact settled quota for representative
// (model, tokens, params, groupRatio) combinations. This is the real-money path:
// upstream delta-settles the pre-charge against this return value.
//
// quota = round( expr(tokens)/1e6 * QuotaPerUnit * groupRatio ).
// QuotaPerUnit is 500000; all cases use c = 1,000,000 tokens so
// expr/1e6 * 500000 = pricePerM * 500000.
func TestAdjustBillingOnComplete(t *testing.T) {
	const tokens = 1_000_000
	require.Equal(t, 500*1000.0, common.QuotaPerUnit, "test expects default QuotaPerUnit")

	cases := []struct {
		name       string
		model      string
		groupRatio float64
		flags      *model.VolcBillingFlags
		taskData   string // Volc fetch response body
		wantQuota  int
	}{
		{
			// 46 RMB/M → 46 * 500000
			name: "2.0 std+text", model: "doubao-seedance-2-0-260128", groupRatio: 1,
			wantQuota: 23_000_000,
		},
		{
			// video discount 0.608696: 46 * 0.608696 * 500000 = 14,000,008
			name: "2.0 std+video (captured flag)", model: "doubao-seedance-2-0-260128", groupRatio: 1,
			flags:     &model.VolcBillingFlags{HasVideoInput: true},
			wantQuota: 14_000_008,
		},
		{
			// 1080p surcharge 1.108696: 46 * 1.108696 * 500000 = 25,500,008
			name: "2.0 1080p+text (resolution from task.Data)", model: "doubao-seedance-2-0-260128", groupRatio: 1,
			taskData:  `{"status":"succeeded","resolution":"1080p"}`,
			wantQuota: 25_500_008,
		},
		{
			// generate_audio=true → 8 * 2 * 500000 = 8,000,000
			name: "1.5-pro with-audio (captured flag)", model: "doubao-seedance-1-5-pro-251215", groupRatio: 1,
			flags:     &model.VolcBillingFlags{GenerateAudio: boolPtr(true)},
			wantQuota: 8_000_000,
		},
		{
			// generate_audio absent → silent 8 * 500000 = 4,000,000
			name: "1.5-pro silent (no flag)", model: "doubao-seedance-1-5-pro-251215", groupRatio: 1,
			wantQuota: 4_000_000,
		},
		{
			// service_tier flex from task.Data → 15 * 0.5 * 500000 = 3,750,000
			name: "1.0-pro flex (service_tier from task.Data)", model: "doubao-seedance-1-0-pro-250528", groupRatio: 1,
			taskData:  `{"status":"succeeded","service_tier":"flex"}`,
			wantQuota: 3_750_000,
		},
		{
			// group ratio applies: 46 * 500000 * 0.5 = 11,500,000
			name: "2.0 std+text groupRatio 0.5", model: "doubao-seedance-2-0-260128", groupRatio: 0.5,
			wantQuota: 11_500_000,
		},
		{
			// bare alias resolves to the same expression
			name: "bare alias 1.0-lite flex", model: "seedance-1-0-lite-t2v-250428", groupRatio: 1,
			flags:     &model.VolcBillingFlags{ServiceTier: "flex"},
			wantQuota: 2_500_000, // 10 * 0.5 * 500000
		},
	}

	a := &TaskAdaptor{}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			task := &model.Task{
				Properties: model.Properties{OriginModelName: tt.model},
			}
			task.PrivateData.BillingContext = &model.TaskBillingContext{
				OriginModelName:  tt.model,
				GroupRatio:       tt.groupRatio,
				VolcBillingFlags: tt.flags,
			}
			if tt.taskData != "" {
				task.Data = json.RawMessage(tt.taskData)
			}
			got := a.AdjustBillingOnComplete(task, &relaycommon.TaskInfo{TotalTokens: tokens})
			require.Equal(t, tt.wantQuota, got)
		})
	}
}

// TestAdjustBillingOnComplete_TokenFallback verifies CompletionTokens is used
// when TotalTokens is unset, and that a zero-token result settles nothing.
func TestAdjustBillingOnComplete_TokenFallback(t *testing.T) {
	a := &TaskAdaptor{}
	task := &model.Task{}
	task.PrivateData.BillingContext = &model.TaskBillingContext{
		OriginModelName: "doubao-seedance-2-0-260128",
		GroupRatio:      1,
	}

	// TotalTokens=0, CompletionTokens=1e6 → falls back → 23,000,000
	got := a.AdjustBillingOnComplete(task, &relaycommon.TaskInfo{CompletionTokens: 1_000_000})
	require.Equal(t, 23_000_000, got)

	// No tokens at all → 0 (keep pre-charge)
	require.Equal(t, 0, a.AdjustBillingOnComplete(task, &relaycommon.TaskInfo{}))
}

// TestAdjustBillingOnComplete_UnknownModel returns 0 so the caller keeps the
// pre-charge / falls back to ratio billing rather than mischarging.
func TestAdjustBillingOnComplete_UnknownModel(t *testing.T) {
	a := &TaskAdaptor{}
	task := &model.Task{}
	task.PrivateData.BillingContext = &model.TaskBillingContext{
		OriginModelName: "some-non-seedance-model",
		GroupRatio:      1,
	}
	require.Equal(t, 0, a.AdjustBillingOnComplete(task, &relaycommon.TaskInfo{TotalTokens: 1_000_000}))
}

// TestBuildSynthesizedBody verifies flag priority over task.Data and content synthesis.
func TestBuildSynthesizedBody(t *testing.T) {
	task := &model.Task{Data: json.RawMessage(`{"resolution":"720p","service_tier":"default","duration":9}`)}

	// Flags win over task.Data; HasVideoInput synthesizes content[].
	flags := &model.VolcBillingFlags{Resolution: "1080p", HasVideoInput: true, GenerateAudio: boolPtr(true)}
	body := buildSynthesizedBody(task, flags)
	var m map[string]interface{}
	require.NoError(t, common.Unmarshal(body, &m))
	require.Equal(t, "1080p", m["resolution"])     // flag overrides task.Data 720p
	require.Equal(t, "default", m["service_tier"]) // filled from task.Data
	require.Equal(t, true, m["generate_audio"])
	require.NotNil(t, m["content"])

	// Nil flags → everything from task.Data, no content[].
	body2 := buildSynthesizedBody(task, nil)
	var m2 map[string]interface{}
	require.NoError(t, common.Unmarshal(body2, &m2))
	require.Equal(t, "720p", m2["resolution"])
	require.Equal(t, "default", m2["service_tier"])
	require.Nil(t, m2["content"])
}

// TestExtractVolcBillingFlags covers the submit-body → flags parse.
func TestExtractVolcBillingFlags(t *testing.T) {
	require.Nil(t, ExtractVolcBillingFlags(nil))

	f := ExtractVolcBillingFlags([]byte(`{
		"model":"doubao-seedance-2-0-260128",
		"resolution":"1080p",
		"service_tier":"flex",
		"duration":5,
		"generate_audio":true,
		"content":[{"type":"video_url","video_url":{"url":"x"}},{"type":"text","text":"hi"}]
	}`))
	require.NotNil(t, f)
	require.Equal(t, "1080p", f.Resolution)
	require.Equal(t, "flex", f.ServiceTier)
	require.Equal(t, 5, f.Duration)
	require.NotNil(t, f.GenerateAudio)
	require.True(t, *f.GenerateAudio)
	require.True(t, f.HasVideoInput)

	// text-only content → HasVideoInput false; generate_audio absent → nil.
	f2 := ExtractVolcBillingFlags([]byte(`{"content":[{"type":"text","text":"hi"}]}`))
	require.NotNil(t, f2)
	require.False(t, f2.HasVideoInput)
	require.Nil(t, f2.GenerateAudio)
}
