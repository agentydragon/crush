# MCP Hook Server — superseded

This proposal is merged into the Sequence Transformer design.

See: ./PROPOSAL-sequence-transformer.md

Notes on mapping
- hook.on_message_added → covered by phase "pre_turn_transition" with op "inject_messages" (and by pre_tool_exec via "deny_all_tools" + injection for synthetic tool errors).
- allow/allow_and_append → in new contract use decision: "allow" | "transform" and ops list (e.g., inject_messages). Appended messages support roles: system, assistant.
- History → v1 always sends FULL session history to the transformer; size-capped and truncated safely.
- Single server → still enforced: at most one MCP server with handles_sequence_transform=true.

Future work can add a simpler callback phase if needed, but the transformer model subsumes the original hook server use cases.
