package omega

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"unicode/utf8"

	"filippo.io/age"
)

const encryptedCustody = "age-scrypt"
const keyWorkFactor = 18 // age's default: 256 MiB per sequential scrypt operation.
const maxEncryptedKey = 16 << 10
const maxKeyPayload = 4 << 10

// Tests can use a cheaper codec without changing the public backend or exposing
// a flag that could accidentally weaken an operator's key files.
type keyCodec struct{ workFactor int }

func defaultKeyCodec() keyCodec { return keyCodec{workFactor: keyWorkFactor} }

// ValidateNewPassphrase applies only to new encryption, never to unlocking
// existing files. Count Unicode characters rather than UTF-8 bytes.
func ValidateNewPassphrase(password []byte) error {
	if !utf8.Valid(password) || utf8.RuneCount(password) < 12 {
		return errors.New("omega: new key passphrases must contain at least 12 characters")
	}
	return nil
}

type keyBinding struct {
	Transaction string `json:"transaction"`
	Network     string `json:"network"`
	Repository  string `json:"repository"`
	Name        string `json:"name"`
	Generation  int    `json:"generation"`
}

type keyPayload struct {
	Schema  int        `json:"schema"`
	Binding keyBinding `json:"binding"`
	Public  string     `json:"public_key"`
	Private string     `json:"private_key"`
}

func (c keyCodec) create(ctx context.Context, binding keyBinding, password []byte) ([]byte, error) {
	if err := ValidateNewPassphrase(password); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	defer clear(private)
	payload := keyPayload{Schema: 1, Binding: binding, Public: base64.StdEncoding.EncodeToString(public), Private: base64.StdEncoding.EncodeToString(private)}
	plaintext := record(payload)
	defer clear(plaintext)
	recipient, err := age.NewScryptRecipient(string(password))
	if err != nil {
		return nil, errors.New("omega: a nonempty key passphrase is required")
	}
	recipient.SetWorkFactor(c.workFactor)
	var out bytes.Buffer
	writer, err := age.Encrypt(&out, recipient)
	if err != nil {
		return nil, errors.New("omega: cannot encrypt key allocation")
	}
	if _, err := writer.Write(plaintext); err != nil {
		return nil, errors.New("omega: cannot encrypt key allocation")
	}
	if err := writer.Close(); err != nil {
		return nil, errors.New("omega: cannot finish encrypted key allocation")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (c keyCodec) unlock(ctx context.Context, ciphertext []byte, binding keyBinding, password []byte) (ed25519.PrivateKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(ciphertext) == 0 || len(ciphertext) > maxEncryptedKey {
		return nil, errors.New("omega: encrypted key exceeds size bounds")
	}
	identity, err := age.NewScryptIdentity(string(password))
	if err != nil {
		return nil, errors.New("omega: a nonempty key passphrase is required")
	}
	identity.SetMaxWorkFactor(c.workFactor)
	reader, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if err != nil {
		return nil, errors.New("omega: cannot unlock key; check the passphrase or restore an intact encrypted copy")
	}
	// Reading through authenticated EOF is essential: accepting only a JSON
	// prefix could miss corruption or truncation in the final age stream chunk.
	plaintext, err := io.ReadAll(io.LimitReader(reader, maxKeyPayload+1))
	defer clear(plaintext)
	if err != nil || len(plaintext) > maxKeyPayload {
		return nil, errors.New("omega: damaged or oversized encrypted key payload")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var payload keyPayload
	if err := decodeRecord(plaintext, &payload); err != nil || payload.Schema != 1 || payload.Binding != binding {
		return nil, errors.New("omega: encrypted key does not match this authority, role, or generation")
	}
	private, err := decodeKey(payload.Private)
	if err != nil {
		return nil, errors.New("omega: invalid decrypted signing key")
	}
	if base64.StdEncoding.EncodeToString(private.Public().(ed25519.PublicKey)) != payload.Public {
		clear(private)
		return nil, errors.New("omega: decrypted public/private key mismatch")
	}
	return private, nil
}

func encryptedKeyName(name string) string { return name + ".key.age" }
