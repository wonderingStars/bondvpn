# The counting endpoint

Downloads count people who took the software once. This counts machines still
running it today, which is the number that decides whether anyone would pay for
it.

Every installation fetches the licence status once an hour, so the arithmetic is:

```
active installations  ≈  requests in a day / 24
```

Nothing is stored per request — no addresses, no cookies, no identifier. The
figure comes from Cloudflare's own request metering, a number rather than a log,
so this can answer "how many" and can never answer "who". That is what the
public README promises and it should stay true.

## Deploying it

You need a free Cloudflare account. Nothing is billed on the free tier: 100,000
requests a day covers roughly 4,000 installations checking in hourly.

```
npm install -g wrangler      # once
wrangler login               # opens a browser
cd counter && wrangler deploy
```

It prints a hostname like `bondvpn-licence.bondvpn.workers.dev`. Check
it works:

```
curl -s https://bondvpn-licence.bondvpn.workers.dev/license.json
```

You should get the same signed JSON the public repository serves.

Prefer not to install anything? The dashboard route works too: Cloudflare
dashboard → Workers & Pages → Create → Worker → paste `worker.js` over the
starter code → Deploy.

## Switching installations onto it

In `license.go`, put the new host FIRST:

```go
var licenseURLs = []string{
    "https://bondvpn-licence.bondvpn.workers.dev/license.json",
    "https://raw.githubusercontent.com/wonderingStars/bondvpn/master/license.json",
}
```

The repository copy stays as the fallback, and that ordering is the important
part: if this Worker is down, blocked or retired, every installation carries on
from GitHub. A counter that can take the product down is a counter that
eventually will.

Then cut a release. Only installations running the new build check in here, so
the figure climbs as people upgrade — it is a floor, never a total.

## Reading the number

Cloudflare dashboard → Workers & Pages → `bondvpn-licence` → Metrics. Requests
per day is the raw figure; divide by 24.

Watch the trend rather than the number. A flat line after a spike means people
tried it and stopped; a line that keeps climbing without you posting anywhere
means they are recommending it, which is the only real evidence that anyone
would pay.

## Keeping the status in step

`worker.js` holds a copy of the signed payload. When you publish a new status —
revoking, or just refreshing the message — regenerate `license.json` with
`tools/signlicense`, update the constant here, and redeploy. If the two ever
disagree, installations take whichever answers first, which is this one.

The private key is not in this Worker and never should be. It signs offline, so
compromising this host cannot keep a revoked installation alive.
