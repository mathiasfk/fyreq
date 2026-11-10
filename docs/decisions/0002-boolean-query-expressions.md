# Boolean query expressions

The WHERE clause uses a tree of conditions rather than a flat predicate list.
AND binds more tightly than OR. Both ordinary and grouped queries use the same
condition evaluator so filtering cannot drift between execution paths.

Evaluation preserves SQL's true, false, and unknown states. Comparisons with
unavailable metadata produce unknown, and WHERE retains only true results. This
preserves existing comparison behavior and allows negation without treating
unknown ownership as a known non-match. Boolean evaluation short-circuits when
the result is already determined.

This change needs no storage format, OS API, or dependency changes.
