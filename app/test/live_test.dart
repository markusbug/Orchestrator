// End-to-end test against a real daemon. Skipped unless ORCH_LIVE_PORT,
// ORCH_LIVE_CODE and ORCH_LIVE_FP are set, e.g. from `orchestrator pair`.
//
//   ORCH_LIVE_PORT=7391 ORCH_LIVE_CODE=123456 ORCH_LIVE_FP=sha256:... \
//     flutter test test/live_test.dart
import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:orchestrator/model/host.dart';
import 'package:orchestrator/net/app_model.dart';
import 'package:orchestrator/net/host_connection.dart';
import 'package:orchestrator/services/host_store.dart';
import 'package:orchestrator/services/keys.dart';
import 'package:orchestrator/services/settings.dart';
import 'package:shared_preferences/shared_preferences.dart';

Future<void> waitFor(
  bool Function() cond, {
  Duration timeout = const Duration(seconds: 15),
  String Function()? what,
}) async {
  final end = DateTime.now().add(timeout);
  while (!cond()) {
    if (DateTime.now().isAfter(end)) {
      fail('timed out waiting${what == null ? '' : ': ${what()}'}');
    }
    await Future<void>.delayed(const Duration(milliseconds: 25));
  }
}

void main() {
  final env = Platform.environment;
  final port = int.tryParse(env['ORCH_LIVE_PORT'] ?? '');
  final code = env['ORCH_LIVE_CODE'];
  final fp = env['ORCH_LIVE_FP'];
  final ip = env['ORCH_LIVE_IP'] ?? '127.0.0.1';
  final enabled = port != null && code != null && fp != null;

  TestWidgetsFlutterBinding.ensureInitialized();
  HttpOverrides.global = null;

  test(
    'pair, create a bash session, type, resize, kill, resume, remove',
    () async {
      SharedPreferences.setMockInitialValues({});
      final store = MemoryHostStore();
      final model = AppModel(
        settings: Settings(),
        keys: KeyService(MemorySecretStore()),
        hostStore: store,
        connectivity: const Stream.empty(),
      );
      await model.init();
      final rec = await model.pair(
        PairPayload(
          host: 'live',
          addrs: [HostAddr(ip, 'lan')],
          port: port!,
          fingerprint: fp!,
          code: code!,
        ),
      );
      expect(rec.deviceId, isNotEmpty);
      final conn = model.connectionFor(rec.id);
      await waitFor(
        () => conn.isOnline,
        what: () => '${conn.state} ${conn.error}',
      );
      expect(conn.info!.home, isNotEmpty);
      expect(conn.host.hostname, conn.info!.host);

      final home = conn.info!.home;
      final listing = await conn.fsList(home);
      expect(listing.path, home);
      expect(await conn.conversations(home), isA<List>());

      final s = await conn.createSession(
        cwd: home,
        cmd: 'bash',
        args: ['--norc', '--noprofile'],
        name: 'live-test',
        cols: 80,
        rows: 24,
      );
      expect(s.isRunning, isTrue);
      expect(conn.sessions.containsKey(s.id), isTrue);

      final out = StringBuffer();
      final detached = <String>[];
      final b = TerminalBinding(
        sessionId: s.id,
        cols: 100,
        rows: 30,
        onOutput: (d) => out.write(utf8.decode(d, allowMalformed: true)),
        onReattach: () {},
        onDetached: detached.add,
      );
      await conn.attach(b);
      // `stty size` reads the PTY, so it reflects the attach size right away.
      conn.sendInput(s.id, utf8.encode('stty size\n'));
      await waitFor(
        () => out.toString().contains('30 100'),
        what: () => out.toString(),
      );

      await conn.resize(s.id, 120, 40);
      conn.sendInput(s.id, utf8.encode('stty size\n'));
      await waitFor(
        () => out.toString().contains('40 120'),
        what: () => out.toString(),
      );

      await conn.rename(s.id, 'renamed');
      await waitFor(() => conn.sessions[s.id]?.name == 'renamed');

      // A second attach of the same session must replay the buffer.
      final replay = StringBuffer();
      final b2 = TerminalBinding(
        sessionId: s.id,
        cols: 100,
        rows: 30,
        onOutput: (d) => replay.write(utf8.decode(d, allowMalformed: true)),
        onReattach: () {},
        onDetached: (_) {},
      );
      await conn.attach(b2); // replaces b on this socket
      await waitFor(
        () =>
            replay.toString().contains('30 100') &&
            replay.toString().contains('40 120'),
        what: () => replay.toString(),
      );

      await conn.kill(s.id, signal: 'KILL'); // interactive bash ignores TERM
      await waitFor(
        () => conn.sessions[s.id]?.isExited ?? false,
        what: () => '${conn.sessions[s.id]?.status}',
      );

      final r = await conn.resumeSession(s.id, cols: 80, rows: 24);
      expect(r.id, isNot(s.id));
      expect(r.cwd, home);
      expect(r.cmd, 'bash');
      await waitFor(
        () =>
            conn.sessions.containsKey(r.id) && !conn.sessions.containsKey(s.id),
      );
      await conn.kill(r.id, signal: 'KILL');
      await waitFor(() => conn.sessions[r.id]?.isExited ?? false);
      await conn.remove(r.id);
      await waitFor(() => !conn.sessions.containsKey(r.id));

      expect(await conn.fsRecents(), contains(home));
      model.dispose();
    },
    skip: enabled ? false : 'set ORCH_LIVE_PORT, ORCH_LIVE_CODE, ORCH_LIVE_FP',
    timeout: const Timeout(Duration(minutes: 2)),
  );
}
