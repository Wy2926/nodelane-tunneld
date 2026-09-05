package domain

import "time"

func (r Route) RecoverableAt(now time.Time) bool {
	return r.Status == RouteDeleted &&
		r.NameReleasedAt == nil &&
		r.RecoverableUntil != nil &&
		now.Before(*r.RecoverableUntil)
}

func (r Run) OccupiesActiveSlot() bool {
	return r.Status == RunStarting || r.Status == RunOnline || r.Status == RunStopping
}

// AllowsConnectionAt checks lifecycle eligibility only. The caller must verify
// the bearer secret hash separately as part of the same authorization operation.
func (r Run) AllowsConnectionAt(route Route, credential RunCredential, now time.Time) bool {
	if route.ID == "" || r.ID == "" || r.RouteID == "" || credential.ID == "" || credential.RunID == "" {
		return false
	}
	if route.ID != r.RouteID || credential.RunID != r.ID {
		return false
	}
	if route.Status != RouteActive || r.DesiredState != DesiredRunning || credential.RevokedAt != nil {
		return false
	}
	if r.Status != RunStarting && r.Status != RunOnline {
		return false
	}

	if r.ConnectedAt == nil {
		return r.Status == RunStarting && now.Before(r.ConnectDeadlineAt)
	}
	return r.LeaseExpiresAt != nil && now.Before(*r.LeaseExpiresAt)
}
