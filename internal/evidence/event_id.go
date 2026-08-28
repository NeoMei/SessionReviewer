package evidence

import "regexp"

const EventIDPattern = `^ev-[0-9a-f]{12}$`

var eventID = regexp.MustCompile(EventIDPattern)

// ValidEventID reports whether value has the canonical identity shape emitted
// by Extractor.eventID.
func ValidEventID(value string) bool { return eventID.MatchString(value) }
