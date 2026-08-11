// Package cursor implements the agent adapter for the Cursor CLI: headless
// `cursor-agent -p --output-format stream-json` runs with stream-json event
// parsing, model passthrough, and tree-kill support (spec §9.7; T5.1).
// Invocation and stream shapes are pinned against cursor-agent
// 2026.08.04-aaa8809.
//
// The dialect is claude-*shaped* and is not claude's — it carries `thinking`
// events claude has no analog for, names tools by object key rather than a
// name field, reports usage in camelCase, and returns every assistant message
// concatenated as the result text. It therefore gets its own parser rather
// than sharing the claude one (§9.7).
package cursor
