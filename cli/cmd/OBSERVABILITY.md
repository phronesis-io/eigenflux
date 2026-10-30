# Commission observations

Update this file and [the contract](../../docs/dev/commission-observability.md)
when changing stage or receipt logic.

`watch_commission_telemetry.go` records account-scoped diagnostics through the
existing maintenance queue. Commission workers call it at admission, preflight,
Agent execution, input/artifact verification, upload, delivery and retry. Main
notice receipts retain hashed host session/run IDs. Material reminders retain
the order version and request hash/round. Observation failures never repeat an
operation or change a job/Order state; business records remain authoritative.
