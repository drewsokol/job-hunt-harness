# 0004: Store and exchange all times in UTC

Status: Accepted, 2026-10-18

## Decision

- Everything stored is UTC, as RFC 3339 strings ending in `Z`. That includes dates people think of as calendar dates, like "applied on" and "follow up on".
- MCP tools and the JSON API take RFC 3339 timestamps. One with an offset is converted to UTC. One without a time zone is rejected with an error explaining the fix.
- The agent converts phrases like "yesterday" or "next Tuesday" from the user's local time to UTC. `get_setup_state` and `get_tracker` give it the current UTC time and the machine's time zone.
- The UI converts to local time for display only, and converts input back to UTC before sending it.
- Inside jhh (core, store, logs) everything is UTC, and a test helper rejects any non-UTC time that reaches the store.

## Reasons

A single rule removes a whole class of off-by-one-day and daylight-saving bugs, and keeps data portable between machines and agents.
