// Serves the licence status file from a host whose request count you can see.
//
// Every installation checks in once an hour, so:
//
//     active installations  ~=  requests in a day / 24
//
// That is the only number this produces, and it is the only number worth
// having: downloads count people who took the software once, this counts
// machines still running it today.
//
// WHAT IT DELIBERATELY DOES NOT DO
//
// No addresses are stored, no cookies set, no identifier assigned, nothing
// written per request. The count comes from Cloudflare's own request metering -
// a number, not a log - so the question "who is running this" has no answer
// here, only "how many". For a privacy tool that is the correct trade and it is
// what the README promises.
//
// The consequence is that the figure is an ESTIMATE. Two installs behind one
// address count as two, which is right. An install that restarts every hour
// counts as several, which is rare. Anything blocking this host falls back to
// the copy in the repository and is invisible, so every figure is a floor.

// The signed status, identical to license.json in the public repo. Signed
// offline: the private key is not here and this Worker cannot mint a status of
// its own, so a compromise of this host cannot keep a revoked install alive.
const LICENSE = {
  payload:
    "eyJpc3N1ZWQiOjE3ODY2MjA0MDEsImxhdGVzdCI6IjEuNi4wIiwibWVzc2FnZSI6IkJvbmRWUE4gaXMgbm93IG9wZW4gc291cmNlIHVuZGVyIEFHUEx2My4iLCJzdGF0dXMiOiJhY3RpdmUifQ==",
  signature:
    "uFTJllLrzflduGo9ZZGx7UUfmXG+leaxH2vxQipx0gBRlL/NQfKfYcfG4iQPiTLLqil9xNh0ur9rM0xZLJBnDQ==",
};

export default {
  async fetch(request) {
    const { pathname } = new URL(request.url);

    if (pathname === "/license.json") {
      return new Response(JSON.stringify(LICENSE, null, 2), {
        headers: {
          "content-type": "application/json",
          // No caching, anywhere. A cached answer is an installation that
          // checked in without being counted, which would quietly deflate
          // every figure this exists to produce.
          "cache-control": "no-store, max-age=0",
        },
      });
    }

    if (pathname === "/healthz") return new Response("ok\n");

    return new Response("not found\n", { status: 404 });
  },
};
