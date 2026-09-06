package anonymous

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

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
	values, err := markConnectedScript.Run(ctx, s.client, []string{s.runKey(runID), s.verificationKey()},
		now.UnixMilli(), runID, proxyName, heartbeatLease.Milliseconds()).Slice()
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
	values, err := runOperationScript.Run(ctx, s.client, []string{s.runKey(runID), s.verificationKey()},
		now.UnixMilli(), action, credentialID, s.hashCredential(credentialToken), proxyName, heartbeatLease.Milliseconds()).Slice()
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

func (s *Store) ConfirmReleased(ctx context.Context, runID, proxyName string) error {
	if !validRandomIdentifier(runID, "anr_", 16) || !validRandomIdentifier(proxyName, "anon_", 16) {
		return ErrInvalidRequest
	}
	now, err := s.now()
	if err != nil {
		return err
	}
	values, err := confirmReleasedScript.Run(ctx, s.client, []string{s.runKey(runID), s.verificationKey()},
		now.UnixMilli(), runID, proxyName, replayLifetime.Milliseconds(),
		s.prefix+":resource:", s.prefix+":proxy:", s.prefix+":active:installation:",
		s.prefix+":active:network:", s.prefix+":replay:").Slice()
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
	entries, err := s.client.ZRangeByScoreWithScores(ctx, s.verificationKey(), &redis.ZRangeBy{
		Min: "-inf", Max: strconv.FormatInt(now.UnixMilli(), 10), Offset: 0, Count: limit,
	}).Result()
	if err != nil {
		return nil, ErrUnavailable
	}
	if len(entries) == 0 {
		return []VerificationItem{}, nil
	}
	pipe := s.client.Pipeline()
	commands := make([]*redis.MapStringStringCmd, len(entries))
	for index, entry := range entries {
		runKey, ok := entry.Member.(string)
		if !ok || !stringsHasKeyPrefix(runKey, s.prefix+":run:") {
			return nil, ErrInvalidState
		}
		commands[index] = pipe.HGetAll(ctx, runKey)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, ErrUnavailable
	}
	items := make([]VerificationItem, len(entries))
	for index, command := range commands {
		fields := command.Val()
		if len(fields) == 0 || fields["run_id"] == "" || fields["proxy_name"] == "" || fields["public_endpoint"] == "" {
			return nil, ErrInvalidState
		}
		items[index] = VerificationItem{
			RunID: fields["run_id"], ProxyName: fields["proxy_name"], PublicEndpoint: fields["public_endpoint"],
			Protocol: Protocol(fields["protocol"]), DueAt: time.UnixMilli(int64(entries[index].Score)).UTC(),
		}
	}
	return items, nil
}

func stringsHasKeyPrefix(value, prefix string) bool {
	return len(value) == len(prefix)+64 && value[:len(prefix)] == prefix
}
