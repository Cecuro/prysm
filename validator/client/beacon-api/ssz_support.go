package beacon_api

import (
	"bytes"
	"context"
	"fmt"

	"github.com/OffchainLabs/prysm/v7/network/httputil"
	"github.com/pkg/errors"
)

// sszSupportKey is per host AND endpoint: a beacon node may accept SSZ bodies on one endpoint but not another.
type sszSupportKey struct {
	host     string
	endpoint string
}

// postSSZWithFallback posts the body from sszFn to endpoint, falling back to JSON from jsonFn
// when the beacon node rejects the SSZ request body with unsupportedStatus.
//
// NOTE: Only rejections are cached: storing a success changes no behavior and could overwrite a concurrent rejection.
func (c *beaconApiValidatorClient) postSSZWithFallback(
	ctx context.Context,
	endpoint string,
	headers map[string]string,
	sszFn func() ([]byte, error),
	jsonFn func() ([]byte, error),
	unsupportedStatus int,
) error {
	postJSON := func() error {
		body, err := jsonFn()
		if err != nil {
			return fmt.Errorf("marshal JSON body: %w", err)
		}
		if err := c.handler.Post(ctx, endpoint, headers, bytes.NewBuffer(body), nil); err != nil {
			return fmt.Errorf("post JSON: %w", err)
		}
		return nil
	}

	generation := c.ConnectionGeneration()
	key := sszSupportKey{host: c.handler.Host(), endpoint: endpoint}

	// If the BN had previously rejected SSZ for this endpoint,
	// go straight to JSON without marshaling or trying SSZ at all.
	if c.isSSZUnsupported(key) {
		return postJSON()
	}

	sszBody, err := sszFn()
	if err != nil {
		return fmt.Errorf("marshal SSZ body: %w", err)
	}
	_, _, err = c.handler.PostSSZ(ctx, endpoint, headers, bytes.NewBuffer(sszBody))
	if err == nil {
		return nil
	}

	errJson := &httputil.DefaultJsonError{}
	if !errors.As(err, &errJson) || errJson.Code != unsupportedStatus {
		return fmt.Errorf("post SSZ: %w", err)
	}

	c.markSSZUnsupported(key, generation)
	log.
		WithError(err).
		WithField("endpoint", endpoint).
		Warn("Beacon node does not accept SSZ request bodies, falling back to JSON")
	return postJSON()
}

// isSSZUnsupported reports whether the beacon node previously rejected SSZ request bodies for this host and endpoint.
func (c *beaconApiValidatorClient) isSSZUnsupported(key sszSupportKey) bool {
	c.sszUnsupportedLock.RLock()
	defer c.sszUnsupportedLock.RUnlock()

	return c.sszUnsupported[key]
}

// markSSZUnsupported records an SSZ rejection for this host and endpoint.
func (c *beaconApiValidatorClient) markSSZUnsupported(key sszSupportKey, generation uint64) {
	// Skip the write if the BN connection switched mid-request: the rejection may belong to a different host.
	if c.ConnectionGeneration() != generation || c.handler.Host() != key.host {
		return
	}

	c.sszUnsupportedLock.Lock()
	defer c.sszUnsupportedLock.Unlock()

	if c.sszUnsupported == nil {
		c.sszUnsupported = make(map[sszSupportKey]bool)
	}

	c.sszUnsupported[key] = true
}
