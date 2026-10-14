# 3. Contracts are JSON Schema, checked on validity sets, FORWARD by default

Status: accepted · 2026-10

## Context

A contract needs a language and a definition of "compatible". Avro's resolution rules
are well understood but apply to Avro readers and writers; most datasets here are Iceberg
tables and JSON topics, and the people writing contracts already write JSON Schema.

Confluent's names for the modes are useful, but their meaning for JSON Schema is easy to
get wrong, so this registry defines them on the sets of documents a schema accepts:

| Mode | Holds when | Protects |
|---|---|---|
| BACKWARD | V(old) ⊆ V(new) | readers of history: everything already written still validates |
| FORWARD | V(new) ⊆ V(old) | consumers: everything written from now on still satisfies the contract they built against |
| FULL | both | both |

## Decision

* Contracts are JSON Schema draft 2020-12 with an object at the root. `format` is
  asserted, not just annotated.
* The checker compares two schemas structurally and reports every change with two facts —
  does it break BACKWARD, does it break FORWARD — and a verdict under the dataset's mode.
  Fields absent from `properties` take the value of `additionalProperties`, so open and
  closed objects fall out of one rule instead of a table of special cases.
* The default mode is **FORWARD**, because the registry exists for consumers.
* Keywords the checker cannot reason about (`anyOf`, `oneOf`, `$ref`, `patternProperties`,
  `multipleOf`, …) and changed regular expressions are reported as breaking in both
  directions. It may call a safe change breaking; it must never call a breaking change
  safe.
* 51 hand-labelled changes are cross-checked by an oracle that validates generated probe
  documents with a real validator, so the labels are not just the checker's opinion.

## Consequences

* Strict semantics surprise people. Adding an optional field to an *open* object breaks
  BACKWARD, because an older document may already carry that name with another type. It
  does not break FORWARD, so the default mode lets it through.
* Removing a field from an open object breaks FORWARD even when it was optional: the new
  contract no longer constrains the name, so consumers can receive anything there. This
  is the change most teams think of as safe and is not.
* The labelled set measures over-reporting: four of 51 cases (regex widening and
  narrowing, an `anyOf` branch, `multipleOf`) are flagged in a direction they do not
  break. MEASUREMENTS.md has the confusion matrix.
* Avro and Protobuf contracts would each need their own checker behind the same report
  shape.
