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
    "eyJpc3N1ZWQiOjE3ODY4ODg2MzcsImxhdGVzdCI6IjEuOC4wIiwibWVzc2FnZSI6IjEuOC4wIGFkZHMgdHVubmVsIHVwbG9hZHMgZnJvbSB0aGUgZGFzaGJvYXJkIGFuZCBTeW5vbG9neSBzdXBwb3J0LiIsInN0YXR1cyI6ImFjdGl2ZSJ9",
  signature:
    "w77GuLuMn8EuDBXggV6URmK+ywYOsQSK5UGuVdIJDsA0EM4aqvSHbMoE5XtHkXjcmO++dHYILxAQS8bUoR+ZDg==",
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
