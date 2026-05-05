package memstore

import (
	"github.com/stempeck/agentfactory/internal/issuestore"
)

// Seed bulk-loads fixtures using the same code path as Create.
//
// Each fixture's resulting Issue is created with the default StatusOpen.
// Tests that need a non-Open fixture should call Patch/Close after Seed
// or use the lower-level SeedAt helper.
func (s *Store) Seed(fixtures ...issuestore.CreateParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range fixtures {
		s.createLocked(f)
	}
	return nil
}

// SeedAt is like Seed but lets the test specify a non-default Status for each
// fixture. Index alignment: statuses[i] is applied to fixtures[i]. If statuses
// is shorter, remaining fixtures default to StatusOpen.
func (s *Store) SeedAt(statuses []issuestore.Status, fixtures ...issuestore.CreateParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, f := range fixtures {
		iss := s.createLocked(f)
		if i < len(statuses) && statuses[i] != "" {
			iss.Status = statuses[i]
			s.issues[iss.ID] = iss
		}
	}
	return nil
}
