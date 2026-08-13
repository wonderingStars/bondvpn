# BondVPN

**WireGuard for your download box, with a kill switch that fails closed.**

Drop in your VPN provider's `.conf` files. Everything behind the gateway goes out
through them — and if a tunnel drops, that traffic **stops** rather than leaking
onto your own address.

- **Nothing leaks.** Every tunnel down means no way out at all. `bondvpn
  leak-test` proves it by measuring, not by claiming.
- **DNS can't leak either.** It is forced down the tunnels, not merely suggested.
- **One tunnel each, or all of them at once.** Pin a torrent client to a single
  exit; spread Usenet across every tunnel for the combined speed.

Free and open source ([AGPLv3](LICENSE)). One static binary, no dependencies.

![A gateway running three bonded tunnels](docs/dashboard.png)

---

## Install — pick the one that matches your machine

| Your setup | Go to | Bonding? |
|---|---|---|
| **Synology NAS** | [NAS steps](#synology-nas) | No — one tunnel per client |
| **Linux PC, server or mini PC** | [PC steps](#linux-pc-or-server) | Yes |
| **Docker on anything else** | [Docker steps](#docker-anywhere-else) | Yes |

Want the whole thing including Sonarr, Radarr, Prowlarr, sabnzbd and qBittorrent?
[SETUP.md](SETUP.md) walks a bare box to a finished stack.

---

## Synology NAS

**What you get:** WireGuard on your NAS with no kernel module, no compiling and
no command line after setup, plus the kill switch and the web page.

**What you don't:** bonding. DSM's kernel is built without multipath routing, so
several tunnels cannot be combined into one faster link. Each client gets pinned
to one tunnel instead — which is the better mode for BitTorrent anyway.

Tested on a DS1019+ running DSM 7.3.

**1.** Install **Container Manager** from Package Center.

**2.** In File Station, make a shared folder called `docker` if you don't have
one, and inside it a folder `bondvpn`, with `wg` and `conf` inside that.

**3.** Put your provider's `.conf` files into `docker/bondvpn/wg`. (You can skip
this and add them from the web page later.)

**4.** Create `docker/bondvpn/conf/config.yml` with this in it:

```yaml
tunnel_dir: /etc/bondvpn/wg
clients: 172.17.0.0/16
default: pin
dns: 10.64.0.1
lan: auto
wan_interface: auto
listen: 0.0.0.0:8099
```

**5.** In Container Manager → **Project** → **Create**, paste this compose file:

```yaml
services:
  bondvpn:
    image: ghcr.io/wonderingstars/bondvpn:latest
    container_name: bondvpn
    network_mode: host
    cap_add: [NET_ADMIN]
    devices: ["/dev/net/tun"]
    environment:
      BONDVPN_ADMIN_TOKEN: pick-a-long-password-here
    volumes:
      - /volume1/docker/bondvpn/conf:/etc/bondvpn
      - /volume1/docker/bondvpn/wg:/etc/bondvpn/wg
    restart: unless-stopped
```

> **Needs the 1.8.0 image or newer.** Earlier images have no userspace WireGuard
> and no legacy iptables, so on DSM they start, look healthy, and cannot create
> a tunnel or arm the kill switch.

**6.** Start it, then open **`http://YOUR-NAS-IP:8099`**.

**7.** Enter the token you chose in step 5 to add or remove tunnels from the page.

`clients: 172.17.0.0/16` is the Docker bridge, so your other containers are the
ones protected. Set it to a subnet you aren't using if you want to try it without
affecting anything.

---

## Linux PC or server

The full thing, bonding included. Any distribution; the binary is static.

**1.** Install what it drives:

```bash
sudo apt install -y wireguard-tools iptables
```

**2.** Download it:

```bash
curl -fsSLO https://github.com/wonderingStars/bondvpn/releases/latest/download/bondvpn-linux-amd64
sudo install -m 0755 bondvpn-linux-amd64 /usr/local/bin/bondvpn
```

(`arm64` for a Pi 4/5, `armv7` for a 32-bit Pi.)

**3.** Put your provider's `.conf` files in one folder:

```bash
sudo mkdir -p /etc/wireguard/bondvpn
sudo cp ~/Downloads/*.conf /etc/wireguard/bondvpn/
```

**4.** Write `/etc/bondvpn/config.yml`:

```yaml
tunnel_dir: /etc/wireguard/bondvpn
clients: 172.20.0.0/24     # the subnet your containers are on
default: bond
dns: 10.64.0.1
lan: auto
wan_interface: auto
listen: 127.0.0.1:8099
```

**5.** Check before you start anything — it changes nothing and tells you what's
wrong:

```bash
sudo bondvpn check
```

**6.** Run it as a service. Save this as `/etc/systemd/system/bondvpn.service`:

```ini
[Unit]
Description=BondVPN
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/bondvpn run
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

Then:

```bash
sudo systemctl enable --now bondvpn
```

**7.** Open `http://localhost:8099`. Your token:

```bash
sudo bondvpn token
```

Then point your containers' gateway at this machine. [SETUP.md](SETUP.md) has the
Docker network side in full.

---

## Docker anywhere else

```yaml
services:
  bondvpn:
    image: ghcr.io/wonderingstars/bondvpn:latest
    container_name: bondvpn
    network_mode: host
    cap_add: [NET_ADMIN]
    devices: ["/dev/net/tun"]
    environment:
      BONDVPN_ADMIN_TOKEN: pick-a-long-password-here
    volumes:
      - ./conf:/etc/bondvpn
      - ./wg:/etc/bondvpn/wg
    restart: unless-stopped
```

For bonding to actually spread traffic, run this once on the **host**:

```bash
sudo sysctl -w net.ipv4.fib_multipath_hash_policy=1
```

Without it every connection to a given server lands on one tunnel. BondVPN says
so on the dashboard rather than letting you assume otherwise.

---

## Adding your VPN files

Two ways, both fine:

**From the web page** — open it, enter the token, drag your `.conf` files onto
the panel. Live in about fifteen seconds.

**By dropping files in the folder** — copy them into the tunnel folder over SMB
or `scp`. Picked up on the next pass, no restart.

Either way BondVPN fixes the two things every provider's file gets wrong, which
you would otherwise have to know about:

- the `DNS =` line is removed — `wg-quick` applies it to your **whole machine**
- routing is disabled for the interface, without which the tunnel comes up,
  handshakes, shows traffic, and carries nothing

Your original files are never modified.

**Filenames become interface names**, so keep them short and simple:
`uk-london.conf`, not `mullvad-gb-lon-wg-001.conf` (Linux stops at 15
characters, and BondVPN will tell you so rather than failing quietly).

### Where the token comes from

Pick your own with `BONDVPN_ADMIN_TOKEN`, or let one be generated and read it
back in any of these ways:

```bash
sudo bondvpn token                  # on a PC
docker exec bondvpn bondvpn token   # in a container
```

It is also printed in the log the first time it is created — Container Manager
shows this, as does `docker logs bondvpn`.

Reading the page needs nothing. **Changing** tunnels always needs the token,
because this runs as root.

---

## Pin or bond?

| | What it does | Use it for |
|---|---|---|
| **pin** | one tunnel, always the same | BitTorrent |
| **bond** | spread over every live tunnel | Usenet, indexers, HTTP |

BitTorrent ties your identity to an address, so spreading one client across
tunnels leaves trackers and peers holding an address you are not reliably at.
Measured on the same swarms: **1 seed at 0 B/s** bonded, against **7 seeds at
2.05 MB/s** pinned.

Usenet and indexers open many independent connections that each authenticate
separately, so a changing source address costs nothing and you get the sum of
your tunnels.

Set the default with `default:`, and override per client:

```yaml
default: bond
routes:
  pin:
    - 172.20.0.10      # qBittorrent
  bond:
    - 172.20.0.20      # sabnzbd
```

---

## Checking it works

```bash
sudo bondvpn check       # before starting: changes nothing
sudo bondvpn status      # what it thinks right now
sudo bondvpn leak-test   # drops every tunnel and proves nothing escapes
```

`leak-test` is disruptive by design — it takes the tunnels down for about thirty
seconds and measures whether anything got out. Probing from the host proves
nothing, because the kill switch lives in a forward chain the host never crosses.

The dashboard names the silent failures, not just the loud ones: a tunnel that is
up but not handshaking, every pinned client landing on the same tunnel, DNS not
being redirected, and traffic not being translated onto the tunnels — that last
one looks completely healthy while every request dies at the far end.

---

## Update check

Once an hour BondVPN fetches a small signed file and tells you if a newer version
exists. **That is all it does** — it cannot stop, block, expire or degrade
anything, and there is no licence check of any kind.

No identifiers are sent: no install ID, no address, nothing that distinguishes
your copy from anyone else's. The request is identical for every installation, so
the count says how many people run this and can never say who.

Turn it off with one line:

```yaml
update_check: false
```

---

## Licence

**AGPLv3.** Run it, read it, change it, share it, at home or at work, at no
charge.

The one obligation: if you **modify** it and let others use your modified version
over a network, [section 13](LICENSE) says those users can ask for your source.
Running it unmodified, or modifying it for yourself, asks nothing.

A [commercial licence](COMMERCIAL.md) exists for shipping it inside a product or
running a modified hosted service without that obligation.

Patches welcome — [CONTRIBUTING.md](CONTRIBUTING.md).

Bugs and requests: [Issues](https://github.com/wonderingStars/bondvpn/issues).
Include `bondvpn status` and `bondvpn version`.

BondVPN routes traffic and blocks it when its tunnels are down. It is not a
guarantee of anonymity. Verify your own setup with `bondvpn leak-test` before
relying on it.
