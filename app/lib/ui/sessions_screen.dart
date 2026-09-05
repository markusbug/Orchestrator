import 'package:flutter/material.dart';

import '../app.dart';
import '../model/fs.dart';
import '../model/session.dart';
import '../net/host_connection.dart';
import 'new_session_screen.dart';
import 'terminal_screen.dart';
import 'widgets/common.dart';

/// All sessions on one host: waiting first, then running, then the rest.
class SessionsScreen extends StatefulWidget {
  const SessionsScreen({super.key, required this.hostId});

  final String hostId;

  @override
  State<SessionsScreen> createState() => _SessionsScreenState();
}

class _SessionsScreenState extends State<SessionsScreen> {
  HostConnection get conn =>
      AppScope.read(context).connectionFor(widget.hostId);

  Future<void> _refresh() async {
    try {
      await conn.refresh();
    } catch (e) {
      if (mounted) toast(context, 'Refresh failed: $e');
    }
  }

  Future<void> _open(SessionInfo s) async {
    if (s.isStale) {
      await _resume(s);
      return;
    }
    await Navigator.push(
      context,
      MaterialPageRoute(
        builder: (_) => TerminalScreen(hostId: widget.hostId, sessionId: s.id),
      ),
    );
  }

  Future<void> _resume(SessionInfo s) async {
    try {
      final size = TerminalScreen.guessSize(
        context,
        AppScope.read(context).settings.fontSize,
      );
      final n = await conn.resumeSession(s.id, cols: size.$1, rows: size.$2);
      if (!mounted) return;
      await Navigator.push(
        context,
        MaterialPageRoute(
          builder: (_) =>
              TerminalScreen(hostId: widget.hostId, sessionId: n.id),
        ),
      );
    } catch (e) {
      if (mounted) toast(context, 'Resume failed: $e');
    }
  }

  Future<bool> _confirmDismiss(SessionInfo s) async {
    if (s.isAlive) {
      final ok = await confirm(
        context,
        title: 'Kill ${s.name}?',
        body: 'Sends SIGTERM to the process.',
        action: 'Kill',
        destructive: true,
      );
      if (!ok) return false;
      try {
        await conn.kill(s.id);
      } catch (e) {
        if (mounted) toast(context, 'Kill failed: $e');
      }
      return false; // the card stays until the daemon reports the exit
    }
    try {
      await conn.remove(s.id);
      return true;
    } catch (e) {
      if (mounted) toast(context, 'Remove failed: $e');
      return false;
    }
  }

  Future<void> _rename(SessionInfo s) async {
    final name = await promptText(
      context,
      title: 'Rename session',
      initial: s.name,
    );
    if (name == null || name.trim().isEmpty) return;
    try {
      await conn.rename(s.id, name.trim());
    } catch (e) {
      if (mounted) toast(context, 'Rename failed: $e');
    }
  }

  @override
  Widget build(BuildContext context) {
    AppScope.of(context); // rebuild on model changes
    final c = conn;
    final sessions = c.sorted;
    return Scaffold(
      appBar: AppBar(
        title: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(c.host.name),
            Text(
              connLabel(c),
              style: Theme.of(context).textTheme.bodySmall
                  ?.copyWith(color: connColor(context, c.state)),
            ),
          ],
        ),
        actions: [
          if (c.state == ConnState.failed || c.state == ConnState.retrying)
            IconButton(
              icon: const Icon(Icons.sync),
              tooltip: 'Reconnect',
              onPressed: c.reconnectNow,
            ),
          IconButton(
            icon: const Icon(Icons.refresh),
            tooltip: 'Refresh',
            onPressed: c.isOnline ? _refresh : null,
          ),
        ],
      ),
      body: RefreshIndicator(
        onRefresh: _refresh,
        child: sessions.isEmpty
            ? ListView(
                children: [
                  const SizedBox(height: 120),
                  Center(
                    child: Text(
                      c.isOnline
                          ? 'No sessions. Tap + to start one.'
                          : connLabel(c),
                      style: Theme.of(context).textTheme.bodyLarge,
                    ),
                  ),
                ],
              )
            : ListView.builder(
                padding: const EdgeInsets.only(top: 8, bottom: 88),
                itemCount: sessions.length,
                itemBuilder: (context, i) {
                  final s = sessions[i];
                  return Dismissible(
                    key: ValueKey('${s.id}-${s.handle}'),
                    direction: DismissDirection.endToStart,
                    confirmDismiss: (_) => _confirmDismiss(s),
                    background: Container(
                      alignment: Alignment.centerRight,
                      padding: const EdgeInsets.only(right: 24),
                      color: Theme.of(context).colorScheme.error,
                      child: Icon(
                        s.isAlive
                            ? Icons.stop_circle_outlined
                            : Icons.delete_outline,
                        color: Colors.white,
                      ),
                    ),
                    child: _SessionCard(
                      session: s,
                      home: c.home,
                      onTap: () => _open(s),
                      onLongPress: () => _rename(s),
                      onResume: (s.isStale || s.isExited)
                          ? () => _resume(s)
                          : null,
                    ),
                  );
                },
              ),
      ),
      floatingActionButton: FloatingActionButton(
        onPressed: c.isOnline
            ? () => Navigator.push(
                context,
                MaterialPageRoute(
                  builder: (_) => NewSessionScreen(hostId: widget.hostId),
                ),
              )
            : null,
        tooltip: 'New session',
        backgroundColor: c.isOnline ? null : Theme.of(context).disabledColor,
        child: const Icon(Icons.add),
      ),
    );
  }
}

class _SessionCard extends StatelessWidget {
  const _SessionCard({
    required this.session,
    required this.home,
    required this.onTap,
    required this.onLongPress,
    this.onResume,
  });

  final SessionInfo session;
  final String? home;
  final VoidCallback onTap;
  final VoidCallback onLongPress;
  final VoidCallback? onResume;

  @override
  Widget build(BuildContext context) {
    final s = session;
    final t = Theme.of(context).textTheme;
    final cs = Theme.of(context).colorScheme;
    return Card(
      margin: const EdgeInsets.symmetric(horizontal: 12, vertical: 4),
      shape: s.isWaiting
          ? RoundedRectangleBorder(
              borderRadius: BorderRadius.circular(12),
              side: BorderSide(color: Colors.amber.shade700, width: 1.5),
            )
          : null,
      child: InkWell(
        onTap: onTap,
        onLongPress: onLongPress,
        borderRadius: BorderRadius.circular(12),
        child: Padding(
          padding: const EdgeInsets.fromLTRB(14, 12, 14, 12),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  Expanded(
                    child: Text(
                      s.name,
                      style: t.titleMedium?.copyWith(
                        fontWeight: FontWeight.w600,
                      ),
                      overflow: TextOverflow.ellipsis,
                    ),
                  ),
                  const SizedBox(width: 8),
                  StatusChip(session: s),
                ],
              ),
              const SizedBox(height: 2),
              Row(
                children: [
                  Icon(Icons.folder_outlined, size: 14, color: cs.outline),
                  const SizedBox(width: 4),
                  Expanded(
                    child: Text(
                      shortenPath(s.cwd, home),
                      style: t.bodySmall?.copyWith(color: cs.outline),
                      overflow: TextOverflow.ellipsis,
                    ),
                  ),
                  Text(
                    relativeTime(s.lastOutputAt),
                    style: t.bodySmall?.copyWith(color: cs.outline),
                  ),
                ],
              ),
              if (s.preview.isNotEmpty || onResume != null)
                const SizedBox(height: 8),
              if (s.preview.isNotEmpty)
                Text(
                  s.preview,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: t.bodySmall?.copyWith(
                    fontFamily: 'monospace',
                    color: cs.onSurfaceVariant,
                  ),
                ),
              if (onResume != null)
                Align(
                  alignment: Alignment.centerRight,
                  child: TextButton.icon(
                    onPressed: onResume,
                    icon: const Icon(Icons.replay, size: 18),
                    label: Text(s.isStale ? 'Resume' : 'Restart'),
                  ),
                ),
            ],
          ),
        ),
      ),
    );
  }
}
