---
id: short-kebab-case-id
status: proposed            # proposed | manual | automated
groups: [basic]             # zero or more group names, see AGENTS.md
requires: [kubernetes, storage-server]
automation: none            # or scripts/run-scenarios.sh <id> once automated
---

# One-sentence title stating the behaviour

## Purpose

Why this matters and which design rule or user need it protects.

## Preconditions

What must already be in place: StorageClasses, snapshot classes, operators, images.

## Steps

1. One concrete, checkable action per step, with names, sizes and timeouts.
2. ...

## Expected

- Observable pass criteria. Each one should be clearly true or false.

## Evidence

- What the report must include to prove each expectation.

## Cleanup

- What to delete so the environment is left as it was found.

## Observations

- Optional. Things to record that are not pass criteria.

## Design notes

- Optional. Constraints or a chosen approach the implementation must follow.
