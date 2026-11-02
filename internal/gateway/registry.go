// Package gateway hosts authorized aggregation; it owns no product business rules.
package gateway

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/url"
	"sync"
	"time"

	"github.com/mapherez/nox-gateway/internal/backend"
	"github.com/mapherez/nox-gateway/internal/trust"

	noxmcp "github.com/mapherez/nox-mcp/go"
)

type Store interface {
	LoadRegistry() ([]byte, error)
	SaveRegistry([]byte) error
}
type producer struct{ ProducerID, Registration, Association, Key, Epoch, Sequence string }
type persisted struct {
	SchemaVersion int                          `json:"schemaVersion"`
	Registry      noxmcp.SuiteRegistrySnapshot `json:"registry"`
	Producers     map[string]producer          `json:"producers"`
}
type authority interface {
	Authority() (noxmcp.SuiteProvisioning, trust.KeySet, error)
	Request(context.Context, string, string, string, any) ([]byte, int, error)
	HumanRegistry(context.Context, string) ([]byte, int, error)
	Reconcile(context.Context) error
}
type Limits struct {
	CallTimeout, ProbeInterval     time.Duration
	MaxConcurrent, MaxPayloadBytes int
}
type Gateway struct {
	limits                 Limits
	active                 map[string]context.CancelFunc
	mu                     sync.RWMutex
	trust                  authority
	store                  Store
	dir, serverID, version string
	state                  persisted
	ready                  bool
	generation             uint64
	authorityID            string
	cache                  *noxmcp.SuiteAuthorizationCache
	secret                 []byte
	slots                  chan struct{}
	network                chan struct{}
	sources                map[string]*sourceEntry
	admissions             map[string]admission
}
type admission struct {
	products map[string]bool
	expires  time.Time
}

func New(dir, id, version string, t authority, s Store, options ...Limits) (*Gateway, error) {
	limits := Limits{CallTimeout: 20 * time.Second, ProbeInterval: 15 * time.Second, MaxConcurrent: 32, MaxPayloadBytes: noxmcp.SuiteMaxPayloadBytes}
	if len(options) > 1 {
		return nil, errors.New("invalid Gateway limits")
	}
	if len(options) == 1 {
		limits = options[0]
	}
	if limits.CallTimeout < 10*time.Second || limits.CallTimeout > time.Hour || limits.ProbeInterval < time.Second || limits.ProbeInterval > time.Hour || limits.MaxConcurrent < 1 || limits.MaxConcurrent > 256 || limits.MaxPayloadBytes < 1024 || limits.MaxPayloadBytes > noxmcp.SuiteMaxPayloadBytes {
		return nil, errors.New("invalid Gateway limits")
	}
	c, e := noxmcp.NewSuiteAuthorizationCache(noxmcp.SuiteCacheOptions{MaxEntries: 256, MaxBytes: 8 << 20})
	if e != nil {
		return nil, e
	}
	_, e = noxmcp.NewDiscovery(id)
	if e != nil {
		return nil, e
	}
	g := &Gateway{limits: limits, active: map[string]context.CancelFunc{}, dir: dir, serverID: id, version: version, trust: t, store: s, cache: c, slots: make(chan struct{}, limits.MaxConcurrent), network: make(chan struct{}, limits.MaxConcurrent), secret: make([]byte, 32), sources: map[string]*sourceEntry{}, admissions: map[string]admission{}}
	if _, e = rand.Read(g.secret); e != nil {
		return nil, e
	}
	raw, e := s.LoadRegistry()
	if e != nil {
		return nil, e
	}
	if raw != nil {
		if _, e = noxmcp.ParseSuiteJSONWithLimit(raw, 8<<20); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &g.state); e != nil || g.state.SchemaVersion != 1 {
			return nil, errors.New("invalid cached Gateway state")
		}
	}
	if g.state.Producers == nil {
		g.state.Producers = map[string]producer{}
	}
	return g, nil
}
func (g *Gateway) Ready() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	p, _, e := g.trust.Authority()
	return g.ready && e == nil && g.authorityID == authorityID(p)
}
func (g *Gateway) saveLocked(next persisted) error {
	next.SchemaVersion = 1
	b, e := json.Marshal(next)
	if e != nil {
		return e
	}
	return g.store.SaveRegistry(b)
}
func (g *Gateway) RefreshRegistry(ctx context.Context) error {
	raw, _, e := g.trust.Request(ctx, "GET", "/api/nox/products", "", nil)
	if e != nil {
		return e
	}
	next, e := noxmcp.LoadSuiteRegistrySnapshot(raw)
	if e != nil {
		return e
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	stage := g.state
	stage.Registry = next
	stage.Producers = currentProducers(next, g.state.Producers)
	if e = g.saveLocked(stage); e != nil {
		return e
	}
	g.cache.Invalidate(noxmcp.SuiteInvalidation{All: true})
	g.sources = map[string]*sourceEntry{}
	g.admissions = map[string]admission{}
	g.generation++
	for _, cancel := range g.active {
		cancel()
	}
	g.state = stage
	p, _, err := g.trust.Authority()
	if err != nil {
		return err
	}
	g.authorityID = authorityID(p)
	g.ready = true
	return nil
}
func (g *Gateway) poll(ctx context.Context) error {
	g.mu.RLock()
	cursor := g.state.Registry.Cursor
	g.mu.RUnlock()
	raw, _, e := g.trust.Request(ctx, "GET", "/api/nox/events?after="+url.QueryEscape(cursor)+"&waitMs=25000&limit=256", "", nil)
	if e != nil {
		var f *backend.Failure
		if errors.As(e, &f) && f.Code == "CURSOR_EXPIRED" {
			return g.RefreshRegistry(ctx)
		}
		return e
	}
	page, e := noxmcp.LoadSuiteEventPage(raw)
	if e != nil {
		return e
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	next, e := noxmcp.ReduceSuiteRegistry(g.state.Registry, raw, cursor)
	if e != nil {
		return e
	}
	stage := g.state
	stage.Registry = next
	stage.Producers = currentProducers(next, g.state.Producers)
	if e = g.saveLocked(stage); e != nil {
		return e
	}
	for _, event := range page.Events {
		b, _ := json.Marshal(event)
		if e = g.cache.ApplyEvent(b); e != nil {
			return e
		}
		if event.Type != "observation.updated" && event.Type != "producer.reassociated" {
			g.generation++
			for _, cancel := range g.active {
				cancel()
			}
			g.sources = map[string]*sourceEntry{}
			g.admissions = map[string]admission{}
		}
	}
	g.state = stage
	return nil
}
func (g *Gateway) Run(ctx context.Context) {
	reports := time.NewTicker(g.limits.ProbeInterval)
	defer reports.Stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-reports.C:
				g.probeAll(ctx)
			}
		}
	}()
	defer func() { <-done }()
	for ctx.Err() == nil {
		if e := g.trust.Reconcile(ctx); e != nil {
			g.mu.Lock()
			if _, _, a := g.trust.Authority(); a != nil {
				g.ready = false
				g.cache.Invalidate(noxmcp.SuiteInvalidation{All: true})
				g.sources = map[string]*sourceEntry{}
				g.admissions = map[string]admission{}
			}
			g.mu.Unlock()
			if !pause(ctx, time.Second) {
				return
			}
			continue
		}
		g.mu.RLock()
		p, _, authorityErr := g.trust.Authority()
		ready := g.ready && authorityErr == nil && g.authorityID == authorityID(p)
		g.mu.RUnlock()
		var e error
		if !ready {
			e = g.RefreshRegistry(ctx)
		} else {
			e = g.poll(ctx)
		}
		if e != nil && !pause(ctx, time.Second) {
			return
		}
	}
}
func pause(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
func (g *Gateway) record(id string) (noxmcp.SuiteRegistrationRecord, uint64, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, r := range g.state.Registry.Products {
		if r.ProductID == id {
			return r, g.generation, nil
		}
	}
	return noxmcp.SuiteRegistrationRecord{}, 0, errors.New("PRODUCT_NOT_REGISTERED")
}

func authorityID(p noxmcp.SuiteProvisioning) string {
	return p.Issuer + ":" + p.Service.ServiceID + ":" + p.Service.CredentialRevision
}

// Keep producer history only for the current association/registration; removed
// products must not grow the private state indefinitely.
func currentProducers(registry noxmcp.SuiteRegistrySnapshot, saved map[string]producer) map[string]producer {
	current := map[string]producer{}
	for _, r := range registry.Products {
		if p, ok := saved[r.ProductID]; ok && p.Registration == r.Revision && p.Association == r.AssociationID {
			current[r.ProductID] = p
		}
	}
	return current
}
