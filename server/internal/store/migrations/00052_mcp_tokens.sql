-- +goose Up
-- the mcp scope: a token that calls the MCP endpoint only. mcp_tools names
-- the tools such a token sees, at most 128 of them; an empty list means every
-- tool, and the column never carries a credential.
ALTER TABLE api_tokens DROP CONSTRAINT api_tokens_scopes_check;
ALTER TABLE api_tokens ADD CONSTRAINT api_tokens_scopes_check
    CHECK (cardinality(scopes) > 0 AND scopes <@ ARRAY['admin', 'ha', 'metrics', 'presence', 'mcp']::text[]);
ALTER TABLE api_tokens ADD COLUMN mcp_tools text[] NOT NULL DEFAULT '{}';
ALTER TABLE api_tokens ADD CONSTRAINT api_tokens_mcp_tools_check
    CHECK (cardinality(mcp_tools) <= 128);

-- +goose Down
-- a token left with no scope goes rather than become a wider one
UPDATE api_tokens SET scopes = array_remove(scopes, 'mcp');
DELETE FROM api_tokens WHERE cardinality(scopes) = 0;
ALTER TABLE api_tokens DROP CONSTRAINT api_tokens_scopes_check;
ALTER TABLE api_tokens ADD CONSTRAINT api_tokens_scopes_check
    CHECK (cardinality(scopes) > 0 AND scopes <@ ARRAY['admin', 'ha', 'metrics', 'presence']::text[]);
ALTER TABLE api_tokens DROP COLUMN mcp_tools;
