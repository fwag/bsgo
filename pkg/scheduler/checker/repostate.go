package checker

import (
	"errors"
	"fmt"

	"github.com/openSUSE/bsgo/pkg/storage"
)

// recordRepoState stores the repository state at the end of a pass (see
// storage.DeriveRepoState for the precedence). The publisher owns publishing
// and published; a pass keeps them while they are current.
func (ps *pass) recordRepoState(counts map[string]int, configError string) error {
	st := ps.c.d.Store
	cur, err := st.GetPrpaState(ps.ctx, ps.prpa.Project, ps.prpa.Repo, ps.prpa.Arch)
	if errors.Is(err, storage.ErrNotFound) {
		cur, err = nil, nil
	}
	if err != nil {
		return fmt.Errorf("read repository state: %w", err)
	}
	hash := ""
	if configError == "" {
		idx, err := ps.c.d.Indexes.Index(ps.ctx, ps.prpa)
		if err != nil {
			return fmt.Errorf("read build repository index: %w", err)
		}
		hash = idx.Hash
	}
	state, details, write := storage.DeriveRepoState(storage.RepoStateInput{
		ConfigError: configError, Counts: counts, PublishEnabled: ps.target.Flags.Publish,
		IndexHash: hash, Current: cur,
	})
	if !write {
		return nil
	}
	row := &storage.PrpaState{
		Project: ps.prpa.Project, Repo: ps.prpa.Repo, Arch: ps.prpa.Arch,
		State: state, Details: details, IndexHash: hash,
	}
	if err := st.SetPrpaState(ps.ctx, row); err != nil {
		return fmt.Errorf("store repository state: %w", err)
	}
	return nil
}

// recordBroken marks the repository broken when err is a configuration error
// and returns err, joined with the failure to record it.
func (ps *pass) recordBroken(err error) error {
	var cfg *configError
	if !errors.As(err, &cfg) {
		return err
	}
	if rerr := ps.recordRepoState(nil, cfg.Error()); rerr != nil {
		return errors.Join(err, rerr)
	}
	return err
}
