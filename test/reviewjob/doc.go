// Package reviewjob hosts end-to-end acceptance tests for the automatic
// review orchestration CLI. Each test drives the real session-reviewer
// binary against a scripted Codex agent fixture to cover the happy path,
// agent failure with retry, cancellation, and interrupted-worker recovery.
package reviewjob
