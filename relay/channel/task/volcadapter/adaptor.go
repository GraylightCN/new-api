// Package volcadapter provides the Volc-native task adaptor. It is selected via
// the named task platform TaskPlatformVolcNative ("volc-native"), not a
// dedicated channel type: native Volc video tasks reuse the existing
// VolcEngine(45) channel.
//
// It embeds doubao.TaskAdaptor for shared task plumbing (Init, BuildRequestURL,
// BuildRequestHeader, DoRequest, FetchTask, ParseTaskResult, ConvertToOpenAIVideo)
// and overrides only the methods that need Volc-native behavior:
//
//   - ValidateRequestAndSetAction — Volc-native body validation (model required)
//   - BuildRequestBody            — byte-identical pass-through + model patching
//   - DoResponse                  — Volc-native submit response shape ({"id":...})
//   - EstimateBilling             — conservative token-formula pre-charge lock
//   - AdjustBillingOnComplete     — exact settle: per-model expression × actual Volc tokens
//   - GetChannelName / GetModelList
//
// Billing follows the locked Option X' design (see seedance_billing.go +
// seedance_presets.go — both self-contained, zero shared billing code): each
// seedance model has a hardcoded billingexpr expression (seedance_presets.go).
// EstimateBilling pre-charges a conservative estimate; AdjustBillingOnComplete
// OVERRIDES the embedded taskcommon.BaseBilling no-op and settles the exact quota
// by evaluating that expression against the actual Volc usage tokens reported at
// task completion. AdjustBillingOnSubmit stays the inherited no-op.
//
// The underlying doubao/volcengine channels continue to use doubao.TaskAdaptor
// unchanged; only tasks submitted with Platform="volc-native" route here.
package volcadapter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel/task/doubao"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

// TaskAdaptor is the Volc-native task adaptor (Platform="volc-native").
// It embeds doubao.TaskAdaptor and inherits all methods that do not need
// Volc-specific overrides.
type TaskAdaptor struct {
	doubao.TaskAdaptor
}

// GetChannelName returns the channel name for this adaptor.
func (a *TaskAdaptor) GetChannelName() string {
	return "volc-adapter-task"
}

// GetModelList returns the VolcAdapter curated model list.
func (a *TaskAdaptor) GetModelList() []string {
	return ModelList
}

// ValidateRequestAndSetAction parses a Volc-native body, validates fields,
// and sets action based on content[] presence of image/video items.
// No RelayFormat check is needed — this adaptor is only reached via the
// TaskPlatformVolcNative ("volc-native") dispatch, never the default doubao path.
func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *dto.TaskError {
	return validateVolcNativeTaskRequest(c, info)
}

// BuildRequestBody forwards the Volc-native body byte-identical to upstream.
// If the model is mapped, only the "model" field is patched; all other fields
// (tools, resolution, ratio, duration, etc.) are preserved as-is.
func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	rawBytes, err := readBodyBytes(c)
	if err != nil {
		return nil, fmt.Errorf("BuildRequestBody (volc native): read body failed: %w", err)
	}

	// If model is mapped, patch just the model field in the JSON.
	if info.IsModelMapped && info.UpstreamModelName != "" {
		rawBytes, err = patchVolcBodyModel(rawBytes, info.UpstreamModelName)
		if err != nil {
			return nil, fmt.Errorf("BuildRequestBody (volc native): patch model failed: %w", err)
		}
	} else if info.UpstreamModelName == "" {
		// Extract model name from raw body so info.UpstreamModelName is populated.
		var bodyMap map[string]json.RawMessage
		if jsonErr := common.Unmarshal(rawBytes, &bodyMap); jsonErr == nil {
			if modelRaw, ok := bodyMap["model"]; ok {
				var m string
				if jsonErr2 := common.Unmarshal(modelRaw, &m); jsonErr2 == nil && m != "" {
					info.UpstreamModelName = m
				}
			}
		}
	}

	// Apply param override after model patch so callers can override any field.
	if len(info.ParamOverride) > 0 {
		overridden, err := relaycommon.ApplyParamOverrideWithRelayInfo(rawBytes, info)
		if err != nil {
			return nil, fmt.Errorf("BuildRequestBody (volc native): apply param override failed: %w", err)
		}
		rawBytes = overridden
	}

	return bytes.NewReader(rawBytes), nil
}

// volcSubmitResponse is the minimal upstream Volc submit response shape.
type volcSubmitResponse struct {
	ID string `json:"id"`
}

// DoResponse parses the Volc-native submit response and returns the Volc-native
// shape ({"id": <public task id>}) to the client, so the Volc SDK's polling
// loop can immediately GET /api/v3/contents/generations/tasks/<id>.
// The upstream task ID is returned to the caller for local persistence.
func (a *TaskAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (taskID string, taskData []byte, taskErr *dto.TaskError) {
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		taskErr = service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
		return
	}
	_ = resp.Body.Close()

	var submitResp volcSubmitResponse
	if err := common.Unmarshal(responseBody, &submitResp); err != nil {
		taskErr = service.TaskErrorWrapper(errors.Wrapf(err, "body: %s", responseBody), "unmarshal_response_body_failed", http.StatusInternalServerError)
		return
	}
	if submitResp.ID == "" {
		taskErr = service.TaskErrorWrapper(fmt.Errorf("task id is empty"), "invalid_response", http.StatusInternalServerError)
		return
	}

	// Return the Volc-native submit shape byte-for-byte: the official Ark
	// POST /contents/generations/tasks response is exactly {"id": <task-id>}.
	// We substitute the PUBLIC task ID so the SDK polls our proxy fetch
	// endpoint (which maps public → upstream ID).
	c.JSON(http.StatusOK, gin.H{
		"id": info.PublicTaskID,
	})
	return submitResp.ID, responseBody, nil
}
