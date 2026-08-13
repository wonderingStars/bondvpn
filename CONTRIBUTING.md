# Contributing

Bug reports, questions and patches are all welcome. A few things worth knowing
before you spend time on something.

## The licensing bit, up front

BondVPN is AGPLv3, and commercial licences are also offered to people who cannot
live with that (see [COMMERCIAL.md](COMMERCIAL.md)). Offering the same software
under two licences only works while one party holds the rights to the whole of
it.

So: **by opening a pull request, you agree that your contribution is licensed
under the AGPLv3, and you grant the project maintainer a perpetual, worldwide,
irrevocable, royalty-free, sublicensable licence to use, modify and distribute
it — including as part of a commercially licensed version.**

You keep your copyright. Nothing is signed over. It is a licence, not a
transfer, and it is the same arrangement most dual-licensed projects use. If
that is not acceptable to you, a fork under the AGPL is entirely legitimate and
no hard feelings.

## What a good change looks like

- **Explain the failure, not the diff.** The commit messages here say what broke
  and why the fix is shaped the way it is. A patch that says "fixes routing" is
  hard to review and impossible to maintain later.
- **A bug fix comes with a test that failed before it.** If the test never went
  red, it does not demonstrate anything.
- **Run the suite**: `make test`, and `gofmt -l .` must print nothing.
- **No dependencies.** The binary runs as root beside VPN keys and its
  `go.mod` has no requirements at all. That is deliberate, and a pull request
  adding one needs a very good argument.

## Testing something that touches routing

`testbed/` builds three WireGuard tunnels with real handshakes, stand-in
containers on a bridge, and — deliberately — a working NAT path straight to the
internet, so a leak test that passes means something. `testbed/README.md`
explains why that control matters. It is destructive to the host's networking:
run it on a VM, never on the machine you care about.

## The update check

`update.go` asks a server hourly whether a newer release exists, and reports it.
It cannot stop, block or degrade anything, a test enforces that, and
`update_check: false` turns it off.

If you would rather it did not exist at all, delete it — that is your right
under the licence and no argument will be made against it. It stays in the
project because the request count is the only way to know whether anyone is
running this, and it is documented rather than hidden.
