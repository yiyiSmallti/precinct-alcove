package credentials

import (
	"errors"

	"github.com/egoist/mygo"
)

// MygoSecrets is the system credential store through MyGo: the Credential
// Manager on Windows, the login Keychain on macOS, under service "Alcove".
type MygoSecrets struct{}

func (MygoSecrets) svc() mygo.SecretsModule { return mygo.Secrets.Service("Alcove") }

func (m MygoSecrets) Set(key string, value []byte) error { return m.svc().Set(key, value) }

func (m MygoSecrets) Get(key string) ([]byte, error) {
	v, err := m.svc().Get(key)
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return nil, ErrNotFound
	}
	return v, err
}

func (m MygoSecrets) Delete(key string) error {
	err := m.svc().Delete(key)
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return ErrNotFound
	}
	return err
}
