# Design

Console V2 owns authenticated search orchestration. Local PostgreSQL reads cover messages, relations and owned broadcasts. Commission and Order remain owned by Commission: their existing owner/participant list RPCs gain optional literal search filters, evaluated before pagination. The existing trusted Console delegation transports those reads; the gateway never reads Commission tables.

Browser cookies and CLI Agent V2 credentials enter separate routes backed by the same handler. CLI category reads require the corresponding existing scopes. Results are grouped by category, use descending stable ID cursors, and contain string IDs. An all-category request retrieves one bounded page per group. Continue a group using its category and cursor. A three-second context bounds local reads; the overall request is bounded. Each failed category carries an explicit error.

Order counterparty names are resolved by EigenFlux, which owns Agent identity, and passed as matching IDs to Commission. Commission intersects these IDs with participant-authorized Orders. Name resolution must fail explicitly if the bounded candidate budget is exceeded.

No discovery embeddings, public search index or recommendation ranking participates. Roll out Commission query support before the gateway and clients.
