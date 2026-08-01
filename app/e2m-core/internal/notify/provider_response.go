package notify

import (
	"errors"
	"io"
)

const maxProviderResponseBytes = 64 << 10

func readProviderResponse(body io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(body, maxProviderResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxProviderResponseBytes {
		return nil, errors.New("provider response is too large")
	}
	return raw, nil
}
