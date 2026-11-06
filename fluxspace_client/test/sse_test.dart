import 'package:flutter_test/flutter_test.dart';
import 'package:fluxspace_client/services/sse.dart';

/// The Hub writes `id: <n>\ndata: <json>\n\n` and nothing else. These tests pin
/// the two cases that break naive clients: an event split across reads, and
/// several events arriving in one read.
void main() {
  test('one complete event is dispatched', () {
    final SseParser parser = SseParser();
    final List<SseRecord> out = parser.add('id: 1\ndata: {"type":"match_start"}\n\n');

    expect(out, hasLength(1));
    expect(out.single.id, '1');
    expect(out.single.data, '{"type":"match_start"}');
  });

  test('an event split across reads is held until its blank line arrives', () {
    final SseParser parser = SseParser();

    expect(parser.add('id: 4\ndata: {"type":"round'), isEmpty);
    expect(parser.add('_begin"}\n'), isEmpty);
    final List<SseRecord> out = parser.add('\n');

    expect(out, hasLength(1));
    expect(out.single.id, '4');
    expect(out.single.data, '{"type":"round_begin"}');
  });

  test('two events in one read both come out, in order', () {
    final SseParser parser = SseParser();
    final List<SseRecord> out = parser.add(
      'id: 1\ndata: {"a":1}\n\nid: 2\ndata: {"a":2}\n\n',
    );

    expect(out.map((SseRecord r) => r.id), <String>['1', '2']);
  });

  test('CRLF framing is accepted', () {
    final SseParser parser = SseParser();
    final List<SseRecord> out = parser.add('id: 7\r\ndata: {"a":7}\r\n\r\n');

    expect(out, hasLength(1));
    expect(out.single.id, '7');
    expect(out.single.data, '{"a":7}');
  });

  test('comment lines are ignored, as the SSE specification requires', () {
    final SseParser parser = SseParser();
    final List<SseRecord> out = parser.add(': keep-alive\nid: 9\ndata: {"a":9}\n\n');

    expect(out, hasLength(1));
    expect(out.single.id, '9');
  });

  test('a final event without its blank line still flushes', () {
    final SseParser parser = SseParser();
    expect(parser.add('id: 3\ndata: {"a":3}'), isEmpty);

    final List<SseRecord> out = parser.flush();

    expect(out, hasLength(1));
    expect(out.single.data, '{"a":3}');
    // Flushing twice must not replay the event.
    expect(parser.flush(), isEmpty);
  });

  test('multi-line data is joined with newlines', () {
    final SseParser parser = SseParser();
    final List<SseRecord> out = parser.add('data: {"a":\ndata: 1}\n\n');

    expect(out.single.data, '{"a":\n1}');
  });

  test('a block with no data field produces no event', () {
    final SseParser parser = SseParser();
    expect(parser.add('id: 5\n\n'), isEmpty);
  });
}
