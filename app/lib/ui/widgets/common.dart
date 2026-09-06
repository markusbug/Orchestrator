import 'package:flutter/material.dart';

import '../../model/session.dart';
import '../../net/host_connection.dart';

/// "3m ago" style relative time for unix-millisecond timestamps.
String relativeTime(int ms) {
  if (ms == 0) return '';
  final d = DateTime.now().difference(DateTime.fromMillisecondsSinceEpoch(ms));
  if (d.inSeconds < 60) return 'just now';
  if (d.inMinutes < 60) return '${d.inMinutes}m ago';
  if (d.inHours < 24) return '${d.inHours}h ago';
  return '${d.inDays}d ago';
}

Color statusColor(BuildContext context, String status) {
  final cs = Theme.of(context).colorScheme;
  return switch (status) {
    SessionStatus.waiting => Colors.amber.shade700,
    SessionStatus.running => Colors.green.shade600,
    SessionStatus.exited => cs.outline,
    _ => cs.error,
  };
}

Color connColor(BuildContext context, ConnState s) {
  final cs = Theme.of(context).colorScheme;
  return switch (s) {
    ConnState.connected => Colors.green.shade600,
    ConnState.connecting || ConnState.retrying => Colors.amber.shade700,
    ConnState.failed => cs.error,
    ConnState.idle => cs.outline,
  };
}

String connLabel(HostConnection c) => switch (c.state) {
  ConnState.connected =>
    c.viaLabel == null ? 'online' : 'online via ${c.viaLabel}',
  ConnState.connecting => 'connecting…',
  ConnState.retrying =>
    c.error == null ? 'reconnecting…' : '${c.error}, retrying…',
  ConnState.failed => c.error ?? 'failed',
  ConnState.idle => 'offline',
};

/// Small pill showing a session status, with exit code when present.
class StatusChip extends StatelessWidget {
  const StatusChip({super.key, required this.session, this.dense = false});

  final SessionInfo session;
  final bool dense;

  @override
  Widget build(BuildContext context) {
    final color = statusColor(context, session.status);
    var label = session.status;
    if (session.isExited && session.exitCode != null) {
      label = 'exited ${session.exitCode}';
    }
    return Container(
      padding: EdgeInsets.symmetric(
        horizontal: dense ? 6 : 8,
        vertical: dense ? 1 : 2,
      ),
      decoration: BoxDecoration(
        color: color.withValues(alpha: 0.15),
        borderRadius: BorderRadius.circular(999),
        border: Border.all(color: color.withValues(alpha: 0.6)),
      ),
      child: Text(
        label,
        style: TextStyle(
          color: color,
          fontSize: dense ? 11 : 12,
          fontWeight: FontWeight.w600,
        ),
      ),
    );
  }
}

class Dot extends StatelessWidget {
  const Dot(this.color, {super.key, this.size = 10});

  final Color color;
  final double size;

  @override
  Widget build(BuildContext context) => Container(
    width: size,
    height: size,
    decoration: BoxDecoration(color: color, shape: BoxShape.circle),
  );
}

/// Yellow count badge used for waiting sessions.
class CountBadge extends StatelessWidget {
  const CountBadge(this.count, {super.key});

  final int count;

  @override
  Widget build(BuildContext context) {
    if (count <= 0) return const SizedBox.shrink();
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 7, vertical: 2),
      decoration: BoxDecoration(
        color: Colors.amber.shade700,
        borderRadius: BorderRadius.circular(999),
      ),
      child: Text(
        '$count',
        style: const TextStyle(
          color: Colors.black,
          fontWeight: FontWeight.bold,
          fontSize: 12,
        ),
      ),
    );
  }
}

Future<String?> promptText(
  BuildContext context, {
  required String title,
  String? initial,
  String? hint,
}) {
  final ctl = TextEditingController(text: initial);
  return showDialog<String>(
    context: context,
    builder: (ctx) => AlertDialog(
      title: Text(title),
      content: TextField(
        controller: ctl,
        autofocus: true,
        decoration: InputDecoration(hintText: hint),
        onSubmitted: (v) => Navigator.pop(ctx, v),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(ctx),
          child: const Text('Cancel'),
        ),
        FilledButton(
          onPressed: () => Navigator.pop(ctx, ctl.text),
          child: const Text('OK'),
        ),
      ],
    ),
  );
}

Future<bool> confirm(
  BuildContext context, {
  required String title,
  String? body,
  String action = 'OK',
  bool destructive = false,
}) async {
  final r = await showDialog<bool>(
    context: context,
    builder: (ctx) => AlertDialog(
      title: Text(title),
      content: body == null ? null : Text(body),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(ctx, false),
          child: const Text('Cancel'),
        ),
        FilledButton(
          style: destructive
              ? FilledButton.styleFrom(
                  backgroundColor: Theme.of(ctx).colorScheme.error,
                )
              : null,
          onPressed: () => Navigator.pop(ctx, true),
          child: Text(action),
        ),
      ],
    ),
  );
  return r ?? false;
}

void toast(BuildContext context, String msg) {
  ScaffoldMessenger.of(context)
    ..hideCurrentSnackBar()
    ..showSnackBar(SnackBar(content: Text(msg)));
}
