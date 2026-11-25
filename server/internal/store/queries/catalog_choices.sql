-- name: GetCatalogChoice :one
SELECT selection FROM catalog_choices WHERE title = @title;

-- name: PutCatalogChoice :exec
INSERT INTO catalog_choices (title, selection, updated_at)
VALUES (@title, @selection, now())
ON CONFLICT (title) DO UPDATE SET selection = EXCLUDED.selection, updated_at = EXCLUDED.updated_at;
