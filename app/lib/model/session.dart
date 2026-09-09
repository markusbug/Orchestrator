/// Session status values, mirroring the daemon.
class SessionStatus {
  static const running = 'running';
  static const waiting = 'waiting';
  static const exited = 'exited';
  static const stale = 'stale';
}

/// A session as reported by the daemon.
class SessionInfo {
  const SessionInfo({
    required this.id,
    required this.handle,
    required this.name,
    required this.cwd,
    required this.cmd,
    required this.args,
    required this.pid,
    required this.status,
    this.waitReason = '',
    required this.exitCode,
    required this.claudeSessionId,
    required this.createdAt,
    required this.lastOutputAt,
    required this.cols,
    required this.rows,
    required this.preview,
  });

  final String id;
  final int handle;
  final String name;
  final String cwd;
  final String cmd;
  final List<String> args;
  final int pid;
  final String status;

  /// Why a waiting session waits: `idle` (turn finished, needs a prompt) or
  /// `input` (a permission or question is on screen). Empty otherwise.
  final String waitReason;
  final int? exitCode;
  final String? claudeSessionId;
  final int createdAt; // unix ms
  final int lastOutputAt; // unix ms
  final int cols;
  final int rows;
  final String preview;

  factory SessionInfo.fromJson(Map<String, dynamic> j) => SessionInfo(
    id: j['id'] as String,
    handle: (j['handle'] as num?)?.toInt() ?? 0,
    name: (j['name'] as String?) ?? '',
    cwd: (j['cwd'] as String?) ?? '',
    cmd: (j['cmd'] as String?) ?? '',
    args: ((j['args'] as List?) ?? const []).cast<String>(),
    pid: (j['pid'] as num?)?.toInt() ?? 0,
    status: (j['status'] as String?) ?? SessionStatus.exited,
    waitReason: (j['wait_reason'] as String?) ?? '',
    exitCode: (j['exit_code'] as num?)?.toInt(),
    claudeSessionId: j['claude_session_id'] as String?,
    createdAt: (j['created_at'] as num?)?.toInt() ?? 0,
    lastOutputAt: (j['last_output_at'] as num?)?.toInt() ?? 0,
    cols: (j['cols'] as num?)?.toInt() ?? 0,
    rows: (j['rows'] as num?)?.toInt() ?? 0,
    preview: (j['preview'] as String?) ?? '',
  );

  bool get isRunning => status == SessionStatus.running;
  bool get isWaiting => status == SessionStatus.waiting;
  bool get isExited => status == SessionStatus.exited;
  bool get isStale => status == SessionStatus.stale;

  /// A permission or a question is blocking Claude until someone answers.
  bool get needsAnswer => isWaiting && waitReason == 'input';

  /// Alive means the PTY is open (the process may or may not be done).
  bool get isAlive => isRunning || isWaiting;

  /// Attachable sessions have a buffer to show: anything but stale.
  bool get isAttachable => !isStale;

  /// Sort rank: waiting first, then running, then exited/stale.
  int get rank => switch (status) {
    SessionStatus.waiting => 0,
    SessionStatus.running => 1,
    SessionStatus.exited => 2,
    _ => 3,
  };

  String get commandLine =>
      [cmd, ...args].where((s) => s.isNotEmpty).join(' ').trim();
}

/// Sorted for the sessions screen: waiting, running by activity, then the rest.
List<SessionInfo> sortSessions(Iterable<SessionInfo> sessions) {
  final list = sessions.toList();
  list.sort((a, b) {
    final r = a.rank.compareTo(b.rank);
    if (r != 0) return r;
    final t = b.lastOutputAt.compareTo(a.lastOutputAt);
    if (t != 0) return t;
    return b.createdAt.compareTo(a.createdAt);
  });
  return list;
}
