import 'package:flutter/material.dart';

import 'app.dart';
import 'net/app_model.dart';
import 'services/host_store.dart';
import 'services/keys.dart';
import 'services/notifications.dart';
import 'services/settings.dart';

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  final notifier = Notifier();
  await notifier.init();
  final model = AppModel(
    settings: Settings(),
    keys: KeyService(KeychainStore()),
    hostStore: PrefsHostStore(),
    notifier: notifier,
  );
  await model.init();
  runApp(OrchestratorApp(model: model));
}
