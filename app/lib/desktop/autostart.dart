import 'dart:io';

/// The desktop app's own login item.
///
/// This is separate from the daemon's service: the daemon is what keeps
/// sessions alive, the app is the window and the tray icon. One switch in the
/// UI flips both, but they are written in different places.
///
/// Written by hand rather than with `launch_at_startup`, because that package
/// pulls the LaunchAtLogin Swift package and an Xcode setup step into the
/// macOS build for what is, on both of our platforms, one small file.
class AppAutostart {
  static const _bundleId = 'io.freedomfactory.orchestrator';

  static Future<bool> isEnabled() async {
    final f = await _entry();
    return f != null && f.existsSync();
  }

  static Future<void> setEnabled(bool on) async {
    final f = await _entry();
    if (f == null) return;
    if (!on) {
      if (f.existsSync()) await f.delete();
      return;
    }
    await f.parent.create(recursive: true);
    await f.writeAsString(Platform.isMacOS ? _plist() : _desktopEntry());
  }

  static Future<File?> _entry() async {
    final home = Platform.environment['HOME'];
    if (home == null || home.isEmpty) return null;
    if (Platform.isMacOS) {
      return File('$home/Library/LaunchAgents/$_bundleId.app.plist');
    }
    if (Platform.isLinux) {
      final config = Platform.environment['XDG_CONFIG_HOME'] ?? '$home/.config';
      return File('$config/autostart/orchestrator.desktop');
    }
    return null;
  }

  static String _desktopEntry() =>
      '''
[Desktop Entry]
Type=Application
Name=Orchestrator
Comment=Run Claude Code sessions on this machine, drive them from your phone
Exec=${Platform.resolvedExecutable}
Icon=orchestrator
Terminal=false
X-GNOME-Autostart-enabled=true
''';

  /// On macOS the login item opens the bundle rather than the inner binary,
  /// so the app gets a normal application launch.
  static String _plist() {
    final exe = Platform.resolvedExecutable;
    // .../Orchestrator.app/Contents/MacOS/Orchestrator -> .../Orchestrator.app
    final app = File(exe).parent.parent.parent.path;
    return '''
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
\t<key>Label</key>
\t<string>$_bundleId.app</string>
\t<key>ProgramArguments</key>
\t<array>
\t\t<string>/usr/bin/open</string>
\t\t<string>-a</string>
\t\t<string>$app</string>
\t</array>
\t<key>RunAtLoad</key>
\t<true/>
</dict>
</plist>
''';
  }
}
