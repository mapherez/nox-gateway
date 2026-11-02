package gateway

import (
	"context"
	"crypto/rand"
	"math/big"
	"sync"
	"time"

	"github.com/mapherez/nox-gateway/internal/backend"

	noxmcp "github.com/mapherez/nox-mcp/go"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (g *Gateway) probeAll(ctx context.Context) {
	g.mu.RLock()
	records := append([]noxmcp.SuiteRegistrationRecord{}, g.state.Registry.Products...)
	g.mu.RUnlock()
	var workers sync.WaitGroup
	limit := make(chan struct{}, 4)
	for _, r := range records {
		if r.ProductID == "nox-gateway" || r.Declaration.Capabilities.SuiteMcp == nil {
			continue
		}
		select {
		case limit <- struct{}{}:
		case <-ctx.Done():
			workers.Wait()
			return
		}
		workers.Add(1)
		go func(r noxmcp.SuiteRegistrationRecord) {
			defer workers.Done()
			defer func() { <-limit }()
			check, cancel := context.WithTimeout(ctx, 8*time.Second)
			state := "unknown"
			id, e := g.authorize(check, "", r, "probe")
			if e == nil {
				session, closeSession, e := g.connect(check, r, id, nil)
				if e != nil {
					state = "unreachable"
				} else {
					if ping(check, session) == nil {
						state = "ready"
					} else {
						state = "notReady"
					}
					closeSession()
				}
			}
			if check.Err() != nil {
				state = "unknown"
			}
			cancel()
			g.report(ctx, r, state)
		}(r)
	}
	workers.Wait()
}

func (g *Gateway) report(ctx context.Context, r noxmcp.SuiteRegistrationRecord, state string) {
	p, _, e := g.trust.Authority()
	if e != nil {
		return
	}
	g.mu.Lock()
	prod := g.state.Producers[r.ProductID]
	if prod.Registration != r.Revision || prod.Association != r.AssociationID || prod.ProducerID != p.Service.ServiceID {
		prod = producer{ProducerID: p.Service.ServiceID, Registration: r.Revision, Association: r.AssociationID, Key: rand.Text(), Sequence: "0"}
		g.state.Producers[r.ProductID] = prod
		if e = g.saveLocked(g.state); e != nil {
			g.mu.Unlock()
			return
		}
	}
	g.mu.Unlock()
	path := "/api/nox/products/" + backend.Resource(r.ProductID)
	if prod.Epoch == "" {
		raw, _, e := g.trust.Request(ctx, "POST", path+"/observation-epochs", prod.Key, noxmcp.SuiteObservationEpochRequest{ContractVersion: "1.0", ProductID: r.ProductID, RegistrationRevision: r.Revision, AssociationID: r.AssociationID, ProducerID: p.Service.ServiceID, Source: "gateway"})
		if e != nil {
			return
		}
		epoch, e := noxmcp.LoadSuiteObservationEpoch(raw)
		if e != nil || epoch.AssociationID != r.AssociationID || epoch.ProducerID != p.Service.ServiceID || epoch.ProductID != r.ProductID || epoch.Source != "gateway" {
			return
		}
		prod.Epoch = epoch.ProducerEpoch
	}
	n, ok := new(big.Int).SetString(prod.Sequence, 10)
	if !ok {
		return
	}
	prod.Sequence = n.Add(n, big.NewInt(1)).String()
	g.mu.Lock()
	g.state.Producers[r.ProductID] = prod
	e = g.saveLocked(g.state)
	g.mu.Unlock()
	if e != nil {
		return
	}
	_, _, _ = g.trust.Request(ctx, "POST", path+"/observations", rand.Text(), map[string]any{"contractVersion": "1.0", "productId": r.ProductID, "registrationRevision": r.Revision, "associationId": r.AssociationID, "source": "gateway", "producerId": p.Service.ServiceID, "producerEpoch": prod.Epoch, "sequence": prod.Sequence, "observedAt": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), "state": state})
}

// MCP 2026 replaces initialize/ping with stateless server/discover. Connect has
// already completed that authenticated exchange; legacy sessions use ping.
func ping(ctx context.Context, session *mcp.ClientSession) error {
	if session.InitializeResult().ProtocolVersion >= "2026-07-28" {
		return nil
	}
	return session.Ping(ctx, &mcp.PingParams{})
}
