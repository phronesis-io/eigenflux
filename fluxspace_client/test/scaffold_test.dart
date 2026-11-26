import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:fluxspace_client/routes.dart';
import 'package:fluxspace_client/services/database.dart';
import 'package:fluxspace_client/services/hub_client.dart';

void main() {
  test('four navigation destinations, each with a distinct page', () {
    expect(AppRoutes.order.length, 4);
    expect(AppRoutes.order.toSet().length, 4);
    expect(AppRoutes.labels.length, 4);

    final Set<Type> pageTypes = <Type>{};
    for (final String route in AppRoutes.order) {
      expect(AppRoutes.labels[route], isNotNull);
      final Widget page = AppRoutes.pageFor(route);
      expect(page, isA<Widget>());
      pageTypes.add(page.runtimeType);
    }
    expect(pageTypes.length, 4);
  });

  test('all six local tables have DDL', () {
    expect(LocalDatabaseSchema.tableNames.length, 6);
    expect(LocalDatabaseSchema.isComplete(), isTrue);
    expect(LocalDatabaseSchema.createStatements['memories'], contains('source_ref_id'));
    expect(LocalDatabaseSchema.createStatements['relationships'], contains('resentment'));
  });

  test('hub port does not collide with EigenFlux services', () {
    expect(HubClient.defaultBaseUrl, contains('8095'));
    for (final int port in <int>[8080, 8088, 8090, 8092]) {
      expect(HubClient.defaultBaseUrl.contains(':$port'), isFalse);
    }
  });
}
