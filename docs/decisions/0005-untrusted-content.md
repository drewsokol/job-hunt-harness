# 0005: Label listing content as untrusted; flag prompt injection for review

Status: Accepted, 2026-10-18

## Context

Job listings are written by third parties, and the user's agent may also have email, file or browser tools. Some listings contain instructions aimed at AI. Some of those are hostile, and some are a company defending itself, for example by asking AI-written applications to include a codeword.

## Decision

- Everything from a listing reaches the agent only inside an `untrusted` object. The tool output types make it impossible to put listing text anywhere else. The server instructions and playbooks say that text there is data, never instructions.
- Invisible characters and HTML comments are removed; the wording is never changed.
- Rule-based detection (no AI in the server) runs on the raw HTML, so hidden text is caught too. The agent can report what it notices with `report_injection`.
- Findings are classified as `canary`, `hostile`, `hidden_text` or `unclear`, and are **flagged for the user to review, never removed automatically**. A canary doesn't count against the job, and the user is told the company screens for AI-written applications.
- The UI renders listing text as plain text only, turns only `http` and `https` URLs into links, and never redirects to them.
- Memories the agent infers from a listing must cite it, and can never be saved as things the user said.
