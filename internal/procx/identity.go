package procx

// Identity returns an opaque token naming the exact process that currently
// holds pid — the strongest form of §12.4's PID-reuse guard (issue #149).
// Returns ErrProcessGone when no process holds the PID.
//
// The contract is **compare, never parse**. Recovery journals the token at
// spawn, reads it again at startup and kills the PID only on a byte-for-byte
// match; nothing anywhere derives a time, a duration or an ordering from it.
// That is what lets each OS contribute whatever value it actually keeps stable
// for the lifetime of a process, rather than a lowest common denominator:
//
//   - **Linux** (`linux1:<boot id>:<start ticks>`) — `/proc/<pid>/stat` field 22
//     exactly as the kernel spells it (USER_HZ = 100, so 10 ms precision),
//     joined with `/proc/sys/kernel/random/boot_id`. The raw tick count is
//     deliberately not converted to an absolute instant via `btime`: kept as a
//     count since boot it cannot move under an NTP step or a suspend/resume,
//     and the boot id makes a reboot a guaranteed mismatch rather than an
//     arithmetic coincidence.
//   - **macOS** (`darwin1:<sec>.<usec>`) — `kinfo_proc.Proc.P_starttime`,
//     1 µs precision, stamped once at fork and never revised by a later clock
//     adjustment.
//   - **Windows** (`windows1:<filetime>`) — the creation `FILETIME` from
//     `GetProcessTimes` in its raw 100 ns unit. The unit is finer than the
//     value: the system clock updates on a tick of roughly 15 ms, so two
//     processes created in the same tick share a stamp. For one PID that means
//     a collision needs a reuse *within a single tick*.
//
// Every token carries a scheme prefix so a future change to a format cannot be
// mistaken for a match: an old token simply stops comparing equal, which fails
// the safe way (§12.4's "cannot prove, do not kill").
func Identity(pid int) (string, error) { return identity(pid) }
