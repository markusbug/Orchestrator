import 'dart:async';
import 'dart:math';

import 'package:flutter/foundation.dart';

import '../model/fs.dart';
import '../model/host.dart';
import '../model/session.dart';
import '../services/keys.dart';
import 'dialer.dart';
import 'host_client.dart';

enum ConnState {
  /// Not started or stopped.
  idle,

  /// Trying addresses.
  connecting,

  /// Authenticated and usable.
  connected,

  /// Lost or failed; will retry after a backoff.
  retrying,

  /// Gave up until the user retries (revoked device, changed certificate).
  failed,
}

/// A terminal view attached to a session. The connection re-attaches every
/// binding after a reconnect and replays the scrollback into it.
class TerminalBinding {
  TerminalBinding({
    required this.sessionId,
    required this.cols,
    required this.rows,
    required this.onOutput,
    required this.onReattach,
    required this.onDetached,
  });

  final String sessionId;
  int cols;
  int rows;
  final void Function(Uint8List data) onOutput;

  /// Called right before a (re)attach so the view can clear itself; the
  /// daemon then replays its buffer and nudges the TUI to redraw.
  final void Function() onReattach;

  /// `session.detached` reason: `exited`, `slow`, `closed`, or `removed`.
  final void Function(String reason) onDetached;
}

/// Everything the app knows about one host: its connection, the session
/// list, and the terminals attached to it. One socket per host.
class HostConnection extends ChangeNotifier {
  HostConnection(
    this.host, {
    required KeyService keys,
    required String Function() deviceName,
    this.onWaiting,
    this.onHostChanged,
  }) : _keys = keys, // ignore: prefer_initializing_formals
       _deviceName = deviceName; // ignore: prefer_initializing_formals

  final HostRecord host;
  final KeyService _keys;
  final String Function() _deviceName;

  /// A session changed to *waiting*.
  void Function(HostConnection conn, SessionInfo session)? onWaiting;

  /// The record changed (last good address) and should be persisted.
  void Function(HostRecord host)? onHostChanged;

  ConnState state = ConnState.idle;
  String? error;
  HostInfo? info;

  /// The address the current socket was opened to (its `ip` field).
  String? connectedVia;
  HostAddr? connectedAddr;
  DateTime? lastConnectedAt;

  /// `relay` or `ip:port`, for status lines.
  String? get viaLabel => connectedAddr?.label(host.port);

  final Map<String, SessionInfo> sessions = {};
  final Map<int, String> _handles = {};
  final Map<String, TerminalBinding> _bindings = {};

  HostClient? _client;
  bool _stopped = true;
  int _attempt = 0;
  Completer<void>? _wake;
  StreamSubscription? _eventSub;

  bool get isOnline => state == ConnState.connected;
  int get waitingCount => sessions.values.where((s) => s.isWaiting).length;
  int get runningCount => sessions.values.where((s) => s.isRunning).length;
  List<SessionInfo> get sorted => sortSessions(sessions.values);
  String? get home => info?.home;
  String get defaultCmd => info?.defaultCmd ?? 'claude';

  /// Starts connecting and keeps reconnecting until [stop] or [dispose].
  void start() {
    if (!_stopped) return;
    _stopped = false;
    _attempt = 0;
    unawaited(_loop());
  }

  void stop() {
    _stopped = true;
    _wakeUp();
    final c = _client;
    _client = null;
    unawaited(c?.close());
    _setState(ConnState.idle, null);
  }

  /// Connect now if not connected, or verify the socket is alive if it is.
  /// Called on app foreground and on network changes.
  void reconnectNow() {
    if (_stopped) {
      start();
      return;
    }
    if (state == ConnState.connected) {
      unawaited(_probe());
      return;
    }
    _attempt = 0;
    _wakeUp();
  }

  Future<void> _probe() async {
    final c = _client;
    if (c == null) return;
    try {
      await c.request('ping', null, const Duration(seconds: 5));
    } catch (_) {
      await c.close();
    }
  }

  void _wakeUp() {
    final w = _wake;
    if (w != null && !w.isCompleted) w.complete();
  }

  void _setState(ConnState s, String? err) {
    state = s;
    error = err;
    notifyListeners();
  }

  Future<void> _loop() async {
    while (!_stopped) {
      _setState(ConnState.connecting, null);
      var giveUp = false;
      try {
        final c = await _connectOnce();
        if (_stopped) {
          await c.close();
          return;
        }
        _attempt = 0;
        _setState(ConnState.connected, null);
        await c.done;
        if (_client == c) _client = null;
        _eventSub?.cancel();
        _eventSub = null;
        if (_stopped) return;
        _setState(ConnState.retrying, 'connection lost');
      } on ProtocolException catch (e) {
        giveUp = e.isUnauthorized || e.code == 'rate_limited';
        _setState(giveUp ? ConnState.failed : ConnState.retrying, e.message);
      } on FingerprintMismatch catch (e) {
        giveUp = true;
        _setState(ConnState.failed, e.toString());
      } catch (e) {
        _setState(ConnState.retrying, _describe(e));
      }
      if (_stopped) return;
      final delay = giveUp ? const Duration(minutes: 5) : _backoff();
      _wake = Completer<void>();
      await Future.any([Future<void>.delayed(delay), _wake!.future]);
    }
  }

  Duration _backoff() {
    final n = min(_attempt++, 5);
    final base = 1000 * (1 << n); // 1s .. 32s
    final jitter = Random().nextInt(500);
    return Duration(milliseconds: min(base, 30000) + jitter);
  }

  static String _describe(Object e) {
    if (e is DialFailed) e = e.last;
    if (e is TimeoutException) return 'connection timed out';
    if (e.toString().contains('SocketException')) return 'host unreachable';
    return e.toString().replaceFirst('Exception: ', '');
  }

  Future<HostClient> _connectOnce() async {
    final key = await _keys.load(host.id);
    if (key == null) {
      throw ProtocolException(
        'unauthorized',
        'device key missing; forget this host and pair again',
      );
    }
    final d = await dialFirst(
      host.orderedAddrs,
      hostPort: host.port,
      fingerprint: host.fingerprint,
    );
    final c = d.client;
    final addr = d.addr;
    if (_stopped) {
      await c.close();
      throw StateError('stopped');
    }
    try {
      final h = await c.hello(name: _deviceName(), deviceId: host.deviceId);
      if (h.authNeeded) {
        await c.auth(hello: h, deviceId: host.deviceId, key: key);
      }
      _client = c;
      connectedVia = addr.ip;
      connectedAddr = addr;
      lastConnectedAt = DateTime.now();
      // Only a direct address is worth remembering: the relay is tried
      // last regardless, so it never shadows a LAN path that came back.
      if (!addr.isRelay && host.lastGoodAddr != addr.ip) {
        host.lastGoodAddr = addr.ip;
        onHostChanged?.call(host);
      }
      _wire(c);
      await _afterConnect(c);
      return c;
    } catch (e) {
      await c.close();
      if (_client == c) _client = null;
      rethrow;
    }
  }

  void _wire(HostClient c) {
    c.onOutput = (handle, data) {
      final id = _handles[handle];
      if (id == null) return;
      _bindings[id]?.onOutput(data);
    };
    _eventSub?.cancel();
    _eventSub = c.events.listen(_onEvent);
  }

  Future<void> _afterConnect(HostClient c) async {
    final infoReply = await c.request('host.info');
    info = HostInfo.fromJson(infoReply);
    if (info!.host.isNotEmpty && host.hostname != info!.host) {
      host.hostname = info!.host;
      onHostChanged?.call(host);
    }
    // The daemon knows its own port; a record paired by hand may carry
    // whatever the user typed.
    if (info!.port > 0 && host.port != info!.port) {
      host.port = info!.port;
      onHostChanged?.call(host);
    }
    // Keep the address list current so a host that moves networks is still
    // reachable, but never drop the address we are connected through.
    final via = connectedVia;
    final merged = mergeAddrs(info!.addrs, [
      ...host.addrs.where((a) => a.ip == via),
    ]);
    if (info!.addrs.isNotEmpty && !listEquals(merged, host.addrs)) {
      host.addrs = merged;
      onHostChanged?.call(host);
    }
    await _refreshWith(c);
    for (final b in List.of(_bindings.values)) {
      try {
        await _attachRemote(c, b);
      } catch (e) {
        b.onDetached(
          e is ProtocolException && e.isNotFound ? 'removed' : 'closed',
        );
      }
    }
  }

  void _onEvent(Map<String, dynamic> m) {
    switch (m['t']) {
      case 'session.event':
        final s = SessionInfo.fromJson(m['session'] as Map<String, dynamic>);
        final old = sessions[s.id];
        _upsert(s);
        if (s.isWaiting && (old == null || !old.isWaiting)) {
          onWaiting?.call(this, s);
        }
        notifyListeners();
      case 'session.removed':
        final id = m['id'] as String;
        final s = sessions.remove(id);
        if (s != null) _handles.remove(s.handle);
        _bindings[id]?.onDetached('removed');
        notifyListeners();
      case 'session.detached':
        final id = m['id'] as String;
        final reason = (m['reason'] as String?) ?? 'closed';
        final b = _bindings[id];
        if (b == null) return;
        if (reason == 'slow') {
          // We fell behind; re-attach to resync from the buffer.
          final c = _client;
          if (c != null) unawaited(_attachRemote(c, b).catchError((_) {}));
          return;
        }
        b.onDetached(reason);
    }
  }

  void _upsert(SessionInfo s) {
    final old = sessions[s.id];
    if (old != null && old.handle != s.handle) _handles.remove(old.handle);
    sessions[s.id] = s;
    _handles[s.handle] = s.id;
  }

  HostClient _need() {
    final c = _client;
    if (c == null || c.isClosed) throw StateError('not connected');
    return c;
  }

  // ---- API ----

  Future<List<SessionInfo>> refresh() => _refreshWith(_need());

  Future<List<SessionInfo>> _refreshWith(HostClient c) async {
    final r = await c.request('session.list');
    final list = ((r['sessions'] as List?) ?? const [])
        .map((e) => SessionInfo.fromJson(e as Map<String, dynamic>))
        .toList();
    sessions.clear();
    _handles.clear();
    for (final s in list) {
      _upsert(s);
    }
    notifyListeners();
    return sorted;
  }

  Future<SessionInfo> createSession({
    required String cwd,
    String? cmd,
    List<String> args = const [],
    String? name,
    required int cols,
    required int rows,
  }) async {
    final r = await _need().request('session.create', {
      'cwd': cwd,
      if (cmd != null && cmd.isNotEmpty) 'cmd': cmd,
      if (args.isNotEmpty) 'args': args,
      if (name != null && name.isNotEmpty) 'name': name,
      'cols': cols,
      'rows': rows,
    });
    final s = SessionInfo.fromJson(r['session'] as Map<String, dynamic>);
    _upsert(s);
    notifyListeners();
    return s;
  }

  Future<SessionInfo> resumeSession(
    String id, {
    required int cols,
    required int rows,
  }) async {
    final r = await _need().request('session.resume', {
      'id': id,
      'cols': cols,
      'rows': rows,
    });
    final s = SessionInfo.fromJson(r['session'] as Map<String, dynamic>);
    final old = sessions.remove(id);
    if (old != null) _handles.remove(old.handle);
    _upsert(s);
    notifyListeners();
    return s;
  }

  /// Registers a terminal and attaches it if connected. Safe to call while
  /// offline: the binding is attached once the socket comes back.
  Future<void> attach(TerminalBinding b) async {
    _bindings[b.sessionId] = b;
    final c = _client;
    if (c == null || c.isClosed) return;
    await _attachRemote(c, b);
  }

  Future<void> _attachRemote(HostClient c, TerminalBinding b) async {
    b.onReattach();
    final r = await c.request('session.attach', {
      'id': b.sessionId,
      'cols': b.cols,
      'rows': b.rows,
    });
    final s = SessionInfo.fromJson(r['session'] as Map<String, dynamic>);
    _upsert(s);
  }

  Future<void> detach(TerminalBinding b) async {
    if (_bindings[b.sessionId] == b) _bindings.remove(b.sessionId);
    final c = _client;
    if (c == null || c.isClosed) return;
    try {
      await c.request('session.detach', {'id': b.sessionId});
    } catch (_) {}
  }

  bool isAttached(String sessionId) => _bindings.containsKey(sessionId);

  void sendInput(String sessionId, List<int> bytes) {
    final s = sessions[sessionId];
    final c = _client;
    if (s == null || c == null || c.isClosed) return;
    c.sendInput(s.handle, bytes);
  }

  Future<void> resize(String sessionId, int cols, int rows) async {
    final b = _bindings[sessionId];
    if (b != null) {
      b.cols = cols;
      b.rows = rows;
    }
    final c = _client;
    if (c == null || c.isClosed) return;
    try {
      await c.request('session.resize', {
        'id': sessionId,
        'cols': cols,
        'rows': rows,
      });
    } catch (_) {}
  }

  Future<void> kill(String id, {String? signal}) =>
      _need().request('session.kill', {'id': id, 'signal': ?signal});

  Future<void> rename(String id, String name) =>
      _need().request('session.rename', {'id': id, 'name': name});

  Future<void> remove(String id) =>
      _need().request('session.remove', {'id': id});

  Future<FsListing> fsList(String path, {bool hidden = false}) async {
    final r = await _need().request('fs.list', {
      'path': path,
      if (hidden) 'hidden': true,
    });
    return FsListing.fromJson(r);
  }

  Future<List<FsEntry>> fsSearch(
    String query, {
    String? root,
    int limit = 50,
  }) async {
    final r = await _need().request('fs.search', {
      'query': query,
      'root': ?root,
      'limit': limit,
    }, const Duration(seconds: 30));
    return ((r['entries'] as List?) ?? const [])
        .map((e) => FsEntry.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  Future<List<String>> fsRecents() async {
    final r = await _need().request('fs.recents');
    return ((r['paths'] as List?) ?? const []).cast<String>();
  }

  Future<List<Conversation>> conversations(String cwd) async {
    final r = await _need().request('claude.conversations', {'cwd': cwd});
    return ((r['conversations'] as List?) ?? const [])
        .map((e) => Conversation.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  @override
  void dispose() {
    stop();
    _eventSub?.cancel();
    super.dispose();
  }
}
