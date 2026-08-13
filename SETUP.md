# Setting it all up, start to finish

From a bare Linux box to Sonarr, Radarr, Prowlarr, sabnzbd and three qBittorrents,
each leaving through the VPN exit you chose for it.

Follow it in order. Every step ends with something you can check, so if it goes
wrong you know which step broke rather than staring at a stack that "doesn't
work".

Already running the arr stack? Skip to [step 4](#4-the-vpn-tunnels) — but read
[step 3](#3-put-the-containers-on-their-own-network-with-fixed-addresses)
first, because BondVPN routes by container address and yours need to be fixed.

---

## What you need

- A Linux box that stays on: a VM, an old laptop, a NUC, a Pi. It becomes the
  gateway, so it wants a wired connection.
- A WireGuard VPN account. Examples use Mullvad; anything that hands you plain
  `.conf` files works. Your account's device limit is the cap on tunnels —
  Mullvad's is 5, and BondVPN supports up to 5, shipping with 3.
- Root on that box.

Time: about half an hour, most of it waiting for containers to pull.

### Which binary

| Build | For |
|---|---|
| `bondvpn-linux-amd64` | x86-64: servers, NUCs, VMs, Intel/AMD Synology models |
| `bondvpn-linux-arm64` | 64-bit ARM: Raspberry Pi 4/5, newer ARM Synology models |
| `bondvpn-linux-armv7` | 32-bit ARM: Pi 2/3 on a 32-bit OS, older Synology DS-j models |

All three are static, so the distribution's age does not matter.

### Running it on a Synology NAS

The binary will run — but on DSM the CPU is the easy part, and the rest usually
is not. Read this before spending an evening on it.

- **DSM ships no WireGuard.** No kernel module, and no `wg` or `wg-quick`. There
  are community packages that build the module for a specific DSM version and
  platform, and they break every DSM update. Without them, nothing here works,
  because BondVPN drives WireGuard rather than implementing it.
- **DSM's `ip` is cut down** and its firewall is managed by DSM itself, which
  will fight anything that rewrites iptables underneath it.
- **`bondvpn check` tells you the truth in one command.** It looks for `wg`,
  `wg-quick`, `ip` and `iptables`, reads your configs, and changes nothing. Run
  that first on any NAS; if it reports missing tools, stop there.

Honestly: a €60 second-hand mini PC or a spare Pi makes a far better gateway
than a NAS, and leaves the NAS doing what it is good at. Point the containers on
the NAS at the gateway instead — that is the arrangement this was built for, and
it is what the author runs.

---

## 1. Prerequisites

```bash
sudo apt update
sudo apt install -y wireguard-tools iptables curl
```

Docker, if it isn't there:

```bash
curl -fsSL https://get.docker.com | sudo sh
```

**Check:** `wg --version` and `docker run --rm hello-world` both work.

---

## 2. Decide the layout

Write this down before touching anything — every later step refers to it.

| Container    | Address       | Routing | Why                                                |
|--------------|---------------|---------|----------------------------------------------------|
| qbittorrent  | `10.99.0.10`  | pin wg0 | BitTorrent ties identity to one address            |
| qbittorrent2 | `10.99.0.12`  | pin wg1 | second client, second exit                         |
| qbittorrent3 | `10.99.0.13`  | pin wg2 | third client, third exit                           |
| sabnzbd      | `10.99.0.11`  | bond    | many connections to one server — spread them       |
| prowlarr     | `10.99.0.20`  | bond    | indexer traffic                                    |
| sonarr       | `10.99.0.21`  | bond    | indexer and download-client traffic                |
| radarr       | `10.99.0.22`  | bond    | same                                               |

Two rules behind that table:

- **Torrent clients are pinned.** A torrent client announces an address to
  trackers and peers, and they connect back to it. Spread one client across
  three tunnels and it is announcing an address it is not reliably at. Measured
  on the same swarms: 1 connected seed at 0 B/s spread, 7 seeds at 2.05 MB/s
  pinned.
- **Everything else is bonded.** Usenet and indexers open many independent
  connections to one server, each authenticating separately, so a rotating
  source address costs nothing and you get the sum of your tunnels.

`10.99.0.0/24` must not be your home network. It is a private network just for
these containers, and BondVPN refuses to start if you point it at your LAN.

---

## 3. Put the containers on their own network, with fixed addresses

BondVPN decides where traffic goes by looking at which container sent it, so the
addresses cannot move. Create the network:

```bash
docker network create --subnet 10.99.0.0/24 bondnet
```

Then a compose file. This is the whole stack — trim what you don't want, but keep
the `networks` blocks and the addresses.

```yaml
# /opt/stack/docker-compose.yml
services:
  qbittorrent:
    image: lscr.io/linuxserver/qbittorrent:latest
    container_name: qbittorrent
    environment: [PUID=1000, PGID=1000, TZ=Europe/London, WEBUI_PORT=8081]
    volumes:
      - /opt/appdata/qbittorrent:/config
      - /data:/data
    ports: ["127.0.0.1:8081:8081", "6881:6881", "6881:6881/udp"]
    networks: { bondnet: { ipv4_address: 10.99.0.10 } }
    restart: unless-stopped

  qbittorrent2:
    image: lscr.io/linuxserver/qbittorrent:latest
    container_name: qbittorrent2
    environment: [PUID=1000, PGID=1000, TZ=Europe/London, WEBUI_PORT=8082]
    volumes:
      - /opt/appdata/qbittorrent2:/config
      - /data:/data
    ports: ["127.0.0.1:8082:8082", "6882:6882", "6882:6882/udp"]
    networks: { bondnet: { ipv4_address: 10.99.0.12 } }
    restart: unless-stopped

  qbittorrent3:
    image: lscr.io/linuxserver/qbittorrent:latest
    container_name: qbittorrent3
    environment: [PUID=1000, PGID=1000, TZ=Europe/London, WEBUI_PORT=8083]
    volumes:
      - /opt/appdata/qbittorrent3:/config
      - /data:/data
    ports: ["127.0.0.1:8083:8083", "6883:6883", "6883:6883/udp"]
    networks: { bondnet: { ipv4_address: 10.99.0.13 } }
    restart: unless-stopped

  sabnzbd:
    image: lscr.io/linuxserver/sabnzbd:latest
    container_name: sabnzbd
    environment: [PUID=1000, PGID=1000, TZ=Europe/London]
    volumes:
      - /opt/appdata/sabnzbd:/config
      - /data:/data
    ports: ["127.0.0.1:8080:8080"]
    networks: { bondnet: { ipv4_address: 10.99.0.11 } }
    restart: unless-stopped

  prowlarr:
    image: lscr.io/linuxserver/prowlarr:latest
    container_name: prowlarr
    environment: [PUID=1000, PGID=1000, TZ=Europe/London]
    volumes: [/opt/appdata/prowlarr:/config]
    ports: ["127.0.0.1:9696:9696"]
    networks: { bondnet: { ipv4_address: 10.99.0.20 } }
    restart: unless-stopped

  sonarr:
    image: lscr.io/linuxserver/sonarr:latest
    container_name: sonarr
    environment: [PUID=1000, PGID=1000, TZ=Europe/London]
    volumes:
      - /opt/appdata/sonarr:/config
      - /data:/data
    ports: ["127.0.0.1:8989:8989"]
    networks: { bondnet: { ipv4_address: 10.99.0.21 } }
    restart: unless-stopped

  radarr:
    image: lscr.io/linuxserver/radarr:latest
    container_name: radarr
    environment: [PUID=1000, PGID=1000, TZ=Europe/London]
    volumes:
      - /opt/appdata/radarr:/config
      - /data:/data
    ports: ["127.0.0.1:7878:7878"]
    networks: { bondnet: { ipv4_address: 10.99.0.22 } }
    restart: unless-stopped

networks:
  bondnet:
    external: true
```

Two details that cause most of the pain people hit later:

- **One `/data` mount, shared by everything.** Sonarr can hardlink a finished
  download into your library instead of copying it only if the download folder
  and the library are inside the same mount. Give qBittorrent `/data/torrents`
  and Sonarr `/data/media`, never `/downloads` and `/tv`.
- **Web UIs are published on `127.0.0.1` only.** They have no authentication
  worth the name by default. Reach them over SSH (`ssh -L 8989:127.0.0.1:8989
  you@gateway`) or put a reverse proxy with a login in front. Publishing them on
  `0.0.0.0` puts your whole stack on your LAN.

```bash
sudo mkdir -p /opt/appdata /data/torrents /data/media
cd /opt/stack && docker compose up -d
```

**Check:** `docker network inspect bondnet -f '{{range .Containers}}{{.Name}} {{.IPv4Address}}{{println}}{{end}}'`
lists every container at the address you chose.

---

## 4. The VPN tunnels

Download one WireGuard config per tunnel from your provider — different cities,
or at least different servers, so you are not sharing one machine's capacity
with yourself. Put them in `/etc/wireguard/wg0.conf`, `wg1.conf`, `wg2.conf`.

**Every file needs three edits.** BondVPN refuses to start without the first
two, and tells you which file — they are silent problems, not fussiness:

```ini
[Interface]
PrivateKey = <yours>
Address = <yours>
Table = off                 # ADD. Otherwise wg-quick installs its own default
                            # route and fights BondVPN for the routing table.
                            # Your traffic takes the wrong tunnel and nothing
                            # reports an error.
# DNS = 10.64.0.1           # DELETE this line. wg-quick uses it to rewrite the
                            # HOST's resolver, and fails outright without
                            # resolvconf installed. BondVPN handles DNS itself.

[Peer]
PublicKey = <theirs>
AllowedIPs = 0.0.0.0/0
Endpoint = <theirs>:51820
PersistentKeepalive = 25    # ADD. WireGuard only rekeys when there is traffic,
                            # so an idle tunnel stops handshaking and gets
                            # dropped from the bond while perfectly healthy.
```

```bash
sudo chmod 600 /etc/wireguard/wg*.conf
```

Don't start them by hand — BondVPN brings them up.

**Check:** `grep -c "Table = off" /etc/wireguard/wg*.conf` returns 1 for each,
and `grep -i "^DNS" /etc/wireguard/wg*.conf` returns nothing.

---

## 5. Install BondVPN

Each release attaches the binaries, `SHA256SUMS`, and the three install helpers
(`config.example.yml`, `bondvpn.service`, `install.sh`). Download the binary for
your architecture plus those four from
[Releases](https://github.com/wonderingStars/bondvpn/releases) into one
directory, then:

```bash
sha256sum --ignore-missing -c SHA256SUMS   # verifies the arch you downloaded
chmod +x install.sh
sudo ./install.sh
```

Nothing starts yet, deliberately: starting before the config is right arms a
kill switch around the wrong network.

Now `/etc/bondvpn/config.yml`, matching the table from step 2:

```yaml
tunnels:
  - /etc/wireguard/wg0.conf
  - /etc/wireguard/wg1.conf
  - /etc/wireguard/wg2.conf

clients: 10.99.0.0/24
default: bond

routes:
  pin:
    - 10.99.0.10      # qbittorrent  -> wg0
    - 10.99.0.12      # qbittorrent2 -> wg1
    - 10.99.0.13      # qbittorrent3 -> wg2
  bond:
    - 10.99.0.11      # sabnzbd
    - 10.99.0.20      # prowlarr
    - 10.99.0.21      # sonarr
    - 10.99.0.22      # radarr

dns: 10.64.0.1        # your provider's resolver. REQUIRED - every client's DNS
                      # is rewritten to this, over the tunnels.

lan: auto             # derived from your network on every start, so replacing
wan_interface: auto   # a router does not silently break the LAN exception

stale_handshake: 180
listen: 127.0.0.1:8099

dashboard: true       # the built-in interface, at the root of `listen`

disks:                # what the machine panel watches, as "label /path"
  - staging /data
  - library /mnt/library
```

Pins are given tunnels in the order you list them: first pinned address to the
first tunnel, and so on.

Once it is running, the interface is at <http://127.0.0.1:8099/> **on the gateway
itself**. It has no login of its own, so reach it over SSH port forwarding
(`ssh -L 8099:127.0.0.1:8099 you@gateway`) rather than by moving `listen` to a
LAN address - that publishes it to everyone on your network.

Before starting anything, have it read the lot back to you:

```bash
sudo bondvpn check
```

It reports every problem it can find in one pass - the config, the WireGuard
files it names, this machine's interfaces, the listen address and the disks -
and changes nothing. Fix whatever it lists, run it again, then:

```bash
sudo systemctl enable --now bondvpn
```

**Check:** `sudo bondvpn status` exits 0 and `"problems"` is empty.

---

## 6. Prove it actually works

Three separate things to confirm, in order of how badly you want to know.

**Each torrent client leaves by its own exit:**

```bash
for c in qbittorrent qbittorrent2 qbittorrent3; do
  echo -n "$c: "; docker exec $c curl -s https://ipv4.icanhazip.com
done
```

Three different addresses, none of them your home IP. If two match, two clients
share a tunnel — check the `pin:` list against your container addresses.

**Nothing escapes when the tunnels drop:**

```bash
sudo bondvpn leak-test
```

It drops every tunnel for about thirty seconds, probes a hostname, a raw IP and
ICMP from inside your container network, then restores the tunnels and checks
your pins came back spread across them. Downloads stall and resume.

**Your home address is nowhere in sight:** compare `curl ipv4.icanhazip.com` on
the gateway itself against the three above. The host is not routed through the
tunnels — only the containers are — so it should differ.

---

## 7. Wire the arrs together

All internal, so use container addresses, not `localhost`.

**Prowlarr** (`10.99.0.20:9696`) — add your indexers, then
*Settings → Apps* → add Sonarr and Radarr:

- Prowlarr Server: `http://10.99.0.20:9696`
- Sonarr Server: `http://10.99.0.21:8989`, API key from Sonarr's *Settings → General*
- Radarr Server: `http://10.99.0.22:7878`

Indexers now sync automatically instead of being pasted into each app.

**Sonarr and Radarr** — *Settings → Download Clients* → add each qBittorrent:

| Field | qbittorrent | qbittorrent2 | qbittorrent3 |
|-------|-------------|--------------|--------------|
| Host  | `10.99.0.10`| `10.99.0.12` | `10.99.0.13` |
| Port  | `8081`      | `8082`       | `8083`       |

Add sabnzbd the same way at `10.99.0.11:8080`.

Adding all three torrent clients is the point: work spreads across them, and
because each is pinned to its own tunnel, three clients means three exits and
three times the per-tunnel capacity.

**Root folders** — Sonarr `/data/media/tv`, Radarr `/data/media/movies`, with
qBittorrent saving to `/data/torrents`. Same mount, so imports hardlink instead
of copying.

**Check:** search for something in Sonarr, watch it land in a qBittorrent, and
confirm the import completes.

---

## 8. Keep an eye on it

```bash
sudo bondvpn status          # JSON, exit 1 if anything is wrong
curl -s localhost:8099/healthz   # 200 when healthy, 503 with reasons when not
journalctl -u bondvpn -f
```

`status` names the silent failures, not just the loud ones: a tunnel that is up
but has stopped handshaking, every pinned client landing on one tunnel, the
multipath hash policy left at its default, DNS not being redirected, a config
that lost `Table = off`. It repairs what it can — the kill switch, the address
translation, the DNS redirect, the routing rules and their tables, and the
tunnels themselves.

The `/healthz` endpoint is a one-liner for any monitor you already run.

---

## When something is wrong

**`bondvpn` refuses to start, naming a config file.** It found a `DNS =` line or
a missing `Table = off`. Fix that file and start it again.

**Containers have no internet.** Expected if no tunnel is up — that is the kill
switch doing its job rather than leaking. `bondvpn status` will say
`no live tunnels`; check `journalctl -u bondvpn` for why they didn't come up
(usually a bad key or an unreachable endpoint).

**Two torrent clients share an exit.** Their addresses aren't in `pin:`, or the
container moved. `docker network inspect bondnet` against your config.

**Downloads work but names never resolve.** Your `dns:` address is not reachable
through the tunnels. Use your provider's resolver.

**It was fine, now everything is blocked.** Check the licence heartbeat:
`bondvpn status` reports `"licensed"`, and the log counts down from the first
failed check. See the README section on it.

**Speeds are no better than one tunnel.** Confirm `"hash_policy": 1` in status.
At the kernel default, every connection to a given server lands on one tunnel
and bonding does nothing — BondVPN sets it, but a sysctl on boot can put it
back.
