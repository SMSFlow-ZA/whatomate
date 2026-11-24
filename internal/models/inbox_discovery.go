package models

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Queue discovery is not a workflow condition. Keeping it separate prevents a
// saved sort/date preference from becoming a silently ignored automation filter.
type InboxQueueDiscovery struct {
	Order          string     `json:"order,omitempty"`
	Timezone       string     `json:"timezone,omitempty"`
	OpenedFrom     *time.Time `json:"opened_from,omitempty"`
	OpenedBefore   *time.Time `json:"opened_before,omitempty"`
	ActivityFrom   *time.Time `json:"activity_from,omitempty"`
	ActivityBefore *time.Time `json:"activity_before,omitempty"`
}

func (d *InboxQueueDiscovery) UnmarshalJSON(data []byte) error {
	type plain InboxQueueDiscovery
	var decoded plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return fmt.Errorf("invalid queue discovery: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("invalid queue discovery: expected one object")
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("invalid queue discovery: expected an object")
	}
	*d = InboxQueueDiscovery(decoded)
	return nil
}
