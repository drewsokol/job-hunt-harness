# Examples

Everything here is made up.

- `filters.json`: filters for a fictional senior engineer in Columbus, Ohio, who wants AI-related work, either remote in the US or in an office near home, paying at least $150,000. The format is described in [docs/design.md](../docs/design.md) under "Filters" and "Prefilter". Try it with `jhh filters preview examples/filters.json`.
- `themuse.json`: The Muse's settings for the same person: one query for senior software engineering and data science jobs in Columbus, which also returns remote jobs. The format is described in [docs/design.md](../docs/design.md) under "Turning sources on and off". Try it with `jhh sources set themuse examples/themuse.json`.
- `himalayas.json`: Himalayas's settings for the same person: two searches for senior remote jobs open to people in the US, one for machine learning engineering and one for AI platform work. Himalayas lists only remote jobs, so the office half of the filters is The Muse's and the boards' job. The format is described in [docs/design.md](../docs/design.md) under "Himalayas". Try it with `jhh sources set himalayas examples/himalayas.json`.
