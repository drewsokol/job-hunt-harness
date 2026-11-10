# Examples

Everything here is made up.

- `filters.json`: filters for a fictional senior engineer in Columbus, Ohio, who wants AI-related work, either remote in the US or in an office near home, paying at least $150,000. The format is described in [docs/design.md](../docs/design.md) under "Filters" and "Prefilter". Try it with `jhh filters preview examples/filters.json`.
- `himalayas.json`: Himalayas's settings for the same person: two searches for senior remote jobs open to people in the US, one for machine learning engineering and one for AI platform work. Himalayas lists only remote jobs, so the office half of the filters is the boards' job. The format is described in [docs/design.md](../docs/design.md) under "Himalayas". Try it with `jhh sources set himalayas examples/himalayas.json`.
