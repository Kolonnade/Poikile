package assettest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"sort"

	"github.com/Kolonnade/Poikile/upload"
)

// Send delivers body to tg the way a browser would and returns the HTTP
// status. A POST writes tg.Fields (sorted), then Content-Type, then the
// file last, since S3 ignores fields after the file.
func Send(ctx context.Context, tg upload.Target, contentType string, body []byte) (int, error) {
	var req *http.Request
	var err error
	switch tg.Method {
	case upload.MethodPut:
		req, err = http.NewRequestWithContext(ctx, http.MethodPut, tg.URL, bytes.NewReader(body))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Content-Type", contentType)
	case upload.MethodPost:
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		keys := make([]string, 0, len(tg.Fields))
		for k := range tg.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := mw.WriteField(k, tg.Fields[k]); err != nil {
				return 0, err
			}
		}
		if err := mw.WriteField("Content-Type", contentType); err != nil {
			return 0, err
		}
		fw, err := mw.CreateFormFile("file", "upload")
		if err != nil {
			return 0, err
		}
		if _, err := fw.Write(body); err != nil {
			return 0, err
		}
		if err := mw.Close(); err != nil {
			return 0, err
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, tg.URL, &buf)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Content-Type", mw.FormDataContentType())
	default:
		return 0, fmt.Errorf("assettest: unknown upload method %q", tg.Method)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode, nil
}
