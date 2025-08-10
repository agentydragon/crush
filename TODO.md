# OpenAI Responses API integration follow-ups

- Stream: handle `response.error` explicitly and close channel; currently only surfaced via stream.Err() in some cases
- Stream: consider `response.output_image.*` and other future output item types to avoid dropping UI signals
- Non-stream/Stream parity: persist encrypted reasoning (response.reasoning_item.encrypted_content) and include it into subsequent requests as a reasoning item; current code notes this but does not persist at the message/service layer
- ContentStop/segmentation: if multiple output_text segments exist, UI should treat `content_stop` as boundary; verify chat UI grouping
- Tool choice: expose config to force `tool_choice: required` for orchestrated steps
- Retry: refine error messaging for nil/EOF, and honor `Retry-After` with ms/s distinction; add jitter cap/backoff ceiling
- Tests: add end-to-end tests for tool-call ID normalization (itemID) and for streams that emit only `.done` without prior `.delta`
- Metrics: record rate-limit retries and forced tool-stop warnings for observability
