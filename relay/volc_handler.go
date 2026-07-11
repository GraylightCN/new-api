package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel/volcadapter"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

// VolcImageHelper handles the /api/v3/images/generations endpoint using the
// native Volc Ark API format (RelayFormatVolc).
//
// Default behavior: forward request body byte-identical to the upstream
// Volc API; Volc-specific fields such as sequential_image_generation,
// optimize_prompt_options, watermark, 2K/4K size literals etc. are preserved.
//
// Optional patches via applyVolcImagePatches:
//   - When info.IsModelMapped: rewrite the "model" field to the upstream name
//   - When len(info.ParamOverride) > 0: apply byte-level JSON patches
//
// Both patch paths preserve unknown fields by operating on
// map[string]json.RawMessage, so caller-supplied Volc-specific keys are
// never dropped.
//
// This mirrors the structure of GeminiHelper; the key difference is that
// the upstream URL is always the Volc /api/v3/images/generations path and
// our Volc image adaptor is used directly (selected by relay_format="volc",
// not by the underlying VolcEngine channel's api_type).
func VolcImageHelper(c *gin.Context, info *relaycommon.RelayInfo) (newAPIError *types.NewAPIError) {
	info.InitChannelMeta(c)

	volcReq, ok := info.Request.(*dto.VolcImageRequest)
	if !ok {
		return types.NewErrorWithStatusCode(
			fmt.Errorf("invalid request type, expected *dto.VolcImageRequest, got %T", info.Request),
			types.ErrorCodeInvalidRequest,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}

	request, err := common.DeepCopy(volcReq)
	if err != nil {
		return types.NewError(
			fmt.Errorf("failed to copy VolcImageRequest: %w", err),
			types.ErrorCodeInvalidRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}

	// model mapped 模型映射
	if err = helper.ModelMappedHelper(c, info, request); err != nil {
		return types.NewError(err, types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())
	}

	// Selection is by relay_format ("volc"), NOT by channel type: this endpoint
	// reuses the existing VolcEngine(45) channel, whose api_type would otherwise
	// resolve to the volcengine adaptor (which does not accept Volc-native image
	// requests). VolcImageHelper is only ever reached for RelayFormatVolc, so we
	// use our Volc image adaptor directly instead of GetAdaptor(info.ApiType).
	adaptor := &volcadapter.Adaptor{}
	adaptor.Init(info)

	if _, err = adaptor.ConvertVolcRequest(c, info, request); err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeConvertRequestFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}

	// Forward the raw body to upstream, applying model mapping and param override
	// at the byte level so that Volc-specific fields (sequential_image_generation,
	// optimize_prompt_options, watermark, etc.) are preserved unchanged.
	storage, storageErr := common.GetBodyStorage(c)
	if storageErr != nil {
		return types.NewErrorWithStatusCode(storageErr, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	rawBytes, readErr := storage.Bytes()
	if readErr != nil {
		return types.NewErrorWithStatusCode(readErr, types.ErrorCodeReadRequestBodyFailed, http.StatusInternalServerError, types.ErrOptionWithSkipRetry())
	}

	rawBytes, patchErr := applyVolcImagePatches(c, rawBytes, info)
	if patchErr != nil {
		return patchErr
	}

	requestBody := bytes.NewReader(rawBytes)

	logger.LogDebug(c, fmt.Sprintf("Volc image request model: %s -> %s", info.OriginModelName, info.UpstreamModelName))

	resp, doErr := adaptor.DoRequest(c, info, requestBody)
	if doErr != nil {
		logger.LogError(c, "Do volc request failed: "+doErr.Error())
		return types.NewOpenAIError(doErr, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}

	statusCodeMappingStr := c.GetString("status_code_mapping")

	var httpResp *http.Response
	if resp != nil {
		httpResp = resp.(*http.Response)
		info.IsStream = info.IsStream || strings.HasPrefix(httpResp.Header.Get("Content-Type"), "text/event-stream")
		if httpResp.StatusCode != http.StatusOK {
			newAPIError = service.RelayErrorHandler(c.Request.Context(), httpResp, false)
			service.ResetStatusCode(newAPIError, statusCodeMappingStr)
			return newAPIError
		}
	}

	usage, openaiErr := adaptor.DoResponse(c, httpResp, info)
	if openaiErr != nil {
		service.ResetStatusCode(openaiErr, statusCodeMappingStr)
		return openaiErr
	}

	service.PostTextConsumeQuota(c, info, usage.(*dto.Usage), nil)
	return nil
}

// applyVolcImagePatches applies byte-level patches to a raw Volc image request body:
//  1. If the model is mapped, replaces the "model" JSON field with the upstream model
//     name. All unknown Volc-specific fields (sequential_image_generation,
//     optimize_prompt_options, watermark, etc.) are preserved because the patch
//     operates on map[string]json.RawMessage, not a typed struct.
//  2. If ParamOverride is configured, applies it via the standard byte-level patch.
//
// On model-patch failure the function logs a warning and continues with the un-patched
// body (conservative: avoids introducing a new error path for a non-critical patch).
// On param-override failure the function returns an error.
func applyVolcImagePatches(c *gin.Context, rawBytes []byte, info *relaycommon.RelayInfo) ([]byte, *types.NewAPIError) {
	// 1. Model mapping patch — byte-level, preserves all unknown fields.
	if info.IsModelMapped && info.UpstreamModelName != "" {
		var bodyMap map[string]json.RawMessage
		if err := common.Unmarshal(rawBytes, &bodyMap); err != nil {
			logger.LogWarn(c, "applyVolcImagePatches: unmarshal body failed: "+err.Error())
		} else {
			newModel, err := common.Marshal(info.UpstreamModelName)
			if err != nil {
				logger.LogWarn(c, "applyVolcImagePatches: marshal upstream model name failed: "+err.Error())
			} else {
				bodyMap["model"] = newModel
				if patched, err := common.Marshal(bodyMap); err != nil {
					logger.LogWarn(c, "applyVolcImagePatches: marshal patched body failed: "+err.Error())
				} else {
					rawBytes = patched
				}
			}
		}
	}

	// 2. Param override — also byte-level, preserves unknown fields.
	if len(info.ParamOverride) > 0 {
		overridden, err := relaycommon.ApplyParamOverrideWithRelayInfo(rawBytes, info)
		if err != nil {
			return nil, newAPIErrorFromParamOverride(err)
		}
		rawBytes = overridden
	}

	return rawBytes, nil
}
