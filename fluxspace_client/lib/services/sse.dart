/// Server-sent event framing, split out from the transport so it can be tested
/// without a socket.
///
/// The Hub writes `id: <n>\ndata: <json>\n\n` and nothing else (see
/// `hub/api/sessions.go`). This parser implements that subset plus the two
/// rules the SSE specification requires a client to honour: ignore comment
/// lines, and dispatch on a blank line.
library;

/// One dispatched event.
class SseRecord {
  const SseRecord({this.id, required this.data});

  /// The `id:` field, if present. It is the Hub's monotonic event id and is
  /// what `Last-Event-ID` reconnects with.
  final String? id;

  /// Concatenated `data:` lines, joined with newlines as the spec requires.
  final String data;

  @override
  String toString() => 'SseRecord($id, ${data.length} chars)';
}

/// Incremental parser: feed it whatever the socket delivered, get back the
/// events that are now complete.
///
/// Chunk boundaries fall wherever TCP puts them, so an event can be split
/// across reads and several events can arrive in one. A parser that assumed
/// one read equals one event would drop data on a slow network — exactly the
/// case where a lost `match_end` matters most.
class SseParser {
  final StringBuffer _buffer = StringBuffer();

  /// Feeds [chunk] and returns every event completed by it, in order.
  List<SseRecord> add(String chunk) {
    _buffer.write(chunk);
    final String pending = _buffer.toString();
    final List<SseRecord> out = <SseRecord>[];

    // Normalise CRLF here rather than in the split, so the block scanner below
    // only has to deal with one separator.
    final List<String> blocks = pending.replaceAll('\r\n', '\n').split('\n\n');
    if (blocks.length == 1) return out;

    // The last block is either empty or a partial event; keep it.
    final String tail = blocks.removeLast();
    _buffer
      ..clear()
      ..write(tail);

    for (final String block in blocks) {
      final SseRecord? record = _parseBlock(block);
      if (record != null) out.add(record);
    }
    return out;
  }

  /// Flushes a partial trailing event. Called when the stream closes: a final
  /// event without its blank line is still an event the Hub sent.
  List<SseRecord> flush() {
    final String pending = _buffer.toString();
    _buffer.clear();
    if (pending.trim().isEmpty) return const <SseRecord>[];
    final SseRecord? record = _parseBlock(pending);
    return record == null ? const <SseRecord>[] : <SseRecord>[record];
  }

  SseRecord? _parseBlock(String block) {
    String? id;
    final List<String> data = <String>[];
    for (final String rawLine in block.split('\n')) {
      final String line = rawLine.trimRight();
      if (line.isEmpty || line.startsWith(':')) continue;
      final int colon = line.indexOf(':');
      final String field = colon < 0 ? line : line.substring(0, colon);
      // A single leading space after the colon is part of the framing, not data.
      String value = colon < 0 ? '' : line.substring(colon + 1);
      if (value.startsWith(' ')) value = value.substring(1);
      switch (field) {
        case 'id':
          id = value;
        case 'data':
          data.add(value);
        default:
          // `event:` and `retry:` are unused by the Hub; ignoring unknown fields
          // is what the specification requires of a client.
          break;
      }
    }
    if (data.isEmpty) return null;
    return SseRecord(id: id, data: data.join('\n'));
  }
}
