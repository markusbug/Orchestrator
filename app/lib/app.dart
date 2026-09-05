import 'package:flutter/material.dart';

import 'net/app_model.dart';
import 'services/settings.dart';
import 'ui/hosts_screen.dart';

/// Makes the [AppModel] available to the widget tree and rebuilds
/// dependents when it changes.
class AppScope extends InheritedNotifier<AppModel> {
  const AppScope({super.key, required AppModel model, required super.child})
    : super(notifier: model);

  static AppModel of(BuildContext context) =>
      context.dependOnInheritedWidgetOfExactType<AppScope>()!.notifier!;

  /// Read without subscribing.
  static AppModel read(BuildContext context) =>
      context.getInheritedWidgetOfExactType<AppScope>()!.notifier!;
}

class OrchestratorApp extends StatelessWidget {
  const OrchestratorApp({super.key, required this.model});

  final AppModel model;

  @override
  Widget build(BuildContext context) {
    return AppScope(
      model: model,
      child: ListenableBuilder(
        listenable: model.settings,
        builder: (context, _) {
          final Settings s = model.settings;
          const seed = Color(0xFFD97757);
          return MaterialApp(
            title: 'Orchestrator',
            debugShowCheckedModeBanner: false,
            themeMode: s.themeMode,
            theme: ThemeData(
              colorScheme: ColorScheme.fromSeed(seedColor: seed),
              useMaterial3: true,
            ),
            darkTheme: ThemeData(
              colorScheme: ColorScheme.fromSeed(
                seedColor: seed,
                brightness: Brightness.dark,
              ),
              useMaterial3: true,
            ),
            home: const HostsScreen(),
          );
        },
      ),
    );
  }
}
