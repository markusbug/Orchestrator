import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/foundation.dart';

import 'models.dart';

/// Talks to the local daemon by running the `orchestrator` binary and reading
/// its JSON.
///
/// The alternative was to speak HTTP over the admin unix socket directly, but
/// that means reimplementing `config.DefaultPaths`' socket resolution (which
/// has an `$XDG_RUNTIME_DIR` branch, a macOS branch and a hashed-tmpdir
/// fallback for long paths) in Dart, and redoing it again for Windows named
/// pipes later. Shelling out keeps one implementation of that logic, in Go.
class DaemonController extends ChangeNotifier {
  DaemonController();

  static const _visibleInterval = Duration(seconds: 2);
  static const _hiddenInterval = Duration(seconds: 20);

  Timer? _timer;
  Duration _interval = _visibleInterval;
  bool _polling = false;

  DaemonStatus? _status;
  ServiceState _service = const ServiceState.unknown();
  List<Device> _devices = const [];
  String? _error;
  String? _exePath;

  DaemonStatus? get status => _status;
  ServiceState get service => _service;
  List<Device> get devices => _devices.where((d) => !d.revoked).toList();
  bool get running => _status != null;

  /// The last error from talking to the binary, or null. "Daemon not running"
  /// is a normal state, not an error, and does not land here.
  String? get error => _error;

  /// Where the bundled binary is, once found. Shown in the About area.
  String? get exePath => _exePath;

  Future<void> start() async {
    await refresh();
    _schedule();
  }

  /// Polls hard while the user is looking at the window and gently when the
  /// app is only a tray icon.
  void setWindowVisible(bool visible) {
    final next = visible ? _visibleInterval : _hiddenInterval;
    if (next == _interval) return;
    _interval = next;
    _schedule();
    if (visible) unawaited(refresh());
  }

  void _schedule() {
    _timer?.cancel();
    _timer = Timer.periodic(_interval, (_) => refresh());
  }

  @override
  void dispose() {
    _timer?.cancel();
    super.dispose();
  }

  Future<void> refresh() async {
    if (_polling) return; // a slow spawn must not stack up behind the timer
    _polling = true;
    try {
      final svc = await _serviceState();
      DaemonStatus? st;
      List<Device> devs = const [];
      if (svc.running) {
        st = await _status_();
        if (st != null) devs = await _devices_();
      }
      _service = svc;
      _status = st;
      _devices = devs;
      notifyListeners();
    } finally {
      _polling = false;
    }
  }

  // ---- commands -----------------------------------------------------------

  /// Installs and starts the background service. Returns null on success or
  /// the error text to show.
  Future<String?> installService() async {
    final r = await _run(['install', '--json']);
    if (r.exitCode != 0) return _stderr(r);
    await refresh();
    return null;
  }

  /// Turns start-at-login on or off. Turning it off deliberately leaves a
  /// running daemon alone, so live sessions survive.
  Future<String?> setStartAtLogin(bool on) async {
    if (on && !_service.installed) return installService();
    final r = await _run(['_service', on ? 'enable' : 'disable']);
    if (r.exitCode != 0) return _stderr(r);
    await refresh();
    return null;
  }

  Future<String?> stopService() async {
    final r = await _run(['_service', 'stop']);
    if (r.exitCode != 0) return _stderr(r);
    await refresh();
    return null;
  }

  Future<String?> startService() async {
    final r = await _run(['_service', 'start']);
    if (r.exitCode != 0) return _stderr(r);
    await refresh();
    return null;
  }

  Future<PairInfo?> pair() async {
    final j = await _json(['pair', '--json']);
    return j == null ? null : PairInfo.fromJson(j as Map<String, dynamic>);
  }

  Future<String?> revoke(String deviceId) async {
    final r = await _run(['devices', 'revoke', deviceId, '--json']);
    if (r.exitCode != 0) return _stderr(r);
    await refresh();
    return null;
  }

  // ---- plumbing -----------------------------------------------------------

  Future<ServiceState> _serviceState() async {
    final j = await _json(['_service']);
    return j == null
        ? const ServiceState.unknown()
        : ServiceState.fromJson(j as Map<String, dynamic>);
  }

  Future<DaemonStatus?> _status_() async {
    final j = await _json(['status', '--json']);
    return j == null ? null : DaemonStatus.fromJson(j as Map<String, dynamic>);
  }

  Future<List<Device>> _devices_() async {
    final j = await _json(['devices', '--json']);
    if (j is! List) return const [];
    return j
        .map((e) => Device.fromJson(e as Map<String, dynamic>))
        .toList(growable: false);
  }

  Future<Object?> _json(List<String> args) async {
    final r = await _run(args);
    if (r.exitCode != 0) {
      _error = _stderr(r);
      return null;
    }
    try {
      final out = (r.stdout as String).trim();
      if (out.isEmpty) return null;
      _error = null;
      return jsonDecode(out);
    } on FormatException catch (e) {
      _error = 'unreadable output from orchestrator: $e';
      return null;
    }
  }

  Future<ProcessResult> _run(List<String> args) async {
    final exe = await _resolveExe();
    if (exe == null) {
      return ProcessResult(
        0,
        1,
        '',
        'the orchestrator binary is missing from this installation',
      );
    }
    try {
      return await Process.run(
        exe,
        args,
        stdoutEncoding: utf8,
        stderrEncoding: utf8,
      );
    } on ProcessException catch (e) {
      return ProcessResult(0, 1, '', e.message);
    }
  }

  static String _stderr(ProcessResult r) {
    final s = (r.stderr as String).trim();
    return s.isEmpty ? 'orchestrator exited with ${r.exitCode}' : s;
  }

  /// Resolves the daemon binary to use, copying the bundled one to a stable
  /// path first.
  ///
  /// The copy is not busywork. The service file and `claude-hooks.json` both
  /// record the daemon's absolute path, and the bundled path is not stable:
  /// an AppImage mounts at /tmp/.mount_XXXX for the life of one run, and a
  /// .app can be dragged anywhere. Installing the service from a path like
  /// that produces a unit that breaks on the next launch.
  Future<String?> _resolveExe() async {
    if (_exePath != null) return _exePath;
    final bundled = await _bundledExe();
    if (bundled == null) return null;
    if (_isStablePath(bundled)) return _exePath = bundled;
    final stable = _stableExePath();
    if (stable == null || bundled == stable) return _exePath = bundled;
    await _syncStable(bundled, stable);
    if (await File(stable).exists()) return _exePath = stable;
    return _exePath = bundled;
  }

  /// Whether a path will still be there next boot. A .deb or a .app in
  /// /Applications already is, and copying those would only leave a second
  /// binary to drift out of date.
  static bool _isStablePath(String path) =>
      path.startsWith('/usr/') ||
      path.startsWith('/opt/') ||
      path.startsWith('/Applications/');

  /// The binary shipped inside this app, or the one from a dev checkout.
  Future<String?> _bundledExe() async {
    final exeDir = File(Platform.resolvedExecutable).parent;
    final candidates = <String>[
      // macOS: Orchestrator.app/Contents/MacOS/.. -> Contents/Resources
      if (Platform.isMacOS) '${exeDir.parent.path}/Resources/orchestrator',
      // The Linux packages put both binaries in the same bundle directory.
      '${exeDir.path}/orchestrator',
      // `flutter run` from the repo, where `make build` wrote bin/orchestrator.
      '${Directory.current.path}/../bin/orchestrator',
      '${Directory.current.path}/bin/orchestrator',
    ];
    for (final c in candidates) {
      if (await File(c).exists()) return c;
    }
    final which = await Process.run('which', ['orchestrator']);
    if (which.exitCode == 0) {
      final p = (which.stdout as String).trim();
      if (p.isNotEmpty) return p;
    }
    return null;
  }

  String? _stableExePath() {
    final home = Platform.environment['HOME'];
    if (home == null || home.isEmpty) return null;
    if (Platform.isMacOS) {
      return '$home/Library/Application Support/orchestrator/bin/orchestrator';
    }
    if (Platform.isLinux) return '$home/.local/bin/orchestrator';
    return null;
  }

  /// Copies the bundled binary over the stable one when the versions differ.
  ///
  /// A running daemon may be executing the stable copy, and writing over a
  /// running binary fails with ETXTBSY, so this writes a sibling and renames.
  /// The rename does not disturb the running process; it picks the new binary
  /// up the next time it restarts, which avoids interrupting live sessions.
  Future<void> _syncStable(String bundled, String stable) async {
    try {
      final want = await _versionOf(bundled);
      if (want == null) return;
      if (await File(stable).exists() && await _versionOf(stable) == want) {
        return;
      }
      final tmp = '$stable.new';
      await File(stable).parent.create(recursive: true);
      await File(bundled).copy(tmp);
      await Process.run('chmod', ['0755', tmp]);
      await File(tmp).rename(stable);
    } on FileSystemException catch (e) {
      _error = 'could not install the daemon to $stable: ${e.message}';
    }
  }

  Future<String?> _versionOf(String exe) async {
    final r = await Process.run(exe, ['version'], stdoutEncoding: utf8);
    if (r.exitCode != 0) return null;
    return (r.stdout as String).trim();
  }
}
