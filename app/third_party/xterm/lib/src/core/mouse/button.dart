enum TerminalMouseButton {
  left(id: 0),

  middle(id: 1),

  right(id: 2),

  wheelUp(id: 64, isWheel: true),

  wheelDown(id: 64 + 1, isWheel: true),

  wheelLeft(id: 64 + 2, isWheel: true),

  wheelRight(id: 64 + 3, isWheel: true),
  ;

  /// The id that is used to report a button press or release to the terminal.
  ///
  /// Wheel buttons are reported as 64 + (0..3): bit 6 marks a wheel button
  /// and the low two bits pick up / down / left / right. Upstream 4.0.0 used
  /// 64 + (4..7), which sets bit 2, the *shift* modifier, so applications saw
  /// shift+wheel instead of a plain wheel. (Orchestrator patch.)
  final int id;

  /// Whether this button is a mouse wheel button.
  final bool isWheel;

  const TerminalMouseButton({required this.id, this.isWheel = false});
}
