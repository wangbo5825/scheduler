package scheduler

import (
	"time"
)

// nextRun returns the next tick after now.
//
// When a cron schedule is configured it is authoritative and its Next method
// decides the next run (cron expressions support CRON_TZ= for timezones).
//
// Otherwise the interval grid is anchored to the Unix epoch, which produces the
// familiar wall-clock-aligned behavior for common intervals: the default 1m
// fires at :00 of every minute, 5m fires at :00/:05/:10/..., and so on.
func (s *Scheduler) nextRun(now time.Time) time.Time {
	if s.cronSchedule != nil {
		next := s.cronSchedule.Next(now)
		if !next.After(now) {
			return now.Add(time.Second)
		}
		return next
	}

	interval := time.Duration(s.Interval)
	if interval <= 0 {
		interval = defaultInterval
	}
	intervalNanos := int64(interval)
	nowNanos := now.UnixNano()

	return time.Unix(0, (nowNanos/intervalNanos+1)*intervalNanos)
}
