# Decision records

Short records of design decisions and why they were made. `design.md` describes how the system works now; these keep the reasons, so a later change can see what it is undoing.

Add a new numbered file for each decision. Don't rewrite old records: when a decision changes, write a new record that supersedes the old one, and update the old one's status line.

| # | Decision | Status |
|---|---|---|
| [0001](0001-go.md) | Build in Go | Accepted |
| [0002](0002-agent-owns-judgment.md) | The agent writes the profile, memories and filters; the UI is a window | Accepted |
| [0003](0003-dedupe-identity-ladder.md) | Dedupe with a ladder of identity signals, not company + title | Accepted |
| [0004](0004-utc-everywhere.md) | Store and exchange all times in UTC | Accepted |
| [0005](0005-untrusted-content.md) | Label listing content as untrusted; flag prompt injection for review | Accepted |
| [0006](0006-agent-as-source.md) | The agent can add jobs (`add_jobs`) | Accepted |
| [0007](0007-source-settings.md) | Sources are turned on and set up per source, not in the filters; keys are entered only in the dashboard | Accepted; keys set aside by 0008, restored as optional by 0009 |
| [0008](0008-free-sources-only.md) | Ship only sources that are free to use; drop Adzuna and the planned key storage | Accepted; key storage superseded by 0009 |
| [0009](0009-optional-keys.md) | A free source may take an optional key, entered only in the dashboard (Himalayas) | Accepted |
