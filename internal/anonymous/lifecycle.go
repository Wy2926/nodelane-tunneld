package anonymous

import (
	"context"
	"strconv"
	"time"
)

// AuthorizeLogin authenticates the run credential before frps creates or reuses
// a control connection. Proxy-bound callbacks must use Authorize instead.
func (s *Store) AuthorizeLogin(ctx context.Context, runID, credentialToken string) (Run, error) {
	run, code, err := s.runOperation(ctx, "authorize", runID, credentialToken, "")
	if err != nil {
		return Run{}, err
	}
	if code == -4 {
		return Run{}, ErrRunExpired
	}
	return run, mapRunOperationCode(code)
}

func (s *Store) Authorize(ctx context.Context, runID, credentialToken, proxyName string) (Run, error) {
	if !validRandomIdentifier(proxyName, "anon_", 16) {
		return Run{}, ErrInvalidCredential
	}
	run, code, err := s.runOperation(ctx, "authorize", runID, credentialToken, proxyName)
	if err != nil {
		return Run{}, err
	}
	if code == -4 {
		return Run{}, ErrRunExpired
	}
	return run, mapRunOperationCode(code)
}

func (s *Store) Heartbeat(ctx context.Context, runID, credentialToken string) (HeartbeatResult, error) {
	run, code, err := s.runOperation(ctx, "heartbeat", runID, credentialToken, "")
	if err != nil {
		return HeartbeatResult{}, err
	}
	if code != 1 {
		return HeartbeatResult{}, mapRunOperationCode(code)
	}
	return HeartbeatResult{
		RunID: run.RunID, DesiredState: run.DesiredState,
		LeaseExpiresAt: run.LeaseExpiresAt, HardExpiresAt: run.HardExpiresAt,
	}, nil
}

func (s *Store) RequestStop(ctx context.Context, runID, credentialToken string) (Run, error) {
	run, code, err := s.runOperation(ctx, "stop", runID, credentialToken, "")
	if err != nil {
		return Run{}, err
	}
	return run, mapRunOperationCode(code)
}

func (s *Store) MarkConnected(ctx context.Context, runID, proxyName string) (Run, error) {
	if !validRandomIdentifier(runID, "anr_", 16) || !validRandomIdentifier(proxyName, "anon_", 16) {
		return Run{}, ErrInvalidRequest
	}
	now, err := s.now()
	if err != nil {
		return Run{}, err
	}
	values, err := markConnectedScript.Run(ctx, s.client, []string{s.runKey(runID), s.verificationKey(), s.readyKey()},
		now.UnixMilli(), runID, proxyName, heartbeatLease.Milliseconds(),
		s.prefix+":active:installation:", s.prefix+":active:network:").Slice()
	if err != nil || len(values) == 0 {
		return Run{}, ErrUnavailable
	}
	code, ok := parseInt64(values[0])
	if !ok {
		return Run{}, ErrUnavailable
	}
	if code != 1 {
		switch code {
		case -1:
			return Run{}, ErrRunNotFound
		case -2:
			return Run{}, ErrInvalidState
		case -3, -4:
			return Run{}, ErrRunStopped
		case -5:
			return Run{}, ErrResourcesUnverified
		default:
			return Run{}, ErrUnavailable
		}
	}
	run, err := decodeRun(values)
	if err != nil || run.RunID != runID || run.ProxyName != proxyName {
		return Run{}, ErrInvalidState
	}
	return run, nil
}

func (s *Store) runOperation(ctx context.Context, action, runID, credentialToken, proxyName string) (Run, int64, error) {
	if !validRandomIdentifier(runID, "anr_", 16) {
		return Run{}, 0, ErrInvalidCredential
	}
	credentialID, _, valid := parseCredential(credentialToken)
	if !valid {
		return Run{}, 0, ErrInvalidCredential
	}
	now, err := s.now()
	if err != nil {
		return Run{}, 0, err
	}
	values, err := runOperationScript.Run(ctx, s.client, []string{s.runKey(runID), s.verificationKey(), s.readyKey()},
		now.UnixMilli(), action, credentialID, s.hashCredential(credentialToken), proxyName, runID, heartbeatLease.Milliseconds(),
		s.prefix+":active:installation:", s.prefix+":active:network:").Slice()
	if err != nil {
		return Run{}, 0, ErrUnavailable
	}
	if len(values) == 0 {
		return Run{}, 0, ErrUnavailable
	}
	code, ok := parseInt64(values[0])
	if !ok {
		return Run{}, 0, ErrUnavailable
	}
	if code != 1 {
		return Run{}, code, nil
	}
	run, err := decodeRun(values)
	if err != nil || run.RunID != runID {
		return Run{}, 0, ErrInvalidState
	}
	return run, code, nil
}

func mapRunOperationCode(code int64) error {
	switch code {
	case 1:
		return nil
	case -1, -2:
		return ErrInvalidCredential
	case -3, -4, -5:
		return ErrRunStopped
	case -6:
		return ErrInvalidState
	case -7:
		return ErrResourcesUnverified
	default:
		return ErrUnavailable
	}
}

func decodeRun(values []any) (Run, error) {
	if len(values) != 11 {
		return Run{}, ErrInvalidState
	}
	strings := make([]string, 10)
	for index := 1; index < len(values); index++ {
		value, ok := asString(values[index])
		if !ok {
			return Run{}, ErrInvalidState
		}
		strings[index-1] = value
	}
	createdAt, err := strconv.ParseInt(strings[6], 10, 64)
	if err != nil {
		return Run{}, ErrInvalidState
	}
	connectDeadline, err := strconv.ParseInt(strings[7], 10, 64)
	if err != nil {
		return Run{}, ErrInvalidState
	}
	leaseExpires, err := strconv.ParseInt(strings[8], 10, 64)
	if err != nil {
		return Run{}, ErrInvalidState
	}
	hardExpires, err := strconv.ParseInt(strings[9], 10, 64)
	if err != nil {
		return Run{}, ErrInvalidState
	}
	run := Run{
		RunID: strings[0], ProxyName: strings[1], PublicEndpoint: strings[2], Protocol: Protocol(strings[3]),
		State: State(strings[4]), DesiredState: DesiredState(strings[5]), CreatedAt: time.UnixMilli(createdAt).UTC(),
		ConnectDeadlineAt: time.UnixMilli(connectDeadline).UTC(), HardExpiresAt: time.UnixMilli(hardExpires).UTC(),
	}
	if leaseExpires != 0 {
		run.LeaseExpiresAt = time.UnixMilli(leaseExpires).UTC()
	}
	if !validRandomIdentifier(run.RunID, "anr_", 16) || !validRandomIdentifier(run.ProxyName, "anon_", 16) ||
		run.PublicEndpoint == "" || run.Protocol != ProtocolHTTP && run.Protocol != ProtocolTCP && run.Protocol != ProtocolUDP ||
		run.State != StateReserved && run.State != StateOnline && run.State != StateStopping && run.State != StateVerifying && run.State != StateReleased ||
		run.DesiredState != DesiredRunning && run.DesiredState != DesiredStopped ||
		!run.ConnectDeadlineAt.Equal(run.CreatedAt.Add(connectLifetime)) || !run.HardExpiresAt.Equal(run.CreatedAt.Add(hardLifetime)) ||
		!run.LeaseExpiresAt.IsZero() && run.LeaseExpiresAt.After(run.HardExpiresAt) {
		return Run{}, ErrInvalidState
	}
	return run, nil
}

func (s *Store) ConfirmReleased(ctx context.Context, evidence ReleaseEvidence) error {
	if !validRandomIdentifier(evidence.RunID, "anr_", 16) || !validRandomIdentifier(evidence.ProxyName, "anon_", 16) {
		return ErrInvalidRequest
	}
	switch evidence.Kind {
	case ReleaseEvidenceOfflineSample:
		if !evidence.ObservedOffline || !evidence.SampleAvailable || evidence.CurrentConnections != 0 || evidence.ConfirmedNeverRegistered {
			return ErrReleaseUnconfirmed
		}
	case ReleaseEvidenceNeverRegistered:
		if !evidence.ConfirmedNeverRegistered || evidence.ObservedOffline || evidence.SampleAvailable || evidence.CurrentConnections != 0 {
			return ErrReleaseUnconfirmed
		}
	default:
		return ErrReleaseUnconfirmed
	}
	now, err := s.now()
	if err != nil {
		return err
	}
	values, err := confirmReleasedScript.Run(ctx, s.client, []string{s.runKey(evidence.RunID), s.verificationKey()},
		now.UnixMilli(), evidence.RunID, evidence.ProxyName, replayLifetime.Milliseconds(),
		s.prefix+":resource:", s.prefix+":proxy:", s.prefix+":active:installation:",
		s.prefix+":active:network:", s.prefix+":replay:", string(evidence.Kind)).Slice()
	if err != nil {
		return ErrUnavailable
	}
	if len(values) != 1 {
		return ErrUnavailable
	}
	code, ok := parseInt64(values[0])
	if !ok {
		return ErrUnavailable
	}
	switch code {
	case 1:
		return nil
	case -1:
		return ErrRunNotFound
	case -2:
		return ErrInvalidRequest
	case -3:
		return ErrInvalidState
	case -4:
		return ErrInvalidState
	case -5:
		return ErrReleaseUnconfirmed
	default:
		return ErrUnavailable
	}
}

func (s *Store) PendingVerification(ctx context.Context, limit int64) ([]VerificationItem, error) {
	if limit < 1 || limit > 1000 {
		return nil, ErrInvalidRequest
	}
	now, err := s.now()
	if err != nil {
		return nil, err
	}
	items := make([]VerificationItem, 0, limit)
	corruptions := 0
	examined, budget := int64(0), 2*limit
	for int64(len(items)) < limit && examined < budget {
		remaining := min(limit-int64(len(items)), budget-examined)
		values, err := claimVerificationScript.Run(ctx, s.client, []string{s.verificationKey()},
			now.UnixMilli(), verificationClaimLease.Milliseconds(), remaining).Slice()
		if err != nil {
			return nil, ErrUnavailable
		}
		if len(values) == 0 {
			break
		}
		if len(values)%2 != 0 {
			return nil, ErrInvalidState
		}
		examined += int64(len(values) / 2)
		claimUntil := now.Add(verificationClaimLease).UnixMilli()
		for index := 0; index < len(values); index += 2 {
			runKey, keyOK := asString(values[index])
			dueAt, dueOK := parseInt64(values[index+1])
			if !keyOK || !dueOK {
				return nil, ErrInvalidState
			}
			item, reason, err := s.inspectVerificationItem(ctx, runKey, dueAt, now)
			if err != nil {
				return nil, err
			}
			if reason == "" {
				if item.RunID != "" {
					items = append(items, item)
				}
				continue
			}
			quarantined, err := s.quarantineVerificationItem(ctx, runKey, claimUntil, now.UnixMilli(), reason)
			if err != nil {
				return nil, err
			}
			if quarantined {
				corruptions++
			}
		}
	}
	if corruptions != 0 {
		return items, &VerificationCorruptionError{Count: corruptions}
	}
	return items, nil
}

func (s *Store) inspectVerificationItem(ctx context.Context, runKey string, dueAtMS int64, now time.Time) (VerificationItem, string, error) {
	if !stringsHasKeyPrefix(runKey, s.prefix+":run:") {
		return VerificationItem{}, "invalid_run_key", nil
	}
	values, err := inspectVerificationScript.Run(ctx, s.client, []string{runKey}).Slice()
	if err != nil || len(values) == 0 {
		return VerificationItem{}, "", ErrUnavailable
	}
	code, ok := parseInt64(values[0])
	if !ok {
		return VerificationItem{}, "", ErrUnavailable
	}
	if code == -1 {
		return VerificationItem{}, "missing_run_hash", nil
	}
	if code != 1 || len(values)%2 != 1 {
		return VerificationItem{}, "", ErrUnavailable
	}
	fields := make(map[string]string, (len(values)-1)/2)
	for index := 1; index < len(values); index += 2 {
		name, nameOK := asString(values[index])
		value, valueOK := asString(values[index+1])
		if !nameOK || !valueOK {
			return VerificationItem{}, "invalid_hash_encoding", nil
		}
		fields[name] = value
	}
	run, err := decodeRunFields(fields)
	if err != nil || s.runKey(run.RunID) != runKey || !s.validPublicEndpoint(run.Protocol, run.PublicEndpoint) {
		return VerificationItem{}, "invalid_run_identity", nil
	}
	everConnected := fields["ever_connected"]
	if everConnected != "0" && everConnected != "1" || run.State == StateReserved && everConnected != "0" || run.State == StateOnline && everConnected != "1" {
		return VerificationItem{}, "invalid_connection_history", nil
	}
	switch run.State {
	case StateReserved:
		if run.DesiredState != DesiredRunning {
			return VerificationItem{}, "invalid_reserved_state", nil
		}
		if now.Before(run.ConnectDeadlineAt) && now.Before(run.HardExpiresAt) {
			return VerificationItem{}, "", nil
		}
	case StateOnline:
		if run.DesiredState != DesiredRunning || run.LeaseExpiresAt.IsZero() {
			return VerificationItem{}, "invalid_online_state", nil
		}
		if now.Before(run.LeaseExpiresAt) && now.Before(run.HardExpiresAt) {
			// A heartbeat or connection callback may renew after this item was claimed.
			return VerificationItem{}, "", nil
		}
	case StateStopping, StateVerifying:
		if run.DesiredState != DesiredStopped {
			return VerificationItem{}, "invalid_stopping_state", nil
		}
	default:
		return VerificationItem{}, "terminal_run_in_verification", nil
	}
	return VerificationItem{
		RunID: run.RunID, ProxyName: run.ProxyName, PublicEndpoint: run.PublicEndpoint,
		Protocol: run.Protocol, DueAt: time.UnixMilli(dueAtMS).UTC(),
	}, "", nil
}

func (s *Store) quarantineVerificationItem(ctx context.Context, runKey string, claimUntil, now int64, reason string) (bool, error) {
	values, err := quarantineVerificationScript.Run(ctx, s.client, []string{
		s.verificationKey(), s.verificationQuarantineKey(), s.verificationQuarantineDetailsKey(), s.readyKey(),
	}, runKey, claimUntil, now, reason).Slice()
	if err != nil || len(values) != 1 {
		return false, ErrUnavailable
	}
	code, ok := parseInt64(values[0])
	if !ok || code != 0 && code != 1 {
		return false, ErrUnavailable
	}
	return code == 1, nil
}

func decodeRunFields(fields map[string]string) (Run, error) {
	return decodeRun([]any{
		int64(1), fields["run_id"], fields["proxy_name"], fields["public_endpoint"], fields["protocol"],
		fields["state"], fields["desired_state"], fields["created_at"], fields["connect_deadline_at"],
		fields["lease_expires_at"], fields["hard_expires_at"],
	})
}

func stringsHasKeyPrefix(value, prefix string) bool {
	return len(value) == len(prefix)+64 && value[:len(prefix)] == prefix
}
