# CvMax browser extension

CvMax ships one Manifest V3 Chromium extension. Microsoft Edge is the primary
development and formal-acceptance browser; Google Chrome remains a supported
compatibility target. Do not fork the extension or maintain an Edge-specific
copy: Edge supports the `chrome.*` extension namespace used by this package.

Version 0.2.21 and later reports `edge`, `chrome`, or `chromium` during the
local handshake. When more than one browser has CvMax installed, the Service
uses its private `browser.preferredFamily` setting and never lets the most
recent connection silently take over. Before a formal run, `cvmax doctor`
must report `browserSelection.selectedFamily: edge`.

## Load in Microsoft Edge

1. Open `edge://extensions`.
2. Enable **Developer mode**.
3. Choose **Load unpacked**.
4. Select this `tools/cvmax/extension/` directory.
5. Open the CvMax extension popup and pair it with the local CvMax service.
6. Run `cvmax doctor` and require `browser: connected` before starting a case.

When files in this directory or `manifest.json` change, return to
`edge://extensions` and reload CvMax before testing. Service, Agent, Client, or
Recipe-only changes do not require an extension reload.

## Chrome compatibility

The same directory can be loaded from `chrome://extensions` with Developer mode
enabled. Chrome compatibility is retained, but formal browser evidence should
record the exact browser name/version and, by default, be collected in Edge.

## Acceptance rule

A browser case is not accepted from unit tests alone. Record the Edge version,
extension version, task ID, resulting page evidence, action count, and proof
that final submission remained disabled. Failures must be retained and rerun as
the same case after a fix before advancing to the next formal case.
