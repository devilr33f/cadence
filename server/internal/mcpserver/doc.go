// Package mcpserver serves Cadence over MCP: the streamable endpoint at
// /api/v1/mcp behind an mcp-scoped API token, so an outside AI client (an
// editor, an agent harness) can read and steer the household the way the
// dashboard and Home Assistant do.
//
// The tools are two kinds. The agent's own (music, catalog, media control,
// the home, tasks…) run exactly as a turn runs them, under a synthetic
// pressed call: the same result ids, the same queues, the same rules. Beside
// them a few service-backed tools (devices, playback, history, links, say,
// typed assistant turns) read and act through the same seams the admin api
// and the integrations use.
//
// A token names the tools it sees (api_tokens.mcp_tools, none named: every
// tool); each request serves exactly that list, so a tool outside the
// policy does not exist for that token's client — its list and its calls
// cannot drift apart across an edit.
//
// # Privacy
//
// A tool's result may carry media metadata and an assistant's reply — the
// owner's own client asked for them — but this package's logs carry tool
// names, codes and timings only: no transcript, no title, no token, no
// stream url. Stream links are the relay's token-sealed ones, with their own
// expiry, never a provider's signed url.
package mcpserver
