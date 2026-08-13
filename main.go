package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"
)

const Version = "1.8.0"

const usage = `bondvpn ` + Version + `

  bondvpn check        validate the configuration and this machine, change nothing
  bondvpn run          bring the tunnels up, route the clients, hold it there
  bondvpn status       print the current state as JSON
  bondvpn leak-test    drop every tunnel and prove nothing escapes (disruptive)
  bondvpn stop         take the tunnels down and REMOVE the kill switch
  bondvpn token        print the token the dashboard's settings panel asks for
  bondvpn version

Options:
  -config PATH   configuration file (default /etc/bondvpn/config.yml)
`

func main() {
	os.Exit(realMain())
}

func realMain() int {
	fs := flag.NewFlagSet("bondvpn", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "/etc/bondvpn/config.yml", "configuration file")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }

	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	cmd := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	if cmd == "version" {
		fmt.Printf("bondvpn %s (%s/%s, %s)\n",
			Version, runtime.GOOS, runtime.GOARCH, runtime.Version())
		return 0
	}

	// Handled before prepare(), deliberately. prepare() stops at the first thing
	// it cannot load or resolve, which is precisely what `check` exists to
	// avoid: it has to reach a broken config and report all of it rather than
	// bouncing off the first error the way every other command does.
	if cmd == "check" {
		if runtime.GOOS != "linux" {
			fmt.Fprintf(os.Stderr, "error: bondvpn runs on Linux only (this is %s)\n", runtime.GOOS)
			return 1
		}
		return cmdCheck(*configPath, os.Stdout)
	}

	cfg, err := prepare(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	switch cmd {
	case "run":
		return cmdRun(cfg)
	case "status":
		return cmdStatus(cfg)
	case "leak-test":
		return cmdLeakTest(cfg)
	case "stop":
		return cmdStop(cfg)
	case "token":
		return cmdToken(cfg)
	default:
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
}

// prepare loads the config and resolves everything before any of it is used.
func prepare(path string) (*Config, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("bondvpn runs on Linux only (this is %s)", runtime.GOOS)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		return nil, err
	}
	if err := cfg.resolve(); err != nil {
		return nil, err
	}
	mgmt, err := ifaceAddrs(cfg.WANIface)
	if err != nil {
		return nil, err
	}
	// Checked before anything touches the kernel: getting this wrong makes the
	// host unreachable, and an unreachable host cannot be told to stop.
	if err := cfg.GuardAgainstSelf(mgmt); err != nil {
		return nil, err
	}
	return cfg, nil
}

// cmdRun is the daemon. It arms the kill switch FIRST, then brings up the
// tunnels, then holds the routing correct as tunnels come and go.
func cmdRun(cfg *Config) int {
	// Refused before anything starts. A provider's file as downloaded takes the
	// routing table off this gateway and rewrites the host's resolver, and both
	// failures present as "traffic went the wrong way" rather than as an error.
	fatal, warn := checkTunnelConfigs(cfg)
	for _, w := range warn {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	if len(fatal) > 0 {
		for _, f := range fatal {
			fmt.Fprintf(os.Stderr, "error: %s\n", f)
		}
		return 1
	}

	// Order matters. If the tunnels came up first and the kill switch failed to
	// install, there would be a window where clients route normally - straight
	// out of the ISP connection. Arming first means the worst case is no
	// connectivity, never unprotected connectivity.
	if err := applyKillSwitch(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: could not arm the kill switch: %v\n", err)
		return 1
	}
	logf("kill switch armed for %s (LAN %s)", cfg.Clients, cfg.LAN)

	// Fatal if it fails: without it every client request dies silently at the
	// far end while every other signal says the gateway is healthy.
	if err := applyNAT(cfg); err != nil {
		fmt.Fprintf(os.Stderr,
			"error: could not translate client traffic onto the tunnels: %v\n", err)
		return 1
	}

	if err := applyDNSRedirect(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: could not redirect client DNS: %v\n", err)
		return 1
	}
	if cfg.DNS != "" {
		logf("client DNS redirected to %s over the tunnels", cfg.DNS)
	}

	if err := ensureHashPolicy(); err != nil {
		// Not fatal, but say so loudly: without it every connection to a given
		// server lands on one tunnel and the bond does nothing.
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}

	tunnels := readTunnels(cfg)
	for _, t := range tunnels {
		if t.Up {
			continue
		}
		if err := bringUp(t); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s did not come up: %v\n", t.Name, err)
		}
	}

	// The update check. Hourly, off by one config line, and incapable of
	// stopping anything - see update.go for why it used to be otherwise.
	var upd UpdateState
	lastUpdateCheck := time.Now()
	if cfg.UpdateCheck {
		upd = checkUpdate()
		if upd.Available {
			logf("a newer release is available: %s (you are running %s)", upd.Latest, Version)
		}
	}

	// current is written by the run loop (this goroutine) and read by the HTTP
	// handler goroutines the server spawns, so every access goes through curMu.
	// Without it the race detector fires and, on weak memory, a handler can
	// observe a half-published *Status - the get() closure below is the read
	// side, refresh() the write side.
	var (
		curMu       sync.Mutex
		current     *Status
		lastSig     string
		lastAttempt = map[string]time.Time{}
	)
	refresh := func() *Status {
		ts := readTunnels(cfg)
		repairFirewall(cfg)
		// Bring back anything that has died since we started, then re-read, so
		// the plan is built from what is true after the recovery rather than
		// before it.
		recoverTunnels(cfg, ts, lastAttempt)
		ts = readTunnels(cfg)
		plan := BuildPlan(cfg, ts)
		// Re-applied when the plan changes, and also when the rules it describes
		// have gone missing - a flush by another tool does not change the plan,
		// so watching the plan alone would never notice.
		sig := plan.Describe()
		if sig != lastSig || !plan.Installed() {
			if sig == lastSig {
				logf("routing rules were removed - reinstalling them")
			}
			if err := plan.Apply(cfg); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not apply routing: %v\n", err)
			} else {
				if sig != lastSig {
					logf("%s", sig)
				}
				lastSig = sig
			}
		}
		st := buildStatus(cfg, ts, plan)
		st.UpdateAvailable = upd.Available
		st.LatestVersion = upd.Latest
		curMu.Lock()
		current = st
		curMu.Unlock()
		return st
	}
	refresh()

	srv := serveStatus(cfg, func() *Status {
		curMu.Lock()
		c := current
		curMu.Unlock()
		if c == nil {
			return refresh()
		}
		return c
	})
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "warning: status API stopped: %v\n", err)
		}
	}()
	logf("status API on http://%s/status", cfg.Listen)
	if cfg.Dashboard {
		logf("dashboard on http://%s/", cfg.Listen)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if cfg.UpdateCheck && time.Since(lastUpdateCheck) >= checkEvery {
				lastUpdateCheck = time.Now()
				was := upd.Available
				upd = checkUpdate()
				// Said once, when it becomes true. An hourly reminder that a
				// release exists is nagging, not information.
				if upd.Available && !was {
					logf("a newer release is available: %s (you are running %s)", upd.Latest, Version)
				}
			}
			refresh()
		case <-stop:
			logf("shutting down - the kill switch stays armed")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = srv.Shutdown(ctx)
			cancel()
			// Deliberately NOT removing the kill switch. A supervisor
			// restarting this process must not open a window where clients
			// route straight out of the ISP connection. `stop` removes it.
			return 0
		}
	}
}

// repairFirewall puts back anything that has been removed underneath us.
//
// Every rule this installs lives in the kernel's tables, which are not ours
// alone: a firewalld or ufw reload, an `iptables-restore`, another tool's
// cleanup, or a container runtime rebuilding its chains will drop them without
// telling anyone. Reporting that in `status` is not enough - the kill switch
// failing open is precisely the case where nobody is reading status - so each
// one is checked cheaply and reinstalled the moment it goes missing.
func repairFirewall(cfg *Config) {
	if !killSwitchArmed(cfg.Clients) || !ipv6Blocked() {
		fmt.Fprintln(os.Stderr, "warning: the kill switch was removed - re-arming")
		if err := applyKillSwitch(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "error: could not re-arm the kill switch: %v\n", err)
		}
	}
	if err := applyNAT(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not restore client NAT: %v\n", err)
	}
	if err := applyDNSRedirect(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not restore the DNS redirect: %v\n", err)
	}
}

func cmdStatus(cfg *Config) int {
	tunnels := readTunnels(cfg)
	st := buildStatus(cfg, tunnels, BuildPlan(cfg, tunnels))
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(st); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if len(st.Problems) > 0 {
		return 1
	}
	return 0
}

// cmdLeakTest is the proof. Exit 0 means nothing escaped.
func cmdLeakTest(cfg *Config) int {
	fmt.Println("This drops every tunnel for around thirty seconds. Downloads will stall.")

	env, err := setupProbe(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not set up the probe: %v\n", err)
		return 1
	}
	defer teardownProbe()
	logf("probing as %s on %s", env.addr, env.bridge)

	tunnels := readTunnels(cfg)
	lt := &LeakTest{
		Probe:    env.probe,
		Progress: func(s string) { logf("%s", s) },
		Restore: func() error {
			return RestoreTunnels(cfg, readTunnels(cfg), func(p *Plan) error {
				return p.Apply(cfg)
			})
		},
	}
	leaks, err := lt.Run(cfg, tunnels)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if leaks > 0 {
		fmt.Printf("\nFAILED: %d leak(s). Client traffic escaped with every tunnel down.\n", leaks)
		return 1
	}
	fmt.Println("\nPASSED: nothing escaped with every tunnel down.")
	return 0
}

func cmdStop(cfg *Config) int {
	for _, t := range readTunnels(cfg) {
		bringDown(t)
	}
	(&Plan{}).flushOurRules()
	quiet(5*time.Second, "ip", "route", "flush", "table", fmt.Sprint(BondTable))
	for i := 0; i < MaxTunnels; i++ {
		quiet(5*time.Second, "ip", "route", "flush", "table", fmt.Sprint(PinTableBase+i))
	}
	removeDNSRedirect(cfg)
	removeNAT(cfg)
	removeKillSwitch(cfg)
	return 0
}

func logf(format string, args ...any) {
	fmt.Printf("%s  %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}
