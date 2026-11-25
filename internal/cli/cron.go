package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/masterdaweb/mw-cli/internal/api"
)

// cronSettings is what a cron job is created with: its schedule, and the time
// zone it is read in when one is given. The API refuses a cron service with no
// schedule, so the CLI asks for it before sending anything; and a schedule on
// any other kind is a mistake worth saying, not a value to drop.
func cronSettings(kind, schedule, timezone string) (map[string]interface{}, error) {
	schedule, timezone = strings.TrimSpace(schedule), strings.TrimSpace(timezone)
	if !strings.EqualFold(kind, "cron") {
		if schedule != "" || timezone != "" {
			return nil, errors.New("--schedule and --timezone are for --kind cron")
		}
		return nil, nil
	}
	if schedule == "" {
		return nil, errors.New("a cron job runs on a schedule: pass --schedule, e.g. --schedule '*/15 * * * *'")
	}
	s := map[string]interface{}{"cron_schedule": schedule}
	if timezone != "" {
		s["cron_timezone"] = timezone
	}
	return s, nil
}

// cronScheduleText is a cron job's schedule with the time zone it is read in:
// "*/2 * * * * (America/Sao_Paulo)".
func cronScheduleText(s api.ServiceSettings) string {
	if s.CronSchedule == nil || strings.TrimSpace(*s.CronSchedule) == "" {
		return "—"
	}
	if s.CronTimezone == "" {
		return *s.CronSchedule
	}
	return fmt.Sprintf("%s (%s)", *s.CronSchedule, s.CronTimezone)
}
