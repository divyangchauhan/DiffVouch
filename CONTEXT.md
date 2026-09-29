# DiffVouch

DiffVouch reviews proposed code changes and reports findings. This glossary defines terms used when discussing its review quality.

## Language

**Native reviewer**:
The DiffVouch reviewer that controls its own review process, without delegating the review to Codex, Claude Code, or the standalone DiffVouch skill.
_Avoid_: Codex review, skill review when referring to the native reviewer.

**Review regression**:
A decline in DiffVouch's review quality following a change to its code or review prompt.
_Avoid_: Test failure when referring specifically to a decline in review quality.

**Published comparison**:
A comparison between DiffVouch's evaluation results and results previously published for another reviewer, with the source and evaluation conditions retained.
_Avoid_: Head-to-head run when the competing reviewer has not been rerun.

**Maintainability finding**:
A review finding that identifies a concrete cost in understanding or changing code and recommends a change with a concrete benefit. Pure formatting preferences and matters of taste do not qualify.
_Avoid_: Clean-code issue when no concrete maintenance cost is identified.

**Rescored comparison**:
A comparison that grades DiffVouch reviews and archived competitor reviews using the same evaluation procedure. Competitor reviews still represent the historical product versions that generated them.
_Avoid_: Reproduced published score when the original grading procedure has changed.
