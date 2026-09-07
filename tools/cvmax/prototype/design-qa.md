# CvMax prototype design QA

> 范围更新：下方 passed 仅针对当时的侧栏本地原型，不能视为当前 Codex/Skill/CLI 架构或产品验收通过。当前设计见 [统一入口](../README.md)。

final result: passed

Date: 2026-09-05. Scope: selected image 1 and the local interactive prototype, not production browser automation.

## Visual source and evidence

The earlier branded reference image was retired when the product was renamed to CvMax. The current AI-first implementation in `src/` is the review source.

Compared the reference and `evidence/reference-size.png` together in the same image-viewing tool response, plus the default desktop viewport in `evidence/desktop-viewport.png`. Browser viewport was temporarily set to reference dimensions for comparison, then reset. Desktop structure preserves the application/sidebar split, white surface, fine separators, teal primary action, field anatomy, vertical radio choices, and compact experience notes. No marketing page or unrelated dashboard was added.

Intentional changes for this user-authorized interaction prototype: a separate demo toolbar; status owner and timestamp; an explicit stop action; school and attachment controls required for the takeover test; collapsed history; local-simulation labels. The static orange connector is replaced by field location/focus during handoff. Native browser chrome is not recreated. Education and attachment content adds scroll height. The generated company logo is used as a bitmap; interface icons use Phosphor.

Responsive check: 390 × 844 viewport; document scroll width 375 px, no horizontal overflow; form controls, primary action and validation message remain reachable. `evidence/mobile-fill.png`. Focus is visible and the school handoff actually focuses the school select; status updates use a polite live region. This is not a formal screen-reader audit.

## Observed browser outcomes

| Scenario | Actual outcome |
| --- | --- |
| Empty salary / complete answers | Error stays actionable; complete answers transition through verification to user review |
| Missing connection / retry / enable | Retry explains only the current missing step; enabling triggers recheck and resumes to missing answers |
| Wrong account | Remains blocked, no application progression |
| Normal login | Automatically verifies and resumes to missing answers |
| Pause then login | Remains paused until explicit resume; resume verifies before continuing |
| Stop during pending upload | Immediately blocks subsequent actions; bounded read check ends as unknown |
| Reload stopped run | Stopped state and unknown attachment remain |
| Wrong school / manual correction | Wrong selection rejected; corrected selection verified read-only |
| Unknown attachment after school fix | Reports partial completion; does not claim complete |
| Display simulated receipt | Reports verified result while preserving stop; does not repeat upload |
| Review checklist | Opens without a submit action |

Browser console error log was empty. State tests: 10 passed. Production build passed.

## Corrected findings

- Changed radio group back to the reference's vertical arrangement.
- Blocked stale callbacks after stop/reset and prevented connection retry from bypassing a stopped state.
- Preserved explicit pause across login completion and reload.
- Corrected stopped copy to distinguish confirmed versus unknown attachments.
- Invalidated pending verification when the user edits values.
- Fixed currency suffix wrapping at the wide desktop breakpoint.

No remaining actionable P0/P1/P2 findings within the defined prototype scope. Cross-site compatibility, server-side validation, real uploads/authentication, experience learning and production security remain untested because they are not implemented in this local demo. This result does not certify the production product.
