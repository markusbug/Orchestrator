import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:xterm/xterm.dart';

import '../app.dart';
import '../model/fs.dart';
import '../model/session.dart';
import '../net/host_connection.dart';
import '../services/settings.dart';
import 'widgets/common.dart';

/// Full-screen terminal attached to one session, with a key bar.
class TerminalScreen extends StatefulWidget {
  const TerminalScreen({
    super.key,
    required this.hostId,
    required this.sessionId,
  });

  final String hostId;
  final String sessionId;

  /// Rough cols/rows for a session created before its terminal is laid out.
  /// The daemon resizes on attach anyway; this only sets the initial PTY size.
  static (int, int) guessSize(BuildContext context, double fontSize) {
    final mq = MediaQuery.of(context);
    final cw = fontSize * 0.6;
    final ch = fontSize * 1.2;
    final cols = ((mq.size.width - 16) / cw).floor().clamp(20, 400);
    final rows = ((mq.size.height * 0.45) / ch).floor().clamp(8, 200);
    return (cols, rows);
  }

  @override
  State<TerminalScreen> createState() => _TerminalScreenState();
}

class _TerminalScreenState extends State<TerminalScreen> {
  late Terminal _terminal;
  final _controller = TerminalController();
  TerminalBinding? _binding;
  ByteConversionSink? _utf8;
  bool _ctrl = false;
  String? _banner;
  double? _scaleStartFont;
  double _fontSize = 13;

  HostConnection get conn =>
      AppScope.read(context).connectionFor(widget.hostId);
  SessionInfo? get session => conn.sessions[widget.sessionId];

  @override
  void initState() {
    super.initState();
    _fontSize = AppScope.read(context).settings.fontSize;
    _terminal = _newTerminal();
  }

  Terminal _newTerminal() {
    final t = Terminal(maxLines: 10000);
    t.onOutput = _onOutput;
    t.onResize = _onResize;
    _utf8 = utf8.decoder.startChunkedConversion(
      StringConversionSink.fromStringSink(_TerminalSink(t)),
    );
    return t;
  }

  @override
  void dispose() {
    final b = _binding;
    if (b != null) conn.detach(b);
    _controller.dispose();
    super.dispose();
  }

  void _onResize(int cols, int rows, int pw, int ph) {
    if (_binding == null) {
      final b = TerminalBinding(
        sessionId: widget.sessionId,
        cols: cols,
        rows: rows,
        onOutput: _onRemoteOutput,
        onReattach: _onReattach,
        onDetached: _onDetached,
      );
      _binding = b;
      conn.attach(b).catchError((Object e) {
        if (mounted) setState(() => _banner = 'attach failed: $e');
      });
      return;
    }
    conn.resize(widget.sessionId, cols, rows);
  }

  void _onRemoteOutput(Uint8List data) {
    _utf8?.add(data);
  }

  void _onReattach() {
    // Start from a blank screen; the daemon replays its buffer and nudges
    // the TUI to redraw at our size.
    if (!mounted) return;
    setState(() {
      _utf8?.close();
      _terminal = _newTerminal();
      _banner = null;
    });
  }

  void _onDetached(String reason) {
    if (!mounted) return;
    switch (reason) {
      case 'exited':
        final code = session?.exitCode;
        _terminal.write(
          '\r\n\x1b[90m[process exited${code == null ? '' : ' with code $code'}]\x1b[0m\r\n',
        );
      case 'removed':
        setState(() => _banner = 'Session was removed');
      default:
        // 'closed': the connection dropped; the binding is re-attached on reconnect.
        break;
    }
  }

  void _onOutput(String data) {
    List<int> bytes = utf8.encode(data);
    if (_ctrl && data.length == 1) {
      final c = data.toUpperCase().codeUnitAt(0);
      if (c >= 0x40 && c <= 0x5f) {
        bytes = [c & 0x1f];
      }
      setState(() => _ctrl = false);
    }
    final s = session;
    if (s != null && s.isWaiting && AppScope.read(context).settings.haptics) {
      HapticFeedback.selectionClick();
    }
    conn.sendInput(widget.sessionId, bytes);
  }

  // ---- key bar ----

  void _key(KeyBarItem k) {
    final t = _terminal;
    final ctrl = _ctrl;
    switch (k) {
      case KeyBarItem.esc:
        t.keyInput(TerminalKey.escape);
      case KeyBarItem.tab:
        t.keyInput(TerminalKey.tab, ctrl: ctrl);
      case KeyBarItem.shiftTab:
        t.keyInput(TerminalKey.tab, shift: true);
      case KeyBarItem.ctrl:
        setState(() => _ctrl = !_ctrl);
        return;
      case KeyBarItem.up:
        t.keyInput(TerminalKey.arrowUp, ctrl: ctrl);
      case KeyBarItem.down:
        t.keyInput(TerminalKey.arrowDown, ctrl: ctrl);
      case KeyBarItem.left:
        t.keyInput(TerminalKey.arrowLeft, ctrl: ctrl);
      case KeyBarItem.right:
        t.keyInput(TerminalKey.arrowRight, ctrl: ctrl);
      case KeyBarItem.slash:
        t.textInput('/');
      case KeyBarItem.enter:
        t.keyInput(TerminalKey.enter);
      case KeyBarItem.ctrlC:
        t.charInput('c'.codeUnitAt(0), ctrl: true);
      case KeyBarItem.paste:
        _paste();
    }
    if (_ctrl && k != KeyBarItem.ctrl) setState(() => _ctrl = false);
  }

  Future<void> _paste() async {
    final d = await Clipboard.getData(Clipboard.kTextPlain);
    final text = d?.text;
    if (text == null || text.isEmpty) return;
    _terminal.paste(text);
  }

  Future<void> _copy() async {
    final sel = _controller.selection;
    if (sel == null) {
      toast(context, 'Long-press to select text first');
      return;
    }
    final text = _terminal.buffer.getText(sel);
    await Clipboard.setData(ClipboardData(text: text));
    _controller.clearSelection();
    if (mounted) toast(context, 'Copied');
  }

  Future<void> _kill({bool force = false}) async {
    final s = session;
    if (s == null) return;
    if (await confirm(
      context,
      title: force ? 'Force kill ${s.name}?' : 'Kill ${s.name}?',
      body: force ? 'Sends SIGKILL; the process cannot clean up.' : null,
      action: 'Kill',
      destructive: true,
    )) {
      try {
        await conn.kill(s.id, signal: force ? 'KILL' : null);
      } catch (e) {
        if (mounted) toast(context, '$e');
      }
    }
  }

  Future<void> _rename() async {
    final s = session;
    if (s == null) return;
    final name = await promptText(
      context,
      title: 'Rename session',
      initial: s.name,
    );
    if (name == null || name.trim().isEmpty) return;
    try {
      await conn.rename(s.id, name.trim());
    } catch (e) {
      if (mounted) toast(context, '$e');
    }
  }

  void _switch(int dir) {
    final list = conn.sorted.where((s) => s.isAttachable).toList();
    final i = list.indexWhere((s) => s.id == widget.sessionId);
    if (list.length < 2 || i < 0) return;
    final next = list[(i + dir + list.length) % list.length];
    Navigator.pushReplacement(
      context,
      PageRouteBuilder(
        pageBuilder: (_, _, _) =>
            TerminalScreen(hostId: widget.hostId, sessionId: next.id),
        transitionsBuilder: (_, anim, _, child) => SlideTransition(
          position: Tween(
            begin: Offset(dir.toDouble(), 0),
            end: Offset.zero,
          ).animate(anim),
          child: child,
        ),
        transitionDuration: const Duration(milliseconds: 200),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final model = AppScope.of(context);
    final settings = model.settings;
    final c = conn;
    final s = session;
    final cs = Theme.of(context).colorScheme;
    final dark = Theme.of(context).brightness == Brightness.dark;
    final theme = dark
        ? TerminalThemes.defaultTheme
        : TerminalThemes.whiteOnBlack;
    return Scaffold(
      backgroundColor: theme.background,
      appBar: AppBar(
        titleSpacing: 0,
        title: GestureDetector(
          behavior: HitTestBehavior.opaque,
          onHorizontalDragEnd: (d) {
            final v = d.primaryVelocity ?? 0;
            if (v.abs() < 200) return;
            _switch(v < 0 ? 1 : -1);
          },
          child: Row(
            children: [
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(s?.name ?? 'Session', overflow: TextOverflow.ellipsis),
                    Text(
                      s == null ? '' : shortenPath(s.cwd, c.home),
                      style: Theme.of(context).textTheme.bodySmall
                          ?.copyWith(color: cs.outline),
                      overflow: TextOverflow.ellipsis,
                    ),
                  ],
                ),
              ),
              if (s != null)
                Padding(
                  padding: const EdgeInsets.only(right: 8),
                  child: StatusChip(session: s, dense: true),
                ),
              if (!c.isOnline)
                Padding(
                  padding: const EdgeInsets.only(right: 8),
                  child: Dot(connColor(context, c.state)),
                ),
            ],
          ),
        ),
        actions: [
          IconButton(
            icon: const Icon(Icons.copy),
            tooltip: 'Copy selection',
            onPressed: _copy,
          ),
          PopupMenuButton<String>(
            onSelected: (v) {
              switch (v) {
                case 'rename':
                  _rename();
                case 'kill':
                  _kill();
                case 'force':
                  _kill(force: true);
                case 'reattach':
                  final b = _binding;
                  if (b != null) c.attach(b);
              }
            },
            itemBuilder: (_) => [
              const PopupMenuItem(value: 'rename', child: Text('Rename')),
              const PopupMenuItem(
                value: 'reattach',
                child: Text('Re-attach / redraw'),
              ),
              if (s?.isAlive ?? false)
                const PopupMenuItem(value: 'kill', child: Text('Kill')),
              if (s?.isAlive ?? false)
                const PopupMenuItem(value: 'force', child: Text('Force kill')),
            ],
          ),
        ],
      ),
      body: Column(
        children: [
          if (_banner != null)
            MaterialBanner(
              content: Text(_banner!),
              actions: [
                TextButton(
                  onPressed: () => setState(() => _banner = null),
                  child: const Text('Dismiss'),
                ),
              ],
            ),
          if (!c.isOnline)
            Container(
              width: double.infinity,
              color: connColor(context, c.state).withValues(alpha: 0.2),
              padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 4),
              child: Text(
                connLabel(c),
                style: Theme.of(context).textTheme.bodySmall,
              ),
            ),
          Expanded(
            child: GestureDetector(
              onScaleStart: (_) => _scaleStartFont = _fontSize,
              onScaleUpdate: (d) {
                if (d.pointerCount < 2) return;
                final base = _scaleStartFont ?? _fontSize;
                setState(() => _fontSize = (base * d.scale).clamp(8.0, 28.0));
              },
              onScaleEnd: (_) {
                _scaleStartFont = null;
                settings.setFontSize(_fontSize);
              },
              child: TerminalView(
                _terminal,
                controller: _controller,
                autofocus: true,
                deleteDetection: true,
                theme: theme,
                textStyle: TerminalStyle(
                  fontSize: _fontSize,
                  fontFamily: 'Menlo',
                  fontFamilyFallback: const ['Courier', 'monospace'],
                ),
                padding: const EdgeInsets.symmetric(horizontal: 4),
                backgroundOpacity: 1,
              ),
            ),
          ),
          _KeyBar(items: settings.keyBar, ctrlActive: _ctrl, onKey: _key),
        ],
      ),
    );
  }
}

/// Feeds decoded text straight into the terminal.
class _TerminalSink implements StringSink {
  _TerminalSink(this.terminal);

  final Terminal terminal;

  @override
  void write(Object? obj) => terminal.write('$obj');

  @override
  void writeAll(Iterable<Object?> objects, [String separator = '']) =>
      write(objects.join(separator));

  @override
  void writeCharCode(int charCode) => write(String.fromCharCode(charCode));

  @override
  void writeln([Object? obj = '']) => write('$obj\r\n');
}

class _KeyBar extends StatelessWidget {
  const _KeyBar({
    required this.items,
    required this.ctrlActive,
    required this.onKey,
  });

  final List<KeyBarItem> items;
  final bool ctrlActive;
  final void Function(KeyBarItem) onKey;

  @override
  Widget build(BuildContext context) {
    final cs = Theme.of(context).colorScheme;
    return Container(
      color: cs.surfaceContainerHighest,
      child: SafeArea(
        top: false,
        child: SizedBox(
          height: 44,
          child: ListView.separated(
            scrollDirection: Axis.horizontal,
            padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 6),
            itemCount: items.length,
            separatorBuilder: (_, _) => const SizedBox(width: 6),
            itemBuilder: (context, i) {
              final k = items[i];
              final active = k == KeyBarItem.ctrl && ctrlActive;
              return Material(
                color: active ? cs.primary : cs.surface,
                borderRadius: BorderRadius.circular(8),
                child: InkWell(
                  borderRadius: BorderRadius.circular(8),
                  onTap: () => onKey(k),
                  child: Container(
                    constraints: const BoxConstraints(minWidth: 44),
                    padding: const EdgeInsets.symmetric(horizontal: 10),
                    alignment: Alignment.center,
                    child: Text(
                      k.label,
                      style: TextStyle(
                        fontWeight: FontWeight.w600,
                        color: active ? cs.onPrimary : cs.onSurface,
                      ),
                    ),
                  ),
                ),
              );
            },
          ),
        ),
      ),
    );
  }
}
