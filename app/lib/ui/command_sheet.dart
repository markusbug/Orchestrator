import 'package:flutter/material.dart';

import '../model/fs.dart';
import '../net/host_connection.dart';
import 'widgets/common.dart';

/// What the user picked in the command sheet.
class SessionSpec {
  const SessionSpec({
    required this.cmd,
    required this.args,
    required this.name,
  });

  final String cmd;
  final List<String> args;
  final String? name;
}

/// Bottom sheet: command, optional conversation to resume, optional name.
class CommandSheet extends StatefulWidget {
  const CommandSheet({super.key, required this.conn, required this.cwd});

  final HostConnection conn;
  final String cwd;

  @override
  State<CommandSheet> createState() => _CommandSheetState();
}

const _newConversation = '__new__';

class _CommandSheetState extends State<CommandSheet> {
  late final TextEditingController _cmd;
  final _name = TextEditingController();
  List<Conversation>? _convs;
  String? _resumeId;

  @override
  void initState() {
    super.initState();
    _cmd = TextEditingController(text: widget.conn.defaultCmd);
    _loadConversations();
  }

  @override
  void dispose() {
    _cmd.dispose();
    _name.dispose();
    super.dispose();
  }

  Future<void> _loadConversations() async {
    try {
      final c = await widget.conn.conversations(widget.cwd);
      if (mounted) setState(() => _convs = c);
    } catch (_) {
      if (mounted) setState(() => _convs = const []);
    }
  }

  bool get _isClaude =>
      _cmd.text.trim().split(RegExp(r'\s+')).first.endsWith('claude');

  void _start() {
    final parts = _cmd.text
        .trim()
        .split(RegExp(r'\s+'))
        .where((p) => p.isNotEmpty)
        .toList();
    final cmd = parts.isEmpty ? widget.conn.defaultCmd : parts.first;
    final args = parts.length > 1 ? parts.sublist(1) : <String>[];
    if (_resumeId != null && _isClaude) args.addAll(['--resume', _resumeId!]);
    Navigator.pop(
      context,
      SessionSpec(
        cmd: cmd,
        args: args,
        name: _name.text.trim().isEmpty ? null : _name.text.trim(),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final bottom = MediaQuery.of(context).viewInsets.bottom;
    final t = Theme.of(context).textTheme;
    final convs = _convs;
    return Padding(
      padding: EdgeInsets.fromLTRB(16, 16, 16, 16 + bottom),
      child: SingleChildScrollView(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Text('Start session', style: t.titleLarge),
            const SizedBox(height: 4),
            Text(
              shortenPath(widget.cwd, widget.conn.home),
              style: t.bodySmall,
              overflow: TextOverflow.ellipsis,
            ),
            const SizedBox(height: 16),
            TextField(
              controller: _cmd,
              autocorrect: false,
              enableSuggestions: false,
              decoration: const InputDecoration(
                labelText: 'Command',
                border: OutlineInputBorder(),
              ),
              onChanged: (_) => setState(() {}),
            ),
            const SizedBox(height: 12),
            TextField(
              controller: _name,
              decoration: const InputDecoration(
                labelText: 'Name (optional)',
                border: OutlineInputBorder(),
              ),
            ),
            if (_isClaude) ...[
              const SizedBox(height: 16),
              Text('Conversation', style: t.labelLarge),
              if (convs == null)
                const Padding(
                  padding: EdgeInsets.all(12),
                  child: LinearProgressIndicator(),
                )
              else
                RadioGroup<String>(
                  groupValue: _resumeId ?? _newConversation,
                  onChanged: (v) => setState(
                    () => _resumeId = v == _newConversation ? null : v,
                  ),
                  child: Column(
                    children: [
                      const RadioListTile<String>(
                        value: _newConversation,
                        title: Text('New conversation'),
                        dense: true,
                      ),
                      for (final c in convs.take(8))
                        RadioListTile<String>(
                          value: c.sessionId,
                          title: Text(
                            c.firstPrompt.isEmpty
                                ? c.sessionId.substring(0, 8)
                                : c.firstPrompt,
                            maxLines: 2,
                            overflow: TextOverflow.ellipsis,
                          ),
                          subtitle: Text(relativeTime(c.modifiedAt)),
                          dense: true,
                        ),
                    ],
                  ),
                ),
            ],
            const SizedBox(height: 16),
            FilledButton.icon(
              onPressed: _start,
              icon: const Icon(Icons.play_arrow),
              label: const Text('Start'),
            ),
          ],
        ),
      ),
    );
  }
}
