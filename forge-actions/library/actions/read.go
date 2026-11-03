package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

func readPage(ctx context.Context, s Source, path string, q url.Values, envelope string, page int) ([]json.RawMessage, bool, error) {
	if q == nil {
		q = url.Values{}
	}

	q.Set("page", strconv.Itoa(page))
	key := "per_page"
	if s.Session.Kind == forgeauth.Forgejo {
		key = "limit"
	}

	q.Set(key, "50")
	for k, v := range q {
		if len(v) == 1 && v[0] == "" {
			q.Del(k)
		}
	}

	var raw json.RawMessage
	h, err := forgerest.Do(ctx, s.Session, "GET", path+"?"+q.Encode(), nil, &raw)
	if err != nil {
		return nil, false, err
	}

	var total *int
	if envelope != "" {
		raw, total, err = unwrapEnvelope(raw, envelope)
		if err != nil {
			return nil, false, err
		}
	}

	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false, err
	}
	if items == nil {
		return nil, false, fmt.Errorf("actions: missing list array")
	}
	if len(items) > 50 {
		return nil, false, ErrTooLarge
	}

	_, more := forgehttp.NextPage(h)
	if total != nil {
		more = more || page*50 < *total
	} else if h.Get("Link") == "" {
		more = len(items) == 50
	}

	return items, more, nil
}

// unwrapEnvelope returns the list inside an envelope object and its
// total_count, if any.
func unwrapEnvelope(raw json.RawMessage, envelope string) (json.RawMessage, *int, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, nil, err
	}
	if len(obj[envelope]) == 0 {
		return nil, nil, fmt.Errorf("actions: missing list envelope")
	}

	var total *int
	if v, ok := obj["total_count"]; ok {
		if err := json.Unmarshal(v, &total); err != nil {
			return nil, nil, err
		}
	}

	return obj[envelope], total, nil
}

func readAll(ctx context.Context, s Source, path string, q url.Values, envelope string) ([]json.RawMessage, error) {
	var out []json.RawMessage
	for page := 1; page <= 20; page++ {
		items, more, err := readPage(ctx, s, path, q, envelope, page)
		if err != nil {
			return nil, err
		}

		out = append(out, items...)
		if !more {
			return out, nil
		}
	}

	return nil, ErrTooLarge
}

func readDownload(ctx context.Context, s Source, path string, limit int64) ([]byte, bool, error) {
	resp, err := forgerest.Open(ctx, s.Session, http.MethodGet, path, nil)
	if err != nil {
		return nil, false, err
	}

	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, false, ErrUnsupported
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, false, err
	}

	over := int64(len(data)) > limit
	if over {
		data = data[:limit]
	}

	return data, over, nil
}
