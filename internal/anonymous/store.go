package anonymous

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/redis/go-redis/v9"
)

type Store struct {
	client           *redis.Client
	prefix           string
	credentialPepper []byte
	replayAEAD       cipher.AEAD
	fenceOwnerHash   string
	clock            func() time.Time
	random           io.Reader
	publicDomain     string
	tcpPorts         []uint16
	udpPorts         []uint16
}

var rawBase32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func NewStore(config Config) (*Store, error) {
	if config.Client == nil || !validPrefix(config.Prefix) || len(config.CredentialPepper) < 32 || len(config.ReplayKey) != 32 || len(config.FenceOwnerToken) < 32 ||
		hmac.Equal(config.CredentialPepper, config.ReplayKey) || hmac.Equal(config.CredentialPepper, config.FenceOwnerToken) ||
		hmac.Equal(config.ReplayKey, config.FenceOwnerToken) || !validDomain(config.PublicDomain) {
		return nil, ErrInvalidConfiguration
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.Random == nil {
		return nil, ErrInvalidConfiguration
	}
	if !validPortPool(config.TCPPorts) || !validPortPool(config.UDPPorts) {
		return nil, ErrInvalidConfiguration
	}
	block, err := aes.NewCipher(append([]byte(nil), config.ReplayKey...))
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	return &Store{
		client:           config.Client,
		prefix:           config.Prefix,
		credentialPepper: append([]byte(nil), config.CredentialPepper...),
		replayAEAD:       aead,
		fenceOwnerHash:   hashFenceOwner(config.FenceOwnerToken),
		clock:            config.Clock,
		random:           config.Random,
		publicDomain:     config.PublicDomain,
		tcpPorts:         append([]uint16(nil), config.TCPPorts...),
		udpPorts:         append([]uint16(nil), config.UDPPorts...),
	}, nil
}

func validPrefix(prefix string) bool {
	if len(prefix) < 3 || len(prefix) > 200 {
		return false
	}
	parts := strings.Split(prefix, ":")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				return false
			}
		}
	}
	return true
}

func validDomain(value string) bool {
	if value == "" || len(value) > 253 || value != strings.ToLower(value) || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func validPortPool(ports []uint16) bool {
	if len(ports) == 0 {
		return false
	}
	seen := make(map[uint16]struct{}, len(ports))
	for _, port := range ports {
		if port == 0 {
			return false
		}
		if _, duplicate := seen[port]; duplicate {
			return false
		}
		seen[port] = struct{}{}
	}
	return true
}

func hashFenceOwner(token []byte) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, "anonymous-resource-fence-owner-v1\x00")
	_, _ = hash.Write(token)
	return hex.EncodeToString(hash.Sum(nil))
}

func (s *Store) ObserveResourceFence(ctx context.Context) (ResourceFence, error) {
	values, err := observeResourceFenceScript.Run(ctx, s.client, []string{s.readyKey()}).Slice()
	if err != nil || len(values) != 4 {
		return ResourceFence{}, ErrUnavailable
	}
	code, ok := parseInt64(values[0])
	redisRunID, runOK := asString(values[1])
	revision, revisionOK := asString(values[2])
	generation, generationOK := parseInt64(values[3])
	if !ok || !runOK || !revisionOK || !generationOK || generation < 0 || !validRedisRunID(redisRunID) {
		return ResourceFence{}, ErrInvalidState
	}
	if code == -1 {
		return ResourceFence{}, ErrInvalidState
	}
	if code != 1 || revision != "" && !validRandomIdentifier(revision, "afr_", 16) {
		return ResourceFence{}, ErrUnavailable
	}
	return ResourceFence{RedisRunID: redisRunID, Revision: revision, Generation: generation}, nil
}

func (s *Store) MarkResourcesVerified(ctx context.Context, observed ResourceFence) (ResourceFence, error) {
	if !validRedisRunID(observed.RedisRunID) || observed.Generation < 0 || observed.Revision != "" && !validRandomIdentifier(observed.Revision, "afr_", 16) {
		return ResourceFence{}, ErrInvalidRequest
	}
	if err := s.validateRedisDeployment(ctx); err != nil {
		return ResourceFence{}, err
	}
	revision, err := s.randomName("afr_", 16)
	if err != nil {
		return ResourceFence{}, err
	}
	values, err := markResourcesVerifiedScript.Run(ctx, s.client, []string{s.readyKey()},
		observed.RedisRunID, observed.Revision, s.fenceOwnerHash, revision, observed.Generation).Slice()
	if err != nil || len(values) != 2 {
		return ResourceFence{}, ErrUnavailable
	}
	code, ok := parseInt64(values[0])
	currentRunID, runOK := asString(values[1])
	if !ok || !runOK || !validRedisRunID(currentRunID) {
		return ResourceFence{}, ErrUnavailable
	}
	if code == -1 || code == -2 || code == -3 {
		return ResourceFence{}, ErrFenceConflict
	}
	if code != 1 || currentRunID != observed.RedisRunID {
		return ResourceFence{}, ErrInvalidState
	}
	return ResourceFence{RedisRunID: currentRunID, Revision: revision, Generation: observed.Generation}, nil

}

func (s *Store) validateRedisDeployment(ctx context.Context) error {
	info, err := s.client.Info(ctx, "server", "replication").Result()
	if err != nil {
		return ErrUnavailable
	}
	fields := make(map[string]string)
	for _, line := range strings.Split(info, "\r\n") {
		name, value, ok := strings.Cut(line, ":")
		if ok {
			fields[name] = value
		}
	}
	if fields["redis_mode"] != "standalone" || fields["role"] != "master" || fields["connected_slaves"] != "0" {
		return ErrResourcesUnverified
	}
	configuration, err := s.client.ConfigGet(ctx, "maxmemory-policy").Result()
	if err != nil {
		return ErrUnavailable
	}
	if configuration["maxmemory-policy"] != "noeviction" {
		return ErrResourcesUnverified
	}
	return nil
}

func (s *Store) BlockAllocations(ctx context.Context) error {
	values, err := blockAllocationsScript.Run(ctx, s.client, []string{s.readyKey()}).Slice()
	if err != nil || len(values) != 1 {
		return ErrUnavailable
	}
	code, ok := parseInt64(values[0])
	if !ok || code != 1 {
		return ErrInvalidState
	}
	return nil
}

func (s *Store) Allocate(ctx context.Context, request AllocateRequest) (Allocation, error) {
	requestHash, err := hashAllocationRequest(request)
	if err != nil {
		return Allocation{}, err
	}
	now, err := s.now()
	if err != nil {
		return Allocation{}, err
	}
	indexKey := s.replayKey(request.InstallationID, request.NetworkKey, request.IdempotencyKey)
	if replayed, found, err := s.lookupReplay(ctx, indexKey, requestHash, now); found || err != nil {
		return replayed, err
	}
	for attempt := 0; attempt < maxResourceAttempts; attempt++ {
		candidate, err := s.newCandidate(request.Protocol, now)
		if err != nil {
			return Allocation{}, err
		}
		ciphertext, err := s.sealReplay(indexKey, requestHash, candidate)
		if err != nil {
			return Allocation{}, err
		}
		result, err := allocateScript.Run(ctx, s.client, []string{
			s.readyKey(), indexKey,
			s.installationActiveKey(request.InstallationID),
			s.networkActiveKey(request.NetworkKey),
			s.installationRateKey(request.InstallationID),
			s.networkRateKey(request.NetworkKey),
			s.resourceKey(candidate.Protocol, candidate.PublicEndpoint),
			s.runKey(candidate.RunID), s.verificationKey(), s.proxyKey(candidate.ProxyName),
		},
			now.UnixMilli(), now.Add(connectLifetime).UnixMilli(), now.Add(hardLifetime).UnixMilli(), now.Add(replayLifetime).UnixMilli(),
			rateWindow.Milliseconds(), installationActiveMax, networkActiveMax, installationRateMax, networkRateMax,
			candidate.RunID, requestHash, ciphertext, candidate.credentialID, s.hashCredential(candidate.CredentialToken),
			candidate.ProxyName, string(candidate.Protocol), candidate.PublicEndpoint,
			s.digest(request.InstallationID), s.digest(request.NetworkKey), now.UnixMilli(), replayLifetime.Milliseconds(),
			s.prefix+":run:",
		).Slice()
		if err != nil {
			return Allocation{}, ErrUnavailable
		}
		allocation, retry, err := s.decodeAllocateResult(indexKey, requestHash, now, result)
		if retry {
			continue
		}
		return allocation, err
	}
	return Allocation{}, ErrResourceUnavailable
}

func (s *Store) lookupReplay(ctx context.Context, key, requestHash string, now time.Time) (Allocation, bool, error) {
	values, err := lookupReplayScript.Run(ctx, s.client, []string{s.readyKey(), key, s.verificationKey()},
		now.UnixMilli(), requestHash, s.prefix+":run:").Slice()
	if err != nil || len(values) == 0 {
		return Allocation{}, false, ErrUnavailable
	}
	code, ok := parseInt64(values[0])
	if !ok {
		return Allocation{}, false, ErrUnavailable
	}
	if code == 0 {
		return Allocation{}, false, nil
	}
	allocation, retry, err := s.decodeAllocateResult(key, requestHash, now, values)
	if retry {
		return Allocation{}, false, ErrInvalidState
	}
	if err != nil {
		return Allocation{}, true, err
	}
	return allocation, true, nil
}

type allocationCandidate struct {
	Allocation
	credentialID string
}

func (s *Store) newCandidate(protocol Protocol, now time.Time) (allocationCandidate, error) {
	runID, err := s.randomName("anr_", 16)
	if err != nil {
		return allocationCandidate{}, err
	}
	proxyName, err := s.randomName("anon_", 16)
	if err != nil {
		return allocationCandidate{}, err
	}
	credentialID, err := s.randomName("nac_", 16)
	if err != nil {
		return allocationCandidate{}, err
	}
	secret, err := s.randomBytes(32)
	if err != nil {
		return allocationCandidate{}, err
	}
	endpoint, err := s.newPublicEndpoint(protocol)
	if err != nil {
		return allocationCandidate{}, err
	}
	return allocationCandidate{
		Allocation: Allocation{
			RunID: runID, ProxyName: proxyName, PublicEndpoint: endpoint,
			CredentialToken: credentialID + "." + base64.RawURLEncoding.EncodeToString(secret),
			Protocol:        protocol, CreatedAt: now,
			ConnectDeadlineAt: now.Add(connectLifetime), HardExpiresAt: now.Add(hardLifetime),
		},
		credentialID: credentialID,
	}, nil
}

func (s *Store) newPublicEndpoint(protocol Protocol) (string, error) {
	switch protocol {
	case ProtocolHTTP:
		label, err := s.randomName("anon-", 16)
		if err != nil {
			return "", err
		}
		return label + "." + s.publicDomain, nil
	case ProtocolTCP:
		port, err := s.randomPort(s.tcpPorts)
		if err != nil {
			return "", err
		}
		return s.publicDomain + ":" + strconv.Itoa(int(port)), nil
	case ProtocolUDP:
		port, err := s.randomPort(s.udpPorts)
		if err != nil {
			return "", err
		}
		return s.publicDomain + ":" + strconv.Itoa(int(port)), nil
	default:
		return "", ErrInvalidRequest
	}
}

func (s *Store) validPublicEndpoint(protocol Protocol, endpoint string) bool {
	if protocol == ProtocolHTTP {
		label, ok := strings.CutSuffix(endpoint, "."+s.publicDomain)
		return ok && validRandomIdentifier(label, "anon-", 16)
	}
	ports := s.tcpPorts
	if protocol == ProtocolUDP {
		ports = s.udpPorts
	} else if protocol != ProtocolTCP {
		return false
	}
	host, encodedPort, err := net.SplitHostPort(endpoint)
	if err != nil || host != s.publicDomain {
		return false
	}
	port, err := strconv.ParseUint(encodedPort, 10, 16)
	if err != nil || strconv.FormatUint(port, 10) != encodedPort {
		return false
	}
	for _, allowed := range ports {
		if uint16(port) == allowed {
			return true
		}
	}
	return false
}

func (s *Store) randomPort(ports []uint16) (uint16, error) {
	if len(ports) == 0 {
		return 0, ErrResourceUnavailable
	}
	random, err := s.randomBytes(8)
	if err != nil {
		return 0, err
	}
	var value uint64
	for _, b := range random {
		value = value<<8 | uint64(b)
	}
	return ports[value%uint64(len(ports))], nil
}

func (s *Store) randomName(prefix string, bytes int) (string, error) {
	random, err := s.randomBytes(bytes)
	if err != nil {
		return "", err
	}
	return prefix + strings.ToLower(rawBase32.EncodeToString(random)), nil
}

func (s *Store) randomBytes(size int) ([]byte, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(s.random, value); err != nil {
		return nil, ErrUnavailable
	}
	return value, nil
}

func hashAllocationRequest(request AllocateRequest) (string, error) {
	if !validOpaqueInput(request.InstallationID, 256) || !validNetworkKey(request.NetworkKey) || !validOpaqueInput(request.IdempotencyKey, 256) ||
		!validOpaqueInput(request.LocalHost, 255) || request.LocalPort == 0 ||
		request.Protocol != ProtocolHTTP && request.Protocol != ProtocolTCP && request.Protocol != ProtocolUDP {
		return "", ErrInvalidRequest
	}
	canonical := struct {
		Version   int      `json:"version"`
		Protocol  Protocol `json:"protocol"`
		LocalHost string   `json:"local_host"`
		LocalPort uint16   `json:"local_port"`
	}{1, request.Protocol, request.LocalHost, request.LocalPort}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", ErrInvalidRequest
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

func validOpaqueInput(value string, max int) bool {
	if value == "" || len(value) > max || !utf8.ValidString(value) {
		return false
	}
	for _, c := range value {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

func validNetworkKey(value string) bool {
	if addr, err := netip.ParseAddr(value); err == nil {
		return addr.Is4() && !addr.IsUnspecified() && !addr.IsMulticast() && addr.String() == value
	}
	prefix, err := netip.ParsePrefix(value)
	if err != nil {
		return false
	}
	addr := prefix.Addr()
	return addr.Is6() && addr.Zone() == "" && !addr.IsUnspecified() && !addr.IsMulticast() && !addr.IsLinkLocalUnicast() &&
		prefix.Bits() == 64 && prefix == prefix.Masked() && prefix.String() == value
}

func validRedisRunID(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func (s *Store) now() (time.Time, error) {
	now := s.clock()
	if now.IsZero() || now.UnixMilli() < 0 {
		return time.Time{}, ErrInvalidConfiguration
	}
	return now.UTC().Truncate(time.Millisecond), nil
}

func (s *Store) hashCredential(token string) string {
	hash := hmac.New(sha256.New, s.credentialPepper)
	_, _ = hash.Write([]byte(token))
	return hex.EncodeToString(hash.Sum(nil))
}

func (s *Store) digest(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

func (s *Store) compositeDigest(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = fmt.Fprintf(hash, "%d:", len(value))
		_, _ = io.WriteString(hash, value)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (s *Store) readyKey() string        { return s.prefix + ":resources:verified" }
func (s *Store) verificationKey() string { return s.prefix + ":verification" }
func (s *Store) verificationQuarantineKey() string {
	return s.prefix + ":verification:quarantine"
}
func (s *Store) verificationQuarantineDetailsKey() string {
	return s.prefix + ":verification:quarantine:details"
}
func (s *Store) runKey(runID string) string { return s.prefix + ":run:" + s.digest(runID) }
func (s *Store) replayKey(installationID, networkKey, idempotencyKey string) string {
	return s.prefix + ":replay:" + s.compositeDigest(installationID, networkKey, idempotencyKey)
}
func (s *Store) installationActiveKey(value string) string {
	return s.prefix + ":active:installation:" + s.digest(value)
}
func (s *Store) networkActiveKey(value string) string {
	return s.prefix + ":active:network:" + s.digest(value)
}
func (s *Store) installationRateKey(value string) string {
	return s.prefix + ":rate:installation:" + s.digest(value)
}
func (s *Store) networkRateKey(value string) string {
	return s.prefix + ":rate:network:" + s.digest(value)
}
func (s *Store) resourceKey(protocol Protocol, endpoint string) string {
	return s.prefix + ":resource:" + string(protocol) + ":" + s.digest(endpoint)
}
func (s *Store) proxyKey(proxyName string) string {
	return s.prefix + ":proxy:" + s.digest(proxyName)
}

type replayPayload struct {
	RunID             string    `json:"run_id"`
	ProxyName         string    `json:"proxy_name"`
	PublicEndpoint    string    `json:"public_endpoint"`
	CredentialToken   string    `json:"credential_token"`
	Protocol          Protocol  `json:"protocol"`
	CreatedAt         time.Time `json:"created_at"`
	ConnectDeadlineAt time.Time `json:"connect_deadline_at"`
	HardExpiresAt     time.Time `json:"hard_expires_at"`
}

type replayAAD struct {
	Kind        string `json:"kind"`
	ReplayKey   string `json:"replay_key"`
	RequestHash string `json:"request_hash"`
	RunID       string `json:"run_id"`
	ExpiresAtMS int64  `json:"expires_at_ms"`
}

func (s *Store) sealReplay(key, requestHash string, candidate allocationCandidate) ([]byte, error) {
	expires := candidate.CreatedAt.Add(replayLifetime).UnixMilli()
	aad, err := json.Marshal(replayAAD{"anonymous-allocation-v1", key, requestHash, candidate.RunID, expires})
	if err != nil {
		return nil, ErrInvalidState
	}
	payload, err := json.Marshal(replayPayload{
		RunID: candidate.RunID, ProxyName: candidate.ProxyName, PublicEndpoint: candidate.PublicEndpoint,
		CredentialToken: candidate.CredentialToken, Protocol: candidate.Protocol, CreatedAt: candidate.CreatedAt,
		ConnectDeadlineAt: candidate.ConnectDeadlineAt, HardExpiresAt: candidate.HardExpiresAt,
	})
	if err != nil {
		return nil, ErrInvalidState
	}
	nonce, err := s.randomBytes(s.replayAEAD.NonceSize())
	if err != nil {
		return nil, err
	}
	return s.replayAEAD.Seal(nonce, nonce, payload, aad), nil
}

func (s *Store) openReplay(key, requestHash, runID string, expiresMS int64, ciphertext []byte) (Allocation, error) {
	nonceSize := s.replayAEAD.NonceSize()
	if len(ciphertext) < nonceSize+s.replayAEAD.Overhead() || len(ciphertext) > 1<<20 {
		return Allocation{}, ErrInvalidState
	}
	aad, err := json.Marshal(replayAAD{"anonymous-allocation-v1", key, requestHash, runID, expiresMS})
	if err != nil {
		return Allocation{}, ErrInvalidState
	}
	plaintext, err := s.replayAEAD.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], aad)
	if err != nil {
		return Allocation{}, ErrInvalidState
	}
	var payload replayPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return Allocation{}, ErrInvalidState
	}
	if payload.RunID != runID || payload.ProxyName == "" || payload.PublicEndpoint == "" || payload.CredentialToken == "" ||
		payload.CreatedAt.UnixMilli()+replayLifetime.Milliseconds() != expiresMS ||
		payload.ConnectDeadlineAt.UnixMilli() != payload.CreatedAt.UnixMilli()+connectLifetime.Milliseconds() ||
		payload.HardExpiresAt.UnixMilli() != payload.CreatedAt.UnixMilli()+hardLifetime.Milliseconds() {
		return Allocation{}, ErrInvalidState
	}
	if _, _, ok := parseCredential(payload.CredentialToken); !ok {
		return Allocation{}, ErrInvalidState
	}
	return Allocation{
		RunID: payload.RunID, ProxyName: payload.ProxyName, PublicEndpoint: payload.PublicEndpoint,
		CredentialToken: payload.CredentialToken, Protocol: payload.Protocol, CreatedAt: payload.CreatedAt,
		ConnectDeadlineAt: payload.ConnectDeadlineAt, HardExpiresAt: payload.HardExpiresAt, Replayed: true,
	}, nil
}

func parseCredential(token string) (id, secret string, valid bool) {
	id, encoded, ok := strings.Cut(token, ".")
	if !ok || !validRandomIdentifier(id, "nac_", 16) || len(encoded) != 43 {
		return "", "", false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != encoded {
		return "", "", false
	}
	return id, encoded, true
}

func validRandomIdentifier(value, prefix string, randomBytes int) bool {
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	encoded := strings.TrimPrefix(value, prefix)
	if encoded != strings.ToLower(encoded) {
		return false
	}
	decoded, err := rawBase32.DecodeString(strings.ToUpper(encoded))
	return err == nil && len(decoded) == randomBytes && strings.ToLower(rawBase32.EncodeToString(decoded)) == encoded
}

func parseInt64(value any) (int64, bool) {
	switch value := value.(type) {
	case int64:
		return value, true
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		return parsed, err == nil
	case []byte:
		parsed, err := strconv.ParseInt(string(value), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func asString(value any) (string, bool) {
	switch value := value.(type) {
	case string:
		return value, true
	case []byte:
		return string(value), true
	default:
		return "", false
	}
}

func (s *Store) decodeAllocateResult(key, requestHash string, now time.Time, values []any) (Allocation, bool, error) {
	if len(values) == 0 {
		return Allocation{}, false, ErrUnavailable
	}
	code, ok := parseInt64(values[0])
	if !ok {
		return Allocation{}, false, ErrUnavailable
	}
	switch code {
	case -1:
		return Allocation{}, false, ErrResourcesUnverified
	case -2:
		return Allocation{}, false, ErrIdempotencyConflict
	case -3, -4:
		if len(values) != 2 {
			return Allocation{}, false, ErrUnavailable
		}
		earliest, ok := parseInt64(values[1])
		if !ok {
			return Allocation{}, false, ErrUnavailable
		}
		retry := time.Duration(earliest-now.UnixMilli()) * time.Millisecond
		if retry < time.Millisecond {
			retry = time.Millisecond
		}
		scope := LimitInstallation
		if code == -4 {
			scope = LimitNetwork
		}
		return Allocation{}, false, &ConcurrencyLimitError{Scope: scope, RetryAfter: retry}
	case -5, -6:
		if len(values) != 2 {
			return Allocation{}, false, ErrUnavailable
		}
		oldest, ok := parseInt64(values[1])
		if !ok {
			return Allocation{}, false, ErrUnavailable
		}
		retry := time.Duration(oldest+rateWindow.Milliseconds()-now.UnixMilli()) * time.Millisecond
		if retry < time.Millisecond {
			retry = time.Millisecond
		}
		scope := LimitInstallation
		if code == -6 {
			scope = LimitNetwork
		}
		return Allocation{}, false, &RateLimitError{Scope: scope, RetryAfter: retry}
	case -7:
		return Allocation{}, true, nil
	case -8:
		return Allocation{}, false, ErrRunStopped
	case -9:
		return Allocation{}, false, ErrRunExpired
	case -10:
		return Allocation{}, false, ErrInvalidState
	case 1, 2:
		if len(values) != 13 {
			return Allocation{}, false, ErrUnavailable
		}
		runID, runOK := asString(values[1])
		expiresMS, expiresOK := parseInt64(values[2])
		storedRequestHash, hashOK := asString(values[3])
		ciphertext, cipherOK := asString(values[4])
		if !runOK || !expiresOK || !hashOK || !cipherOK || storedRequestHash != requestHash || !now.Before(time.UnixMilli(expiresMS)) {
			return Allocation{}, false, ErrInvalidState
		}
		allocation, err := s.openReplay(key, requestHash, runID, expiresMS, []byte(ciphertext))
		if err != nil {
			return Allocation{}, false, err
		}
		current := make([]string, 8)
		for index := 5; index < len(values); index++ {
			value, ok := asString(values[index])
			if !ok {
				return Allocation{}, false, ErrInvalidState
			}
			current[index-5] = value
		}
		credentialID, _, valid := parseCredential(allocation.CredentialToken)
		createdAt, createdOK := parseInt64(current[5])
		connectDeadline, connectOK := parseInt64(current[6])
		hardExpires, hardOK := parseInt64(current[7])
		if !valid || credentialID != current[0] || !hmac.Equal([]byte(s.hashCredential(allocation.CredentialToken)), []byte(current[1])) ||
			allocation.ProxyName != current[2] || string(allocation.Protocol) != current[3] || allocation.PublicEndpoint != current[4] ||
			!createdOK || allocation.CreatedAt.UnixMilli() != createdAt || !connectOK || allocation.ConnectDeadlineAt.UnixMilli() != connectDeadline ||
			!hardOK || allocation.HardExpiresAt.UnixMilli() != hardExpires {
			return Allocation{}, false, ErrInvalidState
		}
		allocation.Replayed = code == 2
		return allocation, false, nil
	default:
		return Allocation{}, false, ErrUnavailable
	}
}

func mapRedisError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, redis.Nil) {
		return ErrRunNotFound
	}
	return ErrUnavailable
}
