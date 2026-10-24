# 0008: Ship only sources that are free to use

Status: Accepted, 2026-10-24. Supersedes the keys part of [0007](0007-source-settings.md).

## Context

Decision 0007 planned Adzuna as a search source, with each user's own key entered only in the dashboard and kept in `keys.json`. Adzuna's API terms list personal job search as a permitted use and say every registered user gets a free key, so the plan assumed it would cost nothing.

Signing up showed otherwise. A new account is on "Pending Activation" with 25 test calls in all. Real use (500 calls a day) is a paid plan at £8 a month, and starting it means adding a card. One jhh refresh can make 25 calls.

## Decision

- **Every source jhh ships is free to use**, without a paid plan, a trial or a card. A source is checked by signing up or calling it, not only by reading its terms, before it goes into the design.
- **Adzuna is dropped.**
- **Key storage isn't built.** `keys.json`, the dashboard's key entry and the rules around keys only existed for Adzuna. If a source that is free but needs a key comes up, its design starts from 0007's rules for keys (entered only in the dashboard, kept out of the database, never returned or logged).

## Consequences

- Every search source runs from the first refresh or after the agent sets it up, with nothing for the user to buy or register.
- Some coverage is out of reach: aggregators that sell their data, and services whose free tier is too small for a refresh.
