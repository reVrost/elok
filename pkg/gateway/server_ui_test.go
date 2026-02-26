package gateway

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func newTestUIServer(t *testing.T) *Server {
	t.Helper()
	uiFS := fstest.MapFS{
		"index.html":           &fstest.MapFile{Data: []byte("<html>ok</html>")},
		"_app/app.js":          &fstest.MapFile{Data: []byte("console.log('ok')")},
		"manifest.webmanifest": &fstest.MapFile{Data: []byte(`{"name":"elok"}`)},
	}
	return &Server{
		uiFS: uiFS,
		ui:   http.FileServer(http.FS(uiFS)),
	}
}

func TestHandleUIRootServesIndexWithoutRedirect(t *testing.T) {
	s := newTestUIServer(t)

	req := httptest.NewRequest(http.MethodGet, "/app", nil)
	rr := httptest.NewRecorder()
	s.handleUI(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Empty(t, rr.Header().Get("Location"))
}

func TestHandleUIFallbackServesIndex(t *testing.T) {
	s := newTestUIServer(t)

	req := httptest.NewRequest(http.MethodGet, "/app/chat/thread/123", nil)
	rr := httptest.NewRecorder()
	s.handleUI(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
}

func TestHandleUIStaticAssetPassThrough(t *testing.T) {
	s := newTestUIServer(t)

	req := httptest.NewRequest(http.MethodGet, "/app/_app/app.js", nil)
	rr := httptest.NewRecorder()
	s.handleUI(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
}

func TestHandleUIIndexMissingReturnsNotFound(t *testing.T) {
	uiFS := fstest.MapFS{
		"_app/app.js": &fstest.MapFile{Data: []byte("console.log('ok')")},
	}
	s := &Server{
		uiFS: uiFS,
		ui:   http.FileServer(http.FS(uiFS)),
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	s.handleUI(rr, req)

	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestHandleRootRedirectsToApp(t *testing.T) {
	s := newTestUIServer(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	s.handleRoot(rr, req)

	require.Equal(t, http.StatusTemporaryRedirect, rr.Code)
	require.Equal(t, "/app", rr.Header().Get("Location"))
}

func TestHandleUIMissingAssetReturnsNotFound(t *testing.T) {
	s := newTestUIServer(t)

	req := httptest.NewRequest(http.MethodGet, "/app/_app/does-not-exist.js", nil)
	rr := httptest.NewRecorder()
	s.handleUI(rr, req)

	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestGatewayURLsUsesAppPath(t *testing.T) {
	uiURL, wsURL, healthzURL := gatewayURLs(":7777")
	require.Equal(t, "http://127.0.0.1:7777/app", uiURL)
	require.Equal(t, "ws://127.0.0.1:7777/ws", wsURL)
	require.Equal(t, "http://127.0.0.1:7777/healthz", healthzURL)
}

var _ fs.FS = fstest.MapFS{}
