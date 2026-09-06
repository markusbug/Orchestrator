import 'dart:async';

import 'package:flutter/material.dart';
import 'package:qr_flutter/qr_flutter.dart';

import '../theme.dart';
import 'autostart.dart';
import 'daemon.dart';
import 'models.dart';

class DesktopApp extends StatelessWidget {
  const DesktopApp({
    super.key,
    required this.daemon,
    required this.pairRequests,
  });

  final DaemonController daemon;

  /// Bumped by the tray's "Pair a device" item.
  final ValueNotifier<int> pairRequests;

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'Orchestrator',
      debugShowCheckedModeBanner: false,
      theme: orchestratorTheme(Brightness.light),
      darkTheme: orchestratorTheme(Brightness.dark),
      home: DesktopHome(daemon: daemon, pairRequests: pairRequests),
    );
  }
}

class DesktopHome extends StatefulWidget {
  const DesktopHome({
    super.key,
    required this.daemon,
    required this.pairRequests,
  });

  final DaemonController daemon;
  final ValueNotifier<int> pairRequests;

  @override
  State<DesktopHome> createState() => _DesktopHomeState();
}

class _DesktopHomeState extends State<DesktopHome> {
  PairInfo? _pair;
  Timer? _ticker;
  bool _startAtLogin = false;
  bool _busy = false;
  String? _message;

  DaemonController get d => widget.daemon;

  @override
  void initState() {
    super.initState();
    _loadStartAtLogin();
    widget.pairRequests.addListener(_onPairRequested);
  }

  @override
  void dispose() {
    widget.pairRequests.removeListener(_onPairRequested);
    _ticker?.cancel();
    super.dispose();
  }

  void _onPairRequested() {
    if (d.running) _showPair();
  }

  Future<void> _loadStartAtLogin() async {
    final app = await AppAutostart.isEnabled();
    if (mounted) setState(() => _startAtLogin = app || d.service.enabled);
  }

  Future<void> _run(Future<String?> Function() action) async {
    setState(() {
      _busy = true;
      _message = null;
    });
    final err = await action();
    if (!mounted) return;
    setState(() {
      _busy = false;
      _message = err;
    });
  }

  Future<void> _setup() => _run(() async {
    final err = await d.installService();
    if (err != null) return err;
    await AppAutostart.setEnabled(_startAtLogin);
    return null;
  });

  Future<void> _toggleStartAtLogin(bool on) async {
    setState(() => _startAtLogin = on);
    await _run(() async {
      await AppAutostart.setEnabled(on);
      return d.setStartAtLogin(on);
    });
    await _loadStartAtLogin();
  }

  Future<void> _showPair() async {
    setState(() => _busy = true);
    final p = await d.pair();
    if (!mounted) return;
    setState(() {
      _busy = false;
      _pair = p;
      _message = p == null ? d.error : null;
    });
    // The code lives five minutes; keep the countdown honest and reissue when
    // it runs out so the QR on screen is never a dead one.
    _ticker?.cancel();
    if (p != null) {
      _ticker = Timer.periodic(const Duration(seconds: 1), (_) {
        if (!mounted) return;
        if (_pair != null && _pair!.expired) {
          _showPair();
        } else {
          setState(() {});
        }
      });
    }
  }

  void _hidePair() {
    _ticker?.cancel();
    _ticker = null;
    setState(() => _pair = null);
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      body: ListenableBuilder(
        listenable: d,
        builder: (context, _) {
          return SafeArea(
            child: Center(
              child: ConstrainedBox(
                constraints: const BoxConstraints(maxWidth: 620),
                child: ListView(
                  padding: const EdgeInsets.symmetric(
                    horizontal: 24,
                    vertical: 28,
                  ),
                  children: [
                    _header(context),
                    const SizedBox(height: 20),
                    if (_message != null) ...[
                      _errorBar(context, _message!),
                      const SizedBox(height: 16),
                    ],
                    if (!d.service.installed && !d.running)
                      _setupCard(context)
                    else ...[
                      _statusCard(context),
                      const SizedBox(height: 16),
                      _pairingCard(context),
                      const SizedBox(height: 16),
                      _devicesCard(context),
                      const SizedBox(height: 16),
                      _settingsCard(context),
                    ],
                    const SizedBox(height: 24),
                  ],
                ),
              ),
            ),
          );
        },
      ),
    );
  }

  Widget _header(BuildContext context) {
    final t = Theme.of(context);
    return Row(
      children: [
        Container(
          width: 36,
          height: 36,
          decoration: BoxDecoration(
            color: seedColor,
            borderRadius: BorderRadius.circular(9),
          ),
          alignment: Alignment.center,
          child: const Text(
            '>_',
            style: TextStyle(
              color: Colors.white,
              fontWeight: FontWeight.w700,
              fontSize: 15,
            ),
          ),
        ),
        const SizedBox(width: 12),
        Text('Orchestrator', style: t.textTheme.titleLarge),
        const Spacer(),
        if (_busy)
          const SizedBox(
            width: 18,
            height: 18,
            child: CircularProgressIndicator(strokeWidth: 2),
          ),
      ],
    );
  }

  Widget _errorBar(BuildContext context, String text) {
    final t = Theme.of(context);
    return Container(
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: t.colorScheme.errorContainer,
        borderRadius: BorderRadius.circular(10),
      ),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Icon(Icons.error_outline, color: t.colorScheme.onErrorContainer),
          const SizedBox(width: 10),
          Expanded(
            child: SelectableText(
              text,
              style: TextStyle(color: t.colorScheme.onErrorContainer),
            ),
          ),
        ],
      ),
    );
  }

  Widget _card({
    required BuildContext context,
    required List<Widget> children,
  }) {
    final t = Theme.of(context);
    return Container(
      padding: const EdgeInsets.all(18),
      decoration: BoxDecoration(
        color: t.colorScheme.surfaceContainerLow,
        borderRadius: BorderRadius.circular(14),
        border: Border.all(color: t.colorScheme.outlineVariant),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: children,
      ),
    );
  }

  Widget _setupCard(BuildContext context) {
    final t = Theme.of(context);
    return _card(
      context: context,
      children: [
        Text('Set up Orchestrator', style: t.textTheme.titleMedium),
        const SizedBox(height: 8),
        Text(
          'Orchestrator runs a small background service on this machine. It '
          'keeps your Claude Code sessions alive so your phone can attach to '
          'them and leave again.',
          style: t.textTheme.bodyMedium,
        ),
        const SizedBox(height: 16),
        CheckboxListTile(
          contentPadding: EdgeInsets.zero,
          controlAffinity: ListTileControlAffinity.leading,
          value: _startAtLogin,
          onChanged: _busy
              ? null
              : (v) => setState(() => _startAtLogin = v ?? true),
          title: const Text('Start Orchestrator when I log in'),
        ),
        const SizedBox(height: 8),
        FilledButton(
          onPressed: _busy ? null : _setup,
          child: const Text('Start Orchestrator'),
        ),
      ],
    );
  }

  Widget _statusCard(BuildContext context) {
    final t = Theme.of(context);
    final st = d.status;
    final relay = st?.relay;
    return _card(
      context: context,
      children: [
        Row(
          children: [
            _dot(d.running ? Colors.green : t.colorScheme.error),
            const SizedBox(width: 10),
            Text(
              d.running ? 'Running' : 'Not running',
              style: t.textTheme.titleMedium,
            ),
            const Spacer(),
            if (!d.running)
              TextButton(
                onPressed: _busy ? null : () => _run(d.startService),
                child: const Text('Start'),
              ),
          ],
        ),
        if (st != null) ...[
          const SizedBox(height: 4),
          Text(
            '${st.host} · orchestrator ${st.version} · up ${_duration(st.uptimeSec)}',
            style: t.textTheme.bodySmall,
          ),
          const SizedBox(height: 14),
          Row(
            children: [
              _dot(
                relay == null
                    ? t.colorScheme.outline
                    : relay.connected
                    ? Colors.green
                    : Colors.orange,
              ),
              const SizedBox(width: 10),
              Expanded(
                child: Text(
                  relay == null
                      ? 'Relay off — your phone must be on the same network'
                      : relay.connected
                      ? 'Reachable from anywhere through the relay'
                      : 'Relay connecting${relay.lastError.isEmpty ? '' : ' — ${relay.lastError}'}',
                  style: t.textTheme.bodyMedium,
                ),
              ),
            ],
          ),
          const SizedBox(height: 14),
          for (final a in st.addrs)
            Padding(
              padding: const EdgeInsets.only(bottom: 4),
              child: Row(
                children: [
                  SizedBox(
                    width: 190,
                    child: Text(a.label, style: t.textTheme.bodySmall),
                  ),
                  Expanded(
                    child: SelectableText(
                      '${a.ip}:${a.port}',
                      style: t.textTheme.bodySmall?.copyWith(
                        fontFamily: 'monospace',
                      ),
                    ),
                  ),
                ],
              ),
            ),
          const SizedBox(height: 10),
          SelectableText(
            'Host id ${st.hostId}',
            style: t.textTheme.bodySmall?.copyWith(
              color: t.colorScheme.outline,
              fontFamily: 'monospace',
            ),
          ),
        ],
      ],
    );
  }

  Widget _pairingCard(BuildContext context) {
    final t = Theme.of(context);
    final p = _pair;
    if (p == null) {
      return _card(
        context: context,
        children: [
          Text('Pair a phone', style: t.textTheme.titleMedium),
          const SizedBox(height: 8),
          Text(
            d.devices.isEmpty
                ? 'No phone is paired with this machine yet.'
                : '${d.devices.length} device${d.devices.length == 1 ? '' : 's'} paired.',
            style: t.textTheme.bodyMedium,
          ),
          const SizedBox(height: 14),
          FilledButton.tonal(
            onPressed: !d.running || _busy ? null : _showPair,
            child: Text(
              d.devices.isEmpty ? 'Show pairing code' : 'Pair another device',
            ),
          ),
        ],
      );
    }
    final left = p.expiresAt.difference(DateTime.now());
    return _card(
      context: context,
      children: [
        Row(
          children: [
            Text(
              'Scan with the Orchestrator app',
              style: t.textTheme.titleMedium,
            ),
            const Spacer(),
            IconButton(
              tooltip: 'Hide',
              onPressed: _hidePair,
              icon: const Icon(Icons.close),
            ),
          ],
        ),
        const SizedBox(height: 14),
        Center(
          child: Container(
            padding: const EdgeInsets.all(14),
            decoration: BoxDecoration(
              color: Colors.white,
              borderRadius: BorderRadius.circular(12),
            ),
            child: QrImageView(
              data: p.uri,
              size: 230,
              backgroundColor: Colors.white,
              padding: EdgeInsets.zero,
            ),
          ),
        ),
        const SizedBox(height: 16),
        Row(
          children: [
            Text('Code', style: t.textTheme.bodySmall),
            const SizedBox(width: 10),
            SelectableText(
              p.code,
              style: t.textTheme.titleMedium?.copyWith(
                fontFamily: 'monospace',
                letterSpacing: 3,
              ),
            ),
            const Spacer(),
            Text(
              left.isNegative ? 'expired' : 'expires in ${_mmss(left)}',
              style: t.textTheme.bodySmall,
            ),
          ],
        ),
        const SizedBox(height: 6),
        Text(
          'Or type the code into the app after entering an address by hand.',
          style: t.textTheme.bodySmall?.copyWith(color: t.colorScheme.outline),
        ),
      ],
    );
  }

  Widget _devicesCard(BuildContext context) {
    final t = Theme.of(context);
    final devices = d.devices;
    return _card(
      context: context,
      children: [
        Text('Paired devices', style: t.textTheme.titleMedium),
        const SizedBox(height: 6),
        if (devices.isEmpty)
          Text('None yet.', style: t.textTheme.bodyMedium)
        else
          for (final dev in devices)
            ListTile(
              contentPadding: EdgeInsets.zero,
              title: Text(dev.name.isEmpty ? dev.id : dev.name),
              subtitle: Text(
                dev.lastSeenAt == null
                    ? 'never seen'
                    : 'last seen ${_ago(dev.lastSeenAt!)}',
              ),
              trailing: TextButton(
                onPressed: _busy ? null : () => _confirmRevoke(dev),
                child: const Text('Revoke'),
              ),
            ),
      ],
    );
  }

  Widget _settingsCard(BuildContext context) {
    return _card(
      context: context,
      children: [
        SwitchListTile(
          contentPadding: EdgeInsets.zero,
          value: _startAtLogin,
          onChanged: _busy ? null : _toggleStartAtLogin,
          title: const Text('Start Orchestrator when I log in'),
          subtitle: const Text(
            'Turning this off leaves running sessions alone.',
          ),
        ),
      ],
    );
  }

  Future<void> _confirmRevoke(Device dev) async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (c) => AlertDialog(
        title: Text('Revoke ${dev.name.isEmpty ? dev.id : dev.name}?'),
        content: const Text(
          'That device will no longer be able to reach this machine. It can '
          'pair again with a new code.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(c, false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(c, true),
            child: const Text('Revoke'),
          ),
        ],
      ),
    );
    if (ok == true) await _run(() => d.revoke(dev.id));
  }

  static Widget _dot(Color c) => Container(
    width: 10,
    height: 10,
    decoration: BoxDecoration(color: c, shape: BoxShape.circle),
  );

  static String _duration(int seconds) {
    if (seconds < 60) return '${seconds}s';
    if (seconds < 3600) return '${seconds ~/ 60}m';
    if (seconds < 86400) return '${seconds ~/ 3600}h';
    return '${seconds ~/ 86400}d';
  }

  static String _mmss(Duration d) =>
      '${d.inMinutes}:${(d.inSeconds % 60).toString().padLeft(2, '0')}';

  static String _ago(DateTime t) {
    final d = DateTime.now().difference(t);
    if (d.inSeconds < 60) return '${d.inSeconds}s ago';
    if (d.inMinutes < 60) return '${d.inMinutes}m ago';
    if (d.inHours < 24) return '${d.inHours}h ago';
    return '${d.inDays}d ago';
  }
}
