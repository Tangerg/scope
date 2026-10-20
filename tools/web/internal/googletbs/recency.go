package googletbs

import "github.com/Tangerg/scope/tools/web"

// Google tbs (to-be-searched) query-date-range codes selected by web.Recency.
const (
	tbsPastHour  = "qdr:h"
	tbsPastDay   = "qdr:d"
	tbsPastWeek  = "qdr:w"
	tbsPastMonth = "qdr:m"
	tbsPastYear  = "qdr:y"
)

func FromRecency(r web.Recency) string {
	switch r {
	case web.RecencyHour:
		return tbsPastHour
	case web.RecencyDay:
		return tbsPastDay
	case web.RecencyWeek:
		return tbsPastWeek
	case web.RecencyMonth:
		return tbsPastMonth
	case web.RecencyYear:
		return tbsPastYear
	}
	return ""
}
