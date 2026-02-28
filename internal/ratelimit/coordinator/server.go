package coordinator

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/ratelimit"
)

// Server is the cross-process rate limit coordinator.
// It owns the authoritative token buckets and arbitrates access among
// all connected Interlink processes (GUI, daemon, CLI).
type Server struct {
	registry *ratelimit.Registry

	mu      sync.Mutex
	buckets map[string]*ratelimit.RateLimiter // keyed by BucketKey.String()
	clients map[string]*clientState           // keyed by ClientID
	leases  map[string]*leaseState            // keyed by LeaseID

	startTime         time.Time
	lastActivity      time.Time
	idleTimeout       time.Duration
	watchdogInterval  time.Duration // for testing; defaults to 30s

	listener net.Listener
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// clientState tracks a connected coordinator client.
type clientState struct {
	clientID     string
	lastSeen     time.Time
	leaseIDs     []string // Leases held by this client
	bucketKeys   []string // Buckets this client has acquired from
}

// leaseState tracks an active lease grant.
type leaseState struct {
	grant    *LeaseGrant
	clientID string
	bucketKey string
}

// DefaultIdleTimeout is how long the coordinator waits with no clients before shutting down.
const DefaultIdleTimeout = 5 * time.Minute

// NewServer creates a coordinator server. Call Start() with a listener to begin serving.
func NewServer() *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		registry:         ratelimit.NewRegistry(),
		buckets:          make(map[string]*ratelimit.RateLimiter),
		clients:          make(map[string]*clientState),
		leases:           make(map[string]*leaseState),
		startTime:        time.Now(),
		lastActivity:     time.Now(),
		idleTimeout:      DefaultIdleTimeout,
		watchdogInterval: 30 * time.Second,
		ctx:              ctx,
		cancel:           cancel,
	}
}

// SetIdleTimeout configures the idle timeout. Must be called before Start().
func (s *Server) SetIdleTimeout(d time.Duration) {
	s.idleTimeout = d
}

// setWatchdogInterval configures how often the idle watchdog checks. For testing only.
func (s *Server) setWatchdogInterval(d time.Duration) {
	s.watchdogInterval = d
}

// Start begins accepting connections on the given listener.
func (s *Server) Start(listener net.Listener) {
	s.listener = listener

	// Accept loop
	s.wg.Add(1)
	go s.acceptLoop()

	// Idle watchdog
	s.wg.Add(1)
	go s.idleWatchdog()

	// Stale client cleanup
	s.wg.Add(1)
	go s.staleClientCleanup()
}

// Stop gracefully shuts down the server.
func (s *Server) Stop() {
	s.cancel()
	if s.listener != nil {
		s.listener.Close()
	}
	s.wg.Wait()
}

// HandleRequest processes a single protocol request and returns a response.
// Exported for direct testing without sockets.
func (s *Server) HandleRequest(req *Request) *Response {
	s.mu.Lock()
	s.lastActivity = time.Now()

	// Ensure client is tracked
	if req.ClientID != "" {
		if _, ok := s.clients[req.ClientID]; !ok {
			s.clients[req.ClientID] = &clientState{
				clientID: req.ClientID,
				lastSeen: time.Now(),
			}
		} else {
			s.clients[req.ClientID].lastSeen = time.Now()
		}
	}
	s.mu.Unlock()

	switch req.Type {
	case MsgAcquire:
		return s.handleAcquire(req)
	case MsgAcquireLease:
		return s.handleAcquireLease(req)
	case MsgDrain:
		return s.handleDrain(req)
	case MsgSetCooldown:
		return s.handleSetCooldown(req)
	case MsgHeartbeat:
		return s.handleHeartbeat(req)
	case MsgGetState:
		return s.handleGetState(req)
	case MsgShutdown:
		return s.handleShutdown(req)
	default:
		return NewErrorResponse(fmt.Sprintf("unknown message type: %s", req.Type))
	}
}

// getOrCreateBucket returns the authoritative limiter for a bucket key.
// Must be called with s.mu held.
func (s *Server) getOrCreateBucket(key BucketKey) *ratelimit.RateLimiter {
	keyStr := key.String()
	if limiter, ok := s.buckets[keyStr]; ok {
		return limiter
	}

	cfg := s.registry.GetScopeConfig(key.Scope)
	limiter := ratelimit.NewRateLimiter(cfg.TargetRate, cfg.BurstCapacity)
	s.buckets[keyStr] = limiter
	return limiter
}

func (s *Server) handleAcquire(req *Request) *Response {
	key := BucketKeyFromRequest(req)

	s.mu.Lock()
	limiter := s.getOrCreateBucket(key)

	// Track which buckets this client uses
	keyStr := key.String()
	if client, ok := s.clients[req.ClientID]; ok {
		found := false
		for _, k := range client.bucketKeys {
			if k == keyStr {
				found = true
				break
			}
		}
		if !found {
			client.bucketKeys = append(client.bucketKeys, keyStr)
		}
	}
	s.mu.Unlock()

	// Check cooldown first
	if cooldown := limiter.CooldownRemaining(); cooldown > 0 {
		return NewWaitResponse(cooldown)
	}

	// Try to acquire
	if limiter.TryAcquire() {
		return NewGrantedResponse()
	}

	// No token available — tell client how long to wait
	waitDur := limiter.TimeUntilNextToken()
	if waitDur < time.Millisecond {
		waitDur = time.Millisecond // Minimum sensible wait
	}
	return NewWaitResponse(waitDur)
}

func (s *Server) handleAcquireLease(req *Request) *Response {
	key := BucketKeyFromRequest(req)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Count active clients for this bucket
	keyStr := key.String()
	activeClients := 0
	for _, ls := range s.leases {
		if ls.bucketKey == keyStr {
			activeClients++
		}
	}
	// Include the requesting client
	activeClients++

	cfg := s.registry.GetScopeConfig(key.Scope)
	rate, burst := CalculateLeaseFraction(cfg, activeClients)

	now := time.Now()
	grant := &LeaseGrant{
		LeaseID:   GenerateLeaseID(),
		Scope:     key.Scope,
		Rate:      rate,
		Burst:     burst,
		ExpiresAt: now.Add(LeaseDefaultTTL),
		RefreshBy: now.Add(LeaseRefreshInterval),
	}

	ls := &leaseState{
		grant:     grant,
		clientID:  req.ClientID,
		bucketKey: keyStr,
	}
	s.leases[grant.LeaseID] = ls

	// Track lease in client
	if client, ok := s.clients[req.ClientID]; ok {
		client.leaseIDs = append(client.leaseIDs, grant.LeaseID)
	}

	return NewLeaseGrantedResponse(grant)
}

func (s *Server) handleDrain(req *Request) *Response {
	key := BucketKeyFromRequest(req)

	s.mu.Lock()
	limiter := s.getOrCreateBucket(key)
	s.mu.Unlock()

	limiter.Drain()
	return NewOKResponse()
}

func (s *Server) handleSetCooldown(req *Request) *Response {
	key := BucketKeyFromRequest(req)

	s.mu.Lock()
	limiter := s.getOrCreateBucket(key)
	s.mu.Unlock()

	if req.CooldownDuration > 0 {
		limiter.SetCooldown(req.CooldownDuration)
	}
	return NewOKResponse()
}

func (s *Server) handleHeartbeat(req *Request) *Response {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Refresh all leases owned by this client
	now := time.Now()
	if client, ok := s.clients[req.ClientID]; ok {
		for _, leaseID := range client.leaseIDs {
			if ls, ok := s.leases[leaseID]; ok {
				ls.grant.ExpiresAt = now.Add(LeaseDefaultTTL)
				ls.grant.RefreshBy = now.Add(LeaseRefreshInterval)
			}
		}
	}

	return NewOKResponse()
}

func (s *Server) handleGetState(_ *Request) *Response {
	s.mu.Lock()
	defer s.mu.Unlock()

	bucketStates := make(map[string]BucketState, len(s.buckets))
	for keyStr, limiter := range s.buckets {
		// Count clients for this bucket
		clientCount := 0
		for _, client := range s.clients {
			for _, k := range client.bucketKeys {
				if k == keyStr {
					clientCount++
					break
				}
			}
		}

		bucketStates[keyStr] = BucketState{
			Tokens:           limiter.GetCurrentTokens(),
			CooldownRemainMs: limiter.CooldownRemaining().Milliseconds(),
			ActiveClients:    clientCount,
		}
	}

	state := &StateInfo{
		Uptime:        time.Since(s.startTime),
		ActiveClients: len(s.clients),
		ActiveLeases:  len(s.leases),
		Buckets:       bucketStates,
	}

	return NewStateDataResponse(state)
}

func (s *Server) handleShutdown(_ *Request) *Response {
	// Schedule shutdown after response is sent
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.cancel()
	}()
	return NewOKResponse()
}

// acceptLoop accepts and handles incoming connections.
func (s *Server) acceptLoop() {
	defer s.wg.Done()

	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.ctx.Done():
				return
			default:
				log.Printf("coordinator: accept error: %v", err)
				continue
			}
		}

		s.wg.Add(1)
		go s.handleConnection(conn)
	}
}

// handleConnection processes messages from a single client connection.
func (s *Server) handleConnection(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(30 * time.Second))
	reader := bufio.NewReader(conn)

	// Read request (newline-delimited JSON)
	data, err := reader.ReadBytes('\n')
	if err != nil {
		if err != io.EOF {
			log.Printf("coordinator: read error: %v", err)
		}
		return
	}

	req, err := DecodeRequest(data)
	if err != nil {
		log.Printf("coordinator: decode error: %v", err)
		s.sendResponse(conn, NewErrorResponse("invalid request format"))
		return
	}

	resp := s.HandleRequest(req)
	s.sendResponse(conn, resp)
}

// sendResponse writes a JSON response to the connection.
func (s *Server) sendResponse(conn net.Conn, resp *Response) {
	data, err := resp.Encode()
	if err != nil {
		log.Printf("coordinator: encode error: %v", err)
		return
	}
	data = append(data, '\n')
	conn.Write(data)
}

// idleWatchdog shuts down the server if no clients are connected for idleTimeout.
func (s *Server) idleWatchdog() {
	defer s.wg.Done()

	ticker := time.NewTicker(s.watchdogInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			clientCount := len(s.clients)
			lastAct := s.lastActivity
			s.mu.Unlock()

			if clientCount == 0 && time.Since(lastAct) > s.idleTimeout {
				log.Printf("coordinator: idle timeout reached (%v with no clients), shutting down", s.idleTimeout)
				s.cancel()
				return
			}
		}
	}
}

// staleClientCleanup removes clients that haven't sent a heartbeat recently.
func (s *Server) staleClientCleanup() {
	defer s.wg.Done()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			now := time.Now()
			for clientID, client := range s.clients {
				if now.Sub(client.lastSeen) > StaleClientTimeout {
					// Remove leases owned by this client
					for _, leaseID := range client.leaseIDs {
						delete(s.leases, leaseID)
					}
					delete(s.clients, clientID)
					log.Printf("coordinator: removed stale client %s", clientID)
				}
			}
			s.mu.Unlock()
		}
	}
}

// Done returns a channel that is closed when the server is shutting down.
func (s *Server) Done() <-chan struct{} {
	return s.ctx.Done()
}
