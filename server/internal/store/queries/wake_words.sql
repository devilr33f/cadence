-- name: InsertWakeWord :one
INSERT INTO wake_words (word, source, status, model, sha256, threshold, takes, recall, false_per_hour, eval_hours,
    dataset, trained_at)
VALUES (@word, @source, @status, @model, @sha256, @threshold, @takes, @recall, @false_per_hour, @eval_hours,
    @dataset, @trained_at)
RETURNING id, created_at;

-- name: ListWakeWords :many
-- everything but the model's and the dataset's bytes
SELECT id, word, source, status, error_code, sha256, threshold, takes, recall, false_per_hour, eval_hours,
    created_at, trained_at, coalesce(octet_length(model), 0)::bigint AS model_size,
    coalesce(octet_length(dataset), 0)::bigint AS dataset_size
FROM wake_words ORDER BY created_at DESC, id;

-- name: GetWakeWordInfo :one
SELECT id, word, source, status, error_code, sha256, threshold, takes, recall, false_per_hour, eval_hours,
    created_at, trained_at, coalesce(octet_length(model), 0)::bigint AS model_size,
    coalesce(octet_length(dataset), 0)::bigint AS dataset_size
FROM wake_words WHERE id = @id;

-- name: GetWakeWordModel :one
SELECT sha256, model FROM wake_words WHERE id = @id AND status = 'ready';

-- name: GetWakeWordDataset :one
SELECT word, dataset FROM wake_words WHERE id = @id AND dataset IS NOT NULL;

-- name: StartWakeWordTraining :execrows
-- a retrain: the stored dataset stays, the model goes until the new one is ready
UPDATE wake_words SET status = 'training', error_code = '', model = NULL, sha256 = NULL, threshold = NULL,
    takes = NULL, recall = NULL, false_per_hour = NULL, eval_hours = NULL
WHERE id = @id AND status <> 'training';

-- name: FinishWakeWordTraining :execrows
UPDATE wake_words SET status = 'ready', error_code = '', model = @model, sha256 = @sha256, threshold = @threshold,
    takes = @takes, recall = @recall, false_per_hour = @false_per_hour, eval_hours = @eval_hours, trained_at = @trained_at
WHERE id = @id AND status = 'training';

-- name: FailWakeWordTraining :execrows
UPDATE wake_words SET status = 'failed', error_code = @error_code WHERE id = @id AND status = 'training';

-- name: FailInterruptedWakeWords :execrows
-- at the server's start: a training no job runs any more
UPDATE wake_words SET status = 'failed', error_code = 'interrupted' WHERE status = 'training';

-- name: RenameWakeWord :execrows
UPDATE wake_words SET word = @word WHERE id = @id;

-- name: DeleteWakeWordDataset :execrows
UPDATE wake_words SET dataset = NULL WHERE id = @id AND status <> 'training';

-- name: DeleteWakeWord :execrows
DELETE FROM wake_words WHERE id = @id;
