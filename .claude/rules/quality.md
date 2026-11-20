---
paths:
  - "**/*.go"
---

# Quality rules — reviewed per rule, pass or fail

- No `if err != nil { return err }` that drops context. Wrap with `fmt.Errorf("<op>: %w", err)`.
- No error that is logged and then also returned.
- No ignored error, except a `Close` on a read-only resource, and then with `//nolint:errcheck` and a reason.
- No defensive nil check against a state that the types or the constructor already exclude.
- No abstraction with a single caller. Inline it until a second caller exists.
- No interface with one implementation, except at a package boundary that a test fakes.
- No configuration for a value nothing varies.
- No dead code and no commented-out code.
- No `panic` in library code for an input error. Return an error.
- No goroutine without a stop path tied to a `context.Context` or a close.
- No global mutable state. Pass dependencies through config structs.
- A function stays under cyclomatic complexity 15 and nesting depth 4 (enforced by `golangci-lint`).
- Every exported identifier has a doc comment that opens with the identifier name and states the result.
- Every FR that a change implements has a test whose name states the behavior.
- A comment states what the code cannot (see the STE rule). Delete comments that restate the code.
