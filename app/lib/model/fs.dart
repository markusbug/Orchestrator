/// Directory entry from `fs.list` / `fs.search`.
class FsEntry {
  const FsEntry({
    required this.name,
    required this.path,
    required this.dir,
    required this.git,
    required this.size,
    required this.mtime,
  });

  final String name;
  final String path;
  final bool dir;
  final bool git;
  final int size;
  final int mtime;

  factory FsEntry.fromJson(Map<String, dynamic> j) => FsEntry(
    name: j['name'] as String,
    path: j['path'] as String,
    dir: (j['dir'] as bool?) ?? false,
    git: (j['git'] as bool?) ?? false,
    size: (j['size'] as num?)?.toInt() ?? 0,
    mtime: (j['mtime'] as num?)?.toInt() ?? 0,
  );
}

/// `fs.list` reply.
class FsListing {
  const FsListing({
    required this.path,
    required this.parent,
    required this.entries,
  });

  final String path;
  final String? parent;
  final List<FsEntry> entries;

  factory FsListing.fromJson(Map<String, dynamic> j) => FsListing(
    path: j['path'] as String,
    parent: j['parent'] as String?,
    entries: ((j['entries'] as List?) ?? const [])
        .map((e) => FsEntry.fromJson(e as Map<String, dynamic>))
        .toList(),
  );
}

/// A previous Claude Code conversation in a folder.
class Conversation {
  const Conversation({
    required this.sessionId,
    required this.firstPrompt,
    required this.modifiedAt,
    required this.size,
  });

  final String sessionId;
  final String firstPrompt;
  final int modifiedAt;
  final int size;

  factory Conversation.fromJson(Map<String, dynamic> j) => Conversation(
    sessionId: j['session_id'] as String,
    firstPrompt: (j['first_prompt'] as String?) ?? '',
    modifiedAt: (j['modified_at'] as num?)?.toInt() ?? 0,
    size: (j['size'] as num?)?.toInt() ?? 0,
  );
}

/// Replaces the home prefix with `~` and keeps paths short for cards.
String shortenPath(String path, String? home) {
  var p = path;
  if (home != null && home.isNotEmpty) {
    if (p == home) return '~';
    if (p.startsWith('$home/')) p = '~${p.substring(home.length)}';
  }
  return p;
}
