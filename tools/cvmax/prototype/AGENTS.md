# Prototype Instructions

Run the local server yourself and open the preview in the browser available to this environment. Do not give the user server-start instructions when you can run it.

Before making substantial visual changes, use the Product Design plugin's `get-context` skill when the visual source is unclear or no longer matches the current goal. When the user gives durable prototype-specific design feedback, preferences, or decisions, record them in `AGENTS.md`.

When implementing from a selected generated mock, treat that image as the source of truth for layout, component anatomy, density, spacing, color, typography, visible content, and hierarchy.

Build app UI in `src/`. Keep `.openai/hosting.json`, `worker/index.js`, `scripts/prepare-sites-build.mjs`, and `tests/sites-worker.test.mjs` intact so the same local prototype can be handed to Sites. Before a Sites handoff, run `npm run build` and `npm run test:sites`; the build must leave `dist/client/index.html`, `dist/server/index.js`, and `dist/.openai/hosting.json`.

## User-approved direction

Use selected mock 1: recruitment application on the left, CvMax assistant on the right. Keep every change inside tools/cvmax/. Do not modify Atoll core code, design or execution flow. State who acts next, show only the current detailed step, and distinguish pause, stop, partial completion and final user submission.

## Current product scope supersedes this historical visual direction

The existing mock is retained as historical interaction evidence. For new product work, follow ../AGENTS.md and ../ARCHITECTURE-FIT.md: Codex + Skill + CLI is the primary entry; do not expand this sidebar into a profile or recommendation product. The image-fidelity instructions apply only when explicitly editing this old prototype.
