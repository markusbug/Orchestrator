import 'package:flutter/foundation.dart';
import 'package:flutter_local_notifications/flutter_local_notifications.dart';

/// Local notifications for sessions that start waiting while the app is in
/// the background. No push: a free Apple ID has no push entitlement.
class Notifier {
  final _plugin = FlutterLocalNotificationsPlugin();
  bool _ready = false;

  Future<void> init() async {
    try {
      const settings = InitializationSettings(
        iOS: DarwinInitializationSettings(
          requestAlertPermission: true,
          requestBadgePermission: true,
          requestSoundPermission: true,
        ),
        android: AndroidInitializationSettings('@mipmap/ic_launcher'),
      );
      _ready = await _plugin.initialize(settings: settings) ?? false;
    } catch (e) {
      debugPrint('notifications unavailable: $e');
      _ready = false;
    }
  }

  Future<void> sessionWaiting({
    required String host,
    required String session,
    required String preview,
    required int id,
  }) async {
    if (!_ready) return;
    try {
      await _plugin.show(
        id: id,
        title: '$session is waiting',
        body: preview.isEmpty ? host : '$host · $preview',
        notificationDetails: const NotificationDetails(
          iOS: DarwinNotificationDetails(
            presentAlert: true,
            presentSound: true,
          ),
          android: AndroidNotificationDetails(
            'waiting',
            'Waiting sessions',
            channelDescription: 'A Claude Code session needs your input',
            importance: Importance.high,
            priority: Priority.high,
          ),
        ),
      );
    } catch (e) {
      debugPrint('notification failed: $e');
    }
  }
}
