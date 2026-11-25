package providers

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"

	"git.shino.boo/devilreef/cadence/server/internal/jsonschemax"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

func checkedConfig(mod Module, cfg json.RawMessage) (json.RawMessage, error) {
	if len(cfg) == 0 || string(cfg) == "null" {
		cfg = json.RawMessage(`{}`)
	}
	schema, err := jsonschemax.Compile(mod.ConfigSchema)
	if err != nil {
		return nil, ErrBuildFailed
	}
	if path, ok := jsonschemax.Validate(schema, cfg); !ok {
		return nil, &ConfigInvalidError{Path: path}
	}
	if mod.Check != nil {
		if path, ok := mod.Check(cfg); !ok {
			return nil, &ConfigInvalidError{Path: path}
		}
	}
	return cfg, nil
}

func hasSecret(mod Module, key string) bool {
	return slices.Contains(mod.Secrets, key) || slices.Contains(mod.OptionalSecrets, key)
}

func checkSecrets(mod Module, sec map[string]string) error {
	for k := range sec {
		if !hasSecret(mod, k) {
			return &ConfigInvalidError{Path: "/secrets"}
		}
	}
	for _, k := range mod.Secrets {
		if sec[k] == "" {
			return &ConfigInvalidError{Path: "/secrets/" + k}
		}
	}
	return nil
}

func (s *Instances) sealSecrets(name string, sec map[string]string) ([]byte, error) {
	if len(sec) == 0 {
		return nil, nil
	}
	plain, err := json.Marshal(sec)
	if err != nil {
		return nil, err
	}
	return s.box.Seal(plain, aad(name))
}

// Update replaces config when one is given, merges secrets and clears the optional ones named. It builds the
// replacement before committing it: a build failure leaves the row and the running cache untouched.
func (s *Instances) Update(ctx context.Context, name string, cfg json.RawMessage, changes map[string]string,
	cleared []string) (Instance, error) {
	// a save by the old build and an edit each read and write the secret map whole
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	row, err := s.q.GetProviderInstance(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return Instance{}, ErrNotFound
	}
	if err != nil {
		return Instance{}, err
	}
	mod, ok := s.reg.Module(row.Module)
	if !ok {
		return Instance{}, ErrUnknownModule
	}
	if len(cfg) > 0 {
		cfg, err = checkedConfig(mod, cfg)
		if err != nil {
			return Instance{}, err
		}
	} else {
		cfg = row.Config
	}
	for key := range changes {
		if !hasSecret(mod, key) {
			return Instance{}, &ConfigInvalidError{Path: "/secrets"}
		}
	}
	sec, err := s.openSecrets(name, row.Secrets)
	if err != nil {
		return Instance{}, err
	}
	for _, key := range cleared {
		if !hasSecret(mod, key) {
			return Instance{}, &ConfigInvalidError{Path: "/clearSecrets"}
		}
		if slices.Contains(mod.Secrets, key) {
			return Instance{}, &ConfigInvalidError{Path: "/clearSecrets/" + key}
		}
		if _, replaced := changes[key]; replaced {
			return Instance{}, &ConfigInvalidError{Path: "/clearSecrets/" + key}
		}
	}
	for key, value := range changes {
		sec[key] = value
	}
	for _, key := range cleared {
		delete(sec, key)
	}
	if err := checkSecrets(mod, sec); err != nil {
		return Instance{}, err
	}
	sealed, err := s.sealSecrets(name, sec)
	if err != nil {
		return Instance{}, err
	}
	v, err := newOf(mod, cfg, sec, s.saver(name, row.ID, mod))
	if err != nil || v == nil {
		return Instance{}, ErrBuildFailed
	}

	s.mu.Lock()
	updated, err := s.q.UpdateProviderInstance(ctx, db.UpdateProviderInstanceParams{
		Config: cfg, Secrets: sealed, Name: name, ID: row.ID,
	})
	if err == nil {
		s.cache[name] = built{kind: mod.Kind, v: v}
	}
	s.mu.Unlock()
	if errors.Is(err, pgx.ErrNoRows) {
		return Instance{}, ErrNotFound
	}
	if err != nil {
		return Instance{}, err
	}
	inst, err := s.toInstance(updated)
	if err != nil {
		return Instance{}, err
	}
	s.notify()
	return inst, nil
}
