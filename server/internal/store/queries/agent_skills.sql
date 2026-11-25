-- name: ListAgentSkills :many
SELECT name, description, body, tools, triggers, modes, enabled, updated_at
FROM agent_skills
ORDER BY name COLLATE "C";

-- name: PutAgentSkill :one
INSERT INTO agent_skills (name, description, body, tools, triggers, modes, enabled, updated_at)
VALUES (@name, @description, @body, @tools, @triggers, @modes, @enabled, now())
ON CONFLICT (name) DO UPDATE SET
    description = EXCLUDED.description, body = EXCLUDED.body, tools = EXCLUDED.tools,
    triggers = EXCLUDED.triggers, modes = EXCLUDED.modes, enabled = EXCLUDED.enabled, updated_at = now()
RETURNING updated_at, (xmax = 0) AS created;

-- name: DeleteAgentSkill :execrows
DELETE FROM agent_skills WHERE name = $1;
