import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'package:connectivity_plus/connectivity_plus.dart';
import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:orchestrator/model/host.dart';
import 'package:orchestrator/net/app_model.dart';
import 'package:orchestrator/net/host_client.dart';
import 'package:orchestrator/net/host_connection.dart';
import 'package:orchestrator/services/host_store.dart';
import 'package:orchestrator/services/keys.dart';
import 'package:orchestrator/services/settings.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'fake_daemon.dart';

Future<void> waitFor(
  bool Function() cond, {
  Duration timeout = const Duration(seconds: 10),
}) async {
  final end = DateTime.now().add(timeout);
  while (!cond()) {
    if (DateTime.now().isAfter(end)) fail('timed out waiting');
    await Future<void>.delayed(const Duration(milliseconds: 20));
  }
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  // flutter_test installs a mock HttpClient that answers 400 to everything;
  // these tests talk to a real local TLS server.
  HttpOverrides.global = null;
  late FakeDaemon daemon;

  setUp(() async {
    daemon = await FakeDaemon.start();
    SharedPreferences.setMockInitialValues({});
  });

  tearDown(() => daemon.close());

  group('HostClient', () {
    test('rejects a certificate that does not match the pin', () async {
      await expectLater(
        HostClient.connect(
          ip: '127.0.0.1',
          port: daemon.port,
          fingerprint: 'sha256:${'0' * 64}',
        ),
        throwsA(isA<FingerprintMismatch>()),
      );
    });

    test('pairs, then authenticates with the stored key', () async {
      daemon.pairCode = '123456';
      final key = await DeviceKey.generate();
      final c = await HostClient.connect(
        ip: '127.0.0.1',
        port: daemon.port,
        fingerprint: daemon.fingerprint,
      );
      final h = await c.hello(name: 'phone');
      expect(h.authNeeded, isTrue);
      expect(h.host, 'fake');
      final id = await c.pair(code: '123456', key: key, name: 'phone');
      expect(id, 'dev-1');
      expect(daemon.pairCode, isNull);
      final info = await c.request('host.info');
      expect(info['home'], '/home/u');
      await c.close();

      // Key survives serialisation, and the signature verifies server-side.
      final restored = DeviceKey.deserialize(await key.serialize());
      final c2 = await HostClient.connect(
        ip: '127.0.0.1',
        port: daemon.port,
        fingerprint: daemon.fingerprint,
      );
      final h2 = await c2.hello(name: 'phone', deviceId: id);
      await c2.auth(hello: h2, deviceId: id, key: restored);
      expect((await c2.request('ping'))['t'], 'pong');
      await c2.close();
    });

    test('wrong key fails auth with unauthorized', () async {
      daemon.devices['dev-x'] = (await DeviceKey.generate()).publicKey;
      final other = await DeviceKey.generate();
      final c = await HostClient.connect(
        ip: '127.0.0.1',
        port: daemon.port,
        fingerprint: daemon.fingerprint,
      );
      final h = await c.hello(name: 'phone', deviceId: 'dev-x');
      await expectLater(
        c.auth(hello: h, deviceId: 'dev-x', key: other),
        throwsA(
          isA<ProtocolException>().having(
            (e) => e.isUnauthorized,
            'unauthorized',
            isTrue,
          ),
        ),
      );
      await c.done.timeout(const Duration(seconds: 5));
    });

    test('routes binary frames and events', () async {
      daemon.pairCode = '1';
      final key = await DeviceKey.generate();
      final s = daemon.addSession(name: 'a');
      final c = await HostClient.connect(
        ip: '127.0.0.1',
        port: daemon.port,
        fingerprint: daemon.fingerprint,
      );
      await c.hello(name: 'p');
      await c.pair(code: '1', key: key, name: 'p');
      final out = <int>[];
      c.onOutput = (handle, data) {
        expect(handle, s['handle']);
        out.addAll(data);
      };
      final events = <Map<String, dynamic>>[];
      c.events.listen(events.add);
      daemon.output(s['id'] as String, utf8.encode('before'));
      await c.request('session.attach', {
        'id': s['id'],
        'cols': 80,
        'rows': 24,
      });
      await waitFor(() => out.length == 6);
      expect(utf8.decode(out), 'before');
      daemon.output(s['id'] as String, utf8.encode('+live'));
      await waitFor(() => out.length == 11);
      c.sendInput(s['handle'] as int, utf8.encode('ls\r'));
      await waitFor(() => daemon.conns.single.inputs.isNotEmpty);
      expect(utf8.decode(daemon.conns.single.inputs.single.$2), 'ls\r');
      daemon.setStatus(s['id'] as String, 'waiting');
      await waitFor(() => events.isNotEmpty);
      expect(events.single['t'], 'session.event');
      await c.close();
    });
  });

  group('HostConnection', () {
    late KeyService keys;
    late HostRecord host;

    setUp(() async {
      keys = KeyService(MemorySecretStore());
      final key = await DeviceKey.generate();
      await keys.save('h1', key);
      daemon.devices['dev-1'] = key.publicKey;
      host = HostRecord(
        id: 'h1',
        name: 'fake',
        hostname: 'fake',
        addrs: [const HostAddr('127.0.0.1', 'lan')],
        port: daemon.port,
        fingerprint: daemon.fingerprint,
        deviceId: 'dev-1',
        createdAt: DateTime.now(),
      );
    });

    test(
      'connects, loads sessions, tracks events, and reconnects with re-attach',
      () async {
        final s = daemon.addSession(name: 'first');
        daemon.output(s['id'] as String, utf8.encode('hello'));
        final waiting = <String>[];
        final conn = HostConnection(
          host,
          keys: keys,
          deviceName: () => 'phone',
          onWaiting: (_, s) => waiting.add(s.id),
        );
        conn.start();
        await waitFor(() => conn.isOnline);
        expect(conn.info?.home, '/home/u');
        expect(conn.sessions.keys, [s['id']]);
        expect(conn.host.lastGoodAddr, '127.0.0.1');

        // Attach a terminal and see the replay.
        final out = StringBuffer();
        var reattaches = 0;
        final detached = <String>[];
        final b = TerminalBinding(
          sessionId: s['id'] as String,
          cols: 100,
          rows: 30,
          onOutput: (d) => out.write(utf8.decode(d)),
          onReattach: () => reattaches++,
          onDetached: detached.add,
        );
        await conn.attach(b);
        await waitFor(() => out.toString() == 'hello');
        expect(reattaches, 1);
        expect(daemon.sessions[s['id']]!['cols'], 100);

        conn.sendInput(s['id'] as String, utf8.encode('x'));
        await waitFor(() => daemon.conns.any((c) => c.inputs.isNotEmpty));

        daemon.setStatus(s['id'] as String, 'waiting');
        await waitFor(() => conn.waitingCount == 1);
        expect(waiting, [s['id']]);
        expect(conn.sorted.first.isWaiting, isTrue);

        // Drop the socket server-side; the client reconnects and re-attaches.
        final before = daemon.connectionsAccepted;
        for (final c in List.of(daemon.conns)) {
          await c.close();
        }
        await waitFor(() => !conn.isOnline);
        await waitFor(
          () => conn.isOnline && daemon.connectionsAccepted > before,
          timeout: const Duration(seconds: 15),
        );
        await waitFor(() => reattaches == 2);
        await waitFor(() => out.toString() == 'hellohello');

        // Exit is reported to the binding, removal drops the session.
        await conn.kill(s['id'] as String);
        await waitFor(() => detached.contains('exited'));
        expect(conn.sessions[s['id']]!.isExited, isTrue);
        await conn.remove(s['id'] as String);
        await waitFor(() => conn.sessions.isEmpty);
        expect(detached.last, 'removed');

        // fs + claude helpers
        final l = await conn.fsList('/home/u');
        expect(l.entries.where((e) => e.dir).single.git, isTrue);
        expect((await conn.fsSearch('proj')).single.path, '/home/u/proj');
        expect(await conn.fsRecents(), ['/home/u/proj']);
        expect(
          (await conn.conversations('/home/u')).single.firstPrompt,
          'hello',
        );

        final created = await conn.createSession(
          cwd: '/home/u/proj',
          cols: 80,
          rows: 24,
          args: ['--resume', 'abc'],
        );
        expect(conn.sessions.containsKey(created.id), isTrue);
        final resumed = await conn.resumeSession(
          created.id,
          cols: 80,
          rows: 24,
        );
        expect(conn.sessions.containsKey(created.id), isFalse);
        expect(conn.sessions.containsKey(resumed.id), isTrue);

        conn.dispose();
      },
    );

    test('a revoked device stops retrying', () async {
      daemon.revoked.add('dev-1');
      final conn = HostConnection(host, keys: keys, deviceName: () => 'phone');
      conn.start();
      await waitFor(() => conn.state == ConnState.failed);
      expect(conn.error, contains('revoked'));
      final accepted = daemon.connectionsAccepted;
      await Future<void>.delayed(const Duration(milliseconds: 500));
      expect(daemon.connectionsAccepted, accepted);
      daemon.revoked.clear();
      conn.reconnectNow();
      await waitFor(() => conn.isOnline);
      conn.dispose();
    });

    test('connects through a relay address with its own port', () async {
      // The host's own port is dead; the relay-kind address names the port
      // that works (in production it is 443 on the relay).
      daemon.relayAddr = {
        'ip': 'localhost',
        'kind': 'relay',
        'port': daemon.port,
      };
      final rec = HostRecord(
        id: 'h1',
        name: 'fake',
        hostname: 'fake',
        addrs: [
          const HostAddr('127.0.0.1', 'lan'),
          HostAddr('localhost', 'relay', port: daemon.port),
        ],
        port: 1,
        fingerprint: daemon.fingerprint,
        deviceId: 'dev-1',
        createdAt: DateTime.now(),
      );
      final conn = HostConnection(rec, keys: keys, deviceName: () => 'phone');
      conn.start();
      await waitFor(() => conn.state == ConnState.connected);
      expect(conn.connectedVia, 'localhost');
      expect(conn.viaLabel, 'relay');
      // A relay session is never remembered as the preferred address.
      expect(conn.host.lastGoodAddr, isNull);
      // host.info re-advertises the relay address, so it survives the merge.
      expect(conn.host.relayAddr?.port, daemon.port);
      // The daemon's own port replaces the dead one the record carried.
      expect(conn.host.port, daemon.port);
      conn.dispose();
    });

    test(
      'a relay that closes before TLS is not a certificate change',
      () async {
        // A relay whose host is offline closes the socket without a byte of
        // TLS. That must read as unreachable (retry), not as a changed pin.
        final closer = await ServerSocket.bind('127.0.0.1', 0);
        closer.listen((s) => s.destroy());
        addTearDown(closer.close);
        final rec = HostRecord(
          id: 'h1',
          name: 'x',
          hostname: 'x',
          addrs: [HostAddr('127.0.0.1', 'relay', port: closer.port)],
          port: 1,
          fingerprint: daemon.fingerprint,
          deviceId: 'dev-1',
          createdAt: DateTime.now(),
        );
        final conn = HostConnection(rec, keys: keys, deviceName: () => 'phone');
        conn.start();
        await waitFor(() => conn.state == ConnState.retrying);
        expect(conn.error, isNot(contains('certificate')));
        conn.dispose();
      },
    );

    test('a dead LAN entry does not delay a live one', () async {
      final rec = HostRecord(
        id: 'h1',
        name: 'fake',
        hostname: 'fake',
        addrs: [
          // Black-hole address: connect hangs until the 6 s timeout.
          const HostAddr('10.255.255.1', 'lan'),
          const HostAddr('127.0.0.1', 'lan'),
        ],
        port: daemon.port,
        fingerprint: daemon.fingerprint,
        deviceId: 'dev-1',
        createdAt: DateTime.now(),
      );
      final conn = HostConnection(rec, keys: keys, deviceName: () => 'phone');
      final start = DateTime.now();
      conn.start();
      await waitFor(() => conn.isOnline);
      expect(DateTime.now().difference(start).inSeconds, lessThan(5));
      expect(conn.connectedVia, '127.0.0.1');
      conn.dispose();
    });

    test('an unreachable host keeps retrying with backoff', () async {
      final dead = HostRecord(
        id: 'h1',
        name: 'x',
        hostname: 'x',
        addrs: [const HostAddr('127.0.0.1', 'lan')],
        port: 1, // nothing listens here
        fingerprint: daemon.fingerprint,
        deviceId: 'dev-1',
        createdAt: DateTime.now(),
      );
      final conn = HostConnection(dead, keys: keys, deviceName: () => 'phone');
      conn.start();
      await waitFor(() => conn.state == ConnState.retrying);
      expect(conn.error, 'host unreachable');
      conn.dispose();
      expect(conn.state, ConnState.idle);
    });
  });

  group('AppModel', () {
    test('pairs from a payload, persists, forgets', () async {
      daemon.pairCode = '654321';
      final store = MemoryHostStore();
      final net = StreamController<List<ConnectivityResult>>.broadcast();
      final model = AppModel(
        settings: Settings(),
        keys: KeyService(MemorySecretStore()),
        hostStore: store,
        connectivity: net.stream,
      );
      await model.init();
      final rec = await model.pair(
        PairPayload(
          host: 'fake',
          addrs: [
            const HostAddr('10.255.255.1', 'lan'),
            const HostAddr('127.0.0.1', 'lan'),
          ],
          port: daemon.port,
          fingerprint: daemon.fingerprint,
          code: '654321',
        ),
      );
      expect(rec.deviceId, 'dev-1');
      expect(rec.name, 'fake');
      expect(rec.lastGoodAddr, '127.0.0.1');
      expect(store.hosts.single.id, rec.id);
      final conn = model.connectionFor(rec.id);
      await waitFor(() => conn.isOnline);

      // Lifecycle and network events trigger a probe, not a disconnect.
      model.didChangeAppLifecycleState(AppLifecycleState.resumed);
      net.add([ConnectivityResult.wifi]);
      await Future<void>.delayed(const Duration(milliseconds: 200));
      expect(conn.isOnline, isTrue);

      await model.renameHost(rec.id, 'Laptop');
      expect(store.hosts.single.name, 'Laptop');
      await model.forgetHost(rec.id);
      expect(model.hosts, isEmpty);
      expect(await model.keys.load(rec.id), isNull);
      model.dispose();
      await net.close();
    }, timeout: const Timeout(Duration(seconds: 30)));

    test('pairing with a wrong code fails cleanly', () async {
      daemon.pairCode = '111111';
      final model = AppModel(
        settings: Settings(),
        keys: KeyService(MemorySecretStore()),
        hostStore: MemoryHostStore(),
        connectivity: const Stream.empty(),
      );
      await model.init();
      await expectLater(
        model.pair(
          PairPayload(
            host: 'f',
            addrs: [const HostAddr('127.0.0.1', 'lan')],
            port: daemon.port,
            fingerprint: daemon.fingerprint,
            code: '000000',
          ),
        ),
        throwsA(isA<ProtocolException>()),
      );
      expect(model.hosts, isEmpty);
      model.dispose();
    });
  });

  test('utf8 output split across frames decodes correctly', () {
    final sink = StringBuffer();
    final dec = utf8.decoder.startChunkedConversion(
      StringConversionSink.fromStringSink(sink),
    );
    final bytes = utf8.encode('héllo ✓');
    dec.add(Uint8List.fromList(bytes.sublist(0, 2)));
    dec.add(Uint8List.fromList(bytes.sublist(2)));
    expect(sink.toString(), 'héllo ✓');
  });
}
