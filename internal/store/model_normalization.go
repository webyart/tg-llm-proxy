package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// ModelNormalization is the number of rows NormalizeModelName rewrote. It carries counts only: the caller logs
// how many chat selections, live task rows and pending approvals moved, never which chat, user or task they
// belong to.
type ModelNormalization struct {
	ChatSettings int64
	Tasks        int64
	// Continuations counts awaiting-approval tasks whose stored continuation was rewritten to name the new
	// model as well. It is a subset of Tasks.
	Continuations int64
}

// NormalizeModelName rewrites persisted selections that still name the model this deployment served before
// startup discovery changed it. A single-model endpoint can come up with a model other than STRATA_MODEL; the
// chat selection in chat_settings and the model column of a task are copies of that configured name, so leaving
// them alone sends every selected chat and every live task to a name the endpoint no longer offers (the
// fallback answers, or the provider rejects the call) even though fresh requests use the discovered model.
//
// Only rows that still equal the old name are touched: another chat's explicit selection and any other model
// stay as they are. Only ACTIVE task rows are touched — planning, ready, running and awaiting_approval still
// have model calls ahead of them — while terminal rows (completed, failed, cancelled) and the model recorded on
// historical messages and stored step results are history and must not be rewritten. There is no migration:
// this is a startup repair over the existing schema, idempotent because the second call matches no row.
//
// An awaiting_approval task also carries the old name inside continuation_json (the snapshot is restored and
// its request.Model is compared with tasks.model before the approval is applied), so the same transaction
// rewrites that field too. Without it the normalized row and its continuation would disagree and the pending
// approval would be dropped as an invalid continuation.
//
// The update is one transaction, so a crash cannot leave chat selections normalized while the live tasks still
// point at the old name. updated_at of a task is deliberately not bumped: the column orders task progress, and a
// startup repair is not progress (a planning row keeps its queue position).
func NormalizeModelName(ctx context.Context, db *sql.DB, from, to string) (ModelNormalization, error) {
	var counts ModelNormalization
	from, err := validModelName(from)
	if err != nil {
		return counts, fmt.Errorf("normalize model: old name: %w", err)
	}
	to, err = validModelName(to)
	if err != nil {
		return counts, fmt.Errorf("normalize model: new name: %w", err)
	}
	if from == to {
		return counts, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return counts, err
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `UPDATE chat_settings SET model = ? WHERE model = ?`, to, from)
	if err != nil {
		return counts, err
	}
	if counts.ChatSettings, err = result.RowsAffected(); err != nil {
		return counts, err
	}

	// The continuation must be read before the task row stops naming the old model.
	var continuations []struct {
		id  int64
		raw string
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, continuation_json FROM tasks
		WHERE model = ? AND status = ? AND continuation_json IS NOT NULL AND continuation_json <> ''`,
		from, TaskStatusAwaitingApproval)
	if err != nil {
		return counts, err
	}
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return counts, err
		}
		if rewritten, changed := normalizeContinuationModel(raw, from, to); changed {
			continuations = append(continuations, struct {
				id  int64
				raw string
			}{id: id, raw: rewritten})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return counts, err
	}
	if err := rows.Close(); err != nil {
		return counts, err
	}

	// ActiveTaskStatus is the single definition of "this row still has work to do"; the four constants below
	// are the same set spelled for SQL.
	result, err = tx.ExecContext(ctx, `UPDATE tasks SET model = ? WHERE model = ? AND status IN (?, ?, ?, ?)`,
		to, from, TaskStatusPlanning, TaskStatusReady, TaskStatusRunning, TaskStatusAwaitingApproval)
	if err != nil {
		return counts, err
	}
	if counts.Tasks, err = result.RowsAffected(); err != nil {
		return counts, err
	}
	for _, continuation := range continuations {
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET continuation_json = ? WHERE id = ?`, continuation.raw, continuation.id); err != nil {
			return counts, err
		}
	}
	counts.Continuations = int64(len(continuations))
	if err := tx.Commit(); err != nil {
		return counts, err
	}
	return counts, nil
}

// normalizeContinuationModel rewrites request.Model inside a stored task continuation snapshot. model.Request
// carries Go field names (it has no json tags), so the key is "Model"; every other key is preserved as raw JSON
// and an unknown or unparsable snapshot is left exactly as it was (the restore path already rejects it).
func normalizeContinuationModel(raw, from, to string) (string, bool) {
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return raw, false
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(snapshot["request"], &request); err != nil {
		return raw, false
	}
	var model string
	if err := json.Unmarshal(request["Model"], &model); err != nil || model != from {
		return raw, false
	}
	encoded, err := json.Marshal(to)
	if err != nil {
		return raw, false
	}
	request["Model"] = encoded
	encodedRequest, err := json.Marshal(request)
	if err != nil {
		return raw, false
	}
	snapshot["request"] = encodedRequest
	encodedSnapshot, err := json.Marshal(snapshot)
	if err != nil {
		return raw, false
	}
	return string(encodedSnapshot), true
}
