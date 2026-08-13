# Commercial licensing

BondVPN is released under the **GNU AGPLv3** — see [LICENSE](LICENSE). For most
people that is the end of it: run it, read it, change it, share it, free of
charge, forever.

A commercial licence exists for the case the AGPL does not suit.

## You do not need one

- running BondVPN at home, on your own machines, for any purpose
- running it inside a company for that company's own use
- modifying it for yourself and never distributing the result
- studying it, packaging it for a distribution, writing about it

The AGPL asks nothing of you in any of those situations. Internal use is not
"distribution", and running software is never restricted by it.

## You probably want one

The AGPL's distinctive clause is section 13: if you **modify** BondVPN and let
other people use it over a network, you must offer those users the complete
corresponding source of your modified version. Plain use is unaffected; it is
modification plus offering it to others that triggers the obligation.

So a commercial licence is the sensible route if you want to:

- build BondVPN into a product or appliance you sell, without publishing your
  own source alongside it;
- run a hosted or managed service on a modified version, and keep those
  modifications private;
- distribute it inside something you would rather not license under the AGPL.

You get the same software under negotiated terms instead, with no copyleft
obligation. Open an issue on the repository saying roughly what you are
building, and we will sort out something proportionate.

## Why this is possible

The copyright in BondVPN is held in one place, and the project has **no
third-party code** — the binary's dependency tree is empty by design, because it
runs as root next to VPN keys. That single ownership is what allows the same
software to be offered under two licences at once.

It is also why [CONTRIBUTING.md](CONTRIBUTING.md) asks contributors to license
their changes for that purpose. Without it, the first outside patch would end
dual licensing for good — not through anyone's fault, but because nobody would
have the right to offer the whole work under other terms.
