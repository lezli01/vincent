//go:build linux

package procx

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// userHZ is the fixed clock-tick unit of /proc timestamps. The kernel
// exports process times in USER_HZ, which is 100 on Linux regardless of the
// scheduler's actual tick rate.
const userHZ = 100

// StartTime reports when the process with the given PID started — the
// crash-recovery guard against PID reuse (spec §12.4): a recorded PID is
// only killed when its start time matches the journaled spawn time.
// Returns ErrProcessGone when no such process exists.
func StartTime(pid int) (time.Time, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, ErrProcessGone
		}
		return time.Time{}, fmt.Errorf("read process %d stat: %w", pid, err)
	}
	// Field 2 (comm) may contain spaces and parentheses; everything after
	// the last ')' is well-formed space-separated fields starting at field 3.
	i := bytes.LastIndexByte(data, ')')
	if i < 0 || i+2 >= len(data) {
		return time.Time{}, fmt.Errorf("process %d stat: malformed", pid)
	}
	fields := strings.Fields(string(data[i+2:]))
	const starttimeField = 22 - 3 // starttime is field 22; fields[0] is field 3
	if len(fields) <= starttimeField {
		return time.Time{}, fmt.Errorf("process %d stat: too few fields", pid)
	}
	ticks, err := strconv.ParseUint(fields[starttimeField], 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("process %d starttime: %w", pid, err)
	}
	boot, err := bootTime()
	if err != nil {
		return time.Time{}, err
	}
	// Split whole seconds from sub-second ticks: multiplying the raw tick
	// count by time.Second first would overflow after ~3 years of uptime.
	sec := time.Duration(ticks/userHZ) * time.Second
	sub := time.Duration(ticks%userHZ) * (time.Second / userHZ)
	return boot.Add(sec + sub), nil
}

// bootTime reads the kernel boot time /proc process start times are
// relative to.
func bootTime() (time.Time, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, fmt.Errorf("read /proc/stat: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(line, "btime ")
		if !ok {
			continue
		}
		sec, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse btime: %w", err)
		}
		return time.Unix(sec, 0), nil
	}
	return time.Time{}, fmt.Errorf("btime not found in /proc/stat")
}
