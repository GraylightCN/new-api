package relay

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// init registers the Volc-native task list response builder so that
// RelayModeVideoFetchList requests are shaped by videoFetchListRespBodyBuilder.
// The by-ID (RelayModeVideoFetchByID) path is handled inline in relay_task.go's
// videoFetchByIDRespBodyBuilder via a relay_format == "volc" branch.
func init() {
	fetchRespBuilders[relayconstant.RelayModeVideoFetchList] = videoFetchListRespBodyBuilder
}

// buildVolcNativeTaskFetchResp returns the Volc-native ContentGenerationTask JSON
// for a GET /api/v3/contents/generations/tasks/:id response.
//
// If task.Data already contains a polled upstream response (has "status" field),
// it is returned with the "id" field patched to the public task ID. Otherwise a
// minimal synthesized response is returned using the task's internal status so
// the SDK can determine the current state without waiting for the next background
// poll cycle.
func buildVolcNativeTaskFetchResp(t *model.Task) []byte {
	// Check if task.Data contains a full polled response (has "status" key).
	if len(t.Data) > 0 {
		var probe map[string]json.RawMessage
		if common.Unmarshal(t.Data, &probe) == nil {
			if _, hasStatus := probe["status"]; hasStatus {
				// task.Data is an upstream Volc response — return it with the
				// public task ID so the SDK's polling loop can match responses.
				if idJSON, err := common.Marshal(t.TaskID); err == nil {
					probe["id"] = json.RawMessage(idJSON)
				}
				if patched, err := common.Marshal(probe); err == nil {
					return patched
				}
				return t.Data
			}
		}
	}

	// No polled data yet — synthesize a minimal response.
	arkStatus := mapTaskStatusToArkStatus(t.Status, t.FailReason)
	modelName := t.Properties.OriginModelName
	if modelName == "" {
		modelName = t.Properties.UpstreamModelName
	}
	synth := map[string]interface{}{
		"id":         t.TaskID,
		"model":      modelName,
		"status":     arkStatus,
		"created_at": t.CreatedAt,
		"updated_at": t.UpdatedAt,
	}
	if t.Status == model.TaskStatusSuccess {
		synth["content"] = map[string]string{
			"video_url":      t.GetResultURL(),
			"last_frame_url": "",
			"file_url":       "",
		}
		synth["usage"] = map[string]int{"completion_tokens": 0}
	}
	if t.FailReason != "" {
		synth["error"] = map[string]string{
			"message": t.FailReason,
			"code":    mapFailReasonToErrorCode(t.FailReason),
		}
	}
	b, err := common.Marshal(synth)
	if err != nil {
		// Fallback: use common.Marshal for individual values to avoid JSON injection.
		idJSON, _ := common.Marshal(t.TaskID)
		statusJSON, _ := common.Marshal(arkStatus)
		return []byte(`{"id":` + string(idJSON) + `,"status":` + string(statusJSON) + `}`)
	}
	return b
}

type volcVideoTaskListItem struct {
	ID        string `json:"id"`
	Model     string `json:"model,omitempty"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

type volcVideoTaskListResponse struct {
	Items []volcVideoTaskListItem `json:"items"`
	Total int64                   `json:"total"`
}

func videoFetchListRespBodyBuilder(c *gin.Context) (respBody []byte, taskResp *dto.TaskError) {
	userID := c.GetInt("id")
	pageNum := parseVolcPositiveInt(c.DefaultQuery("page_num", "1"), 1)
	pageSize := parseVolcPositiveInt(c.DefaultQuery("page_size", "10"), 10)
	if pageSize > 100 {
		pageSize = 100
	}
	startIdx := (pageNum - 1) * pageSize

	queryParams := model.SyncTaskQueryParams{
		Platform: constant.TaskPlatformVolcNative,
	}

	// rawFilterStatus is the original filter.status string from the request.
	// It is preserved so that matchesVolcListFilter can apply a secondary
	// FailReason filter for "cancelled" and "expired" (which both map to the
	// same internal TaskStatusFailure at the DB layer).
	rawFilterStatus := strings.ToLower(strings.TrimSpace(c.Query("filter.status")))

	if rawFilterStatus != "" {
		queryParams.Status = mapArkTaskStatusToInternal(rawFilterStatus)
		if queryParams.Status == "" {
			emptyResp, err := common.Marshal(volcVideoTaskListResponse{
				Items: []volcVideoTaskListItem{},
				Total: 0,
			})
			if err != nil {
				return nil, service.TaskErrorWrapper(err, "marshal_response_failed", http.StatusInternalServerError)
			}
			return emptyResp, nil
		}
	}

	modelFilter := strings.TrimSpace(c.Query("filter.model"))
	taskIDsFilter := parseVolcTaskIDs(c)

	// needsSecondaryStatusFilter is true when the caller requested a status that
	// maps to TaskStatusFailure at the DB layer but requires further narrowing
	// in-memory via FailReason ("cancelled" / "expired" / genuine "failed").
	needsSecondaryStatusFilter := rawFilterStatus == "cancelled" || rawFilterStatus == "expired" || rawFilterStatus == "failed"

	var filtered []*model.Task
	var total int64

	if modelFilter == "" && len(taskIDsFilter) == 0 && !needsSecondaryStatusFilter {
		// No client-side filters: delegate offset/limit directly to the DB layer.
		filtered = model.TaskGetAllUserTask(userID, startIdx, pageSize, queryParams)
		total = model.TaskCountAllUserTask(userID, queryParams)
	} else {
		// Client-side filters (model name, task_ids, or FailReason sub-class) are
		// applied after fetch because the DB layer has no columns for these predicates.
		filtered, total = listFilteredVolcVideoTasks(userID, queryParams, modelFilter, taskIDsFilter, rawFilterStatus, startIdx, pageSize)
	}

	items := make([]volcVideoTaskListItem, 0, len(filtered))
	for _, task := range filtered {
		items = append(items, volcVideoTaskListItem{
			ID:        task.TaskID,
			Model:     task.Properties.OriginModelName,
			Status:    mapTaskStatusToArkStatus(task.Status, task.FailReason),
			CreatedAt: task.CreatedAt,
			UpdatedAt: task.UpdatedAt,
		})
	}

	resp, err := common.Marshal(volcVideoTaskListResponse{
		Items: items,
		Total: total,
	})
	if err != nil {
		return nil, service.TaskErrorWrapper(err, "marshal_response_failed", http.StatusInternalServerError)
	}
	return resp, nil
}

// listScanCap is the maximum number of tasks scanned when client-side filters
// (model name, task_ids, or FailReason sub-class) are active.
const listScanCap = 5000

// matchesVolcListFilter reports whether task passes all in-memory list filters.
func matchesVolcListFilter(task *model.Task, modelFilter string, taskIDsFilter map[string]bool, rawFilterStatus string) bool {
	if len(taskIDsFilter) > 0 && !taskIDsFilter[task.TaskID] {
		return false
	}
	if modelFilter != "" && task.Properties.OriginModelName != modelFilter && task.Properties.UpstreamModelName != modelFilter {
		return false
	}
	switch rawFilterStatus {
	case "cancelled":
		return task.FailReason == "cancelled"
	case "expired":
		return task.FailReason == "expired"
	case "failed":
		// "failed" means a genuine failure — exclude tasks only sub-classified
		// as cancelled or expired (they have their own status).
		return task.FailReason != "cancelled" && task.FailReason != "expired"
	}
	return true
}

// listFilteredVolcVideoTasks scans forward through the user's task set (up to
// listScanCap rows) applying in-memory filters via matchesVolcListFilter, then
// returns the requested page slice and the total number of matching rows found.
func listFilteredVolcVideoTasks(
	userID int,
	queryParams model.SyncTaskQueryParams,
	modelFilter string,
	taskIDsFilter map[string]bool,
	rawFilterStatus string,
	startIdx int,
	pageSize int,
) ([]*model.Task, int64) {
	const chunkSize = 200
	var collected []*model.Task
	need := startIdx + pageSize // stop scanning once we have enough to satisfy this page

	for chunkStart := 0; chunkStart < listScanCap; chunkStart += chunkSize {
		remaining := listScanCap - chunkStart
		fetch := chunkSize
		if fetch > remaining {
			fetch = remaining
		}
		chunk := model.TaskGetAllUserTask(userID, chunkStart, fetch, queryParams)
		for _, task := range chunk {
			if !matchesVolcListFilter(task, modelFilter, taskIDsFilter, rawFilterStatus) {
				continue
			}
			collected = append(collected, task)
		}
		if len(chunk) < fetch {
			break // reached end of result set
		}
		if len(collected) >= need {
			break // collected enough to satisfy this page
		}
	}

	total := int64(len(collected))
	if startIdx >= len(collected) {
		return []*model.Task{}, total
	}
	end := startIdx + pageSize
	if end > len(collected) {
		end = len(collected)
	}
	return collected[startIdx:end], total
}

func parseVolcPositiveInt(raw string, defaultVal int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return defaultVal
	}
	return value
}

func parseVolcTaskIDs(c *gin.Context) map[string]bool {
	result := map[string]bool{}
	values := c.QueryArray("filter.task_ids")
	if len(values) == 0 {
		if single := c.Query("filter.task_ids"); single != "" {
			values = []string{single}
		}
	}
	for _, value := range values {
		for _, id := range strings.Split(value, ",") {
			id = strings.TrimSpace(id)
			if id != "" {
				result[id] = true
			}
		}
	}
	return result
}

func mapArkTaskStatusToInternal(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued":
		return string(model.TaskStatusQueued)
	case "running":
		return string(model.TaskStatusInProgress)
	case "succeeded":
		return string(model.TaskStatusSuccess)
	case "failed", "cancelled", "expired":
		return string(model.TaskStatusFailure)
	default:
		return ""
	}
}

// mapTaskStatusToArkStatus maps an internal task status and optional fail reason
// to the Volc Ark status string. Cancelled and expired tasks are distinguished
// from generic failures via FailReason so the SDK can handle them correctly.
func mapTaskStatusToArkStatus(status model.TaskStatus, failReason string) string {
	switch status {
	case model.TaskStatusQueued, model.TaskStatusSubmitted, model.TaskStatusNotStart:
		return "queued"
	case model.TaskStatusInProgress:
		return "running"
	case model.TaskStatusSuccess:
		return "succeeded"
	case model.TaskStatusFailure:
		switch failReason {
		case "cancelled":
			return "cancelled"
		case "expired":
			return "expired"
		}
		return "failed"
	default:
		return "running"
	}
}

// mapFailReasonToErrorCode maps a task fail reason to the Volc error code string.
func mapFailReasonToErrorCode(failReason string) string {
	switch failReason {
	case "cancelled":
		return "cancelled"
	case "expired":
		return "expired"
	}
	return "task_failed"
}
