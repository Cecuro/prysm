package beacon_api

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/OffchainLabs/prysm/v7/api/rest"
	"github.com/OffchainLabs/prysm/v7/network/httputil"
	"github.com/OffchainLabs/prysm/v7/testing/assert"
	"github.com/OffchainLabs/prysm/v7/testing/require"
	"github.com/OffchainLabs/prysm/v7/validator/client/beacon-api/mock"
	"github.com/pkg/errors"
	"go.uber.org/mock/gomock"
)

func TestPostSSZWithFallback(t *testing.T) {
	const endpoint = "/test/ssz/endpoint"
	key := sszSupportKey{host: "host-a", endpoint: endpoint}
	jsonBody := []byte("[]")
	jsonFn := func() ([]byte, error) { return jsonBody, nil }

	newHandler := func(t *testing.T) *mock.MockHandler {
		handler := mock.NewMockHandler(gomock.NewController(t))
		handler.EXPECT().Host().Return("host-a").AnyTimes()
		return handler
	}
	call := func(t *testing.T, c *beaconApiValidatorClient, unsupportedStatus int) error {
		return c.postSSZWithFallback(t.Context(), endpoint, nil, []byte{1}, jsonFn, unsupportedStatus)
	}
	assertMarked := func(t *testing.T, c *beaconApiValidatorClient) {
		assert.Equal(t, true, c.isSSZUnsupported(key))
	}
	assertNotMarked := func(t *testing.T, c *beaconApiValidatorClient) {
		assert.Equal(t, false, c.isSSZUnsupported(key))
	}

	t.Run("ssz success stores nothing", func(t *testing.T) {
		handler := newHandler(t)
		handler.EXPECT().PostSSZ(gomock.Any(), endpoint, gomock.Any(), gomock.Any()).Return(nil, nil, nil).Times(1)

		c := &beaconApiValidatorClient{handler: handler}
		require.NoError(t, call(t, c, http.StatusUnsupportedMediaType))
		assertNotMarked(t, c)
	})

	t.Run("ssz rejection marks unsupported and falls back to JSON", func(t *testing.T) {
		handler := newHandler(t)
		handler.EXPECT().PostSSZ(gomock.Any(), endpoint, gomock.Any(), gomock.Any()).
			Return(nil, nil, &httputil.DefaultJsonError{Code: http.StatusUnsupportedMediaType, Message: "unsupported media type"}).Times(1)
		handler.EXPECT().Post(gomock.Any(), endpoint, gomock.Any(), bytes.NewBuffer(jsonBody), nil).Return(nil).Times(1)

		c := &beaconApiValidatorClient{handler: handler}
		require.NoError(t, call(t, c, http.StatusUnsupportedMediaType))
		assertMarked(t, c)
	})

	t.Run("ssz rejection marks unsupported even when JSON fallback fails", func(t *testing.T) {
		handler := newHandler(t)
		handler.EXPECT().PostSSZ(gomock.Any(), endpoint, gomock.Any(), gomock.Any()).
			Return(nil, nil, &httputil.DefaultJsonError{Code: http.StatusUnsupportedMediaType, Message: "unsupported media type"}).Times(1)
		handler.EXPECT().Post(gomock.Any(), endpoint, gomock.Any(), gomock.Any(), nil).Return(errors.New("json post failed")).Times(1)

		c := &beaconApiValidatorClient{handler: handler}
		require.ErrorContains(t, "json post failed", call(t, c, http.StatusUnsupportedMediaType))
		assertMarked(t, c)
	})

	t.Run("wrapped rejection error still triggers fallback", func(t *testing.T) {
		handler := newHandler(t)
		wrapped := errors.Wrap(&httputil.DefaultJsonError{Code: http.StatusUnsupportedMediaType, Message: "unsupported media type"}, "request failed")
		handler.EXPECT().PostSSZ(gomock.Any(), endpoint, gomock.Any(), gomock.Any()).Return(nil, nil, wrapped).Times(1)
		handler.EXPECT().Post(gomock.Any(), endpoint, gomock.Any(), gomock.Any(), nil).Return(nil).Times(1)

		c := &beaconApiValidatorClient{handler: handler}
		require.NoError(t, call(t, c, http.StatusUnsupportedMediaType))
		assertMarked(t, c)
	})

	t.Run("known unsupported skips ssz", func(t *testing.T) {
		// No PostSSZ expectation: gomock fails the test if the SSZ attempt happens.
		handler := newHandler(t)
		handler.EXPECT().Post(gomock.Any(), endpoint, gomock.Any(), bytes.NewBuffer(jsonBody), nil).Return(nil).Times(1)

		c := &beaconApiValidatorClient{handler: handler, sszUnsupported: map[sszSupportKey]bool{key: true}}
		require.NoError(t, call(t, c, http.StatusUnsupportedMediaType))
	})

	t.Run("unsupported on another endpoint still tries ssz", func(t *testing.T) {
		handler := newHandler(t)
		handler.EXPECT().PostSSZ(gomock.Any(), endpoint, gomock.Any(), gomock.Any()).Return(nil, nil, nil).Times(1)

		c := &beaconApiValidatorClient{handler: handler, sszUnsupported: map[sszSupportKey]bool{{host: "host-a", endpoint: "/other/endpoint"}: true}}
		require.NoError(t, call(t, c, http.StatusUnsupportedMediaType))
	})

	t.Run("unsupported on another host still tries ssz", func(t *testing.T) {
		handler := newHandler(t)
		handler.EXPECT().PostSSZ(gomock.Any(), endpoint, gomock.Any(), gomock.Any()).Return(nil, nil, nil).Times(1)

		c := &beaconApiValidatorClient{handler: handler, sszUnsupported: map[sszSupportKey]bool{{host: "host-b", endpoint: endpoint}: true}}
		require.NoError(t, call(t, c, http.StatusUnsupportedMediaType))
	})

	t.Run("other error returns without fallback or cache write", func(t *testing.T) {
		handler := newHandler(t)
		handler.EXPECT().PostSSZ(gomock.Any(), endpoint, gomock.Any(), gomock.Any()).
			Return(nil, nil, &httputil.DefaultJsonError{Code: http.StatusInternalServerError, Message: "boom"}).Times(1)

		c := &beaconApiValidatorClient{handler: handler}
		require.ErrorContains(t, "boom", call(t, c, http.StatusUnsupportedMediaType))
		assertNotMarked(t, c)
	})

	t.Run("generation change mid-request skips cache write", func(t *testing.T) {
		provider := &rest.MockRestProvider{}
		handler := newHandler(t)
		handler.EXPECT().PostSSZ(gomock.Any(), endpoint, gomock.Any(), gomock.Any()).
			DoAndReturn(func(context.Context, string, map[string]string, *bytes.Buffer) ([]byte, http.Header, error) {
				provider.ConnCounter++ // BN failover mid-request
				return nil, nil, &httputil.DefaultJsonError{Code: http.StatusUnsupportedMediaType, Message: "unsupported media type"}
			}).Times(1)
		handler.EXPECT().Post(gomock.Any(), endpoint, gomock.Any(), gomock.Any(), nil).Return(nil).Times(1)

		c := &beaconApiValidatorClient{handler: handler, restProvider: provider}
		require.NoError(t, call(t, c, http.StatusUnsupportedMediaType))
		assertNotMarked(t, c)
	})

	t.Run("host change mid-request skips cache write", func(t *testing.T) {
		handler := mock.NewMockHandler(gomock.NewController(t))
		gomock.InOrder(
			handler.EXPECT().Host().Return("host-a"),
			handler.EXPECT().Host().Return("host-b"),
		)
		handler.EXPECT().PostSSZ(gomock.Any(), endpoint, gomock.Any(), gomock.Any()).
			Return(nil, nil, &httputil.DefaultJsonError{Code: http.StatusUnsupportedMediaType, Message: "unsupported media type"}).Times(1)
		handler.EXPECT().Post(gomock.Any(), endpoint, gomock.Any(), gomock.Any(), nil).Return(nil).Times(1)

		c := &beaconApiValidatorClient{handler: handler}
		require.NoError(t, call(t, c, http.StatusUnsupportedMediaType))
		assertNotMarked(t, c)
		assert.Equal(t, false, c.isSSZUnsupported(sszSupportKey{host: "host-b", endpoint: endpoint}))
	})

	t.Run("json marshal error surfaces", func(t *testing.T) {
		handler := newHandler(t)

		c := &beaconApiValidatorClient{handler: handler, sszUnsupported: map[sszSupportKey]bool{key: true}}
		err := c.postSSZWithFallback(t.Context(), endpoint, nil, []byte{1}, func() ([]byte, error) {
			return nil, errors.New("marshal broke")
		}, http.StatusUnsupportedMediaType)
		require.ErrorContains(t, "marshal broke", err)
	})
}
