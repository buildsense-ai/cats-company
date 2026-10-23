# Conversation task status: run storage and delivery

The SQL adapters preserve each publisher run under `(topic_id, source_uid,
run_id)`. The authenticated publisher supplies `source_uid`; a request cannot
choose another source. An empty legacy `run_id` still represents one source slot
and does not establish message-to-run correlation.

A status write locks the existing topic aggregate row, validates the transition
against that run, updates the run and source/topic projections, and inserts an
outbox event in one transaction. Failure to insert the event rolls back all
status changes. An older run's terminal receipt can be accepted while another run
is active. Late progress cannot resurrect a terminal run. Repeated identical
terminal receipts without publisher timestamps are idempotent.

## Backwards-compatible WebSocket events

Existing publishers keep sending `task_status` without a protocol upgrade.
Existing web and SDK consumers keep receiving the topic aggregate in
`task_status`. A new additive field carries the committed source receipt:

```json
{
  "task_status": {
    "topic_id": "grp_80", "source_uid": 42, "run_id": "a",
    "state": "running", "updated_at": "2026-10-23T03:00:01Z"
  },
  "task_status_source": {
    "topic_id": "grp_80", "source_uid": 43, "run_id": "b",
    "state": "completed", "updated_at": "2026-10-23T03:00:00Z"
  },
  "task_status_event_id": 123
}
```

Here B completed while A remains active. A consumer displaying a particular run
can use `task_status_source`; a conversation overview uses `task_status`.
Existing consumers ignore the additive fields. No frontend or XiaoBa changes are
included in this server change.

The aggregate is recomputed from unexpired run records, with running/waiting runs
first and publisher event time as the next ordering key. Source projections use
the same active-first rule. The aggregate has its own server revision timestamp,
so switching back to an earlier-started active run does not look like an older
update to legacy clients. PostgreSQL's aggregate update trigger uses wall-clock
time after the topic lock, rather than transaction-start time. Identical terminal
retries return the persisted aggregate revision without advancing its timestamp
or adding another outbox event.

The internal `ledger_updated_at` stamp records which aggregate revision was
produced by the ledger. It is written atomically with `updated_at`; older writers
leave the stamp unchanged. Reconciliation imports only unmarked or mismatched
revisions. This distinguishes a genuine legacy write from a ledger aggregate
falling back to an earlier run after another run expires, and prevents the
aggregate's server clock from replacing a run's publisher event time.

## Delivery guarantees

A dispatcher starts with the Hub and wakes after status writes, with a periodic
poll as a fallback. It claims bounded batches with `FOR UPDATE SKIP LOCKED` and a
30-second lease. A full batch is followed immediately by another batch. Multiple
nodes can dispatch, and a worker that crashes leaves its events available for
reclaim after lease expiry. Completion checks the opaque claim token so a stale
worker cannot delete another worker's claim.

Each attempt reloads the current aggregate. Replaying an old receipt therefore
does not replay its old aggregate. Group membership and channel-managed group
visibility are resolved before fanout; lookup or Redis publication failures leave
the event pending. Shared runtimes fan out across nodes. Offline users do not
block completion.

Delivery is **at least once to the fanout transport**, with possible duplicates.
It is not a browser acknowledgement or a durable per-user inbox. A WebSocket
connection can still disappear after a frame is queued, and Redis Pub/Sub can
lose a frame after publication. Clients must use run history to recover missed
state and must order/deduplicate source events. Outbox completion removes only
the notification; run records remain available. Superseded event payloads are
not retained as a permanent event audit log.

## Run history

An authenticated WebSocket client can request at most 100 run records:

```json
{"get":{"id":"task-history-1","topic":"grp_80","what":"task_statuses","before_id":0}}
```

The response is a `ctrl` with code 200 and params:

```json
{
  "task_statuses": [
    {"id":456,"topic_id":"grp_80","source_uid":43,"run_id":"b","state":"completed","updated_at":"2026-10-23T03:00:00Z"}
  ],
  "next_before_id": 0
}
```

Records are ordered by descending creation ID. Supply `next_before_id` to fetch
the next page; zero indicates completion. A full last page may be followed by one
empty page. This is a live view of each run's latest state, not a historical
snapshot of all transitions. Topic read authorization matches message history;
nonmembers and humans in channel-managed groups cannot fetch it.

## Migration and recovery

PostgreSQL migration `000052_conversation_task_runs` and startup schema creation
add the run/outbox tables, the internal aggregate provenance stamp, and backfill
the source records currently available.
The migration also adds the publisher event clock when upgrading directly from
older numbered migrations. Re-running schema creation does not replace existing
runs. Rollback drops only the new run/outbox data and restores the previous topic
clock trigger; the legacy projections remain.

MySQL startup schema creation adds the equivalent tables, with the same topic
collation as the existing schema and binary run IDs. Both adapters share the run
transition, aggregation, outbox and recovery implementation.

During rolling upgrades, source projections written by older binaries are
imported on status writes and summary reads, subject to event ordering and
terminal-state protection. The transactional outbox guarantee applies to writes
through the upgraded adapters; an older binary cannot retroactively make its
own writes atomic with the new outbox.

Disconnected-bot recovery keeps the connection-generation fence and timestamp
compare-and-set, and now checks each run. Recovery writes its own outbox event.
It reports that execution results need confirmation; it does not cancel an
external runtime. The current frontend's hard-coded stale label still requires
a separate frontend change.

## Limits and diagnostics

This change cannot recover runs already overwritten before migration, terminal
receipts never received by the server, or authoritative associations between
messages and runs when the publisher omits them. Starting a new run does not prove
an older run stopped: an older active run can remain in the aggregate until its
receipt or expiry. No run-retention deletion is introduced.

Status ingestion logs topic, authenticated source, run, state, publisher event
time and rejection reason. Delivery logs event IDs and failure stages without
logging message summaries or task error bodies. Operators can inspect pending
notifications and run records independently:

```sql
SELECT id, topic_id, source_uid, available_at
FROM conversation_task_status_outbox ORDER BY id LIMIT 100;

SELECT id, topic_id, source_uid, run_id, state, event_updated_at, updated_at, expires_at
FROM conversation_task_runs WHERE topic_id = 'grp_80' ORDER BY id DESC LIMIT 100;
```

CI runs the shared contract suite against PostgreSQL and MySQL, including
transaction rollback, overlapping runs, expiry, concurrent claims and lease
recovery. Server tests cover visibility, source/aggregate separation, replay,
cross-node fanout and a real PostgreSQL publisher-to-fanout restart scenario.
