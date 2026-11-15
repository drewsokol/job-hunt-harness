# 0004: An "Ask your agent" page on the dashboard

Status: Draft

## Problem

Everything jhh does starts with the user saying something to their agent, but nothing in jhh lists what they can say. The workflows live in playbooks (MCP prompts) and in tool descriptions the user never sees. The README names a few phrases, and some dashboard empty states show one, such as "initialize job hunt harness" before setup and "screen new jobs" when jobs wait. Once those states pass, the phrases disappear.

So users find features by accident or not at all. Someone who set up jhh a month ago doesn't know they can rerun the interview when their search changes, debrief an interview, or ask what's new each morning. And nothing explains the one habit that isn't obvious: some work, like interview prep, goes best in a session of its own, because it fills a long chat and jhh carries the state between sessions.

Example (made up): a user tracks applications on the dashboard every day. Their search shifts from platform roles toward backend roles. Not knowing the interview can be run again, they ask their agent to "change my profile" one line at a time, and the filters and memories never catch up.

## Design

### The page

- **A "?" button in the dashboard's header** opens the page. It isn't one of the tabs, which stay about the user's jobs. It has its own address (`#help`), so it can be linked and bookmarked, and the browser's back button leaves it.
- **Title: "Ask your agent".** One line under it: jhh does its work through your agent; say any of these in a chat with it, in your own words.
- **One entry per workflow**, in the order a user meets them. Each has:
  - its name ("Prepare for an interview");
  - one or two sentences on what it does and when to use it;
  - the phrase to say ("help me prepare for my interview with Exampleco"), with a button that copies it;
  - for workflows that are playbooks, the slash command too (see below), with its own copy button;
  - a note "Best in a new session" where that applies, with the reason in a sentence.
- **A short "Sessions" section** at the end: the main search (check-ins, screening, tracking) can live in one long-running session; prep and the profile interview work best in a new one each time, because they fill the chat; jhh keeps everything, so any session picks up where another left off.
- **A line on slash commands**: in Claude Code, playbooks are slash commands named after the server, such as `/mcp__jhh__interview_me` when jhh was added as "jhh". Other clients list them in their prompts menu. The page can't know the name the user gave jhh, so it says to use their own if it isn't "jhh".

### Where the entries come from

- **Playbooks** come from `playbooks.All`, so a new playbook appears on the page without anyone remembering to add it. Each playbook gains two fields: `Phrase`, the one thing a user would say to start it, and `Help`, the page's text about it, written for the user. `Description` stays as it is: it's the agent-facing text of the MCP prompt. A playbook has one phrase; anything else the user can say for it (such as asking for a debrief) goes in its help text.
- **Workflows that aren't playbooks** (the daily check-in with `whats_new`, refreshing jobs, logging an application, tracking outreach, logging an interview, opening the dashboard) are a short fixed list in `internal/web`, with the same fields. "Log an interview" covers the debrief too (`save_interview` with debrief notes, the outcome and the next step), in its help text.
- **A fixed entry can name the playbook that replaces it.** "Log an interview" names interview_prep: while that playbook is in `playbooks.All`, the fixed entry is hidden, and the playbook's entry, whose help text covers preparing and the debrief, takes its place. So the page reads right whichever of this spec and spec 0003 is built first.
- A new endpoint, `GET /api/help`, returns the entries in order, with a "best in a new session" flag and the reason. It's behind the same sign-in as the rest of the API.
- The order: set up (initialize); interview me (interview_me); what's new; screen new jobs (screen_new_jobs); refresh; log an application; outreach; log an interview, or interview_prep in its place; open the dashboard. Playbooks without a place in this order go at the end, so a new one still shows.

### Pointing to it

- **The first-run screen** ("Set up jhh with your agent") gains a link: "See everything you can ask".
- **Empty states that show a phrase** get the same copy button, built by the same helper as the page.
- **`open_dashboard`'s result** gains a `help_url` field: the plain address `http://127.0.0.1:<port>/#help`, built from the port in use. `url` stays the one-time sign-in link it is today. The result's note says `help_url` works once the browser has signed in through `url`. Its description gains one sentence telling the agent to mention the page the first time the user opens the dashboard, or when they ask what jhh can do. The tool list stays within `TestOutputSizes`' cap.
- **README**: the "Connect your agent" section mentions the page.

### Rules

- **No new tools and no data.** The page is read-only text from jhh's own code; nothing about the user's search appears on it.
- **Copying** uses the browser's clipboard. The dashboard runs on `127.0.0.1`, which browsers treat as secure, so it works without a prompt. If it fails, the phrase is selected so the user can copy it by hand, and a short message says so.
- **Every text is built with the `h` helper**, as now; no HTML is parsed.
- **Phrases use made-up companies** ("Exampleco"), like the rest of jhh's text.
- **It works with the keyboard**: the "?" button and every copy button can be reached with Tab, and Escape closes the page, returning to the last tab.

## Out of scope

- A tutorial, a tour or anything that runs on its own.
- Running a workflow from the dashboard. The page tells the user what to say; the agent does the work.
- Telling the user prep exists when an interview is close. That's spec 0003 (`whats_new` and "Not prepped yet").
- Detecting which agent or client the user has.

## Acceptance checks

Go tests (no JavaScript test framework is added):

- `/api/help` needs the dashboard sign-in, like every other API.
- Every playbook in `playbooks.All` is in `/api/help` with its phrase, help text and slash command; a test fails if a playbook has no phrase or help text.
- A playbook added to `playbooks.All` in a test appears without other changes.
- The entries come in the stated order, and those best in a new session say so with the reason.
- Without interview_prep, "Log an interview" is listed; with a playbook of that name added in a test, it's hidden and the playbook is listed in its place.
- `open_dashboard` returns `help_url` with the port in use, and `url` is unchanged.
- No `innerHTML` or the like (the existing test still passes), and `TestOutputSizes` passes.

Checked by driving headless Edge or Chrome over the DevTools protocol on a scratch database with made-up data, as CLAUDE.md describes, and reported in the PR:

- The "?" button opens the page, `#help` opens it directly, and Escape or the back button returns to the last tab; reopening the dashboard doesn't land on the help page.
- Each copy button puts its exact text on the clipboard, and on failure selects the text and says so.
- The first-run screen links to the page, and empty states with a phrase have a copy button.
- The page at desktop and phone widths, light and dark.

## Open questions

None.
