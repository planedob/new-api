package model

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// Image2AsyncJob is the durable metadata/result record for asynchronous
// Image2 delivery. The request body is intentionally not persisted: it can
// contain prompts, image bytes, and other customer data. A submit node keeps
// an ephemeral copy only for the single execution it owns.
type Image2AsyncJob struct {
	ID             string                  `json:"id" gorm:"primaryKey;type:varchar(64)"`
	ScopeKey       string                  `json:"-" gorm:"type:varchar(191);uniqueIndex"`
	IdempotencyKey string                  `json:"-" gorm:"type:varchar(128);index"`
	UserID         int                     `json:"user_id" gorm:"index"`
	Operation      string                  `json:"operation" gorm:"type:varchar(32);index"`
	RequestID      string                  `json:"request_id" gorm:"type:varchar(128);index"`
	Status         string                  `json:"status" gorm:"type:varchar(16);index"`
	HTTPStatus     int                     `json:"http_status"`
	ResponseBody   Image2AsyncResponseBody `json:"-"`
	ErrorCode      string                  `json:"error_code,omitempty" gorm:"type:varchar(128)"`
	ErrorMessage   string                  `json:"error_message,omitempty" gorm:"type:text"`
	CreatedAt      time.Time               `json:"created_at" gorm:"index"`
	FinishedAt     *time.Time              `json:"finished_at,omitempty"`
	ExpiresAt      time.Time               `json:"-" gorm:"index"`
	LeaseOwner     string                  `json:"-" gorm:"type:varchar(128);index"`
	LeaseUntil     *time.Time              `json:"-" gorm:"index"`
}

// Image2AsyncResponseBody is a durable, already-sanitized API result. MySQL's
// TEXT is capped at 64 KiB, which is too small for a normal Image2 b64_json
// response; use LONGTEXT there while keeping TEXT on SQLite/PostgreSQL.
type Image2AsyncResponseBody string

func (Image2AsyncResponseBody) GormDataType() string {
	return "text"
}

func (Image2AsyncResponseBody) GormDBDataType(db *gorm.DB, _ *schema.Field) string {
	switch db.Dialector.Name() {
	case "mysql":
		return "longtext"
	default:
		return "text"
	}
}

const (
	Image2AsyncJobStatusPending   = "pending"
	Image2AsyncJobStatusRunning   = "running"
	Image2AsyncJobStatusSucceeded = "succeeded"
	Image2AsyncJobStatusFailed    = "failed"
)

// Image2AsyncJobSafeFailureBody is deliberately provider-neutral and contains
// no request or channel details. It is used when a process restarts or loses a
// lease before it can safely deliver the result; no upstream replay occurs.
const Image2AsyncJobSafeFailureBody = `{"error":{"message":"Image2 asynchronous delivery was interrupted; the request was not replayed","type":"server_error","code":"image2_async_interrupted"}}`

var errImage2AsyncDBUnavailable = errors.New("image2 async database is unavailable")

func ensureImage2AsyncDB() error {
	if DB == nil {
		return errImage2AsyncDBUnavailable
	}
	return nil
}

// CheckImage2AsyncJobSchema is an explicit startup/request preflight. Production
// nodes may set SKIP_DATABASE_MIGRATION=true, so a missing table must fail closed
// instead of being treated as an empty in-memory queue.
func CheckImage2AsyncJobSchema() error {
	if err := ensureImage2AsyncDB(); err != nil {
		return err
	}
	if !DB.Migrator().HasTable(&Image2AsyncJob{}) {
		return fmt.Errorf("image2 async schema is missing: table image2_async_jobs")
	}
	return nil
}

// CreateImage2AsyncJob persists a new idempotent job. ScopeKey is unique so
// concurrent submitters on different nodes cannot create two executions.
func CreateImage2AsyncJob(job *Image2AsyncJob) error {
	if err := ensureImage2AsyncDB(); err != nil {
		return err
	}
	return DB.Create(job).Error
}

func GetImage2AsyncJob(id string) (*Image2AsyncJob, error) {
	if err := ensureImage2AsyncDB(); err != nil {
		return nil, err
	}
	var job Image2AsyncJob
	if err := DB.Where("id = ?", id).First(&job).Error; err != nil {
		return nil, err
	}
	return &job, nil
}

func GetImage2AsyncJobByScope(scopeKey string) (*Image2AsyncJob, error) {
	if err := ensureImage2AsyncDB(); err != nil {
		return nil, err
	}
	var job Image2AsyncJob
	if err := DB.Where("scope_key = ?", scopeKey).First(&job).Error; err != nil {
		return nil, err
	}
	return &job, nil
}

// CountActiveImage2AsyncJobs is used for the bounded queue guard. Expired
// terminal rows are excluded so a cleanup delay cannot permanently block new
// work.
func CountActiveImage2AsyncJobs(now time.Time) (int64, error) {
	if err := ensureImage2AsyncDB(); err != nil {
		return 0, err
	}
	var count int64
	err := DB.Model(&Image2AsyncJob{}).
		Where("expires_at > ?", now).
		Where("status IN ?", []string{Image2AsyncJobStatusPending, Image2AsyncJobStatusRunning}).
		Count(&count).Error
	return count, err
}

// ClaimImage2AsyncJob is a compare-and-swap transition from pending to
// running. Only the winner may call the upstream relay. A running job is
// never reclaimed by another worker; stale recovery marks it failed instead,
// which prevents duplicate generation and duplicate billing.
func ClaimImage2AsyncJob(id, leaseOwner string, now, leaseUntil time.Time) (bool, error) {
	if err := ensureImage2AsyncDB(); err != nil {
		return false, err
	}
	if id == "" || leaseOwner == "" {
		return false, errors.New("image2 async claim requires id and lease owner")
	}
	result := DB.Model(&Image2AsyncJob{}).
		Where("id = ? AND status = ? AND (lease_until IS NULL OR lease_until <= ?)", id, Image2AsyncJobStatusPending, now).
		Updates(map[string]any{
			"status":      Image2AsyncJobStatusRunning,
			"lease_owner": leaseOwner,
			"lease_until": leaseUntil,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// CompleteImage2AsyncJob conditionally commits a result owned by leaseOwner.
// The response body is the already-sanitized API response, never the original
// request body. A false result means another owner already completed or
// safely failed the job, so the caller must not retry the upstream request.
func CompleteImage2AsyncJob(id, leaseOwner string, httpStatus int, responseBody []byte, errorCode, errorMessage string, now time.Time) (bool, error) {
	if err := ensureImage2AsyncDB(); err != nil {
		return false, err
	}
	if id == "" || leaseOwner == "" {
		return false, errors.New("image2 async completion requires id and lease owner")
	}
	status := Image2AsyncJobStatusFailed
	if httpStatus >= 200 && httpStatus < 300 {
		status = Image2AsyncJobStatusSucceeded
	}
	result := DB.Model(&Image2AsyncJob{}).
		Where("id = ? AND status = ? AND lease_owner = ?", id, Image2AsyncJobStatusRunning, leaseOwner).
		Updates(map[string]any{
			"status":        status,
			"http_status":   httpStatus,
			"response_body": Image2AsyncResponseBody(responseBody),
			"error_code":    errorCode,
			"error_message": errorMessage,
			"finished_at":   now,
			"lease_owner":   "",
			"lease_until":   nil,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// RecoverImage2AsyncJobs converts stale pending/running rows to a safe,
// terminal failure. Pending rows cannot be replayed after a restart because
// their request body is intentionally not durable; failing them is safer than
// guessing or charging a second time. The limit bounds startup work.
func RecoverImage2AsyncJobs(now, pendingStaleBefore time.Time, limit int) (int, error) {
	if err := ensureImage2AsyncDB(); err != nil {
		return 0, err
	}
	if limit <= 0 {
		return 0, nil
	}
	remaining := limit
	changed := 0
	changedRunning, err := recoverImage2AsyncJobsByQuery(
		DB.Where("status = ? AND (lease_until IS NULL OR lease_until <= ?)", Image2AsyncJobStatusRunning, now),
		now,
		remaining,
	)
	if err != nil {
		return changed, err
	}
	changed += changedRunning
	remaining -= changedRunning
	if remaining <= 0 {
		return changed, nil
	}
	changedPending, err := recoverImage2AsyncJobsByQuery(
		DB.Where("status = ? AND created_at <= ?", Image2AsyncJobStatusPending, pendingStaleBefore),
		now,
		remaining,
	)
	if err != nil {
		return changed, err
	}
	return changed + changedPending, nil
}

func recoverImage2AsyncJobsByQuery(query *gorm.DB, now time.Time, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	var jobs []Image2AsyncJob
	if err := query.Order("created_at").Limit(limit).Find(&jobs).Error; err != nil {
		return 0, err
	}
	changed := 0
	for _, job := range jobs {
		result := DB.Model(&Image2AsyncJob{}).
			Where("id = ? AND status = ?", job.ID, job.Status).
			Updates(map[string]any{
				"status":        Image2AsyncJobStatusFailed,
				"http_status":   503,
				"response_body": Image2AsyncJobSafeFailureBody,
				"error_code":    "image2_async_interrupted",
				"error_message": "Image2 asynchronous delivery was interrupted; the request was not replayed",
				"finished_at":   now,
				"lease_owner":   "",
				"lease_until":   nil,
			})
		if result.Error != nil {
			return changed, result.Error
		}
		if result.RowsAffected > 0 {
			changed += int(result.RowsAffected)
		}
	}
	return changed, nil
}

// PruneExpiredImage2AsyncJobs deletes at most limit expired terminal rows.
// Processing rows are retained even when a caller supplies an old expiry so
// cleanup cannot erase an in-flight result.
func PruneExpiredImage2AsyncJobs(now time.Time, limit int) (int, error) {
	if err := ensureImage2AsyncDB(); err != nil {
		return 0, err
	}
	if limit <= 0 {
		return 0, nil
	}
	var jobs []Image2AsyncJob
	if err := DB.Where("expires_at <= ? AND status IN ?", now, []string{Image2AsyncJobStatusSucceeded, Image2AsyncJobStatusFailed}).
		Order("expires_at").Limit(limit).Find(&jobs).Error; err != nil {
		return 0, err
	}
	if len(jobs) == 0 {
		return 0, nil
	}
	ids := make([]string, 0, len(jobs))
	for _, job := range jobs {
		ids = append(ids, job.ID)
	}
	result := DB.Where("id IN ?", ids).Delete(&Image2AsyncJob{})
	return int(result.RowsAffected), result.Error
}
