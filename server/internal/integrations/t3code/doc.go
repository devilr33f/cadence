// Package t3code follows and steers threads on a T3 Code server
// (orchestration protocol 2) over its WebSocket RPC: one connection kept up
// with backoff, the shell and per-thread streams resumed from their last
// sequence, typed commands, and a normalized event layer (stopped, question,
// approval, error). Thread text, questions, answers and the token never reach
// a log or an error; errors are codes.
//
// Source is the module the subscriptions hub follows (module t3code): one
// follower per instance the t3code setting names, topics
// t3code:<instance>:thread:<id> (also matched as
// t3code:<instance>:project:<id>:thread:<id>), the shell for every thread and
// a thread's own stream while a subscription could match it and it is active.
// The hub's question tokens are HubToken's hash of T3's <requestId>.<revision>
// token; an answer finds its request again in the thread's projection.
//
// # Wire framing
//
// T3 Code's /ws route serves Effect RPC (effect 4.1.3, the version t3code's
// pnpm-lock.yaml pins) with RpcSerialization.layerJson, not JSON-RPC. What the
// client relies on, read from effect's own source
// (package/src/rpc/RpcMessage.ts, RpcSerialization.ts, RpcClient.ts,
// RpcServer.ts):
//
//   - each WebSocket text message is one JSON value: an object, or an array of
//     objects (RpcSerialization.ts json.decode: Array.isArray ? it : [it]);
//     the socket carries the framing, so there is no newline delimiting.
//   - client to server (RpcMessage.FromClientEncoded):
//     {"_tag":"Request","id":<number|string>,"tag":"<method>","payload":{...},"headers":[[k,v]...]}
//     (optional isNotification, discard, traceId, spanId, sampled);
//     {"_tag":"Ack","requestId":<id>}; {"_tag":"Interrupt","requestId":<id>};
//     {"_tag":"Ping"}; {"_tag":"Eof"}.
//   - server to client (FromServerEncoded):
//     {"_tag":"Chunk","requestId":<id>,"values":[...]} for a stream's items;
//     {"_tag":"Exit","requestId":<id>,"exit":{"_tag":"Success","value":...}} or
//     {"_tag":"Exit","requestId":<id>,"exit":{"_tag":"Failure","cause":[...]}} where
//     each cause entry is {"_tag":"Fail","error":<the rpc's error, a tagged
//     object>}, {"_tag":"Die","defect":...} or {"_tag":"Interrupt","fiberId":...};
//     {"_tag":"Defect","defect":...} (fatal, ends every request; T3 disables
//     fatal defects); {"_tag":"Pong"}; {"_tag":"ClientProtocolError",...}.
//     Unknown tags are ignored (RpcClient.ts run's default branch).
//   - a stream's Exit success has no value (Rpc.exitSchema: Schema.Void,
//     dropped by JSON.stringify): it ends the stream; a unary call's Exit
//     carries its result.
//   - acks: RpcServer.streamEffect closes a latch after every Chunk and waits
//     for the client's Ack before the next one (RpcClient.ts write sends Ack
//     once the values are queued). T3 wraps the protocol (apps/server/src/
//     terminal/OutputProtocol.ts withOutputWindow) so subscribeShell and
//     subscribeThread keep up to 8 chunks in flight, self-acking until that
//     window is full; this client acks each chunk once its handler ran, which
//     both schemes accept.
//   - ping: the server answers {"_tag":"Ping"} with {"_tag":"Pong"}
//     (RpcServer.ts); effect's client pings every 5 s by default.
//   - request ids are the client's own (RpcClient.ts: a counter); this client
//     numbers them per connection.
//
// T3 Code's own side (/Volumes/data/tmp/t3code): /ws wants
// ?orchestrationProtocol=2 (else 426, apps/server/src/ws.ts
// hasCompatibleOrchestrationProtocol) and authenticates the upgrade with a
// bearer token or ?wsTicket= (apps/server/src/auth/EnvironmentAuth.ts
// authenticateWebSocketUpgrade); a ticket comes from POST
// /api/auth/websocket-ticket with the bearer ({"ticket","expiresAt"}). The
// method names and shapes are packages/contracts/src/orchestrationV2.ts
// (ORCHESTRATION_V2_WS_METHODS, OrchestrationV2RpcSchemas).
//
// Uncertain: the encoded form of a Schema.Void stream exit (taken as an
// absent or null value, both accepted), and whether a server ever batches
// several frames into one array (handled either way).
package t3code
