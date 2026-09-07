# 公民号外设计 QA

## Evidence

- Source visual truth: `/Users/ahaha/.codex/generated_images/01a075b1-16ed-7780-9c7a-66e0580eaa83/exec-911f77d3-1b7b-4c62-84e3-92b0c84f3333.png`
- Source dimensions: 1487 × 1058 px.
- Implementation: `http://127.0.0.1:18932/society/`
- Browser-rendered implementation capture: Codex in-app Browser tab 7, 1440 × 1024 viewport, devicePixelRatio 1, captured 2026-09-07 in the connected/paused/ballot-submitted state.
- Combined comparison surface: `/tmp/society-qa.oYYsHn/society-design-qa.html`; it displayed the source and live implementation together at equal column width for the final comparison.
- State: local experiment connected, day 2, paused, 100 residents, ballot allocation 4/3/3 and sealed.
- Console: zero errors in a fresh browser tab after final reload.

## Full-view comparison evidence

The final side-by-side comparison preserves the selected design's defining composition: oversized masthead, red special-edition banner, three-column A/ballot/B structure, halftone future imagery, central ten-token ballot, resident testimony, live opinion band, countdown, audit promise, and a concealed operator desk. The implemented page keeps the main experience within the first 1024 px; only the lower operator desk continues below the fold.

## Focused region comparison evidence

- Masthead: hierarchy and proportions match; implementation uses a cleaner screen-print treatment to retain Chinese readability.
- Central ballot: all ten tokens, three allocation destinations, remaining-vote state, and stamped submit state are present and interactive.
- Future columns: custom halftone city, community, and resident assets match the source's subject, crop, and ink treatment.
- Lower strip: countdown, live metrics, newswire, and traceability promise remain visible in the desktop viewport.

## Comparison history

### Iteration 1

- P2: the main headline wrapped into three lines and weakened the source's two-line impact.
- P2: the first content row was too tall, pushing the audit promise below the 1024 px viewport.
- Fixes: reduced the headline scale, shortened future-image aspect ratios, tightened outcome rows, resident portrait, policy controls, token size, and submit control.
- Post-fix evidence: the 1440 × 1024 browser capture shows a two-line headline and the complete audit promise above the fold.

### Iteration 2

- P2: a submitted ballot reset visually after reload.
- Fix: restored the 4/3/3 allocation and sealed state from local storage.
- Post-fix evidence: desktop and 390 px mobile reloads both show `本期选票已封存`; mobile document width does not overflow its viewport.

## Required fidelity surfaces

- Fonts and typography: passed. Song-style Chinese display text and a restrained sans-serif UI companion reproduce the editorial hierarchy without illegible distressed body copy.
- Spacing and layout rhythm: passed. Major region proportions, rules, column alignment, and first-fold density match the reference; no horizontal overflow at 390 px.
- Colors and visual tokens: passed. Warm newsprint, black ink, vermilion, blue, and ochre are consistently mapped to the source.
- Image quality and asset fidelity: passed. All visible editorial images are purpose-generated raster assets; no placeholder or code-drawn illustration replaces source imagery.
- Copy and content: passed. The public dilemma, three choices, two futures, affected resident, countdown, public opinion, and audit promise match the selected concept. Live experimental values intentionally replace the mock's fixed values.

## Primary interactions tested

- Selected each of the three policy destinations.
- Distributed all ten tokens 4/3/3 and verified submit enablement.
- Submitted the ballot and verified sealed state plus the updated participation count.
- Reloaded at desktop and mobile widths and verified ballot persistence.
- Opened the experiment evidence room.
- Opened a real resident record from the featured-resident action.
- Confirmed the local Atoll node connection and zero fresh browser console errors.

## Follow-up polish

- P3: a future iteration could add a purpose-generated seamless paper-fiber texture; the current flat stock color is intentionally cleaner than the heavily distressed mock for screen readability.
- Public aggregation is now implemented at `/api/society`; no account or session is required.

final result: passed

## 2026-09-07 civic-loop follow-up

- Removed browser auto-registration, private-world discovery/creation, WebSocket actor control, comparison controls, intervention dialog, and the public operator desk.
- Added one node-persisted public world with anonymous `HttpOnly` voter identifiers and live aggregate results.
- Verified two independent anonymous clients share one tally: ballots 4/3/3 and 1/2/7 produced 2 participants and 25%/25%/50%.
- Verified the public page works while node registration is disabled.
- Verified the browser console contained zero errors or warnings after the full loop.
