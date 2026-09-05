import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:math';
import 'dart:typed_data';

import 'package:crypto/crypto.dart' as crypto;

import '../protocol/challenge.dart';
import '../protocol/frames.dart';
import '../services/keys.dart';

/// Error reply from the daemon.
class ProtocolException implements Exception {
  ProtocolException(this.code, this.message);

  final String code;
  final String message;

  bool get isUnauthorized => code == 'unauthorized';
  bool get isNotFound => code == 'not_found';

  @override
  String toString() => message.isEmpty ? code : message;
}

/// Thrown when the host's certificate does not match the pinned fingerprint.
class FingerprintMismatch implements Exception {
  FingerprintMismatch(this.expected, this.actual);

  final String expected;
  final String actual;

  @override
  String toString() =>
      'host certificate changed (expected $expected, got $actual)';
}

/// The `hello` reply.
class HelloReply {
  HelloReply({
    required this.host,
    required this.version,
    required this.serverNonce,
    required this.fingerprint,
    required this.authNeeded,
  });

  final String host;
  final String version;
  final Uint8List serverNonce;
  final String fingerprint;
  final bool authNeeded;
}

/// Fingerprint of a DER certificate in the daemon's `sha256:<hex>` form.
String certFingerprint(List<int> der) => 'sha256:${crypto.sha256.convert(der)}';

/// One WebSocket to one daemon. Handles the JSON request/reply envelope,
/// server-initiated events, and binary PTY frames. Connection policy
/// (which address, when to retry) lives in [HostConnection].
class HostClient {
  HostClient._(this._ws, this.fingerprint, this.clientNonce);

  final WebSocket _ws;
  final String fingerprint;
  final Uint8List clientNonce;

  int _rid = 0;
  final Map<int, Completer<Map<String, dynamic>>> _pending = {};
  final _events = StreamController<Map<String, dynamic>>.broadcast();
  final _done = Completer<void>();
  bool _closed = false;

  /// Called for every output frame: `(handle, bytes)`.
  void Function(int handle, Uint8List data)? onOutput;

  /// Server-initiated messages (`session.event`, `session.removed`, ...).
  Stream<Map<String, dynamic>> get events => _events.stream;

  /// Completes when the socket closes for any reason.
  Future<void> get done => _done.future;

  bool get isClosed => _closed;

  /// Opens a TLS WebSocket to `wss://ip:port/ws`, pinning [fingerprint].
  /// Nothing else is trusted: the system roots are disabled so the pin is
  /// the only authority.
  static Future<HostClient> connect({
    required String ip,
    required int port,
    required String fingerprint,
    Duration timeout = const Duration(seconds: 6),
  }) async {
    final ctx = SecurityContext(withTrustedRoots: false);
    final http = HttpClient(context: ctx)
      ..connectionTimeout = timeout
      ..badCertificateCallback = (cert, host, p) {
        final fp = certFingerprint(cert.der);
        return fp == fingerprint;
      };
    final uri = Uri(scheme: 'wss', host: ip, port: port, path: '/ws');
    try {
      final ws = await WebSocket.connect(
        uri.toString(),
        customClient: http,
      ).timeout(timeout);
      ws.pingInterval = const Duration(seconds: 20);
      final nonce = _randomBytes(32);
      final c = HostClient._(ws, fingerprint, nonce);
      c._listen();
      return c;
    } on HandshakeException catch (e) {
      // Dart reports a rejected certificate as a handshake failure.
      throw FingerprintMismatch(fingerprint, e.message);
    } finally {
      http.close(force: false);
    }
  }

  static Uint8List _randomBytes(int n) {
    final r = Random.secure();
    return Uint8List.fromList(List.generate(n, (_) => r.nextInt(256)));
  }

  void _listen() {
    _ws.listen(
      _onMessage,
      onDone: _onClose,
      onError: (Object e) => _onClose(),
      cancelOnError: true,
    );
  }

  void _onMessage(dynamic data) {
    if (data is List<int>) {
      final f = Frame.decode(
        data is Uint8List ? data : Uint8List.fromList(data),
      );
      if (f != null && f.kind == kindOutput) {
        onOutput?.call(f.handle, f.payload);
      }
      return;
    }
    if (data is! String) return;
    Map<String, dynamic> m;
    try {
      m = jsonDecode(data) as Map<String, dynamic>;
    } catch (_) {
      return;
    }
    final rid = (m['rid'] as num?)?.toInt();
    if (rid != null && _pending.containsKey(rid)) {
      final c = _pending.remove(rid)!;
      if (m['t'] == 'error' || m['t'] == 'auth.fail') {
        c.completeError(
          ProtocolException(
            (m['code'] as String?) ?? 'error',
            (m['message'] as String?) ?? '',
          ),
        );
      } else {
        c.complete(m);
      }
      return;
    }
    _events.add(m);
  }

  void _onClose() {
    if (_closed) return;
    _closed = true;
    for (final c in _pending.values) {
      if (!c.isCompleted) c.completeError(StateError('connection closed'));
    }
    _pending.clear();
    _events.close();
    if (!_done.isCompleted) _done.complete();
  }

  /// Sends a request and waits for the reply with the same `rid`.
  Future<Map<String, dynamic>> request(
    String t, [
    Map<String, dynamic>? body,
    Duration timeout = const Duration(seconds: 20),
  ]) {
    if (_closed) return Future.error(StateError('connection closed'));
    final id = ++_rid;
    final msg = <String, dynamic>{'t': t, 'rid': id, ...?body};
    final c = Completer<Map<String, dynamic>>();
    _pending[id] = c;
    try {
      _ws.add(jsonEncode(msg));
    } catch (e) {
      _pending.remove(id);
      return Future.error(e);
    }
    return c.future.timeout(
      timeout,
      onTimeout: () {
        _pending.remove(id);
        throw TimeoutException('$t timed out');
      },
    );
  }

  /// Sends typed bytes to an attached session.
  void sendInput(int handle, List<int> bytes) {
    if (_closed) return;
    _ws.add(Frame.encode(kindInput, handle, bytes));
  }

  /// First message on the wire. Verifies the fingerprint the daemon reports
  /// against the pinned one as a second check.
  Future<HelloReply> hello({required String name, String? deviceId}) async {
    final r = await request('hello', {
      'proto': 1,
      'device_id': ?deviceId,
      'name': name,
      'client_nonce': base64.encode(clientNonce),
    });
    final fp = (r['fingerprint'] as String?) ?? '';
    if (fp != fingerprint) throw FingerprintMismatch(fingerprint, fp);
    return HelloReply(
      host: (r['host'] as String?) ?? '',
      version: (r['version'] as String?) ?? '',
      serverNonce: decodeB64((r['server_nonce'] as String?) ?? ''),
      fingerprint: fp,
      authNeeded: (r['auth_needed'] as bool?) ?? true,
    );
  }

  /// Pairs with a one-time code; returns the device id the daemon assigned.
  Future<String> pair({
    required String code,
    required DeviceKey key,
    required String name,
  }) async {
    final r = await request('pair', {
      'code': code,
      'pubkey': key.publicKeyB64,
      'name': name,
    });
    return r['device_id'] as String;
  }

  /// Signs the challenge and authenticates as [deviceId].
  Future<void> auth({
    required HelloReply hello,
    required String deviceId,
    required DeviceKey key,
  }) async {
    final msg = challengeBytes(
      serverNonce: hello.serverNonce,
      clientNonce: clientNonce,
      fingerprint: fingerprint,
      deviceId: deviceId,
    );
    final sig = await key.sign(msg);
    await request('auth', {'device_id': deviceId, 'sig': base64.encode(sig)});
  }

  Future<void> close() async {
    if (_closed) return;
    try {
      await _ws.close();
    } catch (_) {}
    _onClose();
  }
}
