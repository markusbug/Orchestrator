# Running a relay

The relay lets phones reach an Orchestrator host from anywhere with no port
forwarding or firewall rules on the host: the daemon connects *out* to the
relay, and the relay pipes the phone's TLS bytes to it. The relay never sees
plaintext; the phone still pins the daemon's own certificate. Design:
[docs/RELAY.md](../../docs/RELAY.md).

This directory is everything needed to run one: a systemd unit, kernel
tuning, an env template, and `install.sh`. The hosted relay and self-hosted
ones run the identical binary.

## The hosted instance

`relay.markushaas.com`, live since 2026-09-06 on a Hetzner CX23 (Ubuntu 24.04,
IPv4 only, no AAAA records). It was brought up with exactly the steps below,
using `ufw` rather than a provider firewall, and holds a production Let's
Encrypt certificate that autocert renews. The deploy files sit in the admin
user's `~/relay-deploy` on the box, so an upgrade is: build with
`make build-relay-linux`, `scp dist/relay_linux_amd64` there, then
`sudo relay-install --binary ~/relay-deploy/relay_linux_amd64 --upgrade`.
Still to do: an external uptime and certificate-expiry monitor (below), delete
protection in the console.

## What you need

- A small VPS: Ubuntu 24.04, 2 to 4 GB RAM, public IPv4 (and IPv6). A Hetzner
  CX23 is plenty to start. Enable delete protection and use a persistent
  primary IP so DNS survives a rebuild.
- A domain you control, e.g. `relay.example.com`.
- The provider's firewall allowing inbound TCP 22, TCP 443 and ICMP. Prefer it
  over `ufw`: it is enforced outside the VM and costs the box no conntrack
  memory. If you must use `ufw`, drop `--no-ufw` below and the script sizes
  conntrack for you.

## DNS

Point both the apex and a wildcard at the box, TTL 300:

```
relay.example.com.    A     203.0.113.10
relay.example.com.    AAAA  2001:db8::10
*.relay.example.com.  A     203.0.113.10
*.relay.example.com.  AAAA  2001:db8::10
```

Optional: `relay.example.com. CAA 0 issue "letsencrypt.org"`.

Only the apex ever gets a certificate. `<hostid>.relay.example.com` is passed
through to the daemon's own self-signed certificate, so no wildcard
certificate exists or is needed. Verify:

```
dig +short relay.example.com A
dig +short abc.relay.example.com A
```

## Box bootstrap (once)

```
apt update && apt full-upgrade -y && reboot
adduser admin && usermod -aG sudo admin      # your key in ~admin/.ssh/authorized_keys
cat >/etc/ssh/sshd_config.d/10-relay.conf <<'EOF2'
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
AllowUsers admin
MaxAuthTries 3
X11Forwarding no
EOF2
systemctl restart ssh                          # test a new login before closing this one
cat >/etc/apt/apt.conf.d/52relay <<'EOF2'
Unattended-Upgrade::Automatic-Reboot "true";
Unattended-Upgrade::Automatic-Reboot-Time "04:30";
EOF2
```

The monthly security reboot drops every connection once; daemons and phones
reconnect on their own. Not rebooting would mean running old kernels on a
public :443, which is worse.

## First install

Build or download `relay_linux_amd64` (a `relay-v*` tag publishes it on the
GitHub release together with `relay-deploy.tar.gz` and `SHA256SUMS`; or
`make build-relay-linux` locally). Copy it and this directory to the box, then:

```
sudo ./install.sh --binary ./relay_linux_amd64 --domain relay.example.com --staging --no-ufw
```

The script installs the binary to `/usr/local/bin/relay` (keeping the previous
one as `relay.prev`), writes `/etc/relay/relay.env` from the template (never
overwriting an existing one), installs the unit, sysctl and journald drop-ins,
enables and restarts the service, and checks `/healthz`. It also copies itself
to `/usr/local/sbin/relay-install` for upgrades and rollbacks.

Bring-up in this order:

1. **Smoke test with a self-signed certificate.** Put `RELAY_FLAGS=--dev` in
   `/etc/relay/relay.env`, `systemctl restart relay`, then:
   ```
   curl -k https://relay.example.com/healthz
   openssl s_client -servername abc.relay.example.com -connect relay.example.com:443 </dev/null
   ```
   The second command must be closed by the relay without a certificate:
   that proves SNI dispatch works (unknown host id). Also run
   `systemd-analyze security relay` (target: below 2.0).
2. **Let's Encrypt staging.** Clear `RELAY_FLAGS`, keep the staging directory
   line from `--staging`, restart, `curl -k https://relay.example.com/healthz`,
   and check the issuer says STAGING:
   ```
   openssl s_client -connect relay.example.com:443 -servername relay.example.com </dev/null 2>/dev/null | openssl x509 -noout -issuer -dates
   ```
3. **Production certificate.** Remove the `RELAY_ACME_DIRECTORY` line, restart,
   `curl https://relay.example.com/healthz` without `-k`. The cache is keyed
   by ACME directory, so the staging account is not reused.
4. **Monitoring.** Create the uptime and certificate-expiry checks (below).
5. **A daemon.** On your laptop: `orchestrator relay set https://relay.example.com`,
   restart the daemon, `orchestrator relay` shows *connected*, and on the box
   `curl -s localhost:9100/debug/vars | jq .relay.hosts_online` is 1.
6. **A phone on cellular** with Wi-Fi off: pair (the QR now carries the relay
   address) and drive a session; watch `.relay.streams` go 0, 1, 0.

## Upgrade

```
sudo relay-install --binary ./relay_linux_amd64 --upgrade      # or --version relay-v0.2.0 --upgrade
curl -s https://relay.example.com/healthz
```

`--upgrade` is non-interactive, requires an existing env file, restarts, waits
for health, and rolls back to `relay.prev` by itself if the new binary does not
come up. A restart drops active phone streams for a moment; phones reconnect
and replay. Daemons get close code 1012 and reconnect spread over 30 s.

`--version` downloads from GitHub releases, which needs the repository to be
public; while it is private use `--binary`.

## Rollback

```
sudo relay-install --rollback
```

Swaps `relay.prev` back and restarts. Only one previous binary is kept.

## Logs

```
journalctl -u relay -f
journalctl -u relay -p warning --since -1h
journalctl -u relay -o json --since -1h | jq -r 'select(.MESSAGE|test("host online")) | .MESSAGE'
```

Logs are JSON, one line per event. Per-connection events are at debug level
(add `-v` to `RELAY_FLAGS` temporarily); lifecycle, ACME and accept errors at
info and warning.

## Capacity checks

```
ss -s                                                    # sockets in use
ss -Htn state established '( sport = :443 )' | wc -l     # connections on 443
curl -s localhost:9100/debug/vars | jq '.relay | {hosts_online, streams, pending, conns, max_conns}'
curl -s localhost:9100/debug/vars | jq .memstats
systemctl status relay | grep -E 'Memory|Tasks'
cat /proc/net/sockstat                                   # TCP mem pages vs tcp_mem
P=$(systemctl show -p MainPID --value relay); ls /proc/$P/fd | wc -l; grep 'open files' /proc/$P/limits
cat /proc/sys/fs/file-nr
```

Rule of thumb: 30 to 50 KB per idle host, so roughly 40k hosts on 2 GB and
80k to 100k on 4 GB. `docs/RELAY.md` records measured numbers.

**Out of file descriptors** looks like `accept: too many open files` in the
journal while existing connections keep working. Check, in order: the unit's
limit is in effect (`systemctl show relay -p LimitNOFILE` and
`/proc/$P/limits` agree; if not, `systemctl daemon-reload && systemctl restart
relay`); `file-nr` against `fs.file-max`; and whether fds far exceed
established connections (a leak: save
`curl -s localhost:9100/debug/pprof/goroutine?debug=1` and file a bug). If it
is legitimately full, this box is at capacity; add a second relay domain for
new hosts. `systemctl restart relay` recovers immediately either way.

## Monitoring on a shoestring

- Public: `https://relay.example.com/healthz` returns `{"ok":true,"version":"..."}`.
- Loopback: `localhost:9100` has `/healthz`, `/debug/vars` and `/debug/pprof/`.
- External check: UptimeRobot or Better Stack free tier, one HTTPS keyword
  monitor on `/healthz` expecting `"ok":true`, plus their SSL-expiry alert.
  The expiry alert is the one that matters: a silently failing ACME renewal
  is the most likely quiet outage. A GitHub Actions cron is a poor fit here
  (billed minutes, sloppy schedule, disabled after 60 days of inactivity).
- Trends: the provider console graphs (CPU, packets, bandwidth) are enough.

## Backup and rebuild

There is no state worth backing up except `/etc/relay/relay.env` (put it in
your password manager). The ACME cache regenerates. To rebuild: new VPS with
the same primary IP, run `install.sh`, copy the env file back. Ten minutes.

## Security notes

- Only the apex is ever requested from Let's Encrypt (`HostWhitelist`), so
  random SNI probes cannot trigger issuance. Rate limits that matter: 5
  duplicate certificates per week, 5 failed validations per hostname per hour.
  That is why bring-up uses staging first.
- The relay authenticates daemons by Ed25519 signature; host ids are 128-bit
  hashes of the public key. It never authenticates phones: phones are
  authenticated by the daemon, end to end, exactly as on the LAN.
- The service runs as a dynamic user with `CAP_NET_BIND_SERVICE` only and a
  strict systemd sandbox. `systemd-analyze security relay` should report a
  score below 2.0.
- Abuse limits are in-process: per-IP connection rate and concurrent peeks,
  per-host pending dials and active streams, auth-failure lockout, and a
  global connection cap derived from the fd limit.
