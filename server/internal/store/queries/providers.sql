-- name: CreateProviderInstance :one
INSERT INTO provider_instances (name, module, config, secrets) VALUES ($1, $2, $3, $4)
RETURNING id, name, module, config, secrets, created_at;

-- name: GetProviderInstance :one
SELECT id, name, module, config, secrets, created_at FROM provider_instances WHERE name = $1;

-- name: ListProviderInstances :many
SELECT id, name, module, config, secrets, created_at FROM provider_instances ORDER BY created_at, name;

-- name: DeleteProviderInstance :execrows
DELETE FROM provider_instances WHERE name = $1;

-- name: ExistingProviderInstances :many
SELECT name FROM provider_instances WHERE name = ANY(@names::text[]);

-- name: UpdateProviderSecrets :execrows
-- a module that rotates a secret (cookies a site renewed) saves it; the id keeps a build of a deleted instance from
-- writing over the one created again under its name
UPDATE provider_instances SET secrets = @secrets WHERE name = @name AND id = @id;

-- name: UpdateProviderInstance :one
-- an edit keeps the instance's identity and module; the id keeps an edit of a deleted instance from replacing its
-- successor under the same name
UPDATE provider_instances SET config = @config, secrets = @secrets WHERE name = @name AND id = @id
RETURNING id, name, module, config, secrets, created_at;
