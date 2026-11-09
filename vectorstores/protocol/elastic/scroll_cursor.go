package elastic

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"net/http"
	"time"
)

const scrollCleanupTimeout = 5 * time.Second

// A damaged page can still allocate a server cursor. Its latest complete ID
// must outlive page admission and caller cancellation so cleanup can release it.
type scrollCursor struct {
	client *Client
	id     string
}

func (s *scrollCursor) decodePage(raw []byte) (SearchResponse, error) {
	var page SearchResponse
	err := jsonv2.Unmarshal(raw, &page)
	if page.ScrollID != "" {
		s.id = page.ScrollID
	}
	return page, err
}

func (s *scrollCursor) close(ctx context.Context) error {
	if s.id == "" {
		return nil
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), scrollCleanupTimeout)
	defer cancel()
	payload, err := jsonv2.Marshal(struct {
		IDs []string `json:"scroll_id"`
	}{IDs: []string{s.id}})
	if err != nil {
		return err
	}
	raw, err := s.client.Read(cleanup, http.MethodDelete, scrollPath, nil, payload, 0)
	if err != nil {
		return err
	}
	var outcome struct {
		Succeeded *bool `json:"succeeded"`
		Freed     *int  `json:"num_freed"`
	}
	if err := jsonv2.Unmarshal(raw, &outcome); err != nil {
		return err
	}
	if outcome.Succeeded == nil || !*outcome.Succeeded || outcome.Freed == nil || *outcome.Freed < 0 {
		return errors.New("elastic: native scroll cleanup was not acknowledged")
	}
	return nil
}
