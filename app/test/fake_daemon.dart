import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:math';
import 'dart:typed_data';

import 'package:cryptography/cryptography.dart';
import 'package:orchestrator/net/host_client.dart';
import 'package:orchestrator/protocol/challenge.dart';
import 'package:orchestrator/protocol/frames.dart';

/// A minimal in-process daemon speaking protocol v1 over TLS, enough to
/// exercise the client: pairing, challenge auth, sessions, attach replay,
/// events, and binary frames.
class FakeDaemon {
  FakeDaemon._(this._server, this.fingerprint);

  final HttpServer _server;
  final String fingerprint;

  final Map<String, List<int>> devices = {}; // device id -> pubkey
  final Set<String> revoked = {};
  String? pairCode;
  final Map<String, Map<String, dynamic>> sessions = {};
  final Map<String, List<int>> buffers = {};
  final List<FakeConn> conns = [];
  int _handle = 0;
  int _seq = 0;
  int connectionsAccepted = 0;

  /// When set, `host.info` also advertises this relay-kind address.
  Map<String, dynamic>? relayAddr;

  int get port => _server.port;

  static Future<FakeDaemon> start() async {
    final dir = Directory.current.path;
    final ctx = SecurityContext()
      ..useCertificateChain('$dir/test/fixtures/test_cert.txt')
      ..usePrivateKey('$dir/test/fixtures/test_key.txt');
    final pem = File('$dir/test/fixtures/test_cert.txt').readAsStringSync();
    final der = base64.decode(
      pem.split('\n').where((l) => !l.startsWith('-')).join(),
    );
    final server = await HttpServer.bindSecure('127.0.0.1', 0, ctx);
    final d = FakeDaemon._(server, certFingerprint(der));
    server.listen(d._onRequest);
    return d;
  }

  Future<void> close() async {
    for (final c in List.of(conns)) {
      await c.close();
    }
    await _server.close(force: true);
  }

  Map<String, dynamic> addSession({
    String name = 's',
    String status = 'running',
    String cwd = '/home/u',
  }) {
    final id = 'sess-${++_seq}';
    final s = {
      'id': id,
      'handle': ++_handle,
      'name': name,
      'cwd': cwd,
      'cmd': 'claude',
      'args': <String>[],
      'pid': 100 + _handle,
      'status': status,
      'created_at': DateTime.now().millisecondsSinceEpoch,
      'last_output_at': DateTime.now().millisecondsSinceEpoch,
      'cols': 80,
      'rows': 24,
      'preview': '',
    };
    sessions[id] = s;
    buffers[id] = [];
    return s;
  }

  void setStatus(String id, String status) {
    sessions[id]!['status'] = status;
    broadcast({'t': 'session.event', 'session': sessions[id]});
  }

  void removeSession(String id) {
    sessions.remove(id);
    broadcast({'t': 'session.removed', 'id': id});
  }

  /// Emits PTY output for a session to every attached connection.
  void output(String id, List<int> data) {
    buffers[id]!.addAll(data);
    final handle = sessions[id]!['handle'] as int;
    for (final c in conns) {
      if (c.attached.contains(id)) {
        c.ws.add(Frame.encode(kindOutput, handle, data));
      }
    }
  }

  void broadcast(Map<String, dynamic> m) {
    for (final c in conns) {
      if (c.authed) c.ws.add(jsonEncode(m));
    }
  }

  Future<void> _onRequest(HttpRequest req) async {
    if (req.uri.path != '/ws') {
      req.response.statusCode = 404;
      await req.response.close();
      return;
    }
    final ws = await WebSocketTransformer.upgrade(req);
    connectionsAccepted++;
    final c = FakeConn(this, ws);
    conns.add(c);
    ws.listen(
      c.onMessage,
      onDone: () => conns.remove(c),
      onError: (_) => conns.remove(c),
    );
  }
}

class FakeConn {
  FakeConn(this.d, this.ws);

  final FakeDaemon d;
  final WebSocket ws;
  bool hello = false;
  bool authed = false;
  String? deviceId;
  List<int>? serverNonce;
  List<int>? clientNonce;
  final Set<String> attached = {};
  final List<(String, List<int>)> inputs = [];
  final List<Map<String, dynamic>> requests = [];

  Future<void> close() => ws.close();

  void reply(int? rid, String t, [Map<String, dynamic>? body]) {
    ws.add(jsonEncode({'t': t, 'rid': ?rid, ...?body}));
  }

  void error(int? rid, String code, String msg) =>
      reply(rid, 'error', {'code': code, 'message': msg});

  Future<void> onMessage(dynamic data) async {
    if (data is List<int>) {
      final f = Frame.decode(Uint8List.fromList(data));
      if (f == null || !authed) return;
      for (final s in d.sessions.values) {
        if (s['handle'] == f.handle && attached.contains(s['id'])) {
          inputs.add((s['id'] as String, f.payload));
        }
      }
      return;
    }
    final m = jsonDecode(data as String) as Map<String, dynamic>;
    requests.add(m);
    final rid = (m['rid'] as num?)?.toInt();
    final t = m['t'] as String;
    if (!hello) {
      if (t != 'hello') return error(rid, 'bad_request', 'expected hello');
      hello = true;
      clientNonce = decodeB64(m['client_nonce'] as String);
      serverNonce = List.generate(32, (i) => Random().nextInt(256));
      return reply(rid, 'hello', {
        'proto': 1,
        'host': 'fake',
        'version': 'test',
        'server_nonce': base64.encode(serverNonce!),
        'fingerprint': d.fingerprint,
        'auth_needed': true,
      });
    }
    if (!authed) {
      switch (t) {
        case 'pair':
          if (d.pairCode == null || m['code'] != d.pairCode) {
            return error(rid, 'unauthorized', 'wrong pairing code');
          }
          d.pairCode = null;
          final id = 'dev-${d.devices.length + 1}';
          d.devices[id] = decodeB64(m['pubkey'] as String);
          authed = true;
          deviceId = id;
          return reply(rid, 'pair.ok', {'device_id': id});
        case 'auth':
          final id = m['device_id'] as String;
          final pub = d.devices[id];
          if (pub == null || d.revoked.contains(id)) {
            reply(rid, 'auth.fail', {
              'code': 'unauthorized',
              'message': 'unknown or revoked device',
            });
            await ws.close();
            return;
          }
          final msg = challengeBytes(
            serverNonce: serverNonce!,
            clientNonce: clientNonce!,
            fingerprint: d.fingerprint,
            deviceId: id,
          );
          final ok = await Ed25519().verify(
            msg,
            signature: Signature(
              decodeB64(m['sig'] as String),
              publicKey: SimplePublicKey(pub, type: KeyPairType.ed25519),
            ),
          );
          if (!ok) {
            reply(rid, 'auth.fail', {
              'code': 'unauthorized',
              'message': 'signature verification failed',
            });
            await ws.close();
            return;
          }
          authed = true;
          deviceId = id;
          return reply(rid, 'auth.ok', {'device_id': id});
        case 'ping':
          return reply(rid, 'pong');
        default:
          return error(rid, 'unauthorized', 'authenticate first');
      }
    }
    switch (t) {
      case 'ping':
        reply(rid, 'pong');
      case 'host.info':
        reply(rid, 'host.info', {
          'host': 'fake',
          'version': 'test',
          'fingerprint': d.fingerprint,
          'port': d.port,
          'addrs': [
            {'ip': '127.0.0.1', 'kind': 'lan'},
            if (d.relayAddr != null) d.relayAddr,
          ],
          'roots': ['/home/u'],
          'default_cmd': 'claude',
          'home': '/home/u',
        });
      case 'session.list':
        reply(rid, 'session.list', {'sessions': d.sessions.values.toList()});
      case 'session.create':
        final s = d.addSession(
          name: (m['name'] as String?) ?? 'new',
          cwd: m['cwd'] as String,
        );
        s['cols'] = m['cols'];
        s['rows'] = m['rows'];
        reply(rid, 'session.created', {'session': s});
        d.broadcast({'t': 'session.event', 'session': s});
      case 'session.resume':
        final old = d.sessions.remove(m['id']);
        if (old == null) return error(rid, 'not_found', 'no such session');
        final s = d.addSession(
          name: old['name'] as String,
          cwd: old['cwd'] as String,
        );
        reply(rid, 'session.created', {'session': s});
        d.broadcast({'t': 'session.removed', 'id': old['id']});
      case 'session.attach':
        final s = d.sessions[m['id']];
        if (s == null) return error(rid, 'not_found', 'no such session');
        attached.add(s['id'] as String);
        if (m['cols'] != null) s['cols'] = m['cols'];
        if (m['rows'] != null) s['rows'] = m['rows'];
        reply(rid, 'session.attached', {'session': s});
        final buf = d.buffers[s['id']]!;
        if (buf.isNotEmpty) {
          ws.add(Frame.encode(kindOutput, s['handle'] as int, buf));
        }
      case 'session.detach':
        attached.remove(m['id']);
        reply(rid, 'ok');
      case 'session.resize':
        final s = d.sessions[m['id']];
        if (s == null) return error(rid, 'not_found', 'no such session');
        s['cols'] = m['cols'];
        s['rows'] = m['rows'];
        reply(rid, 'ok');
      case 'session.kill':
        final s = d.sessions[m['id']];
        if (s == null) return error(rid, 'not_found', 'no such session');
        reply(rid, 'ok');
        s['status'] = 'exited';
        s['exit_code'] = 0;
        d.broadcast({'t': 'session.event', 'session': s});
        for (final c in d.conns) {
          if (c.attached.remove(s['id'])) {
            c.reply(null, 'session.detached', {
              'id': s['id'],
              'reason': 'exited',
            });
          }
        }
      case 'session.rename':
        d.sessions[m['id']]!['name'] = m['name'];
        reply(rid, 'ok');
        d.broadcast({'t': 'session.event', 'session': d.sessions[m['id']]});
      case 'session.remove':
        if (d.sessions.remove(m['id']) == null) {
          return error(rid, 'not_found', 'no such session');
        }
        reply(rid, 'ok');
        d.broadcast({'t': 'session.removed', 'id': m['id']});
      case 'fs.list':
        reply(rid, 'fs.list', {
          'path': m['path'],
          'parent': '/',
          'entries': [
            {
              'name': 'proj',
              'path': '${m['path']}/proj',
              'dir': true,
              'git': true,
              'mtime': 1,
            },
            {
              'name': 'notes.txt',
              'path': '${m['path']}/notes.txt',
              'dir': false,
              'mtime': 1,
            },
          ],
        });
      case 'fs.search':
        reply(rid, 'fs.search', {
          'entries': [
            {
              'name': m['query'],
              'path': '/home/u/${m['query']}',
              'dir': true,
              'mtime': 1,
            },
          ],
        });
      case 'fs.recents':
        reply(rid, 'fs.recents', {
          'paths': ['/home/u/proj'],
        });
      case 'claude.conversations':
        reply(rid, 'claude.conversations', {
          'conversations': [
            {
              'session_id': 'abc',
              'first_prompt': 'hello',
              'modified_at': 1,
              'size': 10,
            },
          ],
        });
      default:
        error(rid, 'bad_request', 'unknown message type $t');
    }
  }
}
