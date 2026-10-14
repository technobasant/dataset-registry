# 4. Breaking changes wait for consumer acknowledgement; they are not forbidden

Status: accepted · 2026-10

## Context

The usual registry answer to a breaking change is to reject it. Teams then route around
the registry — a new topic, a `_v2` table, a column nobody removes — and the contract
stops describing what is actually produced. Sometimes a breaking change is the right
change; what goes wrong is that the people it breaks find out from an incident.

## Decision

* A compatible change publishes at once.
* A breaking change creates a pending version and a change request. Every consumer
  registered **at that moment** gets a review row. The request is `open` until each one
  acknowledges, then `approved`; only the owner publishes, and only from `approved`. One
  rejection, which must carry a reason, closes it.
* A change with no registered consumers starts `approved`: nobody declared a dependency,
  so nobody is asked. The audit log records that it was breaking.
* An admin cannot acknowledge for a team. A team that stops depending on the dataset can
  unregister, which drops its pending review and may approve the request.
* One open request per dataset (a partial unique index), and no other contract change
  while it is open, so versions stay linear.
* Every transition is in an append-only audit log; triggers refuse `UPDATE`, `DELETE` and
  `TRUNCATE`.

## Consequences

* A breaking change costs a conversation, not a migration plan written to satisfy a
  linter. The report tells each consumer what changes and in which direction it breaks.
* A consumer that never answers blocks the owner. The owner's options are to talk to
  them, withdraw, or have them unregister. There is no timeout; an expiry with
  escalation is on the roadmap.
* Consumers registered after a request opened are not asked about it. They registered
  against the published version and will see the change when it publishes.
* The audit log's protection stops at the database superuser, who can drop the trigger.
