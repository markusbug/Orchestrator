import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:tray_manager/tray_manager.dart';

import 'daemon.dart';

/// The tray / menu-bar icon.
///
/// Not every Linux desktop has a tray: GNOME needs the AppIndicator
/// extension, which Ubuntu ships and enables but stock GNOME does not. When
/// there is no StatusNotifier host, [available] is false and the window must
/// not hide itself on close, or the app becomes invisible with no way back.
class OrchestratorTray with TrayListener {
  OrchestratorTray({
    required this.daemon,
    required this.onOpen,
    required this.onPair,
    required this.onQuit,
  });

  final DaemonController daemon;
  final VoidCallback onOpen;
  final VoidCallback onPair;
  final VoidCallback onQuit;

  bool _available = false;
  bool get available => _available;

  /// Sets the tray up. A desktop that cannot show one is a degraded mode, not
  /// a crash: [available] goes false and the window stops hiding on close.
  Future<void> init() async {
    _available = await _hasTrayHost();
    if (!_available) return;
    try {
      await _setUp();
    } catch (e) {
      debugPrint('tray unavailable: $e');
      _available = false;
    }
  }

  Future<void> _setUp() async {
    trayManager.addListener(this);
    await trayManager.setIcon(
      Platform.isMacOS
          ? 'assets/tray/tray_iconTemplate.png'
          : 'assets/tray/tray_icon.png',
      isTemplate: Platform.isMacOS,
    );
    await _rebuild();
    daemon.addListener(_rebuild);
  }

  Future<void> dispose() async {
    if (!_available) return;
    daemon.removeListener(_rebuild);
    trayManager.removeListener(this);
    await trayManager.destroy();
  }

  /// Registered as a daemon listener, so it must never throw: an error here
  /// would surface as an unhandled async exception on every poll.
  Future<void> _rebuild() async {
    if (!_available) return;
    try {
      await _render();
    } catch (e) {
      debugPrint('tray update failed: $e');
    }
  }

  Future<void> _render() async {
    final st = daemon.status;
    final relay = st?.relay;
    final line = !daemon.running
        ? 'Not running'
        : relay != null && relay.connected
        ? 'Running · reachable anywhere'
        : 'Running · same network only';
    // Linux app indicators have no tooltip, and asking for one throws.
    if (!Platform.isLinux) {
      await trayManager.setToolTip('Orchestrator — $line');
    }
    await trayManager.setContextMenu(
      Menu(
        items: [
          MenuItem(key: 'status', label: line, disabled: true),
          MenuItem.separator(),
          MenuItem(key: 'open', label: 'Open Orchestrator'),
          MenuItem(
            key: 'pair',
            label: 'Pair a device',
            disabled: !daemon.running,
          ),
          MenuItem.separator(),
          // Quitting the window must never take the daemon down with it:
          // sessions outliving the UI is the whole point of the product.
          MenuItem(key: 'quit', label: 'Quit — sessions keep running'),
        ],
      ),
    );
  }

  @override
  void onTrayIconMouseDown() {
    // Left click opens the window on Linux; on macOS the menu bar convention
    // is that any click shows the menu.
    if (Platform.isMacOS) {
      trayManager.popUpContextMenu();
    } else {
      onOpen();
    }
  }

  @override
  void onTrayIconRightMouseDown() => trayManager.popUpContextMenu();

  @override
  void onTrayMenuItemClick(MenuItem menuItem) {
    switch (menuItem.key) {
      case 'open':
        onOpen();
      case 'pair':
        onPair();
      case 'quit':
        onQuit();
    }
  }

  /// Asks DBus whether anything is hosting status icons. On macOS the menu
  /// bar is always there.
  static Future<bool> _hasTrayHost() async {
    if (!Platform.isLinux) return true;
    try {
      final r = await Process.run('gdbus', [
        'call',
        '--session',
        '--dest',
        'org.freedesktop.DBus',
        '--object-path',
        '/org/freedesktop/DBus',
        '--method',
        'org.freedesktop.DBus.NameHasOwner',
        'org.kde.StatusNotifierWatcher',
      ]);
      return r.exitCode == 0 && (r.stdout as String).contains('true');
    } on ProcessException {
      // No gdbus: assume a tray rather than degrading a working desktop.
      return true;
    }
  }
}
