package server

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

type mcpPoolKey struct {
	pg       *db.DB
	id       int64
	revision int64
}

type mcpPoolEntry struct {
	cl    mcpClient
	tools []actool.CoreTool
}

type mcpPoolInflight struct {
	done     chan struct{}
	cancel   context.CancelFunc
	mu       sync.Mutex
	waiters  int
	evicted  bool
	canceled bool
	entry    *mcpPoolEntry
	err      error
}

// mcpClientPool caches authorized MCP clients keyed by (pg, id, revision) so
// concurrent and subsequent agent runs reuse live connections and circuit breakers.
type mcpClientPool struct {
	mu       sync.Mutex
	entries  map[mcpPoolKey]*mcpPoolEntry
	inflight map[mcpPoolKey]*mcpPoolInflight
	setupFn  func(ctx context.Context, pg *db.DB, id, revision int64, budget time.Duration) (mcpClient, []actool.CoreTool, error)
	authFn   func(ctx context.Context, pg *db.DB, id, revision int64) error
}

func newMCPClientPool() *mcpClientPool {
	return &mcpClientPool{
		entries:  make(map[mcpPoolKey]*mcpPoolEntry),
		inflight: make(map[mcpPoolKey]*mcpPoolInflight),
	}
}

var defaultMCPPool = newMCPClientPool()

func (p *mcpClientPool) setup(ctx context.Context, pg *db.DB, id, revision int64, budget time.Duration) (mcpClient, []actool.CoreTool, error) {
	if p.setupFn != nil {
		return p.setupFn(ctx, pg, id, revision, budget)
	}
	return setupAuthorizedMCP(ctx, pg, id, revision, budget)
}

func (p *mcpClientPool) authorize(ctx context.Context, pg *db.DB, id, revision int64) error {
	if p.authFn != nil {
		return p.authFn(ctx, pg, id, revision)
	}
	_, err := pg.AuthorizedMCPContext(ctx, id, revision)
	return err
}

func (p *mcpClientPool) acquire(ctx context.Context, pg *db.DB, id, revision int64, budget time.Duration) ([]actool.CoreTool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := mcpPoolKey{pg: pg, id: id, revision: revision}

	p.mu.Lock()
	if entry, ok := p.entries[key]; ok {
		p.mu.Unlock()
		if err := p.authorize(ctx, pg, id, revision); err != nil {
			if ctx.Err() == nil {
				p.evictEntry(key, entry)
			}
			return nil, err
		}
		return slices.Clone(entry.tools), nil
	}

	if inf, ok := p.inflight[key]; ok {
		inf.mu.Lock()
		inf.waiters++
		inf.mu.Unlock()
		p.mu.Unlock()
		return p.waitInflight(ctx, key, inf)
	}

	dialCtx, dialCancel := context.WithCancel(context.Background())
	inf := &mcpPoolInflight{
		done:    make(chan struct{}),
		cancel:  dialCancel,
		waiters: 1,
	}
	p.inflight[key] = inf
	p.mu.Unlock()

	go p.runDial(dialCtx, dialCancel, key, inf, budget)
	return p.waitInflight(ctx, key, inf)
}

func (p *mcpClientPool) runDial(dialCtx context.Context, dialCancel context.CancelFunc, key mcpPoolKey, inf *mcpPoolInflight, budget time.Duration) {
	defer close(inf.done)
	defer dialCancel()

	cl, ts, err := p.setup(dialCtx, key.pg, key.id, key.revision, budget)

	p.mu.Lock()
	if p.inflight[key] == inf {
		delete(p.inflight, key)
	}
	inf.mu.Lock()
	aborted := inf.evicted || inf.canceled
	inf.mu.Unlock()

	if err != nil {
		p.mu.Unlock()
		if cl != nil {
			_ = cl.Close()
		}
		inf.err = err
		return
	}
	if aborted {
		p.mu.Unlock()
		if cl != nil {
			_ = cl.Close()
		}
		inf.err = errors.New("mcp: client evicted")
		return
	}

	entry := &mcpPoolEntry{cl: cl, tools: ts}
	p.entries[key] = entry
	var stale []mcpClient
	for k, old := range p.entries {
		if k.pg == key.pg && k.id == key.id && k.revision != key.revision {
			stale = append(stale, old.cl)
			delete(p.entries, k)
		}
	}
	p.mu.Unlock()

	for _, oldCl := range stale {
		if oldCl != nil {
			_ = oldCl.Close()
		}
	}
	inf.entry = entry
}

func (p *mcpClientPool) waitInflight(ctx context.Context, key mcpPoolKey, inf *mcpPoolInflight) ([]actool.CoreTool, error) {
	select {
	case <-inf.done:
		if inf.err != nil {
			return nil, inf.err
		}
		if inf.entry == nil {
			return nil, errors.New("mcp: client unavailable")
		}
		return slices.Clone(inf.entry.tools), nil
	case <-ctx.Done():
		p.mu.Lock()
		inf.mu.Lock()
		inf.waiters--
		if inf.waiters == 0 {
			inf.canceled = true
			if p.inflight[key] == inf {
				delete(p.inflight, key)
			}
			inf.cancel()
		}
		inf.mu.Unlock()
		p.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (p *mcpClientPool) evictEntry(key mcpPoolKey, target *mcpPoolEntry) {
	p.mu.Lock()
	cur, ok := p.entries[key]
	if !ok || cur != target {
		p.mu.Unlock()
		return
	}
	delete(p.entries, key)
	p.mu.Unlock()
	if cur.cl != nil {
		_ = cur.cl.Close()
	}
}

func (p *mcpClientPool) evict(pg *db.DB, id int64) {
	var toClose []mcpClient
	p.mu.Lock()
	for k, entry := range p.entries {
		if k.pg == pg && k.id == id {
			toClose = append(toClose, entry.cl)
			delete(p.entries, k)
		}
	}
	for k, inf := range p.inflight {
		if k.pg == pg && k.id == id {
			inf.mu.Lock()
			inf.evicted = true
			inf.mu.Unlock()
			inf.cancel()
			delete(p.inflight, k)
		}
	}
	p.mu.Unlock()
	for _, cl := range toClose {
		if cl != nil {
			_ = cl.Close()
		}
	}
}

func (p *mcpClientPool) closeForDB(pg *db.DB) {
	var toClose []mcpClient
	p.mu.Lock()
	for k, entry := range p.entries {
		if k.pg == pg {
			toClose = append(toClose, entry.cl)
			delete(p.entries, k)
		}
	}
	for k, inf := range p.inflight {
		if k.pg == pg {
			inf.mu.Lock()
			inf.evicted = true
			inf.mu.Unlock()
			inf.cancel()
			delete(p.inflight, k)
		}
	}
	p.mu.Unlock()
	for _, cl := range toClose {
		if cl != nil {
			_ = cl.Close()
		}
	}
}
