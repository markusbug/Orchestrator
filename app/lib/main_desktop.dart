import 'dart:async';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:window_manager/window_manager.dart';

import 'desktop/daemon.dart';
import 'desktop/shell.dart';
import 'desktop/tray.dart';

/// Entry point for the desktop app.
///
/// Deliberately separate from lib/main.dart: that one boots the phone client
/// (keychain, notifications, paired-host store), none of which the desktop app
/// wants — it controls the daemon on this machine instead. Build with
/// `flutter build linux -t lib/main_desktop.dart`.
Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();

  // A second launch should raise the window that is already open, not start a
  // rival copy fighting over the same tray icon.
  if (await _raiseRunningInstance()) {
    exit(0);
  }

  await windowManager.ensureInitialized();
  await windowManager.waitUntilReadyToShow(
    const WindowOptions(
      size: Size(720, 820),
      minimumSize: Size(520, 560),
      title: 'Orchestrator',
      titleBarStyle: TitleBarStyle.normal,
    ),
    () async {
      await windowManager.show();
      await windowManager.focus();
    },
  );

  final daemon = DaemonController();
  final pairRequests = ValueNotifier<int>(0);
  final shutdownRequests = ValueNotifier<int>(0);

  Future<void> quit() async {
    await tearDown();
    exit(0);
  }

  final tray = OrchestratorTray(
    daemon: daemon,
    onOpen: _showWindow,
    onPair: () {
      pairRequests.value++;
      _showWindow();
    },
    onQuit: quit,
    // The tray cannot host a dialog, and stopping the daemon ends live
    // sessions, so this hands off to the window to ask first.
    onShutdown: () {
      shutdownRequests.value++;
      _showWindow();
    },
  );
  await tray.init();

  // With a tray icon, closing the window hides it. Without one (stock GNOME
  // has no StatusNotifier host), hiding would strand the app with no way back,
  // so close means quit -- the daemon keeps running either way.
  windowManager.addListener(_WindowHandler(daemon, tray));
  await windowManager.setPreventClose(tray.available);

  unawaited(daemon.start());
  await _listenForRaise();

  runApp(
    DesktopApp(
      daemon: daemon,
      pairRequests: pairRequests,
      shutdownRequests: shutdownRequests,
      onQuit: quit,
    ),
  );
}

Future<void> tearDown() async {
  await _raiseServer?.close();
  final path = _instanceSocketPath();
  if (path != null) {
    try {
      await File(path).delete();
    } on FileSystemException {
      // Nothing to clean up.
    }
  }
}

Future<void> _showWindow() async {
  await windowManager.show();
  await windowManager.focus();
}

class _WindowHandler with WindowListener {
  _WindowHandler(this.daemon, this.tray);

  final DaemonController daemon;
  final OrchestratorTray tray;

  @override
  void onWindowClose() async {
    if (tray.available) {
      await windowManager.hide();
      daemon.setWindowVisible(false);
    } else {
      await tearDown();
      exit(0);
    }
  }

  @override
  void onWindowFocus() => daemon.setWindowVisible(true);

  @override
  void onWindowMinimize() => daemon.setWindowVisible(false);

  @override
  void onWindowRestore() => daemon.setWindowVisible(true);
}

// ---- single instance ------------------------------------------------------

ServerSocket? _raiseServer;

String? _instanceSocketPath() {
  final env = Platform.environment;
  final runtime = env['XDG_RUNTIME_DIR'];
  if (runtime != null && runtime.isNotEmpty) {
    return '$runtime/orchestrator-desktop.sock';
  }
  final home = env['HOME'];
  if (home == null || home.isEmpty) return null;
  return Platform.isMacOS
      ? '$home/Library/Application Support/orchestrator/desktop.sock'
      : '$home/.config/orchestrator/desktop.sock';
}

/// Returns true when another copy was already running and has been told to
/// show itself.
Future<bool> _raiseRunningInstance() async {
  final path = _instanceSocketPath();
  if (path == null) return false;
  if (!await File(path).exists()) return false;
  try {
    final s = await Socket.connect(
      InternetAddress(path, type: InternetAddressType.unix),
      0,
      timeout: const Duration(milliseconds: 500),
    );
    s.write('show\n');
    await s.flush();
    await s.close();
    return true;
  } on SocketException {
    // A socket file left behind by a crash. Ours now.
    try {
      await File(path).delete();
    } on FileSystemException {
      return false;
    }
    return false;
  }
}

Future<void> _listenForRaise() async {
  final path = _instanceSocketPath();
  if (path == null) return;
  try {
    await File(path).parent.create(recursive: true);
    _raiseServer = await ServerSocket.bind(
      InternetAddress(path, type: InternetAddressType.unix),
      0,
    );
  } on SocketException {
    return; // not fatal: we just lose the raise-the-window trick
  } on FileSystemException {
    return;
  }
  _raiseServer!.listen((socket) {
    socket.listen((_) {}, onDone: () => socket.destroy());
    unawaited(_showWindow());
  });
}
