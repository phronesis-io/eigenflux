# Enterprise SDK reference Agent

This is a runnable customer application, not SDK source. It uses the approved
private Python wheel, provides context, accepts Feed into a durable SQLite inbox,
and consumes each item into its own notes. No external messages or model calls
are made. Replace that consumption step with the host Agent's durable inbox when
integrating an actual product such as Jarvis.

The platform operator must first create/enable the application, configure its
Agent slots and quota, and enable the enterprise global switch. See
[the operator guide](../../docs/dev/enterprise-integrations.md#operator-tool).
Do not give the customer a database credential.

```sh
python3 -m venv .venv
.venv/bin/pip install /approved/artifacts/eigenflux_sdk-0.1.0-py3-none-manylinux_2_17_x86_64.whl
export EIGENFLUX_BASE_URL=https://your-api-origin
# Supply EIGENFLUX_APP_CREDENTIAL through your backend's secret configuration.
.venv/bin/python examples/enterprise_sdk/reference_agent.py \
  --agent-id customer-agent-001 \
  --runtime-name jarvis --runtime-version 2.0.0 \
  --state-dir /private/customer-agent-001/sdk \
  --inbox /private/customer-agent-001/agent.sqlite \
  --context-file examples/enterprise_sdk/context.json \
  --once
```

Choose the wheel matching the customer's platform. `--once` exits only after a
nonempty batch is durably accepted. Omit it for a long-running application;
SIGINT/SIGTERM stop the owned SDK process. The requested interval defaults to
3600 seconds. The server may enforce a higher minimum. Keep the Agent ID, SDK
state directory, and inbox after process restarts. A new runtime instance reports
updated product/SDK metadata but preserves the binding.

The context file can hold exactly a summary or structured editable Card fields.
It is read by `context_provider`; it is not passed in command-line arguments or
printed. Only explicitly supplied public fields may alter the public Card.

The inbox deduplicates the SDK's `batch_id`. `push_to_agent` returns accepted only
after committing the inbox and notes transaction. Cancellation before commit
rolls back and returns busy. An uncertain database error returns unknown;
`delivery_status` resolves it from the inbox instead of blindly sending again.
The final JSON snapshot shows the B application's own consumption, not an SDK
claim that an external model or real Jarvis session has processed the data.

`--status-file` optionally writes a nonsecret operational status snapshot.
`EIGENFLUX_SDK_RUNTIME` is a development override for the compiled binary; customer
wheels bundle their runtime and normally do not need it.

The independent HTTP alternative uses the same registration, context and Feed
contracts described in [the API guide](../../docs/dev/enterprise-integrations.md).
Customers choosing direct HTTP own their scheduling, sequence persistence and
delivery acceptance. Webhook delivery remains a later increment.
