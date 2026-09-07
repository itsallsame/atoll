---
name: cvmax
description: Use CvMax to fill and verify one or more job application pages from an authorized resume or user-provided facts, track progress, resolve missing information, and stop before final submission.
---

# CvMax

Use CvMax through its client. Let the hosted CvMax Agent inspect the recruitment page, choose field mappings, operate the paired Chrome extension, verify the page after each action, and accumulate reusable page experience. Do not implement a second browser-filling loop in the host AI. The hosted Agent follows the product instructions packaged at `../../agent/instructions.md`; the Skill does not replace that internal role.

## Prepare the request

Accept any of these user intents:

- fill the active recruitment page reported by the paired Chrome extension;
- fill one supplied job link;
- process several approved job links sequentially.

Parse only files the user authorized. Treat one resume as one complete `ResumeFact`: its source document and structured projection stay together and never merge with another resume. On first use, save it with `resume-save`; on later use, select its exact `resumeId` and revision. Convert its content into projection facts with a stable `id`, exact `value`, and `sourceRef`. Keep uncertain or absent information missing; never infer it. Keep job-specific answers separate and preserve values already entered on the page.

Before creating work, establish the target and confirm that Chrome is paired. For a supplied link, let the CvMax service open and bind the browser tab. For “current page”, resolve the active tab through the service. Never ask the user to open the link, click the extension, or authorize that site. If the opened page requires login, a verification code, or CAPTCHA before automation can continue, leave the task waiting and guide the user through only the current step. Legal consent is reported as a final manual step and does not keep the task waiting.

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
- `submit`: normally delegate `{clientRequestId, links|currentPage, resumeId, resumeRevision, taskAnswers?}`. The task freezes that exact resume revision and cannot borrow facts from another resume. The older `{facts, resumePath?}` shape remains a one-time compatibility path. Keep the same `clientRequestId` across uncertain retries.
- `status`: read the authoritative task using `{taskId}` or `{clientRequestId}`.
- `report`: retrieve the latest user-facing result with `{taskId}`.
- `answer`: answer the current saved question using its exact `taskId`, `questionId`, `questionRevision`, a unique `commandId`, and sourced `facts` or `acknowledged: true`.
- `pause`, `stop`, `skip`: apply the requested control to `{taskId}`. Treat `stop` as terminal.
- `resume`: resume an explicitly paused task using its current `{taskId, expectedRevision}`.
- `continue`: reconnect the hosted Agent to an existing queued, attention, or reconciliation task using `{taskId}`; never create a replacement task.

`submit` first stages the authorized request in the application service and sends the hosted Agent only its `clientRequestId`; this keeps resume facts out of the natural-language delegation turn. A transport receipt only confirms that Atoll accepted a message. Report a task as created only after `status` returns its persisted `taskId`, and report a field as complete only after the service records fresh page verification.

When the observed page exposes a high-confidence native resume parser, the hosted Agent uses it first through the bounded `native_parse` application action. Some sites expose that control only after an ordinary attachment upload, so the Agent uploads once, re-observes, and may trigger parsing from the retained upload without transferring the file again. CvMax then reads the resulting form, compares it with sourced facts, corrects parser-owned errors, and fills omissions. If the parser has no verified effect within its bounded wait, the same task continues with CvMax autonomous filling. A parser is never allowed to overwrite an unsourced existing draft.

The hosted Agent attempts `run_experience` first. For an exact reusable page, that single service call owns the deterministic observe, native parser, recipe execution, verification, and finish sequence, while every action still passes the ordinary fact, lease, budget, existing-value, and no-submit guards. A cold, compatible, drifted, conflicted, or required-missing page returns control to the same Agent and task for adaptive handling. The service reports an application experience state of `cold_start`, `learning`, `reusable`, or `suspended`. Tell the user when CvMax found an exact reusable page version, is reusing stable fields from a compatible changed version, or is analyzing a page for the first time. Do not expose stored anchors or internal strategy identifiers unless diagnosing a correction. A compatible match fills only service-returned suggestions; the hosted Agent analyzes the reported drift instead of remapping the whole page.

## Follow the task

After every command, state the latest verified fact, the current status, who acts next, and the immediate next action. Present at most three short stages and expand only the current stage.

When the service returns a saved question, ask only for the proven-required facts in that question. Optional and unmarked empty fields remain blank and never create a waiting question. After the user answers, save the answer and continue the same task. For multiple links, keep the frozen order and process them sequentially unless the service reports a stop condition.

On timeout or an ambiguous action result, query the same task. Do not repeat a write whose outcome is unknown. Stop automatic progress on login, CAPTCHA, a missing fact proven necessary for a required field, unsupported required controls, identity mismatch, page drift, exhausted action budgets, or an effect requiring reconciliation.

CvMax fills and verifies application drafts. It never clicks final submit, accepts legal declarations for the user, fabricates profile facts, or changes Atoll core behavior.
