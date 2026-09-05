// Debug client for the Orchestrator daemon. Only reachable with --debug from
// loopback, where the server skips authentication.
(function () {
  const $ = (s) => document.querySelector(s);
  const statusEl = $('#status');
  let ws, rid = 0, pending = new Map();
  let sessions = [];
  let attached = null; // {id, handle}
  let term, fit;

  function connect() {
    const url = (location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/ws';
    ws = new WebSocket(url);
    ws.binaryType = 'arraybuffer';
    ws.onopen = () => {
      statusEl.textContent = 'handshake…';
      const nonce = crypto.getRandomValues(new Uint8Array(32));
      send('hello', { proto: 1, name: 'debug-browser', client_nonce: b64(nonce) }).then((h) => {
        if (h.auth_needed) { statusEl.textContent = 'auth required (not loopback or --debug missing)'; return; }
        statusEl.textContent = 'connected';
        send('host.info').then((info) => { $('#hostinfo').textContent = info.host + ' · ' + info.version + ' · ' + info.fingerprint.slice(0, 23) + '…'; if (!$('input[name=cwd]').value) $('input[name=cwd]').value = info.home; });
        refresh();
      }).catch((e) => statusEl.textContent = 'error: ' + e.message);
    };
    ws.onclose = () => { statusEl.textContent = 'disconnected, retrying…'; setTimeout(connect, 1500); };
    ws.onmessage = (ev) => {
      if (ev.data instanceof ArrayBuffer) { onBinary(new Uint8Array(ev.data)); return; }
      const m = JSON.parse(ev.data);
      if (m.rid && pending.has(m.rid)) {
        const p = pending.get(m.rid); pending.delete(m.rid);
        if (m.t === 'error') p.reject(new Error(m.code + ': ' + m.message)); else p.resolve(m);
        return;
      }
      if (m.t === 'session.event') { upsert(m.session); render(); if (attached && m.session.id === attached.id) setTermStatus(m.session); }
      if (m.t === 'session.removed') { sessions = sessions.filter((s) => s.id !== m.id); render(); }
      if (m.t === 'session.detached' && attached && m.id === attached.id) { term.write('\r\n[detached: ' + m.reason + ']\r\n'); }
    };
  }

  function send(t, body) {
    return new Promise((resolve, reject) => {
      const id = ++rid;
      pending.set(id, { resolve, reject });
      ws.send(JSON.stringify(Object.assign({ t, rid: id }, body || {})));
    });
  }

  function b64(bytes) { return btoa(String.fromCharCode(...bytes)); }

  function upsert(s) { const i = sessions.findIndex((x) => x.id === s.id); if (i >= 0) sessions[i] = s; else sessions.unshift(s); }

  function refresh() { send('session.list').then((r) => { sessions = r.sessions; render(); }); }

  function render() {
    const tb = $('#sessions'); tb.innerHTML = '';
    for (const s of sessions) {
      const tr = document.createElement('tr');
      const canAttach = s.status === 'running' || s.status === 'waiting' || s.status === 'exited';
      tr.innerHTML = `<td>${esc(s.name)}</td><td><span class="st ${s.status}">${s.status}${s.exit_code != null ? ' ' + s.exit_code : ''}</span></td>` +
        `<td>${esc(s.cwd)}</td><td class="preview">${esc(s.preview || '')}</td><td></td>`;
      const actions = tr.lastElementChild;
      if (canAttach) actions.appendChild(btn('Attach', '', () => attach(s)));
      if (s.status === 'stale' || s.status === 'exited') actions.appendChild(btn('Resume', 'secondary', () => send('session.resume', { id: s.id, cols: 120, rows: 36 }).then((r) => { upsert(r.session); render(); attach(r.session); })));
      if (s.status === 'running' || s.status === 'waiting') actions.appendChild(btn('Kill', 'danger', () => send('session.kill', { id: s.id })));
      else actions.appendChild(btn('Remove', 'secondary', () => send('session.remove', { id: s.id })));
      tb.appendChild(tr);
    }
  }
  function btn(label, cls, fn) { const b = document.createElement('button'); b.textContent = label; b.className = cls; b.style.marginRight = '6px'; b.onclick = fn; return b; }
  function esc(s) { return String(s).replace(/[&<>]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' }[c])); }

  $('#create').onsubmit = (e) => {
    e.preventDefault();
    const f = e.target;
    const cmdRaw = f.cmd.value.trim();
    const parts = cmdRaw ? cmdRaw.split(/\s+/) : [];
    send('session.create', { cwd: f.cwd.value.trim(), cmd: parts[0] || '', args: parts.slice(1), name: f.name.value.trim(), cols: 120, rows: 36 })
      .then((r) => { upsert(r.session); render(); attach(r.session); })
      .catch((err) => alert(err.message));
  };
  $('#refresh').onclick = refresh;

  function ensureTerm() {
    if (term) return;
    term = new Terminal({ fontSize: 13, cursorBlink: true, scrollback: 5000, theme: { background: '#000' }, allowProposedApi: true });
    fit = new FitAddon.FitAddon();
    term.loadAddon(fit);
    term.open($('#term'));
    term.onData((d) => { if (attached) sendInput(new TextEncoder().encode(d)); });
    term.onBinary((d) => { if (attached) sendInput(Uint8Array.from(d, (c) => c.charCodeAt(0))); });
    window.addEventListener('resize', () => { if (attached) { fit.fit(); send('session.resize', { id: attached.id, cols: term.cols, rows: term.rows }); } });
  }
  function sendInput(bytes) {
    const frame = new Uint8Array(5 + bytes.length);
    frame[0] = 1; new DataView(frame.buffer).setUint32(1, attached.handle);
    frame.set(bytes, 5);
    ws.send(frame);
  }
  function onBinary(f) {
    if (f.length < 5 || f[0] !== 2) return;
    const handle = new DataView(f.buffer, f.byteOffset).getUint32(1);
    if (attached && handle === attached.handle) term.write(f.subarray(5));
  }
  function setTermStatus(s) { const el = $('#term-status'); el.textContent = s.status; el.className = 'st ' + s.status; }

  function attach(s) {
    ensureTerm();
    if (attached) send('session.detach', { id: attached.id });
    term.reset();
    $('#term-view').classList.add('open');
    $('#term-title').textContent = s.name + ' — ' + s.cwd;
    setTermStatus(s);
    fit.fit();
    attached = { id: s.id, handle: s.handle };
    send('session.attach', { id: s.id, cols: term.cols, rows: term.rows }).then(() => term.focus()).catch((e) => term.write('\r\n' + e.message + '\r\n'));
  }
  $('#back').onclick = () => { if (attached) send('session.detach', { id: attached.id }); attached = null; $('#term-view').classList.remove('open'); refresh(); };
  $('#kill').onclick = () => { if (attached) send('session.kill', { id: attached.id }); };
  $('#send-esc').onclick = () => { if (attached) { sendInput(new Uint8Array([27])); term.focus(); } };
  $('#send-ctrlc').onclick = () => { if (attached) { sendInput(new Uint8Array([3])); term.focus(); } };
  $('#send-shifttab').onclick = () => { if (attached) { sendInput(new TextEncoder().encode('\x1b[Z')); term.focus(); } };

  connect();
})();
