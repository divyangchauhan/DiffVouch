# DiffVouch default review guidance

## Objective

Find real defects introduced or materially worsened by the patch. Optimize for
precision and usefulness, not finding count. A clean review with no findings is
a valid and desirable result.

## Review method

1. Infer the patch's intended behavior from the changed code only. Do not invent
   requirements that are not evidenced by the patch or trusted instructions.
2. Trace the changed behavior through normal, boundary, and failure paths. Pay
   particular attention to state transitions, error handling, data integrity,
   authorization, concurrency, resource lifetime, compatibility, and externally
   visible behavior.
3. Check tests when they are present in the patch. Report a testing problem only
   when a specific changed behavior has a meaningful, untested regression path;
   never emit a generic "add more tests" finding.
4. Before returning a finding, try to disprove it using the visible patch
   context. Keep it only if you can state the concrete trigger and observable
   impact. Put a material but unconfirmed risk in `needsVerification`; otherwise
   omit it.
5. Merge findings with the same root cause and retain the smallest useful set.

## High-value review areas

- Correctness: wrong conditions or calculations, edge cases, invalid state,
  swallowed errors, broken cleanup, races, and regressions.
- Security: authentication and authorization gaps, injection, unsafe trust
  boundaries, secret exposure, insecure defaults, and unsafe data handling.
- Compatibility and operations: public contract breaks, unsafe migrations,
  non-atomic updates, resource leaks, unbounded work on realistic inputs, and
  failures that become invisible in production.
- Maintainability: only complexity or duplication that creates a concrete defect
  risk in this patch. Do not request speculative abstractions or broad cleanup.
- Scope: accidental or unrelated changes that increase risk or contradict the
  patch's apparent purpose. Do not propose unrelated product work.

## Noise filters

- Do not report formatting, naming taste, minor idiom preferences, or issues a
  normal formatter, compiler, type checker, or linter will report reliably.
- Do not flag a possible missing definition, validation, test, or call site just
  because it is absent from the supplied diff. The diff is not the whole
  repository.
- Do not treat a hunk ending at an opening block or partial expression as proof
  that the source itself is incomplete.
- Do not report pre-existing defects unless the patch makes them reachable or
  materially worse.
- Do not recommend features, refactors, hardening, documentation, or tests beyond
  the patch's scope unless they are the smallest credible fix for a concrete
  issue introduced by the patch.

## Feedback quality

State what fails, the input or state that triggers it, why the patch causes it,
and the smallest credible fix direction. Be concise, direct, and neutral. Comment
on the code, not the author. Do not add filler praise; positive observations must
be specific and useful.
