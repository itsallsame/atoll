---
name: cvmax
description: Use CvMax to fill and verify one or more job application pages from an authorized resume or user-provided facts, track progress, resolve missing information, and stop before final submission.
---

# CvMax

Use CvMax through its client. The hosted CvMax Agent owns observation, interpretation, bounded learning and verification. The Service persists that evidence and compiles verified knowledge into reusable Recipes. Known workflows can execute directly; missing knowledge returns to the same hosted Agent. The external host prepares the authorized resume, delegates, and reports state; it does not run a second browser-filling loop. Internal instructions are packaged at `../../agent/instructions.md`.

Normal `submit` requests use `learningMode: "user"` by default. For an explicitly requested prelearning test using authorized sites and test resumes, use `learningMode: "prelearn"` on that same submit command. Prelearning expands the exploration budget, not permissions. It does not allow final submission or bypass user gates. Do not silently turn an ordinary user's application into a prelearning experiment.

If submission returns a persisted task ID with `path: "paused"` or `"needs_reconcile"`, report that task's failure and latest state. Do not submit the resume again or create another task to retry.

## Prepare the request

Accept any of these user intents:

- fill the active recruitment page reported by the paired Chromium extension (Microsoft Edge is the primary acceptance browser; Chrome is compatible);
- fill one supplied job link;
- process several approved job links sequentially.

Parse only files the user authorized. Treat one resume as one complete `ResumeFact`: its source document and structured projection stay together and never merge with another resume. On first use, save it with `resume-save`; on later use, select its exact `resumeId` and revision. Convert its content into projection facts with a stable `id`, exact `value`, and `sourceRef`. Keep uncertain or absent information missing; never infer it. Keep job-specific answers separate and preserve unrelated values already entered on the page. The selected ResumeFact is authoritative for mapped resume fields. A recruitment login email may differ from the resume contact email and must never cause an identity check or pause.

Before creating work, establish the target and confirm that the Edge/Chrome extension is paired. A supplied link is task-scoped authorization for that Application; the CvMax service opens it only when that Application begins. For “current page”, resolve the active tab through the service. Never ask the user to open the link, click the extension, authorize that site, or update the plugin for a new recruitment domain. If the opened page requires login, a verification code, CAPTCHA, or a uniquely observed legal-consent dialog that blocks entry to the resume workflow, leave the task waiting and guide the user through only the current step. Legal consent shown at final submission is reported as a final manual step and does not keep the task waiting. Sites without an observed consent gate skip this branch entirely.

## Use the client

Prefer the installed `cvmax` command. In this repository, the equivalent development entry is:

```text
node <repo>/tools/cvmax/src/cli.mjs <command> --config <private-client-config>
```

Send one JSON object on stdin. The private client configuration contains the user's Atoll identity and channel; never print, copy, or expose its cookie.

Available commands:

- `install`: idempotently install or upgrade the CvMax hosted Agent and stdio MCP service through Atoll's existing declarations and channel membership. It requires the private config's `serviceConfig` path.
- `resume-save`: create or revise one resume using `{saveRequestId, name, projectionFacts, resumePath, resumeId?, expectedRevision?}`. A revision without a new file may omit `resumePath`; its projection still belongs only to that resume. Keep the same `saveRequestId` across uncertain retries.
- `resume-list` / `resume-get`: return saved resume summaries without projection values or source paths.
- `submit`: normally delegate `{clientRequestId, links|currentPage, resume: {id, revision}, taskAnswers?}`. The Client validates this reference and its resolved fact/document summary before opening a recruitment page. The task freezes that exact resume revision and cannot borrow facts from another resume. `{facts, resumePath?}` is the first-use path before a resume has been saved. Keep the same `clientRequestId` across uncertain retries.
- `status`: read the authoritative task using `{taskId}` or `{clientRequestId}`.
- `report`: retrieve the latest user-facing result with `{taskId}`.
- `answer`: answer the current saved question using its exact `taskId`, `questionId`, `questionRevision`, a unique `commandId`, and sourced `facts` or `acknowledged: true`.
- `pause`, `stop`, `skip`: apply the requested control to `{taskId}`. Treat `stop` as terminal.
- `resume`: resume an explicitly paused task using its current `{taskId, expectedRevision}`.
- `continue`: reconnect the hosted Agent to an existing queued, attention, or reconciliation task using `{taskId}`; never create a replacement task.

`submit` first stages the authorized request in the application service and sends the hosted Agent only its `clientRequestId`; this keeps resume facts out of the natural-language delegation turn. A transport receipt only confirms that Atoll accepted a message. Report a task as created only after `status` returns its persisted `taskId`, and report a field as complete only after the service records fresh page verification.

Recipe is the sole page-operation state machine, but it never owns business completeness. At task creation the Service freezes every selected resume fact, task answer and authorized attachment into a non-shrinkable ApplicationPlan with an evidence ledger. The Service persists its active Recipe transition cursor and keeps ordinary field writes closed while it discovers the site's native path, uploads once, handles a verified replacement/upload-success transition, and observes parsing. Key facts prove that native parsing occurred; the task continues through project, campus and other relevant sections until every plan obligation is verified or has an evidence-backed site limitation. Existing site resume content may be replaced by the user-selected ResumeFact; unrelated unsourced values remain untouched.

The hosted Agent attempts `run_recipe` first. A unique exact Recipe state runs programmatically through the Service and ordinary action guards without another model turn. A missing, ambiguous, or unbindable transition hands the same task to the hosted Agent, which analyzes only that state. Every newly verified action becomes a value-free Recipe transition immediately; task completion marks the terminal state. There is no similarity score and no separate entry/form/recovery/interaction experience. Public signed Recipe releases hot-load through the Service, so a new company does not require a plugin update.

## Follow the task

After every command, state the latest verified fact, the current status, who acts next, and the immediate next action. Present at most three short stages and expand only the current stage.

When the service returns a saved question, ask only for the proven-required facts in that question. Optional and unmarked empty fields remain blank and never create a waiting question. After the user answers, save the answer and continue the same task. For multiple links, keep the frozen order and process them sequentially unless the service reports a stop condition.

On timeout or an ambiguous action result, query the same task. Do not repeat a write whose outcome is unknown. Stop automatic progress on login, CAPTCHA, a missing fact proven necessary for a required field, unsupported required controls, identity mismatch, page drift, exhausted action budgets, or an effect requiring reconciliation.

CvMax fills and verifies application drafts. It never clicks final submit, accepts legal declarations for the user, fabricates profile facts, or changes Atoll core behavior.
