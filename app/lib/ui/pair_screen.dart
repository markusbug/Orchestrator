import 'dart:io';

import 'package:flutter/material.dart';
import 'package:mobile_scanner/mobile_scanner.dart';

import '../app.dart';
import '../model/host.dart';
import '../services/pair_link.dart';

/// Scan the QR code printed by `orchestrator pair`, or type the details.
class PairScreen extends StatefulWidget {
  const PairScreen({super.key});

  @override
  State<PairScreen> createState() => _PairScreenState();
}

class _PairScreenState extends State<PairScreen> {
  final _scanner = MobileScannerController(
    formats: const [BarcodeFormat.qrCode],
  );
  bool _busy = false;
  bool _handled = false;
  String? _error;

  @override
  void dispose() {
    _scanner.dispose();
    super.dispose();
  }

  Future<void> _onDetect(BarcodeCapture capture) async {
    if (_handled || _busy) return;
    for (final b in capture.barcodes) {
      final raw = b.rawValue;
      if (raw == null) continue;
      final p = parsePairLink(raw);
      if (p == null) continue;
      _handled = true;
      await _pair(p);
      return;
    }
  }

  Future<void> _pair(PairPayload p) async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      if (p.isExpired) {
        throw StateError(
          'this pairing code has expired; run `orchestrator pair` again',
        );
      }
      final rec = await AppScope.read(context).pair(p);
      if (mounted) Navigator.pop(context, rec);
    } catch (e) {
      setState(() {
        _busy = false;
        _handled = false;
        _error = e.toString().replaceFirst(
          RegExp(r'^\w+(Error|Exception): '),
          '',
        );
      });
    }
  }

  Future<void> _manual() async {
    final p = await showModalBottomSheet<PairPayload>(
      context: context,
      isScrollControlled: true,
      builder: (_) => const _ManualSheet(),
    );
    if (p != null) await _pair(p);
  }

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    return Scaffold(
      appBar: AppBar(title: const Text('Pair a host')),
      body: Column(
        children: [
          Expanded(
            child: Stack(
              fit: StackFit.expand,
              children: [
                MobileScanner(
                  controller: _scanner,
                  onDetect: _onDetect,
                  errorBuilder: (context, error) => Center(
                    child: Padding(
                      padding: const EdgeInsets.all(24),
                      child: Text(
                        'Camera unavailable (${error.errorCode.name}). Use manual entry below.',
                        textAlign: TextAlign.center,
                      ),
                    ),
                  ),
                ),
                Center(
                  child: Container(
                    width: 240,
                    height: 240,
                    decoration: BoxDecoration(
                      border: Border.all(color: Colors.white70, width: 2),
                      borderRadius: BorderRadius.circular(16),
                    ),
                  ),
                ),
                if (_busy)
                  Container(
                    color: Colors.black54,
                    child: const Center(child: CircularProgressIndicator()),
                  ),
              ],
            ),
          ),
          SafeArea(
            top: false,
            child: Padding(
              padding: const EdgeInsets.all(16),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  if (_error != null)
                    Padding(
                      padding: const EdgeInsets.only(bottom: 12),
                      child: Text(_error!, style: TextStyle(color: cs.error)),
                    ),
                  Text(
                    'Run `orchestrator pair` on your computer and point the camera at the QR code.',
                    style: Theme.of(context).textTheme.bodyMedium,
                  ),
                  const SizedBox(height: 12),
                  OutlinedButton.icon(
                    onPressed: _busy ? null : _manual,
                    icon: const Icon(Icons.keyboard_outlined),
                    label: const Text('Enter manually'),
                  ),
                ],
              ),
            ),
          ),
        ],
      ),
    );
  }
}

/// Turns what the user typed into an address. IPs are LAN (or Tailscale for
/// the CGNAT range); anything else is a DNS name and so a relay address,
/// which carries its own port (443 unless given as `name:port`). An IP may
/// also carry `:port`, which then overrides [hostPort] for that address.
HostAddr parseManualAddr(String text, int hostPort) {
  var s = text.trim();
  int? port;
  final m = RegExp(r'^(.*):(\d{1,5})$').firstMatch(s);
  if (m != null && !s.contains(']') && ':'.allMatches(s).length == 1) {
    s = m.group(1)!;
    port = int.parse(m.group(2)!);
  }
  final ip = InternetAddress.tryParse(s);
  if (ip == null) {
    return HostAddr(s.toLowerCase(), 'relay', port: port ?? 443);
  }
  final kind = s.startsWith('100.') && _cgnat(ip) ? 'tailscale' : 'lan';
  return HostAddr(s, kind, port: port);
}

bool _cgnat(InternetAddress ip) {
  if (ip.type != InternetAddressType.IPv4) return false;
  final b = ip.rawAddress;
  return b[0] == 100 && (b[1] & 0xC0) == 64;
}

class _ManualSheet extends StatefulWidget {
  const _ManualSheet();

  @override
  State<_ManualSheet> createState() => _ManualSheetState();
}

class _ManualSheetState extends State<_ManualSheet> {
  final _addr = TextEditingController();
  final _port = TextEditingController(text: '7391');
  final _code = TextEditingController();
  final _fp = TextEditingController();
  final _form = GlobalKey<FormState>();

  @override
  void dispose() {
    _addr.dispose();
    _port.dispose();
    _code.dispose();
    _fp.dispose();
    super.dispose();
  }

  void _submit() {
    if (!_form.currentState!.validate()) return;
    final addr = parseManualAddr(_addr.text, int.parse(_port.text.trim()));
    Navigator.pop(
      context,
      PairPayload(
        host: addr.ip,
        addrs: [addr],
        port: int.parse(_port.text.trim()),
        fingerprint: normalizeFingerprint(_fp.text),
        code: _code.text.trim(),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final bottom = MediaQuery.of(context).viewInsets.bottom;
    return Padding(
      padding: EdgeInsets.fromLTRB(16, 16, 16, 16 + bottom),
      child: Form(
        key: _form,
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Text(
              'Manual pairing',
              style: Theme.of(context).textTheme.titleLarge,
            ),
            const SizedBox(height: 12),
            TextFormField(
              controller: _addr,
              decoration: const InputDecoration(
                labelText: 'Address',
                hintText: '192.168.1.20 or <id>.relay.example:443',
                helperText:
                    'An IP on your network, or the relay name '
                    '(with its port) that `orchestrator pair` prints.',
              ),
              keyboardType: TextInputType.url,
              autocorrect: false,
              validator: (v) =>
                  (v == null || v.trim().isEmpty) ? 'required' : null,
            ),
            const SizedBox(height: 8),
            TextFormField(
              controller: _port,
              decoration: const InputDecoration(
                labelText: 'Host port',
                helperText: 'The daemon\'s own port (7391 unless changed).',
              ),
              keyboardType: TextInputType.number,
              validator: (v) =>
                  int.tryParse((v ?? '').trim()) == null ? 'number' : null,
            ),
            const SizedBox(height: 8),
            TextFormField(
              controller: _code,
              decoration: const InputDecoration(
                labelText: 'Pairing code',
                hintText: '6 digits',
              ),
              keyboardType: TextInputType.number,
              validator: (v) =>
                  (v == null || v.trim().length != 6) ? '6 digits' : null,
            ),
            const SizedBox(height: 8),
            TextFormField(
              controller: _fp,
              decoration: const InputDecoration(
                labelText: 'Fingerprint',
                hintText: 'sha256:…',
              ),
              autocorrect: false,
              validator: (v) => normalizeFingerprint(v ?? '').length != 7 + 64
                  ? '64 hex characters'
                  : null,
            ),
            const SizedBox(height: 16),
            FilledButton(onPressed: _submit, child: const Text('Pair')),
          ],
        ),
      ),
    );
  }
}
