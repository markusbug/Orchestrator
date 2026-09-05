import 'dart:typed_data';

/// Binary WebSocket frame kinds (see MVP.md, protocol v1).
const int kindInput = 1; // client -> host
const int kindOutput = 2; // host -> client

const int frameHeaderLen = 5;

/// A decoded binary frame: `[kind u8][handle u32 BE][payload]`.
class Frame {
  Frame(this.kind, this.handle, this.payload);

  final int kind;
  final int handle;
  final Uint8List payload;

  static Frame? decode(Uint8List b) {
    if (b.length < frameHeaderLen) return null;
    final kind = b[0];
    if (kind != kindInput && kind != kindOutput) return null;
    final handle = ByteData.sublistView(b, 1, 5).getUint32(0, Endian.big);
    return Frame(kind, handle, Uint8List.sublistView(b, frameHeaderLen));
  }

  static Uint8List encode(int kind, int handle, List<int> payload) {
    final out = Uint8List(frameHeaderLen + payload.length);
    out[0] = kind;
    ByteData.sublistView(out, 1, 5).setUint32(0, handle, Endian.big);
    out.setRange(frameHeaderLen, out.length, payload);
    return out;
  }
}
