package cliproxy

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// sessionAffinitySaveInterval bounds the bindings lost when the process is killed
// rather than shut down; a graceful shutdown saves the final state.
const sessionAffinitySaveInterval = 15 * time.Second

// sessionAffinityPersister saves bindings and carries them into a replacement selector
// (startup restore, or a routing change that rebuilds the selector).
type sessionAffinityPersister struct {
	mu       sync.Mutex
	selector *coreauth.SessionAffinitySelector
	records  []coreauth.SessionBindingRecord
	loaded   bool
}

// sync restores carried bindings into a selector it has not seen, then saves its live bindings.
func (p *sessionAffinityPersister) sync(selector *coreauth.SessionAffinitySelector, store *coreauth.FileSessionAffinityStore) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.loaded {
		records, errLoad := store.Load()
		if errLoad != nil {
			log.Warnf("session affinity: %v", errLoad)
		}
		p.records, p.loaded = records, true
	}
	if selector != p.selector {
		if restored := selector.RestoreSessionBindings(p.records); restored > 0 {
			log.Infof("session affinity: restored %d binding(s) from %s", restored, store.Path())
		}
		p.selector = selector
	}
	p.records = selector.SessionBindings()
	if errSave := store.Save(p.records); errSave != nil {
		log.Warnf("session affinity: %v", errSave)
	}
}

// sessionAffinityStore returns the bindings file store, or nil when persistence is off.
func sessionAffinityStore(cfg *config.Config) *coreauth.FileSessionAffinityStore {
	if cfg == nil || cfg.Home.Enabled || !cfg.Routing.SessionAffinity || !cfg.Routing.SessionAffinityPersist {
		return nil
	}
	authDir, errResolve := util.ResolveAuthDir(cfg.AuthDir)
	if errResolve != nil || authDir == "" {
		return nil
	}
	return coreauth.NewFileSessionAffinityStore(filepath.Join(authDir, coreauth.SessionAffinityStateFile))
}

// syncSessionAffinity restores and saves bindings for the current selector when persistence is on.
func (s *Service) syncSessionAffinity() {
	if s == nil || s.coreManager == nil {
		return
	}
	store := sessionAffinityStore(s.currentConfig())
	selector, ok := s.coreManager.Selector().(*coreauth.SessionAffinitySelector)
	if store == nil || !ok || selector == nil {
		return
	}
	s.affinityPersister.sync(selector, store)
}

// runSessionAffinitySaving saves bindings on an interval until ctx ends.
func (s *Service) runSessionAffinitySaving(ctx context.Context) {
	ticker := time.NewTicker(sessionAffinitySaveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.syncSessionAffinity()
		}
	}
}
