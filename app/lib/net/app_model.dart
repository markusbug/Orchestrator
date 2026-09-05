import 'dart:async';

import 'package:connectivity_plus/connectivity_plus.dart';
import 'package:flutter/services.dart';
import 'package:flutter/widgets.dart';

import '../model/host.dart';
import '../model/session.dart';
import '../services/host_store.dart';
import '../services/keys.dart';
import '../services/notifications.dart';
import '../services/settings.dart';
import 'host_client.dart';
import 'host_connection.dart';

/// Pairing could not reach the host on any address.
class PairException implements Exception {
  PairException(this.port, this.failures);

  final int port;
  final List<String> failures;

  @override
  String toString() {
    if (failures.isEmpty) return 'no address to connect to';
    return 'Could not reach port $port on any address. '
        'Check that the phone is on the same Wi-Fi and that the host '
        'firewall allows the port (ufw: `sudo ufw allow $port/tcp`).\n'
        '${failures.map((f) => '• $f').join('\n')}';
  }
}

/// Short human text for a connection error.
String describeError(Object e) {
  final s = e.toString();
  if (e is TimeoutException) return 'timed out';
  if (e is FingerprintMismatch) return 'certificate does not match';
  final m = RegExp(r'OS Error: ([^,]+)').firstMatch(s);
  if (m != null) return m.group(1)!.toLowerCase();
  if (s.contains('SocketException')) return 'unreachable';
  return s.replaceFirst(RegExp(r'^\w+(Exception|Error): '), '');
}

/// Root state: paired hosts and their connections. Reconnects everything on
/// app foreground and on network changes.
class AppModel extends ChangeNotifier with WidgetsBindingObserver {
  AppModel({
    required this.settings,
    required this.keys,
    required this.hostStore,
    this.notifier,
    Stream<List<ConnectivityResult>>? connectivity,
  }) : _connectivity = connectivity; // ignore: prefer_initializing_formals

  final Settings settings;
  final KeyService keys;
  final HostStore hostStore;
  final Notifier? notifier;
  final Stream<List<ConnectivityResult>>? _connectivity;

  final List<HostRecord> hosts = [];
  final Map<String, HostConnection> connections = {};
  AppLifecycleState lifecycle = AppLifecycleState.resumed;
  StreamSubscription? _netSub;
  int _notifId = 0;

  Future<void> init() async {
    await settings.load();
    hosts.addAll(await hostStore.load());
    for (final h in hosts) {
      _connect(h);
    }
    WidgetsBinding.instance.addObserver(this);
    final net = _connectivity ?? Connectivity().onConnectivityChanged;
    _netSub = net.listen((_) => reconnectAll());
    notifyListeners();
  }

  HostConnection connectionFor(String hostId) => connections[hostId]!;

  HostConnection _connect(HostRecord h) {
    final c = HostConnection(
      h,
      keys: keys,
      deviceName: () => settings.deviceName,
      onWaiting: _onWaiting,
      onHostChanged: (_) => _persist(),
    );
    c.addListener(notifyListeners);
    connections[h.id] = c;
    c.start();
    return c;
  }

  void reconnectAll() {
    for (final c in connections.values) {
      c.reconnectNow();
    }
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    lifecycle = state;
    if (state == AppLifecycleState.resumed) reconnectAll();
  }

  void _onWaiting(HostConnection conn, SessionInfo s) {
    if (lifecycle == AppLifecycleState.resumed) {
      if (settings.haptics) HapticFeedback.mediumImpact();
      return;
    }
    if (!settings.notifyWaiting) return;
    unawaited(
      notifier?.sessionWaiting(
        host: conn.host.name,
        session: s.name,
        preview: s.preview,
        id: _notifId++,
      ),
    );
  }

  Future<void> _persist() => hostStore.save(hosts);

  /// Pairs with a host. Tries every address the payload lists; the code is
  /// only consumed by the daemon on success, so retries across addresses
  /// are safe.
  Future<HostRecord> pair(PairPayload p) async {
    if (p.code.isEmpty) throw ArgumentError('pairing code missing');
    if (p.fingerprint.isEmpty) throw ArgumentError('fingerprint missing');
    if (p.addrs.isEmpty) throw ArgumentError('no address to connect to');
    final key = await DeviceKey.generate();
    final failures = <String>[];
    for (final addr in p.addrs) {
      HostClient c;
      try {
        c = await HostClient.connect(
          ip: addr.ip,
          port: p.port,
          fingerprint: p.fingerprint,
        );
      } catch (e) {
        failures.add('${addr.ip}: ${describeError(e)}');
        continue;
      }
      try {
        final hello = await c.hello(name: settings.deviceName);
        final deviceId = await c.pair(
          code: p.code,
          key: key,
          name: settings.deviceName,
        );
        final info = HostInfo.fromJson(await c.request('host.info'));
        final hostname = info.host.isNotEmpty
            ? info.host
            : (p.host.isNotEmpty ? p.host : hello.host);
        final rec = HostRecord(
          id: deviceId,
          name: hostname.isEmpty ? addr.ip : hostname,
          hostname: hostname,
          addrs: mergeAddrs(info.addrs, [addr, ...p.addrs]),
          port: p.port,
          fingerprint: p.fingerprint,
          deviceId: deviceId,
          createdAt: DateTime.now(),
          lastGoodAddr: addr.ip,
        );
        await keys.save(rec.id, key);
        hosts.add(rec);
        await _persist();
        await c.close();
        _connect(rec);
        notifyListeners();
        return rec;
      } finally {
        await c.close();
      }
    }
    throw PairException(p.port, failures);
  }

  Future<void> renameHost(String id, String name) async {
    final h = hosts.firstWhere((h) => h.id == id);
    h.name = name.trim().isEmpty ? h.hostname : name.trim();
    await _persist();
    notifyListeners();
  }

  Future<void> forgetHost(String id) async {
    final c = connections.remove(id);
    c?.removeListener(notifyListeners);
    c?.dispose();
    hosts.removeWhere((h) => h.id == id);
    await keys.delete(id);
    await _persist();
    notifyListeners();
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    _netSub?.cancel();
    for (final c in connections.values) {
      c.dispose();
    }
    super.dispose();
  }
}
